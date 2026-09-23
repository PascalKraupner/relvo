// Package control exposes a local, newline-delimited JSON-RPC control socket.
package control

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/PascalKraupner/relvo/internal/app"
)

const maxInput = 1 << 20
const requestTimeout = 30 * time.Second

var sessionPattern = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

type Dispatcher interface {
	Dispatch(context.Context, app.Action) (app.Snapshot, error)
	Snapshot() app.Snapshot
	Cancel()
}

type Server struct {
	listener    *net.UnixListener
	path        string
	identity    os.FileInfo
	app         Dispatcher
	ctx         context.Context
	cancel      context.CancelFunc
	mu          sync.Mutex
	closed      bool
	connections map[net.Conn]struct{}
	wg          sync.WaitGroup
	once        sync.Once
	closeErr    error
}

// runtimeDir never repairs permissions or follows directory symlinks.
func runtimeDir() (string, error) {
	base := os.Getenv("XDG_RUNTIME_DIR")
	var path string
	if base != "" {
		path = filepath.Join(base, "relvo")
	} else {
		var err error
		base, err = os.UserCacheDir()
		if err != nil {
			return "", err
		}
		path = filepath.Join(base, "relvo", "run")
	}
	if !filepath.IsAbs(base) {
		return "", errors.New("runtime base must be absolute")
	}
	current := string(filepath.Separator)
	for _, part := range strings.Split(strings.TrimPrefix(filepath.Clean(path), "/"), "/") {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if os.IsNotExist(err) {
			if err = os.Mkdir(current, 0700); err != nil && !os.IsExist(err) {
				return "", err
			}
			info, err = os.Lstat(current)
		}
		if err != nil {
			return "", err
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("unsafe runtime directory: %s", current)
		}
		uid := uint32(os.Geteuid())
		if stat.Uid != uid && stat.Uid != 0 {
			return "", fmt.Errorf("runtime directory is not owned by this user or root: %s", current)
		}
		// Root-owned sticky ancestors (such as /tmp) are safe to traverse.
		if info.Mode().Perm()&0022 != 0 && !(stat.Uid == 0 && info.Mode()&os.ModeSticky != 0) {
			return "", fmt.Errorf("writable runtime ancestor: %s", current)
		}
		if current == filepath.Clean(base) && stat.Uid != uid {
			return "", errors.New("runtime base is not owned by this user")
		}
		if current == filepath.Join(base, "relvo") || current == path {
			if stat.Uid != uid || info.Mode().Perm() != 0700 {
				return "", fmt.Errorf("runtime directory must be owned by this user with mode 0700: %s", current)
			}
		}
	}
	return path, nil
}

func Start(a Dispatcher, session string) (*Server, error) {
	if a == nil {
		return nil, errors.New("nil dispatcher")
	}
	if !sessionPattern.MatchString(session) {
		return nil, errors.New("session must contain only letters, digits, dash, or underscore")
	}
	dir, err := runtimeDir()
	if err != nil {
		return nil, err
	}
	path := filepath.Join(dir, session+".sock")
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		if err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("session %q already exists; refusing to replace its socket", session)
	}
	l, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		return nil, err
	}
	l.SetUnlinkOnClose(false)
	info, err := os.Lstat(path)
	if err != nil {
		l.Close()
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	s := &Server{listener: l, path: path, identity: info, app: a, ctx: ctx, cancel: cancel, connections: make(map[net.Conn]struct{})}
	if err := os.Chmod(path, 0600); err != nil {
		s.Close()
		return nil, err
	}
	s.wg.Add(1)
	go s.accept()
	return s, nil
}

func (s *Server) Path() string { return s.path }

func (s *Server) Close() error {
	s.once.Do(func() {
		s.mu.Lock()
		s.closed = true
		s.cancel()
		s.closeErr = s.listener.Close()
		for c := range s.connections {
			c.Close()
		}
		s.mu.Unlock()
		s.wg.Wait()
		if info, err := os.Lstat(s.path); err == nil && os.SameFile(s.identity, info) {
			s.closeErr = errors.Join(s.closeErr, os.Remove(s.path))
		}
	})
	return s.closeErr
}

