package mysql

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/PascalKraupner/relvo/internal/database"
	driverMysql "github.com/go-sql-driver/mysql"
)

func TestConnectionConfig(t *testing.T) {
	c, err := connectionConfig(database.Config{Database: "a/b?tls=skip-verify", User: "tester", Password: "p@:/?"})
	if err != nil {
		t.Fatal(err)
	}
	if c.Addr != "localhost:3306" || c.TLSConfig != "true" || c.MultiStatements || c.InterpolateParams || c.ParseTime || c.AllowAllFiles || c.AllowCleartextPasswords || c.AllowFallbackToPlaintext {
		t.Fatalf("unsafe defaults: %#v", c)
	}
	if c.Timeout <= 0 || c.ReadTimeout <= 0 || c.WriteTimeout <= 0 {
		t.Fatal("missing timeouts")
	}
	round, err := driverMysql.ParseDSN(c.FormatDSN())
	if err != nil {
		t.Fatal(err)
	}
	if round.DBName != c.DBName || round.Passwd != c.Passwd {
		t.Fatal("DSN did not round trip")
	}
	for _, cfg := range []database.Config{
		{Database: "db", TLS: "preferred"}, {Database: "db", TLS: "skip-verify"}, {Database: "db", TLS: "custom"},
		{Database: "db", Port: "0"}, {Database: "db", Port: "65536"}, {Database: "db", Port: "abc"},
		{Database: "db", Host: "localhost:3306"}, {Database: "db", Host: "bad host"}, {Database: "db", Socket: "relative"}, {},
	} {
		if _, err := connectionConfig(cfg); err == nil {
			t.Errorf("accepted invalid config: %#v", cfg)
		}
	}
	c, err = connectionConfig(database.Config{Database: "db", Host: "::1", TLS: "off"})
	if err != nil || c.Addr != "[::1]:3306" || c.TLSConfig != "false" {
		t.Fatalf("IPv6/off: %v %v", c, err)
	}
	c, err = connectionConfig(database.Config{Database: "db", Socket: "/tmp/mysql.sock", TLS: "off"})
	if err != nil || c.Net != "unix" || c.Addr != "/tmp/mysql.sock" {
		t.Fatalf("socket: %v %v", c, err)
	}
}

func metadata() database.Schema {
	return database.Schema{Columns: []database.Column{{Name: "id"}, {Name: "part"}, {Name: "na`me", Nullable: true}}, Indexes: []database.Index{{Name: "PRIMARY", Columns: []string{"id", "part"}, Unique: true}}}
}

func TestBrowseSQL(t *testing.T) {
	r := database.BrowseRequest{Table: "t`able", Sort: "na`me", Desc: true, Limit: 20, Offset: 40, Filters: []database.Filter{{Column: "na`me", Op: "contains", Value: "a!_%' OR 1=1"}, {Column: "id", Op: "gte", Value: "9007199254740993"}, {Column: "part", Op: "not-null"}}}
	query, args, notice, err := browseSQL("s`chema", r, metadata())
	if err != nil {
		t.Fatal(err)
	}
	want := "SELECT `id`, `part`, `na``me` FROM `s``chema`.`t``able` WHERE `na``me` LIKE ? ESCAPE '!' AND `id` >= ? AND `part` IS NOT NULL ORDER BY `na``me` DESC, `id` DESC, `part` DESC LIMIT ? OFFSET ?"
	if query != want {
		t.Fatalf("query:\n%s\nwant:\n%s", query, want)
	}
	if !reflect.DeepEqual(args, []any{"%a!!!_!%' OR 1=1%", "9007199254740993", 21, 40}) {
		t.Fatalf("args: %#v", args)
	}
	if !strings.Contains(notice, "Offset pagination") {
		t.Fatal(notice)
	}
	for op, fragment := range map[string]string{"eq": "= ?", "ne": "<> ?", "gt": "> ?", "gte": ">= ?", "lt": "< ?", "lte": "<= ?", "is-null": "IS NULL", "not-null": "IS NOT NULL"} {
		r.Filters = []database.Filter{{Column: "id", Op: op, Value: "x"}}
		q, _, _, err := browseSQL("db", r, metadata())
		if err != nil || !strings.Contains(q, "WHERE `id` "+fragment) {
			t.Errorf("%s: %s %v", op, q, err)
		}
	}
}

