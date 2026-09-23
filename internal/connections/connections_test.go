package connections

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PascalKraupner/relvo/internal/database"
)

func writeEnv(t *testing.T, path, text string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestDiscoverResolve(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	t.Setenv("DB_HOST", "unchanged")
	marker := filepath.Join(dir, "executed")
	writeEnv(t, path, "DB_CONNECTION=mysql\nDB_HOST=mysql\nDB_DATABASE='quoted database'\nDB_USERNAME=\"quoted user\"\nDB_PASSWORD='$(touch "+marker+")'\n")
	profiles, err := Discover(dir)
	if err != nil || len(profiles) != 1 {
		t.Fatalf("Discover: %v, %v", profiles, err)
	}
	if profiles[0].Config.Password != "" || profiles[0].EnvFile != path || profiles[0].Name == "" {
		t.Fatal("discovery must return a named, file-backed candidate without credentials")
	}
	cfg, err := Resolve(profiles[0])
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Host != "mysql" || cfg.Port != "3306" || cfg.User != "quoted user" || cfg.Database != "quoted database" || cfg.Password != "$(touch "+marker+")" {
		t.Fatal("incorrect quoted environment resolution")
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("dotenv executed a command")
	}
	if os.Getenv("DB_HOST") != "unchanged" {
		t.Fatal("dotenv mutated the process environment")
	}
	writeEnv(t, path, "DB_HOST=localhost\nDB_USERNAME=root\nDB_DATABASE=updated\nDB_PASSWORD=\n")
	cfg, err = Resolve(profiles[0])
	if err != nil || cfg.Database != "updated" || cfg.Password != "" {
		t.Fatal("Resolve did not reread the environment file")
	}
}

func TestDiscoveryScope(t *testing.T) {
	dir := t.TempDir()
	writeEnv(t, filepath.Join(dir, ".env.local"), "DB_CONNECTION=mysql\n")
	profiles, err := Discover(dir)
	if err != nil || len(profiles) != 0 {
		t.Fatal("discovery must ignore other dotenv files")
	}
	writeEnv(t, filepath.Join(dir, ".env"), "DB_CONNECTION=sqlite\n")
	profiles, err = Discover(dir)
	if err != nil || len(profiles) != 0 {
		t.Fatal("discovery must ignore non-MySQL drivers")
	}
}

func TestDotenvDoesNotExecuteOrUseProcessInterpolation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	marker := filepath.Join(dir, "executed")
	t.Setenv("RELVO_EXTERNAL_SECRET", "process-secret")
	for _, quote := range []string{"", "\"", "'"} {
		value := "$(touch " + marker + ") `touch " + marker + "`"
		writeEnv(t, path, "DB_HOST=db\nDB_USERNAME=root\nDB_DATABASE=app\nDB_PASSWORD="+quote+value+quote+"\nEXTERNAL=${RELVO_EXTERNAL_SECRET}\n")
		cfg, err := Resolve(Profile{EnvFile: path})
		if err != nil || cfg.Password != value {
			t.Fatalf("command-like value with quote %q was not preserved: %v", quote, err)
		}
		values, err := readEnv(path)
		if err != nil || values["EXTERNAL"] != "" {
			t.Fatal("dotenv must not interpolate process secrets")
		}
		if _, err := os.Stat(marker); !os.IsNotExist(err) {
			t.Fatal("dotenv executed a command")
		}
	}
}

func TestStaticProfilePasswordEnv(t *testing.T) {
	p := Profile{Name: "socket", Config: database.Config{Socket: "/tmp/mysql.sock", User: "root", Database: "app"}, PasswordEnv: "RELVO_TEST_UNSET_PASSWORD"}
	t.Setenv(p.PasswordEnv, "")
	if _, err := Resolve(p); err != nil {
		t.Fatalf("explicit empty password and socket should work: %v", err)
	}
	if err := os.Unsetenv(p.PasswordEnv); err != nil {
		t.Fatal(err)
	}
	if _, err := Resolve(p); err == nil {
		t.Fatal("missing password environment variable must fail")
	}
}

func TestResolveMapping(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	writeEnv(t, path, "HOST=db\nPORT=3307\nUSER=alice\nPASS='a # b'\nDB=app\nSOCKET=/tmp/mysql.sock\nSSL=true\n")
	p := Profile{Name: "custom", EnvFile: path, Mapping: map[string]string{
		"host": "HOST", "port": "PORT", "user": "USER", "password": "PASS",
		"database": "DB", "socket": "SOCKET", "tls": "SSL",
	}}
	cfg, err := Resolve(p)
	if err != nil {
		t.Fatal(err)
	}
	if cfg != (database.Config{Name: "custom", Host: "db", Port: "3307", User: "alice", Password: "a # b", Database: "app", Socket: "/tmp/mysql.sock", TLS: "true"}) {
		t.Fatal("custom mapping not applied")
	}
	t.Setenv("RELVO_TEST_PASSWORD", "override")
	p.PasswordEnv = "RELVO_TEST_PASSWORD"
	cfg, err = Resolve(p)
	if err != nil || cfg.Password != "override" {
		t.Fatal("PasswordEnv not applied")
	}
	p.Mapping["host"] = "MISSING"
	if _, err := Resolve(p); err == nil || !strings.Contains(err.Error(), "missing mapped host") {
		t.Fatalf("missing mapping error: %v", err)
	}
}

