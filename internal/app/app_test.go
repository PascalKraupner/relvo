package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/PascalKraupner/relvo/internal/connections"
	"github.com/PascalKraupner/relvo/internal/database"
)

type fakeStore struct {
	tablesFn  func(context.Context) ([]database.Table, error)
	schemaFn  func(context.Context, string) (database.Schema, error)
	browseFn  func(context.Context, database.BrowseRequest) (database.Result, error)
	queryFn   func(context.Context, string) (database.Result, error)
	executeFn func(context.Context, string) (database.Result, error)
	mu        sync.Mutex
	browses   []database.BrowseRequest
	executed  []string
	closes    int
}

var _ database.Store = (*fakeStore)(nil)

func (f *fakeStore) Tables(ctx context.Context) ([]database.Table, error) {
	if f.tablesFn != nil {
		return f.tablesFn(ctx)
	}
	return []database.Table{{Name: "users", Kind: "BASE TABLE"}, {Name: "orders"}}, nil
}

func (f *fakeStore) Schema(ctx context.Context, table string) (database.Schema, error) {
	if f.schemaFn != nil {
		return f.schemaFn(ctx, table)
	}
	d := "0"
	return database.Schema{
		Columns:     []database.Column{{Name: "id", Type: "bigint", Default: &d}},
		Indexes:     []database.Index{{Name: "PRIMARY", Columns: []string{"id"}, Unique: true}},
		ForeignKeys: []database.ForeignKey{{Name: "owner", Column: "id", Table: "owners", Target: "id"}},
	}, nil
}

func (f *fakeStore) Browse(ctx context.Context, req database.BrowseRequest) (database.Result, error) {
	f.mu.Lock()
	req.Filters = append([]database.Filter(nil), req.Filters...)
	f.browses = append(f.browses, req)
	f.mu.Unlock()
	if f.browseFn != nil {
		return f.browseFn(ctx, req)
	}
	return database.Result{Columns: []string{"id"}, Rows: [][]database.Value{{{Text: req.Table}}}, HasMore: true}, nil
}

func (f *fakeStore) Query(ctx context.Context, sql string) (database.Result, error) {
	if f.queryFn != nil {
		return f.queryFn(ctx, sql)
	}
	return database.Result{Columns: []string{"query"}, Rows: [][]database.Value{{{Text: sql}}}}, nil
}

func (f *fakeStore) Execute(ctx context.Context, sql string) (database.Result, error) {
	f.mu.Lock()
	f.executed = append(f.executed, sql)
	f.mu.Unlock()
	if f.executeFn != nil {
		return f.executeFn(ctx, sql)
	}
	return database.Result{Affected: 1}, nil
}

func (f *fakeStore) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closes++
	return nil
}

func (f *fakeStore) counts() (browses, executions, closes int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.browses), len(f.executed), f.closes
}

func newTestApp(t *testing.T, writes bool, stores ...*fakeStore) *App {
	t.Helper()
	profiles := []connections.Profile{
		{Name: "one", Config: database.Config{Host: "localhost", User: "tester", Database: "db_one"}},
		{Name: "two", Config: database.Config{Host: "localhost", User: "tester", Database: "db_two"}},
	}
	var mu sync.Mutex
	opened := 0
	a, err := New(Options{Profiles: profiles, AllowWrites: writes, Open: func(ctx context.Context, cfg database.Config) (database.Store, error) {
		mu.Lock()
		defer mu.Unlock()
		if opened >= len(stores) {
			return nil, errors.New("unexpected database open")
		}
		f := stores[opened]
		opened++
		return f, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := a.Close(); err != nil {
			t.Error(err)
		}
	})
	return a
}

func dispatch(t *testing.T, a *App, action Action) Snapshot {
	t.Helper()
	s, err := a.Dispatch(context.Background(), action)
	if err != nil {
		t.Fatalf("dispatch %q: %v", action.Type, err)
	}
	return s
}

func connect(t *testing.T, a *App, name string) Snapshot {
	t.Helper()
	return dispatch(t, a, Action{Type: "connect", Connection: name})
}

func waitSignal(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for operation")
	}
}

func TestNewAndProfiles(t *testing.T) {
	if _, err := New(Options{}); err == nil {
		t.Fatal("missing opener accepted")
	}
	a := newTestApp(t, false)
	if s := a.Snapshot(); s.WritesEnabled || s.Browse.Limit != 100 || s.Busy {
		t.Fatalf("initial state: %+v", s)
	}
	for _, name := range []string{"", "  ", "one"} {
		if err := a.AddProfile(connections.Profile{Name: name}); err == nil {
			t.Errorf("accepted invalid/duplicate profile %q", name)
		}
	}
	if err := a.AddProfile(connections.Profile{Name: "three"}); err != nil {
		t.Fatal(err)
	}
	if got := a.Snapshot().Connections; !reflect.DeepEqual(got, []string{"one", "two", "three"}) {
		t.Fatalf("profiles: %v", got)
	}
	if _, err := a.Dispatch(context.Background(), Action{Type: "query", SQL: "SELECT 1"}); err == nil {
		t.Fatal("query without connection accepted")
	}
}