func TestBrowseValidationAndOrdering(t *testing.T) {
	for _, req := range []database.BrowseRequest{
		{Limit: 0}, {Limit: 1001}, {Limit: 1, Offset: -1}, {Limit: 1, Sort: "id; DROP TABLE x"},
		{Limit: 1, Filters: []database.Filter{{Column: "unknown", Op: "eq"}}},
		{Limit: 1, Filters: []database.Filter{{Column: "id", Op: "raw"}}},
		{Limit: 1, Filters: []database.Filter{{Column: "id", Op: "eq", Value: strings.Repeat("x", maxValueBytes+1)}}},
	} {
		if _, _, _, err := browseSQL("db", req, metadata()); err == nil {
			t.Errorf("accepted %#v", req)
		}
	}
	meta := metadata()
	meta.Indexes = []database.Index{{Name: "nullable", Columns: []string{"na`me"}, Unique: true}, {Name: "functional", Columns: []string{""}, Unique: true}, {Name: "unique", Columns: []string{"part", "id"}, Unique: true}}
	q, _, notice, err := browseSQL("db", database.BrowseRequest{Table: "t", Limit: 1}, meta)
	if err != nil || !strings.Contains(q, "ORDER BY `part` ASC, `id` ASC") || strings.Contains(notice, "not deterministic") {
		t.Fatalf("unique: %s %s %v", q, notice, err)
	}
	meta.Indexes = meta.Indexes[:2]
	_, _, notice, err = browseSQL("db", database.BrowseRequest{Table: "t", Limit: 1}, meta)
	if err != nil || !strings.Contains(notice, "not deterministic") {
		t.Fatalf("fallback: %s %v", notice, err)
	}
}

func TestValidateQuery(t *testing.T) {
	for _, q := range []string{"SELECT 1;", "WITH x AS (SELECT 1) SELECT * FROM x", "SHOW TABLES", "DESCRIBE `t`", "EXPLAIN SELECT * FROM t", "SELECT 'DROP; TABLE', `update`, 'it''s fine'"} {
		if err := validateQuery(q); err != nil {
			t.Errorf("rejected %q: %v", q, err)
		}
	}
	for _, q := range []string{"", "DELETE FROM t", "SELECT 1; DELETE FROM t", "COMMIT", "START TRANSACTION", "SET autocommit=1", "CALL p()", "WITH x AS (SELECT 1) DELETE FROM t", "SELECT 1 INTO OUTFILE '/tmp/x'", "/*!50000 DELETE FROM t */", "SELECT 1 /* comment */", "SELECT 'unterminated", "SELECT 'back\\slash'", "EXPLAIN ANALYZE SELECT 1", "SELECT 1; -- trailing"} {
		if err := validateQuery(q); err == nil {
			t.Errorf("accepted %q", q)
		}
	}
}

