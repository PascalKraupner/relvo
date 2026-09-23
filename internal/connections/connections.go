// Package connections discovers connection candidates without connecting to them.
package connections

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/PascalKraupner/relvo/internal/database"
	"github.com/joho/godotenv"
)

type Profile struct {
	Name        string            `json:"name"`
	EnvFile     string            `json:"env_file,omitempty"`
	Mapping     map[string]string `json:"mapping,omitempty"`
	Config      database.Config   `json:"config"`
	PasswordEnv string            `json:"password_env,omitempty"`
	Endpoint    *Endpoint         `json:"endpoint,omitempty"`
}

// Endpoint overrides only the network address of a resolved profile.
type Endpoint struct {
	Host string `json:"host"`
	Port string `json:"port"`
}

func readEnv(path string) (map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open environment file: %w", err)
	}
	defer f.Close()
	values, err := godotenv.Parse(f)
	if err != nil {
		// Parser errors can quote source lines containing passwords.
		return nil, errors.New("invalid environment file syntax")
	}
	return values, nil
}

// Discover inspects only dir/.env; saved profiles are loaded separately with Load.
func Discover(dir string) ([]Profile, error) {
	path, err := filepath.Abs(filepath.Join(dir, ".env"))
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("inspect environment file: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("environment file is not a regular file")
	}
	values, err := readEnv(path)
	if err != nil {
		return nil, err
	}
	if driver := values["DB_CONNECTION"]; driver != "" && driver != "mysql" && driver != "mariadb" {
		return nil, nil
	}
	if values["DB_CONNECTION"] == "" && values["DB_DATABASE"] == "" {
		return nil, nil
	}
	return []Profile{{Name: filepath.Base(filepath.Dir(path)) + " (.env)", EnvFile: path}}, nil
}

// Load returns no profiles when the file does not exist.
func Load(path string) ([]Profile, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read connection profiles: %w", err)
	}
	var profiles []Profile
	if err := json.Unmarshal(data, &profiles); err != nil {
		return nil, errors.New("invalid connection profiles JSON")
	}
	return profiles, nil
}

// Save atomically replaces path with a private file. Passwords are never encoded.
func Save(path string, profiles []Profile) error {
	data, err := json.MarshalIndent(profiles, "", "  ")
	if err != nil {
		return fmt.Errorf("encode connection profiles: %w", err)
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("create profile directory: %w", err)
	}
	f, err := os.CreateTemp(dir, ".connections-*")
	if err != nil {
		return fmt.Errorf("create profile file: %w", err)
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if err := f.Chmod(0600); err != nil {
		return err
	}
	if _, err := f.Write(append(data, '\n')); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(f.Name(), path); err != nil {
		return fmt.Errorf("replace connection profiles: %w", err)
	}
	return nil
}

func DefaultPath() string {
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		dir, _ = os.UserConfigDir()
	}
	return filepath.Join(dir, "relvo", "connections.json")
}

// Resolve rereads EnvFile without changing the process environment. Mapping
// overrides Laravel variable names. PasswordEnv, if set, overrides the password
// using a process environment variable (an explicitly empty value is valid).
func Resolve(p Profile) (database.Config, error) {
	cfg := p.Config
	cfg.Name = p.Name
	fields := map[string]*string{
		"host": &cfg.Host, "port": &cfg.Port, "user": &cfg.User,
		"password": &cfg.Password, "database": &cfg.Database,
		"socket": &cfg.Socket, "tls": &cfg.TLS,
	}
	mapping := map[string]string{
		"host": "DB_HOST", "port": "DB_PORT", "user": "DB_USERNAME",
		"password": "DB_PASSWORD", "database": "DB_DATABASE",
		"socket": "DB_SOCKET", "tls": "DB_TLS",
	}
	for field, variable := range p.Mapping {
		if fields[field] == nil || variable == "" {
			return database.Config{}, errors.New("invalid connection environment mapping")
		}
		mapping[field] = variable
	}
	if len(p.Mapping) > 0 && p.EnvFile == "" {
		return database.Config{}, errors.New("environment mapping requires an environment file")
	}
	if p.EnvFile != "" {
		values, err := readEnv(p.EnvFile)
		if err != nil {
			return database.Config{}, err
		}
		for field, variable := range mapping {
			value, ok := values[variable]
			if !ok {
				if _, explicit := p.Mapping[field]; explicit {
					return database.Config{}, fmt.Errorf("environment file is missing mapped %s variable", field)
				}
				continue
			}
			*fields[field] = value
		}
	}
	if p.PasswordEnv != "" {
		var ok bool
		cfg.Password, ok = os.LookupEnv(p.PasswordEnv)
		if !ok {
			return database.Config{}, errors.New("password environment variable is not set")
		}
	}
	if p.Endpoint != nil {
		cfg.Host = p.Endpoint.Host
		cfg.Port = p.Endpoint.Port
		cfg.Socket = ""
	}
	if cfg.Host == "" && cfg.Socket == "" {
		return database.Config{}, errors.New("connection requires host or socket (DB_HOST or DB_SOCKET)")
	}
	if cfg.User == "" {
		return database.Config{}, errors.New("connection requires user (DB_USERNAME)")
	}
	if cfg.Database == "" {
		return database.Config{}, errors.New("connection requires database (DB_DATABASE)")
	}
	if cfg.Port == "" {
		cfg.Port = "3306"
	}
	return cfg, nil
}