func TestConnectionSwitchPreservesIndependentViews(t *testing.T) {
	one, two, reopened := &fakeStore{}, &fakeStore{}, &fakeStore{}
	a := newTestApp(t, false, one, two, reopened, &fakeStore{})
	connect(t, a, "one")
	dispatch(t, a, Action{Type: "open-table", Table: "orders"})
	dispatch(t, a, Action{Type: "page-size", PageSize: 25})
	dispatch(t, a, Action{Type: "filter", Filters: []database.Filter{{Column: "id", Op: "=", Value: "7"}}})
	dispatch(t, a, Action{Type: "sort", Sort: "id", Desc: true})
	first := dispatch(t, a, Action{Type: "next"}).Browse
	second := connect(t, a, "two")
	if second.Connection != "two" || second.Database != "db_two" || second.Browse.Table != "users" || second.Browse.Limit != 100 || second.Browse.Offset != 0 || len(second.Browse.Filters) != 0 {
		t.Fatalf("new connection inherited view: %+v", second)
	}
	dispatch(t, a, Action{Type: "page-size", PageSize: 10})
	secondView := dispatch(t, a, Action{Type: "next"}).Browse
	back := connect(t, a, "one")
	if !reflect.DeepEqual(back.Browse, first) || back.Result.Rows[0][0].Text != "orders" {
		t.Fatalf("view not restored: %+v; want %+v", back, first)
	}
	if got := connect(t, a, "two").Browse; !reflect.DeepEqual(got, secondView) {
		t.Fatalf("second view: %+v; want %+v", got, secondView)
	}
	for _, f := range []*fakeStore{one, two, reopened} {
		if _, _, n := f.counts(); n != 1 {
			t.Errorf("replaced store closed %d times", n)
		}
	}
}

func TestFilterSortAndPagingResets(t *testing.T) {
	f := &fakeStore{}
	a := newTestApp(t, false, f)
	connect(t, a, "one")
	filters := []database.Filter{{Column: "id", Op: ">", Value: "3"}}
	for _, action := range []Action{
		{Type: "filter", Filters: filters},
		{Type: "sort", Sort: "id", Desc: true},
		{Type: "page-size", PageSize: 20},
		{Type: "open-table", Table: "orders"},
	} {
		dispatch(t, a, Action{Type: "next"})
		s := dispatch(t, a, action)
		if s.Browse.Offset != 0 {
			t.Errorf("%s failed to reset offset: %+v", action.Type, s.Browse)
		}
	}
	s := a.Snapshot()
	if s.Browse.Limit != 20 || s.Browse.Sort != "" || s.Browse.Desc || len(s.Browse.Filters) != 0 {
		t.Fatalf("open-table reset: %+v", s.Browse)
	}
	if s = dispatch(t, a, Action{Type: "prev"}); s.Browse.Offset != 0 {
		t.Fatal("previous page underflow")
	}
	dispatch(t, a, Action{Type: "next"})
	if s = dispatch(t, a, Action{Type: "prev"}); s.Browse.Offset != 0 {
		t.Fatal("previous page did not decrement")
	}
	for _, size := range []int{-1, 0, 1001} {
		if _, err := a.Dispatch(context.Background(), Action{Type: "page-size", PageSize: size}); err == nil {
			t.Errorf("accepted page size %d", size)
		}
	}
	f.browseFn = func(context.Context, database.BrowseRequest) (database.Result, error) { return database.Result{}, nil }
	dispatch(t, a, Action{Type: "refresh"})
	n, _, _ := f.counts()
	if s = dispatch(t, a, Action{Type: "next"}); s.Browse.Offset != 0 {
		t.Fatal("advanced past last page")
	}
	if after, _, _ := f.counts(); after != n {
		t.Fatal("last page unnecessarily fetched")
	}
}

func TestFailedActionsPreservePriorViewAndResult(t *testing.T) {
	for _, kind := range []string{"query", "schema", "browse", "tables", "unknown-table", "unknown-action", "connect"} {
		t.Run(kind, func(t *testing.T) {
			f, bad := &fakeStore{}, &fakeStore{}
			a := newTestApp(t, false, f, bad)
			before := connect(t, a, "one")
			failure := errors.New("database failure")
			action := Action{Type: "filter", Filters: []database.Filter{{Column: "id", Op: "=", Value: "9"}}}
			switch kind {
			case "query":
				f.queryFn = func(context.Context, string) (database.Result, error) { return database.Result{}, failure }
				action = Action{Type: "query", SQL: "SELECT 9"}
			case "schema":
				f.schemaFn = func(context.Context, string) (database.Schema, error) { return database.Schema{}, failure }
			case "browse":
				f.browseFn = func(context.Context, database.BrowseRequest) (database.Result, error) {
					return database.Result{}, failure
				}
			case "tables":
				f.tablesFn = func(context.Context) ([]database.Table, error) { return nil, failure }
				action.Type = "refresh"
			case "unknown-table":
				action = Action{Type: "open-table", Table: "absent"}
			case "unknown-action":
				action.Type = "not-an-action"
			case "connect":
				bad.tablesFn = func(context.Context) ([]database.Table, error) { return nil, failure }
				action = Action{Type: "connect", Connection: "two"}
			}
			after, err := a.Dispatch(context.Background(), action)
			if err == nil || after.Error == "" || after.Busy {
				t.Fatalf("failure state: %+v, %v", after, err)
			}
			if !reflect.DeepEqual(after.Result, before.Result) || !reflect.DeepEqual(after.Schema, before.Schema) || !reflect.DeepEqual(after.Browse, before.Browse) || after.Connection != before.Connection || after.Query != before.Query {
				t.Fatalf("failed action replaced prior data: %+v", after)
			}
			if kind == "connect" {
				if _, _, n := bad.counts(); n != 1 {
					t.Fatal("failed new store not closed")
				}
				if _, _, n := f.counts(); n != 0 {
					t.Fatal("active store closed by failed connection")
				}
			}
		})
	}
}