func TestValues(t *testing.T) {
	for _, tc := range []struct {
		raw  []byte
		typ  string
		want database.Value
	}{
		{nil, "TEXT", database.Value{Null: true}}, {[]byte{}, "TEXT", database.Value{}},
		{[]byte{0, 255}, "BLOB", database.Value{Text: "0x00ff", Binary: true}},
		{[]byte("abc"), "VARBINARY", database.Value{Text: "0x616263", Binary: true}},
		{[]byte{255}, "VARCHAR", database.Value{Text: "0xff", Binary: true}},
		{[]byte("18446744073709551615"), "UNSIGNED BIGINT", database.Value{Text: "18446744073709551615"}},
		{[]byte("1234567890.123456789"), "DECIMAL", database.Value{Text: "1234567890.123456789"}},
		{[]byte("0000-00-00"), "DATE", database.Value{Text: "0000-00-00"}},
	} {
		got, cut := value(tc.raw, tc.typ)
		if got != tc.want || cut {
			t.Errorf("%s: %#v %v", tc.typ, got, cut)
		}
	}
	got, cut := value([]byte(strings.Repeat("x", maxValueBytes-1)+"\u20ac"), "TEXT")
	if !cut || !got.Truncated || len(got.Text) != maxValueBytes-1 || got.Binary {
		t.Fatal("UTF-8 truncation failed")
	}
	got, cut = value([]byte(strings.Repeat("x", maxValueBytes+1)), "BLOB")
	if !cut || !got.Truncated || len(got.Text) != 2+maxValueBytes*2 || !got.Binary {
		t.Fatal("binary truncation failed")
	}
	got, cut = value([]byte(strings.Repeat("x", maxValueBytes)), "TEXT")
	if cut || got.Truncated || len(got.Text) != maxValueBytes {
		t.Fatal("exactly bounded value was marked truncated")
	}
}

// A small driver exercises database/sql transaction and result lifecycle without
// adding a mocking dependency or requiring a running server.
type testDriver struct{ conn *testConn }

func (d testDriver) Open(string) (driver.Conn, error) { return d.conn, nil }

type testConnector struct{ conn *testConn }

func (c testConnector) Connect(context.Context) (driver.Conn, error) { return c.conn, nil }
func (c testConnector) Driver() driver.Driver                        { return testDriver{c.conn} }

type testConn struct {
	queued                       []*testRows
	queries                      []string
	arguments                    [][]driver.NamedValue
	readOnly, rolledBack, closed atomic.Bool
	closeDone                    chan struct{}
	closeOnce                    sync.Once
	rows                         *testRows
	queryErr                     error
}

func (c *testConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("unused") }
func (c *testConn) Close() error {
	c.closed.Store(true)
	c.closeOnce.Do(func() {
		if c.closeDone != nil {
			close(c.closeDone)
		}
	})
	return nil
}
func (c *testConn) Begin() (driver.Tx, error) { return nil, errors.New("use BeginTx") }
func (c *testConn) BeginTx(_ context.Context, opts driver.TxOptions) (driver.Tx, error) {
	c.readOnly.Store(opts.ReadOnly)
	return testTx{c}, nil
}
func (c *testConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if _, ok := ctx.Deadline(); !ok {
		return nil, errors.New("missing deadline")
	}
	c.queries = append(c.queries, query)
	c.arguments = append(c.arguments, args)
	if len(c.queued) > 0 {
		r := c.queued[0]
		c.queued = c.queued[1:]
		return r, nil
	}
	return c.rows, c.queryErr
}
func (c *testConn) ExecContext(context.Context, string, []driver.NamedValue) (driver.Result, error) {
	return driver.RowsAffected(7), nil
}

type testTx struct{ conn *testConn }

func (t testTx) Commit() error   { return errors.New("must not commit") }
func (t testTx) Rollback() error { t.conn.rolledBack.Store(true); return nil }

type testRows struct {
	names      []string
	data       [][]driver.Value
	count, pos int
	text       string
	extra      bool
	closed     atomic.Bool
}

func (r *testRows) Columns() []string {
	if r.names != nil {
		return r.names
	}
	return []string{"v"}
}
func (r *testRows) Close() error { r.closed.Store(true); return nil }
func (r *testRows) Next(dest []driver.Value) error {
	if r.data != nil {
		if r.pos >= len(r.data) {
			return io.EOF
		}
		copy(dest, r.data[r.pos])
		r.pos++
		return nil
	}
	if r.pos >= r.count {
		return io.EOF
	}
	r.pos++
	dest[0] = []byte(r.text)
	return nil
}
func (r *testRows) HasNextResultSet() bool { return r.extra }
func (r *testRows) NextResultSet() error {
	if r.extra {
		r.extra = false
		return nil
	}
	return io.EOF
}
func (r *testRows) ColumnTypeDatabaseTypeName(int) string { return "TEXT" }

