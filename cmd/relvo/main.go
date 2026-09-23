package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/PascalKraupner/relvo/internal/app"
	"github.com/PascalKraupner/relvo/internal/connections"
	"github.com/PascalKraupner/relvo/internal/control"
	"github.com/PascalKraupner/relvo/internal/database"
	"github.com/PascalKraupner/relvo/internal/database/demo"
	"github.com/PascalKraupner/relvo/internal/database/mysql"
	"github.com/PascalKraupner/relvo/internal/tui"
)

var version = "0.1.0-dev"

type mappings map[string]string

func (m mappings) String() string { return "field=VARIABLE" }
func (m mappings) Set(value string) error {
	k, v, ok := strings.Cut(value, "=")
	if !ok || v == "" {
		return errors.New("mapping must be field=VARIABLE")
	}
	switch k {
	case "host", "port", "user", "password", "database", "socket", "tls":
	default:
		return fmt.Errorf("unknown mapping field %q", k)
	}
	m[k] = v
	return nil
}

type options struct {
	profile                                           connections.Profile
	profiles, connect, session                        string
	demo, docker, allowWrites, noControl, showVersion bool
}

func parse(args []string, out io.Writer) (options, error) {
	o := options{profile: connections.Profile{Mapping: make(mappings)}}
	f := flag.NewFlagSet("relvo", flag.ContinueOnError)
	f.SetOutput(out)
	f.StringVar(&o.profile.Name, "name", "", "name for an explicit connection")
	f.StringVar(&o.profile.EnvFile, "env", "", "environment file (Laravel variables by default)")
	f.Var(mappings(o.profile.Mapping), "map", "map a connection field, e.g. --map user=MYSQL_USER (repeatable)")
	f.StringVar(&o.profile.Config.Host, "host", "", "database hostname")
	f.StringVar(&o.profile.Config.Port, "port", "3306", "database TCP port")
	f.StringVar(&o.profile.Config.User, "user", "", "database username")
	f.StringVar(&o.profile.Config.Database, "database", "", "database name")
	f.StringVar(&o.profile.Config.Socket, "socket", "", "absolute MySQL Unix socket path")
	f.StringVar(&o.profile.Config.TLS, "tls", "", "TLS: true (verified, default) or off (trusted local databases)")
	f.StringVar(&o.profile.PasswordEnv, "password-env", "", "process environment variable containing the password")
	f.StringVar(&o.profiles, "profiles", connections.DefaultPath(), "saved connection profiles JSON")
	f.StringVar(&o.connect, "connect", "", "connect to a candidate by name on startup")
	f.StringVar(&o.session, "session", fmt.Sprintf("relvo-%d", os.Getpid()), "local control session name")
	f.BoolVar(&o.demo, "demo", false, "explore a seeded database without a server")
	f.BoolVar(&o.docker, "docker", false, "offer published endpoints from this project's running Compose services")
	f.BoolVar(&o.allowWrites, "allow-writes", false, "allow staging SQL writes; each requires local TUI approval")
	f.BoolVar(&o.noControl, "no-control", false, "disable the local AI control socket")
	f.BoolVar(&o.showVersion, "version", false, "print version")
	f.Usage = func() {
		fmt.Fprintln(out, "Relvo - explore your data, stay in flow.\n\nUsage: relvo [options]\n       relvo ctl [--session NAME] COMMAND\n       relvo connections list [--profiles PATH]\n       relvo connections add --name NAME [connection options]\n\nKeyboard: c connections, n manual setup, e env setup, ? help.\n\nOptions:")
		f.PrintDefaults()
	}
	if err := f.Parse(args); err != nil {
		return o, err
	}
	if f.NArg() != 0 {
		return o, fmt.Errorf("unexpected argument %q", f.Arg(0))
	}
	if o.profile.EnvFile != "" {
		path, err := filepath.Abs(o.profile.EnvFile)
		if err != nil {
			return o, err
		}
		o.profile.EnvFile = path
	}
	return o, nil
}

func explicit(o options) bool {
	return o.profile.EnvFile != "" || o.profile.Config.Host != "" || o.profile.Config.Socket != "" || o.profile.Config.Database != ""
}