func TestQueryModeRejectsBrowseControls(t *testing.T) {
	a := newTestApp(t, false, &fakeStore{})
	connect(t, a, "one")
	before := dispatch(t, a, Action{Type: "query", SQL: "SELECT 1"})
	if !before.Query {
		t.Fatal("query mode not set")
	}
	for _, kind := range []string{"filter", "clear-filters", "sort", "page-size", "next", "prev"} {
		s, err := a.Dispatch(context.Background(), Action{Type: kind, PageSize: 10})
		if err == nil || !reflect.DeepEqual(s.Result, before.Result) {
			t.Errorf("query control %q: %+v, %v", kind, s, err)
		}
	}
	if s := dispatch(t, a, Action{Type: "open-table", Table: "orders"}); s.Query {
		t.Fatal("open-table did not exit query mode")
	}
}

func TestSnapshotsAndEventsAreDeepCopies(t *testing.T) {
	a := newTestApp(t, true, &fakeStore{})
	connect(t, a, "one")
	filters := []database.Filter{{Column: "id", Op: "=", Value: "7"}}
	dispatch(t, a, Action{Type: "filter", Filters: filters})
	filters[0].Value = "caller mutation"
	returned := dispatch(t, a, Action{Type: "write", SQL: "UPDATE users SET id = 8"})
	baseline := a.Snapshot()
	if baseline.Browse.Filters[0].Value != "7" {
		t.Fatal("action filters alias app state")
	}
	var event Snapshot
	select {
	case event = <-a.Events():
	default:
		t.Fatal("missing event")
	}
	if !reflect.DeepEqual(event, baseline) {
		t.Fatal("event is not latest state")
	}
	for _, s := range []*Snapshot{&returned, &event} {
		s.Connections[0] = "changed"
		s.Tables[0].Name = "changed"
		s.Browse.Filters[0].Value = "changed"
		s.Schema.Columns[0].Name = "changed"
		*s.Schema.Columns[0].Default = "changed"
		s.Schema.Indexes[0].Columns[0] = "changed"
		s.Schema.ForeignKeys[0].Target = "changed"
		s.Result.Columns[0] = "changed"
		s.Result.Rows[0][0].Text = "changed"
		s.Result.Rows[0] = nil
		s.Pending.SQL = "DELETE FROM users"
	}
	snapshot := a.Snapshot()
	snapshot.Result.Rows[0][0].Text = "snapshot mutation"
	*snapshot.Schema.Columns[0].Default = "snapshot mutation"
	snapshot.Schema.Indexes[0].Columns[0] = "snapshot mutation"
	snapshot.Pending.ID = "snapshot mutation"
	if got := a.Snapshot(); !reflect.DeepEqual(got, baseline) {
		t.Fatalf("receiver mutation reached app state: %+v", got)
	}
}

func TestConnectionTestDoesNotReplaceSession(t *testing.T) {
	active, probe := &fakeStore{}, &fakeStore{}
	a := newTestApp(t, true, active, probe)
	connect(t, a, "one")
	before := dispatch(t, a, Action{Type: "write", SQL: "DELETE FROM users WHERE id = 1"})
	after := dispatch(t, a, Action{Type: "test", Connection: "two"})
	if after.Connection != before.Connection || after.Database != before.Database || !reflect.DeepEqual(after.Browse, before.Browse) || !reflect.DeepEqual(after.Result, before.Result) || !reflect.DeepEqual(after.Pending, before.Pending) {
		t.Fatalf("test replaced session: %+v", after)
	}
	if _, _, n := active.counts(); n != 0 {
		t.Fatal("test closed active store")
	}
	if n, _, closes := probe.counts(); n != 0 || closes != 1 {
		t.Fatalf("probe browsed %d times and closed %d times", n, closes)
	}
	dispatch(t, a, Action{Type: "refresh"})
	if n, _, _ := active.counts(); n != 2 {
		t.Fatal("refresh did not use active store")
	}
}

func TestWritesDisabledByDefault(t *testing.T) {
	f := &fakeStore{}
	// Omit AllowWrites rather than explicitly passing false to test the default.
	a, err := New(Options{Open: func(context.Context, database.Config) (database.Store, error) { return f, nil }, Profiles: []connections.Profile{{Name: "one", Config: database.Config{Host: "localhost", User: "tester", Database: "db"}}}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.Close() })
	connect(t, a, "one")
	if s, err := a.Dispatch(context.Background(), Action{Type: "write", SQL: "DELETE FROM users"}); err == nil || s.Pending != nil || s.WritesEnabled {
		t.Fatalf("default accepted write: %+v, %v", s, err)
	}
	if _, err := a.Approve(context.Background(), "anything"); err == nil {
		t.Fatal("approval accepted without pending write")
	}
	if _, n, _ := f.counts(); n != 0 {
		t.Fatal("disabled write executed")
	}
}