func (c *testConn) waitClosed(t *testing.T) {
	t.Helper()
	select {
	case <-c.closeDone:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for connection cleanup")
	}
}

func TestQueryLifecycle(t *testing.T) {
	for _, tc := range []struct {
		name              string
		count             int
		text              string
		extra, fail, more bool
	}{
		{"normal", 2, "ok", false, false, false}, {"row cap", 1002, "ok", false, false, true},
		{"truncated value", 1, strings.Repeat("x", maxValueBytes+1), false, false, false},
		{"byte cap", 200, strings.Repeat("x", maxValueBytes), false, true, false},
		{"multiple results", 1, "ok", true, true, false}, {"query error", 0, "", false, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := &testConn{rows: &testRows{count: tc.count, text: tc.text, extra: tc.extra}, closeDone: make(chan struct{})}
			if tc.name == "query error" {
				c.queryErr = errors.New("server failure")
			}
			db := sql.OpenDB(testConnector{c})
			defer db.Close()
			s := &store{db: db}
			result, err := s.Query(context.Background(), "SELECT 1")
			c.waitClosed(t)
			if (err != nil) != tc.fail {
				t.Fatalf("unexpected error: %v", err)
			}
			if !c.readOnly.Load() || !c.rolledBack.Load() || !c.closed.Load() {
				t.Fatalf("lifecycle: readOnly=%v rolledBack=%v closed=%v", c.readOnly.Load(), c.rolledBack.Load(), c.closed.Load())
			}
			if tc.name == "byte cap" && (err.Error() != "result exceeds 8 MiB; reduce page size or select fewer/smaller values" || !reflect.DeepEqual(result, database.Result{})) {
				t.Fatalf("byte cap returned partial result or wrong error: rows=%d err=%v", len(result.Rows), err)
			}
			if result.HasMore != tc.more || len(result.Rows) > maxRows {
				t.Fatalf("bounds: rows=%d more=%v", len(result.Rows), result.HasMore)
			}
			if tc.name == "truncated value" && (len(result.Rows) != 1 || !result.Rows[0][0].Truncated || !strings.Contains(result.Notice, "truncated")) {
				t.Fatal("truncation flag or notice missing from query result")
			}
			if c.queryErr == nil && !c.rows.closed.Load() {
				t.Fatal("rows not closed")
			}
		})
	}
}

func TestExecuteAffected(t *testing.T) {
	c := &testConn{closeDone: make(chan struct{})}
	db := sql.OpenDB(testConnector{c})
	defer db.Close()
	result, err := (&store{db: db}).Execute(context.Background(), "UPDATE t SET x = 1")
	c.waitClosed(t)
	if err != nil || result.Affected != 7 || !c.closed.Load() {
		t.Fatalf("execute: %#v %v", result, err)
	}
}

func TestBrowseByteBudget(t *testing.T) {
	for _, tc := range []struct {
		name          string
		columns, rows int
		fail          bool
	}{
		{"partial page rejected", 1, 129, true},
		{"oversized first row rejected", 129, 1, true},
		{"exact byte budget allowed", 128, 1, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			columns := &testRows{names: []string{"name", "type", "nullable", "default", "key", "extra"}, data: [][]driver.Value{}}
			data := &testRows{names: []string{}, data: [][]driver.Value{}}
			row := make([]driver.Value, tc.columns)
			text := strings.Repeat("x", maxValueBytes)
			for i := range row {
				name := fmt.Sprintf("c%d", i)
				columns.data = append(columns.data, []driver.Value{name, "text", "NO", nil, "", ""})
				data.names = append(data.names, name)
				row[i] = text
			}
			for i := 0; i < tc.rows; i++ {
				data.data = append(data.data, row)
			}
			c := &testConn{queued: []*testRows{
				columns,
				{names: []string{"name", "column", "non_unique", "sub_part"}, data: [][]driver.Value{}},
				{names: []string{"name", "column", "database", "table", "target"}, data: [][]driver.Value{}},
				data,
			}}
			db := sql.OpenDB(testConnector{c})
			defer db.Close()
			result, err := (&store{db: db, schema: "db"}).Browse(context.Background(), database.BrowseRequest{Table: "t", Limit: 200})
			if tc.fail {
				if err == nil || err.Error() != "result exceeds 8 MiB; reduce page size or select fewer/smaller values" || !reflect.DeepEqual(result, database.Result{}) {
					t.Fatalf("expected byte-budget error with no page: rows=%d err=%v", len(result.Rows), err)
				}
			} else if err != nil || len(result.Rows) != tc.rows || result.HasMore {
				t.Fatalf("exact byte budget: rows=%d more=%v err=%v", len(result.Rows), result.HasMore, err)
			}
			if !data.closed.Load() {
				t.Fatal("browse rows leaked")
			}
		})
	}
}