func profileCommand(args []string, out io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: relvo connections list|add [options]")
	}
	o, err := parse(args[1:], out)
	if err != nil {
		return err
	}
	profiles, err := connections.Load(o.profiles)
	if err != nil {
		return err
	}
	switch args[0] {
	case "list":
		// Config.Password has json:"-" and is never printed.
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		return enc.Encode(profiles)
	case "add":
		if o.profile.Name == "" || !explicit(o) {
			return errors.New("add requires --name and --env, --host, or --socket with database details")
		}
		if err := connections.Add(o.profiles, o.profile); err != nil {
			return err
		}
		fmt.Fprintf(out, "Saved %s. Passwords are referenced, never stored.\n", o.profile.Name)
		return nil
	default:
		return errors.New("usage: relvo connections list|add [options]")
	}
}

func run(ctx context.Context, args []string, out, stderr io.Writer) error {
	if len(args) > 0 {
		switch args[0] {
		case "ctl":
			return control.RunCLI(ctx, args[1:], out)
		case "connections":
			return profileCommand(args[1:], out)
		}
	}
	o, err := parse(args, out)
	if err != nil {
		return err
	}
	if o.showVersion {
		fmt.Fprintln(out, "relvo "+version)
		return nil
	}
	profiles, err := connections.Load(o.profiles)
	if err != nil {
		return err
	}
	dir, err := os.Getwd()
	if err != nil {
		return err
	}
	if explicit(o) {
		if o.profile.Name == "" {
			o.profile.Name = "project"
		}
		profiles = append(profiles, o.profile)
	} else if !o.demo {
		found, err := connections.Discover(dir)
		if err != nil {
			fmt.Fprintln(stderr, "Discovery:", err)
		} else {
			for _, p := range found {
				p.Config.TLS = o.profile.Config.TLS
				exists := false
				for _, old := range profiles {
					if old.EnvFile == p.EnvFile {
						exists = true
					}
				}
				if !exists {
					profiles = append(profiles, p)
				}
			}
		}
	}
	if o.docker {
		for _, p := range append([]connections.Profile(nil), profiles...) {
			cfg, err := connections.Resolve(p)
			if err != nil {
				continue
			}
			probe, cancel := context.WithTimeout(ctx, 5*time.Second)
			candidates, err := connections.DiscoverDocker(probe, dir, cfg)
			cancel()
			if err != nil {
				fmt.Fprintln(stderr, "Docker discovery:", err)
				continue
			}
			for _, cfg := range candidates {
				candidate := p
				candidate.Name = cfg.Name
				candidate.Endpoint = &connections.Endpoint{Host: cfg.Host, Port: cfg.Port}
				profiles = append(profiles, candidate)
			}
		}
	}
	if o.demo {
		profiles = append(profiles, connections.Profile{Name: "demo", Config: database.Config{Host: "demo", User: "demo", Database: "relvo_demo"}})
		if o.connect == "" {
			o.connect = "demo"
		}
	}
	open := func(ctx context.Context, cfg database.Config) (database.Store, error) {
		if o.demo && cfg.Name == "demo" {
			return demo.Open(ctx, cfg)
		}
		return mysql.Open(ctx, cfg)
	}
	a, err := app.New(app.Options{Profiles: profiles, Open: open, AllowWrites: o.allowWrites})
	if err != nil {
		return err
	}
	defer a.Close()
	if !o.noControl {
		server, err := control.Start(a, o.session)
		if err != nil {
			return fmt.Errorf("local control: %w (use --no-control to disable)", err)
		}
		defer server.Close()
	}
	program := tea.NewProgram(tui.New(a), tea.WithContext(ctx))
	if o.connect != "" {
		go func() { _, _ = a.Dispatch(ctx, app.Action{Type: "connect", Connection: o.connect}) }()
	}
	_, err = program.Run()
	if errors.Is(err, tea.ErrProgramKilled) && ctx.Err() != nil {
		return nil
	}
	return err
}

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM)
	defer cancel()
	if err := run(ctx, os.Args[1:], os.Stdout, os.Stderr); err != nil && !errors.Is(err, flag.ErrHelp) {
		fmt.Fprintln(os.Stderr, "relvo:", err)
		os.Exit(1)
	}
}
