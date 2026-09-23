package control

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/PascalKraupner/relvo/internal/app"
	"github.com/PascalKraupner/relvo/internal/database"
)

type fakeDispatcher struct {
	mu        sync.Mutex
	actions   []app.Action
	cancelled bool
	dispatch  func(context.Context, app.Action) (app.Snapshot, error)
}

func (f *fakeDispatcher) Snapshot() app.Snapshot {
	return app.Snapshot{Connection: "local", Database: "demo", Result: database.Result{Columns: []string{"id"}, Rows: [][]database.Value{{{Text: "9007199254740993"}}}}}
}
func (f *fakeDispatcher) Cancel() { f.mu.Lock(); defer f.mu.Unlock(); f.cancelled = true }
func (f *fakeDispatcher) Dispatch(ctx context.Context, a app.Action) (app.Snapshot, error) {
	f.mu.Lock()
	f.actions = append(f.actions, a)
	f.mu.Unlock()
	if f.dispatch != nil {
		return f.dispatch(ctx, a)
	}
	s := f.Snapshot()
	if a.Type == "write" {
		s.Pending = &app.PendingWrite{ID: "pending", SQL: a.SQL}
	}
	return s, nil
}

func setupRuntime(t *testing.T) string {
	t.Helper()
	// Keep socket paths below the Unix-domain address length limit.
	dir, err := os.MkdirTemp("", "relvo-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	// macOS temporary directories commonly start with the /var symlink.
	dir, err = filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_RUNTIME_DIR", dir)
	return dir
}
func startTest(t *testing.T, f Dispatcher, name string) *Server {
	t.Helper()
	s, err := Start(f, name)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}
func exchange(t *testing.T, path, input string) response {
	t.Helper()
	c, err := net.Dial("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err := io.WriteString(c, input+"\n"); err != nil {
		t.Fatal(err)
	}
	var r response
	if err := json.NewDecoder(c).Decode(&r); err != nil {
		t.Fatal(err)
	}
	return r
}

func TestProtocol(t *testing.T) {
	setupRuntime(t)
	f := &fakeDispatcher{}
	s := startTest(t, f, "one")
	cases := []struct {
		name, input, id string
		code            int
	}{
		{"numeric ID", `{"jsonrpc":"2.0","id":9007199254740993,"method":"state"}`, `9007199254740993`, 0},
		{"string ID", `{"jsonrpc":"2.0","id":"abc","method":"state"}`, `"abc"`, 0},
		{"null ID", `{"jsonrpc":"2.0","id":null,"method":"state"}`, `null`, 0},
		{"invalid ID", `{"jsonrpc":"2.0","id":true,"method":"state"}`, `null`, -32600},
		{"object ID", `{"jsonrpc":"2.0","id":{},"method":"state"}`, `null`, -32600},
		{"version", `{"jsonrpc":"1.0","id":1,"method":"state"}`, `null`, -32600},
		{"parse", `{`, `null`, -32700},
		{"batch", `[]`, `null`, -32600},
		{"unknown method", `{"jsonrpc":"2.0","id":2,"method":"approve"}`, `2`, -32601},
		{"reject method", `{"jsonrpc":"2.0","id":2,"method":"reject"}`, `2`, -32601},
		{"approval action", `{"jsonrpc":"2.0","id":3,"method":"action","params":{"type":"approve"}}`, `3`, -32602},
		{"unknown field", `{"jsonrpc":"2.0","id":3,"method":"action","params":{"type":"write","sql":"DELETE FROM x","approve":true}}`, `3`, -32602},
		{"state params", `{"jsonrpc":"2.0","id":1,"method":"state","params":{"sql":"x"}}`, `1`, -32602},
		{"invalid action", `{"jsonrpc":"2.0","id":1,"method":"action","params":{"type":"page-size","page_size":0}}`, `1`, -32602},
		{"action state", `{"jsonrpc":"2.0","id":1,"method":"action","params":{"type":"state"}}`, `1`, 0},
		{"cancel", `{"jsonrpc":"2.0","id":1,"method":"cancel"}`, `1`, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := exchange(t, s.Path(), tc.input)
			if r.Version != "2.0" || string(r.ID) != tc.id {
				t.Fatalf("invalid envelope: %+v", r)
			}
			if tc.code == 0 {
				if r.Error != nil || r.Result == nil {
					t.Fatalf("unexpected response: %+v", r)
				}
			} else if r.Error == nil || r.Error.Code != tc.code {
				t.Fatalf("wanted code %d: %+v", tc.code, r)
			}
		})
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.cancelled || len(f.actions) != 0 {
		t.Fatalf("unexpected dispatch: %+v", f.actions)
	}
}

func TestInputLimitAndNotification(t *testing.T) {
	setupRuntime(t)
	s := startTest(t, &fakeDispatcher{}, "limit")
	r := exchange(t, s.Path(), strings.Repeat(" ", maxInput))
	if r.Error == nil || r.Error.Code != -32600 {
		t.Fatalf("oversized request accepted: %+v", r)
	}
	base := `{"jsonrpc":"2.0","id":1,"method":"state"}`
	r = exchange(t, s.Path(), base+strings.Repeat(" ", maxInput-len(base)-1))
	if r.Error != nil {
		t.Fatalf("boundary request rejected: %+v", r.Error)
	}
	c, err := net.Dial("unix", s.Path())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(time.Second))
	io.WriteString(c, "{\"jsonrpc\":\"2.0\",\"method\":\"state\"}\n")
	if _, err := bufio.NewReader(c).ReadByte(); err != io.EOF {
		t.Fatalf("notification should close without response: %v", err)
	}
}