func TestWriteStagingAndLocalApproval(t *testing.T) {
	f := &fakeStore{}
	a := newTestApp(t, true, f)
	connect(t, a, "one")
	const sql = "  UPDATE users SET id = 2 WHERE id = 1;  "
	start := time.Now()
	s := dispatch(t, a, Action{Type: "write", SQL: sql})
	p := s.Pending
	if p == nil || p.ID == "" || p.SQL != sql || p.Connection != "one" || p.Database != "db_one" || !p.Expires.After(start) || p.Expires.After(time.Now().Add(5*time.Minute)) {
		t.Fatalf("invalid staged write: %+v", p)
	}
	if _, n, _ := f.counts(); n != 0 {
		t.Fatal("staging executed SQL")
	}
	for _, action := range []Action{{Type: "approve", SQL: p.ID}, {Type: "write", SQL: "DELETE FROM users"}} {
		if got, err := a.Dispatch(context.Background(), action); err == nil || !reflect.DeepEqual(got.Pending, p) {
			t.Fatalf("remote approval/replacement accepted: %+v, %v", got, err)
		}
	}
	if _, err := a.Approve(context.Background(), p.ID+"wrong"); err == nil {
		t.Fatal("inexact approval ID accepted")
	}
	if _, n, _ := f.counts(); n != 0 {
		t.Fatal("invalid approval executed SQL")
	}
	s, err := a.Approve(context.Background(), p.ID)
	if err != nil || s.Pending != nil || !strings.Contains(s.Status, "1 rows affected") {
		t.Fatalf("local approval: %+v, %v", s, err)
	}
	if _, err := a.Approve(context.Background(), p.ID); err == nil {
		t.Fatal("write replay accepted")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if !reflect.DeepEqual(f.executed, []string{sql}) {
		t.Fatalf("executed SQL: %q", f.executed)
	}
}

func TestApprovalExpiryAndConnectionBinding(t *testing.T) {
	for _, kind := range []string{"expiry", "connection-mismatch", "database-mismatch", "connection-switch"} {
		t.Run(kind, func(t *testing.T) {
			first, second := &fakeStore{}, &fakeStore{}
			a := newTestApp(t, true, first, second)
			connect(t, a, "one")
			id := dispatch(t, a, Action{Type: "write", SQL: "DELETE FROM users"}).Pending.ID
			if kind == "connection-switch" {
				if s := connect(t, a, "two"); s.Pending != nil {
					t.Fatal("switch retained pending write")
				}
			} else {
				// Exercise validation without waiting five minutes or changing the public API.
				a.mu.Lock()
				switch kind {
				case "expiry":
					a.state.Pending.Expires = time.Now().Add(-time.Second)
				case "connection-mismatch":
					a.state.Pending.Connection = "two"
				case "database-mismatch":
					a.state.Pending.Database = "other"
				}
				a.mu.Unlock()
			}
			if _, err := a.Approve(context.Background(), id); err == nil {
				t.Fatal("invalid approval accepted")
			}
			for _, f := range []*fakeStore{first, second} {
				if _, n, _ := f.counts(); n != 0 {
					t.Fatal("invalid approval executed")
				}
			}
		})
	}
}

func TestFailedExecuteConsumesPendingWrite(t *testing.T) {
	f := &fakeStore{executeFn: func(context.Context, string) (database.Result, error) {
		return database.Result{}, errors.New("network lost")
	}}
	a := newTestApp(t, true, f)
	connect(t, a, "one")
	before := dispatch(t, a, Action{Type: "write", SQL: "DELETE FROM users"})
	s, err := a.Approve(context.Background(), before.Pending.ID)
	if err == nil || !strings.Contains(err.Error(), "outcome is unknown") || s.Pending != nil || s.Busy || !reflect.DeepEqual(s.Result, before.Result) {
		t.Fatalf("failed execute state: %+v, %v", s, err)
	}
	if _, err := a.Approve(context.Background(), before.Pending.ID); err == nil {
		t.Fatal("failed write replay accepted")
	}
	if _, n, _ := f.counts(); n != 1 {
		t.Fatalf("execute count: %d", n)
	}
}

func TestRejectAndWriteValidation(t *testing.T) {
	f := &fakeStore{}
	a := newTestApp(t, true, f)
	connect(t, a, "one")
	for _, sql := range []string{"", "  \n", strings.Repeat("x", (1<<19)+1)} {
		if s, err := a.Dispatch(context.Background(), Action{Type: "write", SQL: sql}); err == nil || s.Pending != nil {
			t.Fatal("invalid SQL staged")
		}
	}
	id := dispatch(t, a, Action{Type: "write", SQL: "DELETE FROM users"}).Pending.ID
	if err := a.Reject(id + "wrong"); err == nil || a.Snapshot().Pending == nil {
		t.Fatal("wrong rejection ID changed pending write")
	}
	if err := a.Reject(id); err != nil {
		t.Fatal(err)
	}
	if a.Snapshot().Pending != nil {
		t.Fatal("rejected write retained")
	}
	if _, err := a.Approve(context.Background(), id); err == nil {
		t.Fatal("rejected write replayed")
	}
	if err := a.Reject(id); err == nil {
		t.Fatal("duplicate rejection accepted")
	}
	if _, n, _ := f.counts(); n != 0 {
		t.Fatal("rejection executed SQL")
	}
}

func TestCancellationRejectsConcurrentSwitchAndPreventsStaleResults(t *testing.T) {
	entered := make(chan struct{})
	f := &fakeStore{queryFn: func(ctx context.Context, _ string) (database.Result, error) {
		close(entered)
		<-ctx.Done()
		return database.Result{Rows: [][]database.Value{{{Text: "stale"}}}}, ctx.Err()
	}}
	a := newTestApp(t, false, f, &fakeStore{})
	before := connect(t, a, "one")
	done := make(chan struct{})
	var result Snapshot
	var queryErr error
	go func() {
		defer close(done)
		result, queryErr = a.Dispatch(context.Background(), Action{Type: "query", SQL: "SELECT slow"})
	}()
	t.Cleanup(a.Cancel)
	waitSignal(t, entered)
	if !a.Snapshot().Busy {
		t.Fatal("running query not busy")
	}
	if _, err := a.Dispatch(context.Background(), Action{Type: "connect", Connection: "two"}); err == nil {
		t.Fatal("concurrent switch accepted")
	}
	if err := a.AddProfile(connections.Profile{Name: "during-operation"}); err == nil {
		t.Fatal("concurrent profile mutation accepted")
	}
	if _, err := a.Approve(context.Background(), "wrong"); err == nil {
		t.Fatal("concurrent approval accepted")
	}
	if err := a.Reject("wrong"); err == nil {
		t.Fatal("concurrent rejection accepted")
	}
	dispatch(t, a, Action{Type: "state"})
	dispatch(t, a, Action{Type: "cancel"})
	waitSignal(t, done)
	if queryErr == nil || result.Busy || !reflect.DeepEqual(result.Result, before.Result) || result.Connection != "one" {
		t.Fatalf("cancelled query replaced view: %+v, %v", result, queryErr)
	}
	if s := connect(t, a, "two"); s.Connection != "two" || s.Query || s.Result.Rows[0][0].Text == "stale" {
		t.Fatalf("stale result after switch: %+v", s)
	}
}

func TestCancelledContextDoesNotStartDatabaseWork(t *testing.T) {
	f := &fakeStore{}
	a := newTestApp(t, false, f)
	before := connect(t, a, "one")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s, err := a.Dispatch(ctx, Action{Type: "next"})
	if err == nil || s.Busy || !reflect.DeepEqual(s.Browse, before.Browse) {
		t.Fatalf("cancelled dispatch: %+v, %v", s, err)
	}
	if n, _, _ := f.counts(); n != 1 {
		t.Fatal("cancelled dispatch reached store")
	}
}

func TestCloseCancelsActiveQuery(t *testing.T) {
	entered := make(chan struct{})
	f := &fakeStore{queryFn: func(ctx context.Context, _ string) (database.Result, error) {
		close(entered)
		<-ctx.Done()
		return database.Result{}, ctx.Err()
	}}
	a := newTestApp(t, false, f)
	before := connect(t, a, "one")
	queryDone := make(chan struct{})
	var queryErr error
	go func() {
		defer close(queryDone)
		_, queryErr = a.Dispatch(context.Background(), Action{Type: "query", SQL: "SELECT slow"})
	}()
	t.Cleanup(a.Cancel)
	waitSignal(t, entered)
	closeDone := make(chan struct{})
	var closeErr error
	go func() { defer close(closeDone); closeErr = a.Close() }()
	waitSignal(t, closeDone)
	waitSignal(t, queryDone)
	if closeErr != nil || queryErr == nil {
		t.Fatalf("Close error = %v; query error = %v", closeErr, queryErr)
	}
	if s := a.Snapshot(); s.Busy || !reflect.DeepEqual(s.Result, before.Result) {
		t.Fatalf("Close changed result or left operation busy: %+v", s)
	}
	if _, _, n := f.counts(); n != 1 {
		t.Fatalf("store closed %d times", n)
	}
}

func TestCancelledExecuteConsumesPendingBeforeDatabaseReturns(t *testing.T) {
	entered := make(chan struct{})
	f := &fakeStore{executeFn: func(ctx context.Context, _ string) (database.Result, error) {
		close(entered)
		<-ctx.Done()
		return database.Result{}, ctx.Err()
	}}
	a := newTestApp(t, true, f)
	connect(t, a, "one")
	id := dispatch(t, a, Action{Type: "write", SQL: "DELETE FROM users"}).Pending.ID
	done := make(chan struct{})
	var result Snapshot
	var executeErr error
	go func() { defer close(done); result, executeErr = a.Approve(context.Background(), id) }()
	t.Cleanup(a.Cancel)
	waitSignal(t, entered)
	if s := a.Snapshot(); s.Pending != nil || !s.Busy {
		t.Fatalf("pending write not consumed before Execute returned: %+v", s)
	}
	a.Cancel()
	waitSignal(t, done)
	if executeErr == nil || result.Pending != nil || result.Busy {
		t.Fatalf("cancelled Execute: %+v, %v", result, executeErr)
	}
	if _, err := a.Approve(context.Background(), id); err == nil {
		t.Fatal("cancelled write replay accepted")
	}
	if _, n, _ := f.counts(); n != 1 {
		t.Fatalf("Execute called %d times", n)
	}
}

func TestCloseAndConcurrentEvents(t *testing.T) {
	entered := make(chan struct{})
	f := &fakeStore{queryFn: func(ctx context.Context, _ string) (database.Result, error) {
		close(entered)
		<-ctx.Done()
		return database.Result{}, ctx.Err()
	}}
	a := newTestApp(t, false, f)
	connect(t, a, "one")
	queryDone := make(chan struct{})
	go func() {
		defer close(queryDone)
		_, _ = a.Dispatch(context.Background(), Action{Type: "query", SQL: "SELECT slow"})
	}()
	t.Cleanup(a.Cancel)
	waitSignal(t, entered)
	eventsDone := make(chan struct{})
	go func() {
		defer close(eventsDone)
		var revision uint64
		for s := range a.Events() {
			if s.Revision <= revision {
				t.Errorf("event revision %d after %d", s.Revision, revision)
			}
			revision = s.Revision
			if len(s.Result.Rows) > 0 {
				s.Result.Rows[0][0].Text = "event consumer"
			}
		}
	}()
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 50 {
				s := a.Snapshot()
				if len(s.Schema.Indexes) > 0 {
					s.Schema.Indexes[0].Columns[0] = "snapshot consumer"
				}
				a.Cancel()
			}
			if err := a.Close(); err != nil {
				t.Error(err)
			}
		}()
	}
	closed := make(chan struct{})
	go func() { wg.Wait(); close(closed) }()
	waitSignal(t, closed)
	waitSignal(t, queryDone)
	waitSignal(t, eventsDone)
	if a.Snapshot().Busy {
		t.Fatal("closed app remains busy")
	}
	if _, _, n := f.counts(); n != 1 {
		t.Fatalf("store closed %d times", n)
	}
	if _, err := a.Dispatch(context.Background(), Action{Type: "refresh"}); err == nil {
		t.Fatal("closed app accepted operation")
	}
	if _, err := a.Approve(context.Background(), "id"); err == nil {
		t.Fatal("closed app accepted approval")
	}
	if err := a.AddProfile(connections.Profile{Name: "closed"}); err == nil {
		t.Fatal("closed app accepted profile")
	}
	if err := a.Reject("id"); err == nil {
		t.Fatal("closed app accepted rejection")
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
}