func TestMetadata(t *testing.T) {
	columnRows := &testRows{names: []string{"name", "type", "nullable", "default", "key", "extra"}, data: [][]driver.Value{
		{"id", "bigint", "NO", nil, "PRI", "auto_increment"},
		{"part", "int", "NO", "0", "PRI", ""},
		{"name", "varchar(20)", "YES", nil, "", ""},
	}}
	indexRows := &testRows{names: []string{"name", "column", "non_unique", "sub_part"}, data: [][]driver.Value{
		{"PRIMARY", "id", int64(0), nil}, {"PRIMARY", "part", int64(0), nil}, {"by_name", "name", int64(1), int64(10)},
	}}
	fkRows := &testRows{names: []string{"name", "column", "database", "table", "target"}, data: [][]driver.Value{
		{"parent_fk", "id", "other_db", "parent", "id"}, {"parent_fk", "part", "other_db", "parent", "part"},
		{"local_fk", "id", "db", "local_parent", "id"},
	}}
	tableRows := &testRows{names: []string{"name", "kind"}, data: [][]driver.Value{{"child", "BASE TABLE"}, {"view", "VIEW"}}}
	c := &testConn{queued: []*testRows{columnRows, indexRows, fkRows, tableRows}}
	db := sql.OpenDB(testConnector{c})
	defer db.Close()
	s := &store{db: db, schema: "db"}
	meta, err := s.Schema(context.Background(), "child' OR 1=1")
	if err != nil {
		t.Fatal(err)
	}
	if len(meta.Columns) != 3 || meta.Columns[0].Default != nil || *meta.Columns[1].Default != "0" || !meta.Columns[2].Nullable || meta.Columns[0].Extra != "auto_increment" {
		t.Fatalf("columns: %#v", meta.Columns)
	}
	if !reflect.DeepEqual(meta.Indexes, []database.Index{{Name: "PRIMARY", Columns: []string{"id", "part"}, Unique: true}, {Name: "by_name", Columns: []string{"name"}}}) {
		t.Fatalf("indexes: %#v", meta.Indexes)
	}
	if !reflect.DeepEqual(meta.ForeignKeys, []database.ForeignKey{
		{Name: "parent_fk", Column: "id", Database: "other_db", Table: "parent", Target: "id"},
		{Name: "parent_fk", Column: "part", Database: "other_db", Table: "parent", Target: "part"},
		{Name: "local_fk", Column: "id", Database: "db", Table: "local_parent", Target: "id"},
	}) {
		t.Fatalf("foreign keys: %#v", meta.ForeignKeys)
	}
	if !strings.Contains(c.queries[2], "REFERENCED_TABLE_SCHEMA") {
		t.Fatal("FK query omitted referenced schema")
	}
	for i, q := range c.queries {
		if strings.Contains(q, "child'") || len(c.arguments[i]) != 2 || c.arguments[i][0].Value != "db" || c.arguments[i][1].Value != "child' OR 1=1" {
			t.Fatalf("metadata not parameterized: %s %#v", q, c.arguments[i])
		}
	}
	tables, err := s.Tables(context.Background())
	if err != nil || len(tables) != 2 || tables[1].Kind != "VIEW" {
		t.Fatalf("tables: %#v %v", tables, err)
	}
	for _, rows := range []*testRows{columnRows, indexRows, fkRows, tableRows} {
		if !rows.closed.Load() {
			t.Fatal("metadata rows leaked")
		}
	}
	c.queued = []*testRows{{names: columnRows.names, data: [][]driver.Value{}}}
	if _, err := s.Schema(context.Background(), "missing"); err == nil {
		t.Fatal("missing table accepted")
	}
}

