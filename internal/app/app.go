// Package app owns session state and the actions shared by humans and local tools.
package app

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/PascalKraupner/relvo/internal/connections"
	"github.com/PascalKraupner/relvo/internal/database"
)

type App struct {
	mu       sync.Mutex
	op       sync.Mutex
	state    Snapshot
	profiles []connections.Profile
	open     OpenFunc
	store    database.Store
	password string
	views    map[string]database.BrowseRequest
	events   chan Snapshot
	cancel   context.CancelFunc
	closed   bool
}

func New(opts Options) (*App, error) {
	if opts.Open == nil {
		return nil, errors.New("database opener is required")
	}
	a := &App{open: opts.Open, views: make(map[string]database.BrowseRequest), events: make(chan Snapshot, 1)}
	a.state = Snapshot{Browse: database.BrowseRequest{Limit: 100}, Status: "Choose a connection with c, add one with n, or open an .env with e", WritesEnabled: opts.AllowWrites}
	for _, p := range opts.Profiles {
		if err := a.AddProfile(p); err != nil {
			return nil, err
		}
	}
	return a, nil
}

// Snapshots are owned by the receiver, never shared mutable UI state.
func clone(s Snapshot) Snapshot {
	s.Connections = slices.Clone(s.Connections)
	s.Tables = slices.Clone(s.Tables)
	s.Browse.Filters = slices.Clone(s.Browse.Filters)
	s.Schema.Columns = slices.Clone(s.Schema.Columns)
	for i, c := range s.Schema.Columns {
		if c.Default != nil {
			d := *c.Default
			s.Schema.Columns[i].Default = &d
		}
	}
	s.Schema.Indexes = slices.Clone(s.Schema.Indexes)
	for i := range s.Schema.Indexes {
		s.Schema.Indexes[i].Columns = slices.Clone(s.Schema.Indexes[i].Columns)
	}
	s.Schema.ForeignKeys = slices.Clone(s.Schema.ForeignKeys)
	s.Result.Columns = slices.Clone(s.Result.Columns)
	s.Result.Rows = slices.Clone(s.Result.Rows)
	for i := range s.Result.Rows {
		s.Result.Rows[i] = slices.Clone(s.Result.Rows[i])
	}
	if s.Pending != nil {
		p := *s.Pending
		s.Pending = &p
	}
	return s
}

func (a *App) Snapshot() Snapshot      { a.mu.Lock(); defer a.mu.Unlock(); return clone(a.state) }
func (a *App) Events() <-chan Snapshot { return a.events }

// A slow renderer needs the newest state, not a backlog of intermediate frames.
func (a *App) publishLocked() {
	a.state.Revision++
	select {
	case <-a.events:
	default:
	}
	a.events <- clone(a.state)
}

func (a *App) AddProfile(p connections.Profile) error {
	if !a.op.TryLock() {
		return errors.New("another operation is in progress")
	}
	defer a.op.Unlock()
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return errors.New("application is closed")
	}
	if strings.TrimSpace(p.Name) == "" {
		return errors.New("connection name is required")
	}
	for _, old := range a.profiles {
		if old.Name == p.Name {
			return fmt.Errorf("connection %q already exists", p.Name)
		}
	}
	p.Mapping = maps.Clone(p.Mapping)
	if p.Endpoint != nil {
		endpoint := *p.Endpoint
		p.Endpoint = &endpoint
	}
	a.profiles = append(a.profiles, p)
	a.state.Connections = append(a.state.Connections, p.Name)
	a.publishLocked()
	return nil
}

func (a *App) Cancel() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.cancel != nil {
		a.cancel()
	}
}

func (a *App) Close() error {
	a.Cancel()
	a.op.Lock()
	defer a.op.Unlock()
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return nil
	}
	a.closed = true
	close(a.events)
	if a.store != nil {
		return a.store.Close()
	}
	return nil
}

func (a *App) begin(ctx context.Context) (context.Context, Snapshot, error) {
	if !a.op.TryLock() {
		return ctx, a.Snapshot(), errors.New("another operation is in progress; cancel it or wait")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		a.op.Unlock()
		return ctx, clone(a.state), errors.New("application is closed")
	}
	ctx, a.cancel = context.WithTimeout(ctx, 30*time.Second)
	a.state.Busy = true
	a.state.Error = ""
	a.publishLocked()
	return ctx, clone(a.state), nil
}

func (a *App) finish(s Snapshot, err error) (Snapshot, error) {
	a.mu.Lock()
	a.cancel()
	a.cancel = nil
	if err != nil {
		message := err.Error()
		if a.password != "" {
			message = strings.ReplaceAll(message, a.password, "[redacted]")
		}
		err = errors.New(message)
		a.state.Error = message
	} else {
		s.Revision = a.state.Revision
		a.state = s
	}
	a.state.Busy = false
	a.publishLocked()
	out := clone(a.state)
	a.mu.Unlock()
	a.op.Unlock()
	return out, err
}