func assertSessionUnchanged(t *testing.T, before, after Snapshot) {
	t.Helper()
	// Operations may publish new status/error messages without changing the session.
	after.Revision, after.Status, after.Error = before.Revision, before.Status, before.Error
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("session changed:\n got: %+v\nwant: %+v", after, before)
	}
}

func TestSetupTestDoesNotSaveOrReplaceSession(t *testing.T) {
	for _, connected := range []bool{false, true} {
		t.Run(map[bool]string{false: "no-active-connection", true: "active-connection"}[connected], func(t *testing.T) {
			active, probe, saved := &fakeStore{}, &fakeStore{}, &fakeStore{}
			stores := []*fakeStore{probe, saved}
			if connected {
				stores = append([]*fakeStore{active}, stores...)
			}
			a := newTestApp(t, true, stores...)
			if connected {
				connect(t, a, "one")
				dispatch(t, a, Action{Type: "filter", Filters: []database.Filter{{Column: "id", Op: ">", Value: "3"}}})
				dispatch(t, a, Action{Type: "next"})
				dispatch(t, a, Action{Type: "query", SQL: "SELECT 7"})
				dispatch(t, a, Action{Type: "write", SQL: "DELETE FROM users WHERE id = 7"})
			}
			before := a.Snapshot()
			p := connections.Profile{Name: "draft", Config: database.Config{Host: "localhost", User: "tester", Database: "draft_db"}}
			after, err := a.Setup(context.Background(), p, true)
			if err != nil {
				t.Fatal(err)
			}
			assertSessionUnchanged(t, before, after)
			assertSessionUnchanged(t, before, a.Snapshot())
			if n, _, closes := probe.counts(); n != 0 || closes != 1 {
				t.Fatalf("probe browse count = %d, close count = %d", n, closes)
			}
			if _, _, closes := active.counts(); closes != 0 {
				t.Fatal("Setup test closed active store")
			}
			if connected {
				dispatch(t, a, Action{Type: "refresh"})
				if n, _, _ := active.counts(); n != 4 {
					t.Fatalf("refresh did not use active store: %d browses", n)
				}
			}
			after, err = a.Setup(context.Background(), p, false)
			if err != nil {
				t.Fatalf("test reserved draft name: %v", err)
			}
			if after.Connection != p.Name || after.Database != p.Config.Database || after.Pending != nil || after.Query || !reflect.DeepEqual(after.Connections, []string{"one", "two", "draft"}) {
				t.Fatalf("Setup connect: %+v", after)
			}
			if n, _, closes := saved.counts(); n != 1 || closes != 0 {
				t.Fatalf("saved store browse count = %d, close count = %d", n, closes)
			}
			if connected {
				if _, _, closes := active.counts(); closes != 1 {
					t.Fatal("successful Setup did not close previous store")
				}
			}
		})
	}
}