func (s *Server) accept() {
	defer s.wg.Done()
	for {
		c, err := s.listener.Accept()
		if err != nil {
			return
		}
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			c.Close()
			return
		}
		s.connections[c] = struct{}{}
		s.wg.Add(1)
		s.mu.Unlock()
		go func() {
			defer s.wg.Done()
			defer func() { c.Close(); s.mu.Lock(); delete(s.connections, c); s.mu.Unlock() }()
			s.serve(c)
		}()
	}
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *rpcError) Error() string { return fmt.Sprintf("RPC error %d: %s", e.Code, e.Message) }

type response struct {
	Version string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  *app.Snapshot   `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}
type request struct {
	Version string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

func validID(id json.RawMessage) bool {
	if len(id) == 0 {
		return true
	}
	var v any
	d := json.NewDecoder(bytes.NewReader(id))
	d.UseNumber()
	if d.Decode(&v) != nil {
		return false
	}
	switch v.(type) {
	case nil, string, json.Number:
		return true
	}
	return false
}

func strictJSON(data []byte, v any) error {
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		return err
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return errors.New("expected one JSON value")
	}
	return nil
}

func (s *Server) serve(c net.Conn) {
	// One request per connection bounds both input and connection lifetime.
	deadline := time.Now().Add(requestTimeout)
	c.SetDeadline(deadline)
	line, err := bufio.NewReaderSize(c, maxInput+1).ReadSlice('\n')
	r := response{Version: "2.0", ID: json.RawMessage("null")}
	fail := func(code int, message string) { r.Error = &rpcError{code, message}; json.NewEncoder(c).Encode(r) }
	if len(line) > maxInput || errors.Is(err, bufio.ErrBufferFull) {
		fail(-32600, "request exceeds 1 MiB")
		return
	}
	if err != nil && err != io.EOF {
		return
	}
	if !json.Valid(line) {
		fail(-32700, "invalid JSON")
		return
	}
	var req request
	if strictJSON(line, &req) != nil || req.Version != "2.0" || req.Method == "" || !validID(req.ID) {
		fail(-32600, "invalid request")
		return
	}
	notification := len(req.ID) == 0
	if !notification {
		r.ID = req.ID
	}
	var action app.Action
	switch req.Method {
	case "state", "cancel":
		p := strings.TrimSpace(string(req.Params))
		if p != "" && p != "{}" && p != "null" {
			r.Error = &rpcError{-32602, "method takes no params"}
		} else {
			action.Type = req.Method
		}
	case "action":
		if strictJSON(req.Params, &action) != nil {
			r.Error = &rpcError{-32602, "invalid action params"}
		} else if err := validateAction(action); err != nil {
			r.Error = &rpcError{-32602, err.Error()}
		}
	default:
		r.Error = &rpcError{-32601, "method not found"}
	}
	if r.Error == nil {
		ctx, cancel := context.WithDeadline(s.ctx, deadline)
		defer cancel()
		var snapshot app.Snapshot
		var err error
		switch action.Type {
		case "state":
			snapshot = s.app.Snapshot()
		case "cancel":
			s.app.Cancel()
			snapshot = s.app.Snapshot()
		default:
			snapshot, err = s.app.Dispatch(ctx, action)
		}
		if err != nil {
			// Driver errors may contain credentials; never relay them verbatim.
			r.Error = &rpcError{-32000, "action failed; inspect the application"}
		} else {
			r.Result = &snapshot
		}
	}
	if !notification {
		json.NewEncoder(c).Encode(r)
	}
}

func validateAction(a app.Action) error {
	switch a.Type {
	case "state", "cancel", "clear-filters", "next", "prev", "refresh":
	case "connect":
		if a.Connection == "" {
			return errors.New("connection is required")
		}
	case "open-table":
		if a.Table == "" {
			return errors.New("table is required")
		}
	case "filter":
		if len(a.Filters) == 0 {
			return errors.New("at least one filter is required")
		}
		for _, f := range a.Filters {
			if f.Column == "" || f.Op == "" {
				return errors.New("filter column and op are required")
			}
		}
	case "sort":
		if a.Sort == "" {
			return errors.New("sort column is required")
		}
	case "page-size":
		if a.PageSize <= 0 {
			return errors.New("page size must be positive")
		}
	case "query", "write":
		if strings.TrimSpace(a.SQL) == "" {
			return errors.New("SQL is required")
		}
	default:
		return errors.New("unsupported action type")
	}
	return nil
}