func (a *App) Dispatch(ctx context.Context, action Action) (Snapshot, error) {
	if action.Type == "state" {
		return a.Snapshot(), nil
	}
	if action.Type == "cancel" {
		a.Cancel()
		return a.Snapshot(), nil
	}
	ctx, s, err := a.begin(ctx)
	if err != nil {
		return s, err
	}
	s, err = a.perform(ctx, s, action)
	return a.finish(s, err)
}

// Setup tests a draft without saving it, or adds and connects it as one operation.
// Failed drafts never reserve a profile name or replace the current connection.
func (a *App) Setup(ctx context.Context, p connections.Profile, test bool) (Snapshot, error) {
	ctx, s, err := a.begin(ctx)
	if err != nil {
		return s, err
	}
	if strings.TrimSpace(p.Name) == "" {
		return a.finish(s, errors.New("connection name is required"))
	}
	for _, old := range a.profiles {
		if old.Name == p.Name {
			return a.finish(s, errors.New("connection name already exists; choose a new name"))
		}
	}
	s, err = a.connectProfile(ctx, s, p, test)
	if err == nil && !test {
		p.Mapping = maps.Clone(p.Mapping)
		if p.Endpoint != nil {
			endpoint := *p.Endpoint
			p.Endpoint = &endpoint
		}
		a.profiles = append(a.profiles, p)
		s.Connections = append(s.Connections, p.Name)
	}
	return a.finish(s, err)
}

func (a *App) perform(ctx context.Context, s Snapshot, action Action) (Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return s, err
	}
	if action.Type == "connect" || action.Type == "test" {
		var profile *connections.Profile
		for i := range a.profiles {
			if a.profiles[i].Name == action.Connection {
				profile = &a.profiles[i]
				break
			}
		}
		if profile == nil {
			return s, errors.New("unknown connection")
		}
		return a.connectProfile(ctx, s, *profile, action.Type == "test")
	}
	if a.store == nil {
		return s, errors.New("choose a connection first")
	}
	if s.Query {
		switch action.Type {
		case "filter", "clear-filters", "sort", "page-size", "next", "prev":
			return s, errors.New("raw query results are bounded; edit SQL to filter or paginate")
		}
	}
	switch action.Type {
	case "open-table":
		if !hasTable(s.Tables, action.Table) {
			return s, errors.New("unknown table; refresh the table list if it was recently created")
		}
		s.Browse = database.BrowseRequest{Table: action.Table, Limit: s.Browse.Limit, Filters: slices.Clone(action.Filters)}
	case "filter", "clear-filters":
		if action.Type == "clear-filters" {
			action.Filters = nil
		}
		s.Browse.Filters = slices.Clone(action.Filters)
		s.Browse.Offset = 0
	case "sort":
		s.Browse.Sort = action.Sort
		s.Browse.Desc = action.Desc
		s.Browse.Offset = 0
	case "page-size":
		if action.PageSize < 1 || action.PageSize > 1000 {
			return s, errors.New("page size must be between 1 and 1000")
		}
		s.Browse.Limit = action.PageSize
		s.Browse.Offset = 0
	case "next":
		if !s.Result.HasMore {
			return s, nil
		}
		if s.Browse.Offset > int(^uint(0)>>1)-s.Browse.Limit {
			return s, errors.New("page offset overflow")
		}
		s.Browse.Offset += s.Browse.Limit
	case "prev":
		s.Browse.Offset = max(0, s.Browse.Offset-s.Browse.Limit)
	case "refresh":
		tables, err := a.store.Tables(ctx)
		if err != nil {
			return s, err
		}
		s.Tables = tables
		if !hasTable(tables, s.Browse.Table) {
			s.Browse = database.BrowseRequest{Limit: 100}
			s.Schema = database.Schema{}
			s.Result = database.Result{}
			s.Query = false
			s.Status = "Table list refreshed"
			return s, nil
		}
		loaded, err := a.browse(ctx, s)
		if err != nil {
			s.Result = database.Result{}
			s.Query = false
			s.Status = "Table list refreshed; current table could not reload"
			s.Error = err.Error()
			return s, nil
		}
		return loaded, nil
	case "query":
		if strings.TrimSpace(action.SQL) == "" {
			return s, errors.New("SQL is empty")
		}
		result, err := a.store.Query(ctx, action.SQL)
		if err != nil {
			return s, err
		}
		s.Result = result
		s.Query = true
		s.Status = fmt.Sprintf("Query returned %d rows (maximum 1000)", len(result.Rows))
		return s, nil
	case "write":
		if !s.WritesEnabled {
			return s, errors.New("writes are disabled; restart with --allow-writes and use a suitable database account")
		}
		if strings.TrimSpace(action.SQL) == "" || len(action.SQL) > 1<<19 {
			return s, errors.New("SQL must contain 1..524288 bytes")
		}
		if s.Pending != nil && time.Now().Before(s.Pending.Expires) {
			return s, errors.New("a write is already awaiting local approval")
		}
		s.Pending = &PendingWrite{ID: rand.Text(), Connection: s.Connection, Database: s.Database, SQL: action.SQL, Expires: time.Now().Add(5 * time.Minute)}
		s.Status = "Write staged; review and approve in the local TUI. Nothing has been executed."
		return s, nil
	default:
		return s, fmt.Errorf("unknown action %q", action.Type)
	}
	return a.browse(ctx, s)
}

