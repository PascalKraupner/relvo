package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/PascalKraupner/relvo/internal/connections"
	"github.com/PascalKraupner/relvo/internal/database"
)

func TestParseDefaults(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	o, err := parse(nil, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	want := options{
		profile:  connections.Profile{Config: database.Config{Port: "3306"}, Mapping: map[string]string{}},
		profiles: filepath.Join(dir, "relvo", "connections.json"),
		session:  fmt.Sprintf("relvo-%d", os.Getpid()),
	}
	if !reflect.DeepEqual(o, want) {
		t.Fatalf("parse defaults = %#v, want %#v", o, want)
	}
}

func TestParseFlags(t *testing.T) {
	dir := t.TempDir()
	env, err := filepath.Abs(filepath.Join("testdata", ".env"))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "profiles.json")
	socket := filepath.Join(dir, "mysql.sock")
	args := []string{
		"--name", "work", "--env", filepath.Join("testdata", ".env"),
		"--host", "db.example", "--port", "3307", "--user", "reader",
		"--database", "reports", "--socket", socket, "--tls", "off",
		"--password-env", "RELVO_MAIN_TEST_PASSWORD", "--profiles", path,
		"--connect", "work", "--session", "test-session", "--demo", "--docker",
		"--allow-writes", "--no-control", "--version",
	}
	mapping := map[string]string{}
	for _, field := range []string{"host", "port", "user", "password", "database", "socket", "tls"} {
		mapping[field] = "RELVO_MAIN_TEST_" + strings.ToUpper(field)
		args = append(args, "--map", field+"="+mapping[field])
	}
	o, err := parse(args, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	want := options{
		profile: connections.Profile{
			Name: "work", EnvFile: env, Mapping: mapping, PasswordEnv: "RELVO_MAIN_TEST_PASSWORD",
			Config: database.Config{Host: "db.example", Port: "3307", User: "reader", Database: "reports", Socket: socket, TLS: "off"},
		},
		profiles: path, connect: "work", session: "test-session",
		demo: true, docker: true, allowWrites: true, noControl: true, showVersion: true,
	}
	if !reflect.DeepEqual(o, want) {
		t.Fatalf("parse flags = %#v, want %#v", o, want)
	}
	o, err = parse([]string{"--env", env, "--map", "user=FIRST", "--map", "user=SECOND"}, io.Discard)
	if err != nil || o.profile.EnvFile != env || o.profile.Mapping["user"] != "SECOND" {
		t.Fatalf("absolute env and repeated mapping: %#v, %v", o, err)
	}
}

func TestParseRejectsInvalidArguments(t *testing.T) {
	for _, tt := range []struct {
		name string
		args []string
		want string
	}{
		{"mapping without equals", []string{"--map", "user"}, "mapping must be field=VARIABLE"},
		{"empty mapping variable", []string{"--map", "user="}, "mapping must be field=VARIABLE"},
		{"unknown mapping field", []string{"--map", "username=USER"}, "unknown mapping field"},
		{"empty mapping field", []string{"--map", "=USER"}, "unknown mapping field"},
		{"unknown flag", []string{"--unknown"}, "flag provided but not defined"},
		{"missing flag value", []string{"--host"}, "flag needs an argument"},
		{"invalid boolean", []string{"--demo=maybe"}, "invalid boolean value"},
		{"positional argument", []string{"extra"}, `unexpected argument "extra"`},
		{"trailing argument", []string{"--host", "localhost", "extra"}, `unexpected argument "extra"`},
		{"after separator", []string{"--", "extra"}, `unexpected argument "extra"`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := parse(tt.args, io.Discard)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("parse(%q) error = %v, want %q", tt.args, err, tt.want)
			}
		})
	}
}