func TestResolveErrors(t *testing.T) {
	for _, field := range []string{"host", "user", "database"} {
		t.Run(field, func(t *testing.T) {
			values := map[string]string{"host": "DB_HOST=db\n", "user": "DB_USERNAME=root\n", "database": "DB_DATABASE=app\n"}
			delete(values, field)
			var text string
			for _, value := range values {
				text += value
			}
			path := filepath.Join(t.TempDir(), ".env")
			writeEnv(t, path, text)
			if _, err := Resolve(Profile{EnvFile: path}); err == nil || !strings.Contains(err.Error(), field) {
				t.Fatalf("required field error: %v", err)
			}
		})
	}
	path := filepath.Join(t.TempDir(), ".env")
	writeEnv(t, path, "DB_PASSWORD='super-secret")
	if _, err := Resolve(Profile{EnvFile: path}); err == nil || strings.Contains(err.Error(), "super-secret") {
		t.Fatal("parse errors must not expose source secrets")
	}
	if _, err := Resolve(Profile{EnvFile: path + ".missing"}); err == nil {
		t.Fatal("missing env file must fail resolution")
	}
}

func TestSaveLoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "connections.json")
	p := Profile{Name: "saved", PasswordEnv: "MYSQL_PASSWORD", Config: database.Config{Host: "localhost", User: "root", Password: "super-secret", Database: "app"}}
	if err := Save(path, []Profile{p}); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if err := Save(path, []Profile{p}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "super-secret") || strings.Contains(string(data), `"password":`) {
		t.Fatal("password persisted")
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("profile permissions must be 0600")
	}
	profiles, err := Load(path)
	if err != nil || len(profiles) != 1 || profiles[0].Config.Password != "" || profiles[0].PasswordEnv != p.PasswordEnv {
		t.Fatal("profile roundtrip failed")
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil || len(entries) != 1 {
		t.Fatal("temporary profile files left behind")
	}
	if profiles, err := Load(path + ".missing"); err != nil || len(profiles) != 0 {
		t.Fatal("missing profiles should be empty")
	}
	writeEnv(t, path, `[{"config":{"password":"injected"}}]`)
	profiles, err = Load(path)
	if err != nil || profiles[0].Config.Password != "" {
		t.Fatal("Load must ignore stored passwords")
	}
}

func TestDefaultPath(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	if got := DefaultPath(); got != filepath.Join(dir, "relvo", "connections.json") {
		t.Fatalf("DefaultPath: %s", got)
	}
}

func TestEndpointReconnect(t *testing.T) {
	dir := t.TempDir()
	env := filepath.Join(dir, ".env")
	writeEnv(t, env, "DB_HOST=mysql\nDB_PORT=3306\nDB_SOCKET=/tmp/mysql.sock\nDB_USERNAME=root\nDB=first\nPASS=first-secret\n")
	original := Profile{Name: "app", EnvFile: env, Mapping: map[string]string{"database": "DB", "password": "PASS"}}
	candidate := original
	candidate.Name = "app (Docker)"
	candidate.Endpoint = &Endpoint{Host: "127.0.0.1", Port: "13306"}
	path := filepath.Join(dir, "connections.json")
	if err := Save(path, []Profile{candidate}); err != nil {
		t.Fatal(err)
	}
	profiles, err := Load(path)
	if err != nil || len(profiles) != 1 {
		t.Fatalf("load candidate: %v", err)
	}
	p := profiles[0]
	for _, generation := range []string{"first", "second"} {
		writeEnv(t, env, "DB_HOST=changed\nDB_PORT=3307\nDB_SOCKET=/tmp/changed.sock\nDB_USERNAME="+generation+"\nDB="+generation+"\nPASS="+generation+"-secret\n")
		cfg, err := Resolve(p)
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Host != "127.0.0.1" || cfg.Port != "13306" || cfg.Socket != "" || cfg.Name != candidate.Name || cfg.Database != generation || cfg.User != generation || cfg.Password != generation+"-secret" {
			t.Fatal("reconnect must refresh credentials and database while preserving only the endpoint")
		}
	}
	p.PasswordEnv = "RELVO_ENDPOINT_PASSWORD"
	if err := Save(path, []Profile{p}); err != nil {
		t.Fatal(err)
	}
	profiles, err = Load(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, password := range []string{"process-first", "process-second"} {
		t.Setenv(p.PasswordEnv, password)
		cfg, err := Resolve(profiles[0])
		if err != nil || cfg.Password != password || cfg.Host != "127.0.0.1" || cfg.Port != "13306" || cfg.Socket != "" {
			t.Fatal("endpoint must preserve PasswordEnv resolution")
		}
	}
	if original.Endpoint != nil {
		t.Fatal("original profile changed")
	}
	p.Endpoint = &Endpoint{}
	if _, err := Resolve(p); err == nil {
		t.Fatal("endpoint must be applied before host validation and clear the socket")
	}
}