func TestPermissionsAndCollisions(t *testing.T) {
	dir := setupRuntime(t)
	s := startTest(t, &fakeDispatcher{}, "session")
	for path, mode := range map[string]os.FileMode{filepath.Join(dir, "relvo"): 0700, s.Path(): 0600} {
		info, err := os.Lstat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != mode {
			t.Fatalf("%s mode %o", path, info.Mode().Perm())
		}
	}
	if _, err := Start(&fakeDispatcher{}, "session"); err == nil {
		t.Fatal("active socket replaced")
	}
	stale := filepath.Join(dir, "relvo", "stale.sock")
	l, err := net.ListenUnix("unix", &net.UnixAddr{Name: stale, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	l.SetUnlinkOnClose(false)
	l.Close()
	if _, err := Start(&fakeDispatcher{}, "stale"); err == nil {
		t.Fatal("stale socket replaced")
	}
	for _, name := range []string{"", "../escape", "a.b", "a/b", "a b"} {
		if _, err := Start(&fakeDispatcher{}, name); err == nil {
			t.Fatalf("accepted session %q", name)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(s.Path()); !os.IsNotExist(err) {
		t.Fatalf("socket not removed: %v", err)
	}
	if _, err := os.Lstat(stale); err != nil {
		t.Fatal("removed unrelated stale socket")
	}
}

func TestUnsafeDirectory(t *testing.T) {
	for _, kind := range []string{"symlink", "shared", "nonowner"} {
		t.Run(kind, func(t *testing.T) {
			dir := setupRuntime(t)
			path := filepath.Join(dir, "relvo")
			if kind == "symlink" {
				if err := os.Symlink(dir, path); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.Mkdir(path, 0700); err != nil {
					t.Fatal(err)
				}
				if kind == "shared" {
					if err := os.Chmod(path, 0755); err != nil {
						t.Fatal(err)
					}
				} else {
					if os.Geteuid() != 0 {
						t.Skip("changing directory owner requires root")
					}
					if err := os.Chown(path, 12345, 12345); err != nil {
						t.Fatal(err)
					}
				}
			}
			if _, err := Start(&fakeDispatcher{}, "unsafe"); err == nil {
				t.Fatal("unsafe directory accepted")
			}
			if kind == "shared" {
				info, _ := os.Lstat(path)
				if info.Mode().Perm() != 0755 {
					t.Fatal("changed existing directory permissions")
				}
			}
		})
	}
}

func TestClosePreservesReplacement(t *testing.T) {
	setupRuntime(t)
	s := startTest(t, &fakeDispatcher{}, "replace")
	if err := os.Remove(s.Path()); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(s.Path(), []byte("replacement"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(s.Path())
	if err != nil || string(data) != "replacement" {
		t.Fatalf("replacement removed: %v", err)
	}
}

func TestShutdownOngoingRequests(t *testing.T) {
	setupRuntime(t)
	entered := make(chan struct{})
	cancelled := make(chan struct{})
	f := &fakeDispatcher{dispatch: func(ctx context.Context, a app.Action) (app.Snapshot, error) {
		close(entered)
		<-ctx.Done()
		close(cancelled)
		return app.Snapshot{}, ctx.Err()
	}}
	s := startTest(t, f, "blocking")
	c, err := net.Dial("unix", s.Path())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	io.WriteString(c, "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"action\",\"params\":{\"type\":\"refresh\"}}\n")
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("dispatch not entered")
	}
	idle, err := net.Dial("unix", s.Path())
	if err != nil {
		t.Fatal(err)
	}
	defer idle.Close()
	done := make(chan error, 1)
	go func() { done <- s.Close() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("shutdown blocked")
	}
	select {
	case <-cancelled:
	default:
		t.Fatal("dispatch context not cancelled")
	}
}

func TestCLI(t *testing.T) {
	setupRuntime(t)
	f := &fakeDispatcher{}
	startTest(t, f, "one")
	var out bytes.Buffer
	if err := RunCLI(context.Background(), []string{"state"}, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "9007199254740993") {
		t.Fatal("row data missing")
	}
	out.Reset()
	if err := RunCLI(context.Background(), []string{"--session", "one", "write", "--sql", "DELETE FROM x"}, &out); err != nil {
		t.Fatal(err)
	}
	var snapshot app.Snapshot
	if err := json.Unmarshal(out.Bytes(), &snapshot); err != nil {
		t.Fatal(err)
	}
	if snapshot.Pending == nil || snapshot.Pending.SQL != "DELETE FROM x" {
		t.Fatal("write was not staged")
	}
	startTest(t, &fakeDispatcher{}, "two")
	if err := RunCLI(context.Background(), []string{"state"}, io.Discard); err == nil {
		t.Fatal("ambiguous session accepted")
	}
	out.Reset()
	if err := RunCLI(context.Background(), []string{"sessions"}, &out); err != nil {
		t.Fatal(err)
	}
	var sessions []sessionState
	if err := json.Unmarshal(out.Bytes(), &sessions); err != nil || len(sessions) != 2 {
		t.Fatalf("sessions: %s (%v)", out.String(), err)
	}
	for _, cmd := range []string{"approve", "reject"} {
		if err := RunCLI(context.Background(), []string{"--session", "one", cmd}, io.Discard); err == nil {
			t.Fatal("approval command accepted")
		}
	}
}

func TestParseAction(t *testing.T) {
	cases := []struct {
		args []string
		want app.Action
	}{
		{[]string{"open-table", "users"}, app.Action{Type: "open-table", Table: "users"}},
		{[]string{"connect", "local"}, app.Action{Type: "connect", Connection: "local"}},
		{[]string{"filter", "--column", "id", "--op", "eq", "--value", ""}, app.Action{Type: "filter", Filters: []database.Filter{{Column: "id", Op: "eq", Value: ""}}}},
		{[]string{"filter", "--json", `[{"column":"id","op":"eq","value":"1"}]`}, app.Action{Type: "filter", Filters: []database.Filter{{Column: "id", Op: "eq", Value: "1"}}}},
		{[]string{"sort", "id", "--desc"}, app.Action{Type: "sort", Sort: "id", Desc: true}},
		{[]string{"page-size", "50"}, app.Action{Type: "page-size", PageSize: 50}},
		{[]string{"query", "--sql", "SELECT 1"}, app.Action{Type: "query", SQL: "SELECT 1"}},
	}
	for _, tc := range cases {
		got, err := parseAction(tc.args)
		if err != nil || !reflect.DeepEqual(got, tc.want) {
			t.Fatalf("%v: %+v, %v", tc.args, got, err)
		}
	}
	for _, args := range [][]string{{"page-size", "0"}, {"sort", "id", "junk"}, {"query", "--sql", ""}, {"state", "extra"}, {"filter", "--column", "id", "--op", "eq"}, {"filter", "--json", "null"}, {"filter", "--json", "[]", "--value", "x"}, {"filter", "--column", "id", "--column", "name", "--op", "eq", "--value", "x"}} {
		if _, err := parseAction(args); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
}

func TestDispatchErrorDoesNotLeak(t *testing.T) {
	setupRuntime(t)
	s := startTest(t, &fakeDispatcher{dispatch: func(context.Context, app.Action) (app.Snapshot, error) {
		return app.Snapshot{}, errors.New("password=secret")
	}}, "error")
	r := exchange(t, s.Path(), `{"jsonrpc":"2.0","id":1,"method":"action","params":{"type":"refresh"}}`)
	if r.Error == nil || r.Error.Code != -32000 || strings.Contains(r.Error.Message, "secret") {
		t.Fatalf("unsafe error: %+v", r)
	}
}

func TestClientValidatesResponse(t *testing.T) {
	for _, wire := range []string{
		`{"jsonrpc":"1.0","id":1,"result":{}}`,
		`{"jsonrpc":"2.0","id":2,"result":{}}`,
		`{"jsonrpc":"2.0","id":"1","result":{}}`,
		`{"jsonrpc":"2.0","id":true,"result":{}}`,
		`{"jsonrpc":"2.0","id":1,"result":{},"error":{"code":-32000,"message":"bad"}}`,
		`{"jsonrpc":"2.0","id":1}`,
	} {
		t.Run(wire, func(t *testing.T) {
			dir := setupRuntime(t)
			path := filepath.Join(dir, "peer.sock")
			l, err := net.Listen("unix", path)
			if err != nil {
				t.Fatal(err)
			}
			defer l.Close()
			if err := os.Chmod(path, 0600); err != nil {
				t.Fatal(err)
			}
			done := make(chan struct{})
			go func() {
				defer close(done)
				c, err := l.Accept()
				if err != nil {
					return
				}
				defer c.Close()
				c.SetDeadline(time.Now().Add(time.Second))
				bufio.NewReader(c).ReadString('\n')
				io.WriteString(c, wire+"\n")
			}()
			if _, err := call(context.Background(), path, app.Action{Type: "state"}); err == nil {
				t.Fatal("invalid response accepted")
			}
			<-done
		})
	}
}

func TestDiscoverySkipsStaleAndClientCancellation(t *testing.T) {
	dir := setupRuntime(t)
	if err := RunCLI(context.Background(), []string{"state"}, io.Discard); err == nil {
		t.Fatal("missing session accepted")
	}
	path := filepath.Join(dir, "relvo", "stale.sock")
	l, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	l.SetUnlinkOnClose(false)
	l.Close()
	startTest(t, &fakeDispatcher{}, "live")
	sessions, err := listSessions(context.Background())
	if err != nil || len(sessions) != 1 || sessions[0].Session != "live" {
		t.Fatalf("sessions: %+v, %v", sessions, err)
	}
	if err := RunCLI(context.Background(), []string{"--session", "", "state"}, io.Discard); err == nil {
		t.Fatal("empty explicit session accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := RunCLI(ctx, []string{"--session", "live", "state"}, io.Discard); err == nil {
		t.Fatal("cancelled call succeeded")
	}
}

func TestRuntimeFallbackAndAncestorSymlink(t *testing.T) {
	dir := setupRuntime(t)
	t.Setenv("XDG_RUNTIME_DIR", "")
	t.Setenv("XDG_CACHE_HOME", dir)
	// UserCacheDir uses the platform's cache path on macOS instead of XDG_CACHE_HOME.
	base, err := os.UserCacheDir()
	if err != nil {
		t.Fatal(err)
	}
	if base == dir {
		got, err := runtimeDir()
		if err != nil || got != filepath.Join(dir, "relvo", "run") {
			t.Fatalf("fallback: %s, %v", got, err)
		}
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(dir, link); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_RUNTIME_DIR", link)
	if _, err := runtimeDir(); err == nil {
		t.Fatal("symlink ancestor accepted")
	}
}