func TestRunHelpAndVersion(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	// An unreadable-as-JSON profile proves these paths return before loading it.
	path := filepath.Join(dir, "profiles.json")
	if err := os.WriteFile(path, []byte("not JSON"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, help := range []string{"--help", "-h"} {
		t.Run(help, func(t *testing.T) {
			var out, stderr bytes.Buffer
			err := run(context.Background(), []string{"--profiles", path, help}, &out, &stderr)
			if !errors.Is(err, flag.ErrHelp) {
				t.Fatalf("help error = %v, want flag.ErrHelp (handled by main)", err)
			}
			for _, text := range []string{"Usage: relvo", "relvo ctl", "relvo connections", "-password-env", "-map"} {
				if !strings.Contains(out.String(), text) {
					t.Errorf("help missing %q: %s", text, &out)
				}
			}
			if stderr.Len() != 0 {
				t.Fatalf("help stderr = %q", &stderr)
			}
		})
	}
	var out, stderr bytes.Buffer
	if err := run(context.Background(), []string{"--profiles", path, "--version"}, &out, &stderr); err != nil {
		t.Fatal(err)
	}
	if out.String() != "relvo "+version+"\n" || stderr.Len() != 0 {
		t.Fatalf("version stdout=%q stderr=%q", &out, &stderr)
	}
}

func TestRunConnectionsAddList(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	const variable = "RELVO_MAIN_TEST_PASSWORD"
	const secret = "main-test-password-never-persist"
	t.Setenv(variable, secret)
	path := filepath.Join(dir, "profiles.json")
	args := []string{"connections", "add", "--profiles", path, "--name", "work", "--host", "localhost", "--user", "reader", "--database", "reports", "--password-env", variable}
	var out, stderr bytes.Buffer
	if err := run(context.Background(), args, &out, &stderr); err != nil {
		t.Fatal(err)
	}
	if out.String() != "Saved work. Passwords are referenced, never stored.\n" || stderr.Len() != 0 {
		t.Fatalf("add stdout=%q stderr=%q", &out, &stderr)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("profile permissions = %o, want 600", info.Mode().Perm())
	}
	out.Reset()
	if err := run(context.Background(), []string{"connections", "list", "--profiles", path}, &out, &stderr); err != nil {
		t.Fatal(err)
	}
	want := []connections.Profile{{Name: "work", PasswordEnv: variable, Config: database.Config{Host: "localhost", Port: "3306", User: "reader", Database: "reports"}}}
	for label, data := range map[string][]byte{"file": before, "list": out.Bytes()} {
		if bytes.Contains(data, []byte(secret)) || bytes.Contains(data, []byte(`"password"`)) {
			t.Errorf("%s contains a password value or field", label)
		}
		var got []connections.Profile
		if err := json.Unmarshal(data, &got); err != nil {
			t.Fatalf("%s JSON: %v", label, err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s profiles = %#v, want %#v", label, got, want)
		}
	}
	out.Reset()
	err = run(context.Background(), args, &out, &stderr)
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("duplicate error = %v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	afterInfo, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) || !os.SameFile(info, afterInfo) || !info.ModTime().Equal(afterInfo.ModTime()) {
		t.Fatal("duplicate addition rewrote profiles")
	}
	if out.Len() != 0 || stderr.Len() != 0 {
		t.Fatalf("duplicate stdout=%q stderr=%q", &out, &stderr)
	}
}

func TestRunConnectionsEnvReferences(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	const variable = "RELVO_MAIN_TEST_ENV_PASSWORD"
	const secret = "env-file-secret-never-persist"
	t.Setenv(variable, "process-secret-never-persist")
	env := filepath.Join(dir, ".env")
	if err := os.WriteFile(env, []byte("CUSTOM_HOST=localhost\nCUSTOM_USER=reader\nCUSTOM_DATABASE=reports\nCUSTOM_PASSWORD="+secret+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "profiles.json")
	args := []string{"connections", "add", "--profiles", path, "--name", "env", "--env", env, "--password-env", variable}
	mapping := map[string]string{"host": "CUSTOM_HOST", "user": "CUSTOM_USER", "database": "CUSTOM_DATABASE", "password": "CUSTOM_PASSWORD"}
	for _, field := range []string{"host", "user", "database", "password"} {
		args = append(args, "--map", field+"="+mapping[field])
	}
	var out, stderr bytes.Buffer
	if err := run(context.Background(), args, &out, &stderr); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), secret) || strings.Contains(out.String(), os.Getenv(variable)) {
		t.Fatal("add output leaked a secret")
	}
	out.Reset()
	if err := run(context.Background(), []string{"connections", "list", "--profiles", path}, &out, &stderr); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := []connections.Profile{{Name: "env", EnvFile: env, Mapping: mapping, PasswordEnv: variable, Config: database.Config{Port: "3306"}}}
	for label, raw := range map[string][]byte{"file": data, "list": out.Bytes()} {
		if bytes.Contains(raw, []byte(secret)) || bytes.Contains(raw, []byte(os.Getenv(variable))) {
			t.Errorf("%s leaked a secret", label)
		}
		var got []connections.Profile
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s must retain references rather than resolved values: %#v", label, got)
		}
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q", &stderr)
	}
}

func TestRunRejectsMalformedCommands(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("XDG_RUNTIME_DIR", dir)
	path := filepath.Join(dir, "profiles.json")
	for _, tt := range []struct {
		name string
		args []string
		want string
	}{
		{"root unknown", []string{"--unknown"}, "flag provided but not defined"},
		{"root missing value", []string{"--host"}, "flag needs an argument"},
		{"root positional", []string{"unexpected"}, "unexpected argument"},
		{"connections missing command", []string{"connections"}, "usage: relvo connections"},
		{"connections unknown", []string{"connections", "unknown", "--profiles", path}, "usage: relvo connections"},
		{"add missing name", []string{"connections", "add", "--profiles", path, "--host", "localhost"}, "add requires --name"},
		{"add missing connection", []string{"connections", "add", "--profiles", path, "--name", "work"}, "add requires --name"},
		{"add missing user", []string{"connections", "add", "--profiles", path, "--name", "work", "--host", "localhost", "--database", "reports"}, "connection requires user"},
		{"list extra argument", []string{"connections", "list", "--profiles", path, "extra"}, "unexpected argument"},
		{"ctl missing command", []string{"ctl"}, "expected a control command"},
		{"ctl missing session value", []string{"ctl", "--session"}, "flag needs an argument"},
		{"ctl invalid session", []string{"ctl", "--session", "../invalid", "state"}, "invalid session name"},
		{"ctl unknown command", []string{"ctl", "unknown"}, "unknown control command"},
		{"ctl extra argument", []string{"ctl", "state", "extra"}, "command takes no arguments"},
		{"ctl missing argument", []string{"ctl", "open-table"}, "command requires exactly one argument"},
		{"ctl malformed JSON", []string{"ctl", "filter", "--json", "{"}, "--json must be an array of filters"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var out, stderr bytes.Buffer
			err := run(context.Background(), tt.args, &out, &stderr)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("run(%q) error = %v, want %q", tt.args, err, tt.want)
			}
			if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("rejected command created profiles: %v", err)
			}
		})
	}
}

func TestRunRejectsMalformedProfiles(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	path := filepath.Join(dir, "profiles.json")
	const malformed = "{not JSON"
	if err := os.WriteFile(path, []byte(malformed), 0600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"--profiles", path},
		{"connections", "list", "--profiles", path},
		{"connections", "add", "--profiles", path, "--name", "work", "--host", "localhost", "--user", "reader", "--database", "reports"},
	} {
		var out, stderr bytes.Buffer
		err := run(context.Background(), args, &out, &stderr)
		if err == nil || !strings.Contains(err.Error(), "invalid connection profiles JSON") {
			t.Fatalf("run(%q) error = %v", args, err)
		}
		if out.Len() != 0 || stderr.Len() != 0 {
			t.Fatalf("malformed profiles stdout=%q stderr=%q", &out, &stderr)
		}
		data, err := os.ReadFile(path)
		if err != nil || string(data) != malformed {
			t.Fatalf("malformed profiles changed: %q, %v", data, err)
		}
	}
}
