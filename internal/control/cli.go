package control

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/PascalKraupner/relvo/internal/app"
	"github.com/PascalKraupner/relvo/internal/database"
)

type sessionState struct {
	Session  string       `json:"session"`
	Snapshot app.Snapshot `json:"snapshot"`
}

// RunCLI accepts the arguments after "relvo ctl". Successful commands emit JSON only.
func RunCLI(ctx context.Context, args []string, out io.Writer) error {
	global := flag.NewFlagSet("ctl", flag.ContinueOnError)
	global.SetOutput(io.Discard)
	session := global.String("session", "", "exact session name")
	if err := global.Parse(args); err != nil {
		return err
	}
	args = global.Args()
	if len(args) == 0 {
		return errors.New("expected a control command")
	}
	explicitSession := false
	global.Visit(func(f *flag.Flag) {
		if f.Name == "session" {
			explicitSession = true
		}
	})
	if explicitSession && !sessionPattern.MatchString(*session) {
		return errors.New("invalid session name")
	}
	if args[0] == "sessions" {
		if len(args) != 1 || *session != "" {
			return errors.New("usage: relvo ctl sessions")
		}
		sessions, err := listSessions(ctx)
		if err != nil {
			return err
		}
		return json.NewEncoder(out).Encode(sessions)
	}
	action, err := parseAction(args)
	if err != nil {
		return err
	}
	dir, err := runtimeDir()
	if err != nil {
		return err
	}
	if *session == "" {
		sessions, err := listSessions(ctx)
		if err != nil {
			return err
		}
		if len(sessions) != 1 {
			return fmt.Errorf("found %d live sessions; specify --session NAME", len(sessions))
		}
		*session = sessions[0].Session
	}
	snapshot, err := call(ctx, filepath.Join(dir, *session+".sock"), action)
	if err != nil {
		return err
	}
	return json.NewEncoder(out).Encode(snapshot)
}

func listSessions(ctx context.Context) ([]sessionState, error) {
	dir, err := runtimeDir()
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	sessions := make([]sessionState, 0)
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		name, ok := strings.CutSuffix(entry.Name(), ".sock")
		if !ok || !sessionPattern.MatchString(name) {
			continue
		}
		probe, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
		snapshot, err := call(probe, filepath.Join(dir, entry.Name()), app.Action{Type: "state"})
		cancel()
		if err == nil {
			sessions = append(sessions, sessionState{name, snapshot})
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return sessions, nil
}

func call(ctx context.Context, path string, action app.Action) (app.Snapshot, error) {
	var zero app.Snapshot
	info, err := os.Lstat(path)
	if err != nil {
		return zero, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || info.Mode()&os.ModeSocket == 0 || stat.Uid != uint32(os.Geteuid()) || info.Mode().Perm() != 0600 {
		return zero, errors.New("unsafe control socket")
	}
	params, err := json.Marshal(action)
	if err != nil {
		return zero, err
	}
	req := request{Version: "2.0", ID: json.RawMessage(`1`), Method: "action", Params: params}
	if action.Type == "state" || action.Type == "cancel" {
		req.Method = action.Type
		req.Params = nil
	}
	data, err := json.Marshal(req)
	if err != nil {
		return zero, err
	}
	if len(data)+1 > maxInput {
		return zero, errors.New("request exceeds 1 MiB")
	}
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	c, err := (&net.Dialer{}).DialContext(ctx, "unix", path)
	if err != nil {
		return zero, err
	}
	defer c.Close()
	stop := context.AfterFunc(ctx, func() { c.Close() })
	defer stop()
	deadline, _ := ctx.Deadline()
	c.SetDeadline(deadline)
	if _, err := c.Write(append(data, '\n')); err != nil {
		return zero, err
	}
	var r response
	if err := json.NewDecoder(c).Decode(&r); err != nil {
		return zero, err
	}
	if r.Version != "2.0" || string(r.ID) != "1" || (r.Result == nil) == (r.Error == nil) {
		return zero, errors.New("invalid JSON-RPC response")
	}
	if r.Error != nil {
		return zero, r.Error
	}
	return *r.Result, nil
}

func parseAction(args []string) (app.Action, error) {
	a := app.Action{Type: args[0]}
	rest := args[1:]
	flags := flag.NewFlagSet(a.Type, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	switch a.Type {
	case "state", "cancel", "clear-filters", "next", "prev", "refresh":
		if len(rest) != 0 {
			return a, errors.New("command takes no arguments")
		}
	case "open-table", "connect", "page-size":
		if len(rest) != 1 {
			return a, errors.New("command requires exactly one argument")
		}
		switch a.Type {
		case "open-table":
			a.Table = rest[0]
		case "connect":
			a.Connection = rest[0]
		case "page-size":
			n, err := strconv.Atoi(rest[0])
			if err != nil {
				return a, errors.New("invalid page size")
			}
			a.PageSize = n
		}
	case "sort":
		if len(rest) == 0 {
			return a, errors.New("sort requires a column")
		}
		a.Sort = rest[0]
		flags.BoolVar(&a.Desc, "desc", false, "descending")
		if err := flags.Parse(rest[1:]); err != nil {
			return a, err
		}
		if flags.NArg() != 0 {
			return a, errors.New("unexpected sort arguments")
		}
	case "query", "write":
		flags.StringVar(&a.SQL, "sql", "", "SQL")
		if err := flags.Parse(rest); err != nil {
			return a, err
		}
		if flags.NArg() != 0 {
			return a, errors.New("unexpected SQL arguments")
		}
	case "filter":
		var f database.Filter
		var raw string
		flags.StringVar(&f.Column, "column", "", "column")
		flags.StringVar(&f.Op, "op", "", "operator")
		flags.StringVar(&f.Value, "value", "", "value")
		flags.StringVar(&raw, "json", "", "filter array")
		// Repeating flags is ambiguous: use --json for multiple filters instead.
		seen := make(map[string]bool)
		for i := 0; i < len(rest); i++ {
			name, _, equal := strings.Cut(strings.TrimLeft(rest[i], "-"), "=")
			if seen[name] {
				return a, fmt.Errorf("repeated filter flag %q; use --json", name)
			}
			seen[name] = true
			if !equal {
				i++
			}
		}
		if err := flags.Parse(rest); err != nil {
			return a, err
		}
		if flags.NArg() != 0 {
			return a, errors.New("unexpected filter arguments")
		}
		if seen["json"] {
			if len(seen) != 1 {
				return a, errors.New("--json cannot be combined with filter flags")
			}
			if err := strictJSON([]byte(raw), &a.Filters); err != nil {
				return a, errors.New("--json must be an array of filters")
			}
		} else {
			if !seen["column"] || !seen["op"] || !seen["value"] {
				return a, errors.New("filter requires --column, --op, and --value")
			}
			a.Filters = []database.Filter{f}
		}
	default:
		return a, fmt.Errorf("unknown control command %q", a.Type)
	}
	return a, validateAction(a)
}