func TestFailedSetupDoesNotReserveName(t *testing.T) {
	for _, kind := range []string{"validation", "open", "tables"} {
		t.Run(kind, func(t *testing.T) {
			active, bad, corrected := &fakeStore{}, &fakeStore{}, &fakeStore{}
			failure := errors.New("draft " + kind + " failure")
			bad.tablesFn = func(context.Context) ([]database.Table, error) { return nil, failure }
			fail := true
			var opened []database.Config
			a, err := New(Options{
				AllowWrites: true,
				Profiles:    []connections.Profile{{Name: "active", Config: database.Config{Host: "localhost", User: "tester", Database: "active_db"}}},
				Open: func(_ context.Context, cfg database.Config) (database.Store, error) {
					opened = append(opened, cfg)
					if cfg.Name == "active" {
						return active, nil
					}
					if fail && kind == "open" {
						return nil, failure
					}
					if fail && kind == "tables" {
						return bad, nil
					}
					return corrected, nil
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := a.Close(); err != nil {
					t.Error(err)
				}
			})
			connect(t, a, "active")
			before := dispatch(t, a, Action{Type: "write", SQL: "DELETE FROM users"})
			p := connections.Profile{Name: "draft", Config: database.Config{Host: "localhost", User: "tester", Database: "draft_db"}}
			if kind == "validation" {
				p.Config.User = ""
			}
			after, err := a.Setup(context.Background(), p, false)
			if err == nil || after.Error == "" {
				t.Fatalf("failed Setup returned %+v, %v", after, err)
			}
			assertSessionUnchanged(t, before, after)
			assertSessionUnchanged(t, before, a.Snapshot())
			wantOpens := 2
			if kind == "validation" {
				wantOpens = 1
			}
			if len(opened) != wantOpens {
				t.Fatalf("failed Setup opened %d stores; want %d", len(opened), wantOpens)
			}
			if _, _, closes := active.counts(); closes != 0 {
				t.Fatal("failed Setup closed active store")
			}
			if kind == "tables" {
				if _, _, closes := bad.counts(); closes != 1 {
					t.Fatal("failed table discovery leaked draft store")
				}
			}
			fail = false
			p.Config.User = "corrected-user"
			after, err = a.Setup(context.Background(), p, false)
			if err != nil {
				t.Fatalf("corrected same-name Setup: %v", err)
			}
			if after.Error != "" || after.Connection != "draft" || after.Database != "draft_db" || !reflect.DeepEqual(after.Connections, []string{"active", "draft"}) {
				t.Fatalf("corrected Setup: %+v", after)
			}
			if got := opened[len(opened)-1]; got.User != "corrected-user" || got.Name != "draft" {
				t.Fatalf("corrected profile not used: %+v", got)
			}
		})
	}
}

func TestSetupRejectsWhileBusy(t *testing.T) {
	entered := make(chan struct{})
	f := &fakeStore{queryFn: func(ctx context.Context, _ string) (database.Result, error) {
		close(entered)
		<-ctx.Done()
		return database.Result{}, ctx.Err()
	}}
	a := newTestApp(t, false, f, &fakeStore{})
	connect(t, a, "one")
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = a.Dispatch(context.Background(), Action{Type: "query", SQL: "SELECT slow"})
	}()
	t.Cleanup(a.Cancel)
	waitSignal(t, entered)
	before := a.Snapshot()
	p := connections.Profile{Name: "draft", Config: database.Config{Host: "localhost", User: "tester", Database: "draft_db"}}
	for _, test := range []bool{true, false} {
		s, err := a.Setup(context.Background(), p, test)
		if err == nil || !strings.Contains(err.Error(), "operation is in progress") {
			t.Fatalf("busy Setup(test=%v): %v", test, err)
		}
		if !reflect.DeepEqual(s, before) || !reflect.DeepEqual(a.Snapshot(), before) {
			t.Fatal("rejected Setup changed running operation state")
		}
	}
	a.Cancel()
	waitSignal(t, done)
	if _, err := a.Setup(context.Background(), p, false); err != nil {
		t.Fatalf("Setup after cancellation: %v", err)
	}
}