func TestIntegration(t *testing.T) {
	dsn := os.Getenv("RELVO_TEST_DSN")
	if dsn == "" {
		t.Skip("set RELVO_TEST_DSN for an isolated MySQL/MariaDB test database")
	}
	c, err := driverMysql.ParseDSN(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg := database.Config{Database: c.DBName, User: c.User, Password: c.Passwd, TLS: "off"}
	if c.TLSConfig != "" && c.TLSConfig != "false" {
		cfg.TLS = c.TLSConfig
	}
	if c.Net == "unix" {
		cfg.Socket = c.Addr
	} else {
		var splitErr error
		cfg.Host, cfg.Port, splitErr = net.SplitHostPort(c.Addr)
		if splitErr != nil {
			t.Fatal(splitErr)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	s, err := Open(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	name := "relvo_adapter_test_" + time.Now().Format("20060102150405.000000000")
	if _, err := s.Execute(ctx, "CREATE TABLE "+quote(name)+" (id BIGINT UNSIGNED PRIMARY KEY, body VARBINARY(10), note TEXT NULL) ENGINE=InnoDB"); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, err := s.Execute(context.Background(), "DROP TABLE "+quote(name)); err != nil {
			t.Error(err)
		}
	}()
	if _, err := s.Execute(ctx, "INSERT INTO "+quote(name)+" VALUES (18446744073709551615, X'00FF', NULL), (1, X'61', 'hello%')"); err != nil {
		t.Fatal(err)
	}
	meta, err := s.Schema(ctx, name)
	if err != nil || len(meta.Columns) != 3 || len(meta.Indexes) != 1 {
		t.Fatalf("schema: %#v %v", meta, err)
	}
	tables, err := s.Tables(ctx)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, table := range tables {
		found = found || table.Name == name
	}
	if !found {
		t.Fatal("table missing")
	}
	r, err := s.Browse(ctx, database.BrowseRequest{Table: name, Limit: 1, Desc: true})
	if err != nil {
		t.Fatal(err)
	}
	if !r.HasMore || r.Rows[0][0].Text != "18446744073709551615" || r.Rows[0][1].Text != "0x00ff" || !r.Rows[0][2].Null {
		t.Fatalf("browse: %#v", r)
	}
	r, err = s.Browse(ctx, database.BrowseRequest{Table: name, Limit: 10, Filters: []database.Filter{{Column: "note", Op: "contains", Value: "%"}}})
	if err != nil || len(r.Rows) != 1 {
		t.Fatalf("filter: %#v %v", r, err)
	}
	if _, err := s.Query(ctx, "SELECT 1; SELECT 2"); err == nil {
		t.Fatal("multiple statements accepted")
	}
	if _, err := s.Execute(ctx, "SELECT 1; SELECT 2"); err == nil {
		t.Fatal("driver accepted multiple statements")
	}
	// Bypass the lexical guard to prove the server itself rejects a write in
	// the same transaction mode used by Query.
	tx, err := s.(*store).db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, "DELETE FROM "+quote(name)); err == nil {
		t.Fatal("server allowed write in read-only transaction")
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	r, err = s.Query(ctx, "SELECT COUNT(*) FROM "+quote(name))
	if err != nil || r.Rows[0][0].Text != "2" {
		t.Fatalf("read query: %#v %v", r, err)
	}
}