func hasTable(tables []database.Table, name string) bool {
	for _, t := range tables {
		if t.Name == name {
			return true
		}
	}
	return false
}

func (a *App) connectProfile(ctx context.Context, s Snapshot, profile connections.Profile, test bool) (Snapshot, error) {
	cfg, err := connections.Resolve(profile)
	if err != nil {
		return s, err
	}
	db, err := a.open(ctx, cfg)
	if err != nil {
		if cfg.Password != "" {
			err = errors.New(strings.ReplaceAll(err.Error(), cfg.Password, "[redacted]"))
		}
		return s, err
	}
	if test {
		db.Close()
		s.Status = "Connection test succeeded: " + cfg.Name
		return s, nil
	}
	tables, err := db.Tables(ctx)
	if err != nil {
		db.Close()
		return s, err
	}
	if a.store != nil {
		a.views[s.Connection] = s.Browse
		a.store.Close()
	}
	a.store, a.password = db, cfg.Password
	s.Connection, s.Database = cfg.Name, cfg.Database
	s.Tables, s.Schema, s.Result = tables, database.Schema{}, database.Result{}
	s.Query, s.Pending = false, nil
	s.Browse = database.BrowseRequest{Limit: 100}
	if saved, ok := a.views[cfg.Name]; ok {
		s.Browse = saved
	}
	if !hasTable(tables, s.Browse.Table) {
		s.Browse = database.BrowseRequest{Limit: s.Browse.Limit}
	}
	if s.Browse.Table == "" && len(tables) > 0 {
		s.Browse.Table = tables[0].Name
	}
	s.Status = "Connected to " + cfg.Name
	if s.Browse.Table != "" {
		loaded, err := a.browse(ctx, s)
		// Connectivity succeeded even when the first table is inaccessible.
		if err != nil {
			s.Status += "; table could not be loaded"
			s.Error = err.Error()
			return s, nil
		}
		return loaded, nil
	}
	return s, nil
}

func (a *App) browse(ctx context.Context, s Snapshot) (Snapshot, error) {
	if s.Browse.Table == "" {
		return s, errors.New("open a table first")
	}
	meta, err := a.store.Schema(ctx, s.Browse.Table)
	if err != nil {
		return s, err
	}
	result, err := a.store.Browse(ctx, s.Browse)
	if err != nil {
		return s, err
	}
	s.Schema = meta
	s.Result = result
	s.Query = false
	s.Status = fmt.Sprintf("%d rows loaded", len(result.Rows))
	return s, nil
}

// Approve is deliberately not exposed by the control protocol. Consuming the
// request before execution prevents replay even when a write times out.
func (a *App) Approve(ctx context.Context, id string) (Snapshot, error) {
	ctx, s, err := a.begin(ctx)
	if err != nil {
		return s, err
	}
	p := s.Pending
	if p == nil || p.ID != id || !s.WritesEnabled || p.Connection != s.Connection || p.Database != s.Database || time.Now().After(p.Expires) {
		return a.finish(s, errors.New("write approval is missing, expired, or no longer matches this connection"))
	}
	a.mu.Lock()
	a.state.Pending = nil
	a.publishLocked()
	a.mu.Unlock()
	s.Pending = nil
	result, err := a.store.Execute(ctx, p.SQL)
	if err != nil {
		return a.finish(s, fmt.Errorf("write failed or its outcome is unknown; inspect the database before retrying: %w", err))
	}
	s.Status = fmt.Sprintf("Write executed: %d rows affected", result.Affected)
	status := s.Status
	tables, refreshErr := a.store.Tables(ctx)
	if refreshErr == nil {
		s.Tables = tables
		if hasTable(tables, s.Browse.Table) {
			s, refreshErr = a.browse(ctx, s)
		} else {
			s.Browse = database.BrowseRequest{Limit: 100}
			s.Schema = database.Schema{}
			s.Result = database.Result{}
			s.Query = false
		}
	}
	s.Status = status
	if refreshErr != nil {
		s.Result = database.Result{}
		s.Status += "; refresh failed, press r to retry"
	}
	return a.finish(s, nil)
}

func (a *App) Reject(id string) error {
	if !a.op.TryLock() {
		return errors.New("another operation is in progress")
	}
	defer a.op.Unlock()
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return errors.New("application is closed")
	}
	if a.state.Pending == nil || a.state.Pending.ID != id {
		return errors.New("write request is no longer pending")
	}
	a.state.Pending = nil
	a.state.Status = "Write rejected; nothing was executed"
	a.publishLocked()
	return nil
}