func TestDuplicateProfileDoesNotReplaceOriginal(t *testing.T) {
	for _, method := range []string{"AddProfile", "Setup-connect", "Setup-test"} {
		t.Run(method, func(t *testing.T) {
			var opened []database.Config
			a, err := New(Options{Open: func(_ context.Context, cfg database.Config) (database.Store, error) {
				opened = append(opened, cfg)
				return &fakeStore{}, nil
			}})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := a.Close(); err != nil {
					t.Error(err)
				}
			})
			original := connections.Profile{Name: "same", Config: database.Config{Host: "original", Port: "3307", User: "original-user", Database: "original_db"}}
			if err := a.AddProfile(original); err != nil {
				t.Fatal(err)
			}
			before := connect(t, a, original.Name)
			replacement := connections.Profile{Name: original.Name, Config: database.Config{Host: "replacement", User: "other-user", Database: "other_db"}}
			if method == "AddProfile" {
				err = a.AddProfile(replacement)
			} else {
				_, err = a.Setup(context.Background(), replacement, method == "Setup-test")
			}
			if err == nil {
				t.Fatal("duplicate profile accepted")
			}
			assertSessionUnchanged(t, before, a.Snapshot())
			if len(opened) != 1 {
				t.Fatal("duplicate profile reached opener")
			}
			connect(t, a, original.Name)
			want := original.Config
			want.Name = original.Name
			if len(opened) != 2 || opened[1] != want {
				t.Fatalf("duplicate replaced profile: %+v; want %+v", opened, want)
			}
		})
	}
}

func TestProfileRegistrationCopiesEndpointAndMapping(t *testing.T) {
	for _, method := range []string{"AddProfile", "Setup"} {
		t.Run(method, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), ".env")
			if err := os.WriteFile(path, []byte("ORIGINAL_USER=original-user\nOTHER_USER=other-user\n"), 0600); err != nil {
				t.Fatal(err)
			}
			var opened []database.Config
			a, err := New(Options{Open: func(_ context.Context, cfg database.Config) (database.Store, error) {
				opened = append(opened, cfg)
				return &fakeStore{}, nil
			}})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := a.Close(); err != nil {
					t.Error(err)
				}
			})
			p := connections.Profile{
				Name: "owned", EnvFile: path, Mapping: map[string]string{"user": "ORIGINAL_USER"},
				Endpoint: &connections.Endpoint{Host: "127.0.0.1", Port: "13306"},
				Config:   database.Config{Host: "internal", Socket: "/original.sock", Database: "original_db"},
			}
			if method == "AddProfile" {
				err = a.AddProfile(p)
			} else {
				_, err = a.Setup(context.Background(), p, false)
			}
			if err != nil {
				t.Fatal(err)
			}
			p.Mapping["user"] = "OTHER_USER"
			p.Endpoint.Host, p.Endpoint.Port = "changed", "9999"
			p.Config.Database = "changed_db"
			s := a.Snapshot()
			s.Connections[0] = "changed-name"
			connect(t, a, "owned")
			want := database.Config{Name: "owned", Host: "127.0.0.1", Port: "13306", User: "original-user", Database: "original_db"}
			for _, cfg := range opened {
				if cfg != want {
					t.Fatalf("caller mutation reached stored profile: %+v; want %+v", cfg, want)
				}
			}
			if got := a.Snapshot().Connections; !reflect.DeepEqual(got, []string{"owned"}) {
				t.Fatalf("snapshot names alias state: %v", got)
			}
		})
	}
}

func TestRefreshKeepsNewTablesWhenBrowseFails(t *testing.T) {
	for _, query := range []bool{false, true} {
		t.Run(map[bool]string{false: "table-result", true: "query-result"}[query], func(t *testing.T) {
			f := &fakeStore{}
			a := newTestApp(t, false, f)
			connect(t, a, "one")
			dispatch(t, a, Action{Type: "filter", Filters: []database.Filter{{Column: "id", Op: ">", Value: "3"}}})
			dispatch(t, a, Action{Type: "next"})
			if query {
				dispatch(t, a, Action{Type: "query", SQL: "SELECT 7"})
			}
			before := a.Snapshot()
			tables := []database.Table{{Name: "users", Kind: "VIEW"}, {Name: "new_table", Kind: "BASE TABLE"}}
			f.tablesFn = func(context.Context) ([]database.Table, error) { return tables, nil }
			failure := errors.New("browse unavailable")
			f.browseFn = func(context.Context, database.BrowseRequest) (database.Result, error) {
				return database.Result{}, failure
			}
			s := dispatch(t, a, Action{Type: "refresh"})
			if !reflect.DeepEqual(s.Tables, tables) || !reflect.DeepEqual(s.Result, database.Result{}) || s.Error != failure.Error() || s.Busy || s.Query {
				t.Fatalf("partial refresh did not retain metadata and clear stale results: %+v", s)
			}
			if s.Connection != before.Connection || s.Database != before.Database || !reflect.DeepEqual(s.Browse, before.Browse) {
				t.Fatal("partial refresh changed connection or browse request")
			}
			if !reflect.DeepEqual(a.Snapshot(), s) {
				t.Fatal("partial refresh was not committed")
			}
			select {
			case event := <-a.Events():
				if !reflect.DeepEqual(event, s) {
					t.Fatal("partial refresh event differs from returned state")
				}
			default:
				t.Fatal("partial refresh event missing")
			}
			f.browseFn = nil
			s = dispatch(t, a, Action{Type: "refresh"})
			if s.Error != "" || len(s.Result.Rows) == 0 || !reflect.DeepEqual(s.Tables, tables) {
				t.Fatalf("refresh did not recover: %+v", s)
			}
		})
	}
}

func TestClearFiltersResetsFiltersAndOffset(t *testing.T) {
	f := &fakeStore{}
	a := newTestApp(t, false, f)
	connect(t, a, "one")
	dispatch(t, a, Action{Type: "page-size", PageSize: 25})
	dispatch(t, a, Action{Type: "sort", Sort: "id", Desc: true})
	filters := []database.Filter{{Column: "id", Op: ">", Value: "3"}}
	dispatch(t, a, Action{Type: "filter", Filters: filters})
	before := dispatch(t, a, Action{Type: "next"})
	if before.Browse.Offset == 0 || len(before.Browse.Filters) == 0 {
		t.Fatal("test requires paginated filtered view")
	}
	n, _, _ := f.counts()
	// A clear action must ignore any filters supplied in its payload.
	s := dispatch(t, a, Action{Type: "clear-filters", Filters: filters})
	want := before.Browse
	want.Filters, want.Offset = nil, 0
	if !reflect.DeepEqual(s.Browse, want) || s.Error != "" || s.Query {
		t.Fatalf("clear-filters: %+v; want browse %+v", s, want)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.browses) != n+1 || !reflect.DeepEqual(f.browses[len(f.browses)-1], want) {
		t.Fatalf("clear-filters did not fetch reset view: %+v", f.browses)
	}
}
