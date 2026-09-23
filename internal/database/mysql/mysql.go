// Package mysql implements MySQL and MariaDB access using offset pagination.
package mysql

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/PascalKraupner/relvo/internal/database"
	driverMysql "github.com/go-sql-driver/mysql"
)

const (
	requestTimeout = 30 * time.Second
	maxRows        = 1000
	maxColumns     = 1024
	maxValueBytes  = 64 * 1024
	maxResultBytes = 8 * 1024 * 1024
	maxMetadata    = 10000
)

type store struct {
	db     *sql.DB
	schema string
}

func connectionConfig(cfg database.Config) (*driverMysql.Config, error) {
	if strings.TrimSpace(cfg.Database) == "" || strings.ContainsRune(cfg.Database, 0) {
		return nil, errors.New("database is required and must not contain NUL")
	}
	c := driverMysql.NewConfig()
	c.User, c.Passwd, c.DBName = cfg.User, cfg.Password, cfg.Database
	switch cfg.TLS {
	case "", "true":
		c.TLSConfig = "true"
	case "off":
		c.TLSConfig = "false"
	default:
		return nil, errors.New("TLS must be empty, true (verified TLS), or off")
	}
	host, port := cfg.Host, cfg.Port
	if host == "" {
		host = "localhost"
	}
	if port == "" {
		port = "3306"
	}
	p, err := strconv.Atoi(port)
	if err != nil || p < 1 || p > 65535 {
		return nil, errors.New("port must be between 1 and 65535")
	}
	if strings.ContainsAny(host, " /\\\t\r\n\x00") || (strings.Contains(host, ":") && net.ParseIP(host) == nil) {
		return nil, errors.New("host must be a hostname or an unbracketed IP address without a port")
	}
	c.Net, c.Addr = "tcp", net.JoinHostPort(host, strconv.Itoa(p))
	if cfg.Socket != "" {
		if !filepath.IsAbs(cfg.Socket) || strings.ContainsRune(cfg.Socket, 0) {
			return nil, errors.New("socket must be an absolute path without NUL")
		}
		c.Net, c.Addr = "unix", cfg.Socket
	}
	c.Timeout, c.ReadTimeout, c.WriteTimeout = 5*time.Second, requestTimeout, requestTimeout
	c.MultiStatements, c.InterpolateParams, c.ParseTime = false, false, false
	c.MaxAllowedPacket = maxResultBytes
	return c, nil
}

// Open validates configuration and verifies connectivity. TLS defaults to verified
// TLS even for sockets; explicitly choose off for a trusted local socket.
func Open(ctx context.Context, cfg database.Config) (database.Store, error) {
	c, err := connectionConfig(cfg)
	if err != nil {
		return nil, err
	}
	connector, err := driverMysql.NewConnector(c)
	if err != nil {
		return nil, err
	}
	db := sql.OpenDB(connector)
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(4)
	db.SetConnMaxLifetime(3 * time.Minute)
	db.SetConnMaxIdleTime(time.Minute)
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("mysql connect: %w", err)
	}
	return &store{db: db, schema: cfg.Database}, nil
}

func (s *store) Close() error { return s.db.Close() }

func (s *store) Tables(ctx context.Context) ([]database.Table, error) {
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	rows, err := s.db.QueryContext(ctx, "SELECT TABLE_NAME, TABLE_TYPE FROM information_schema.TABLES WHERE TABLE_SCHEMA = ? ORDER BY TABLE_NAME LIMIT 10001", s.schema)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []database.Table{}
	for rows.Next() {
		if len(result) == maxMetadata {
			cancel()
			return nil, errors.New("too many tables")
		}
		var t database.Table
		if err := rows.Scan(&t.Name, &t.Kind); err != nil {
			return nil, err
		}
		result = append(result, t)
	}
	return result, rows.Err()
}

func (s *store) Schema(ctx context.Context, table string) (database.Schema, error) {
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	result := database.Schema{Columns: []database.Column{}, Indexes: []database.Index{}, ForeignKeys: []database.ForeignKey{}}
	rows, err := s.db.QueryContext(ctx, "SELECT COLUMN_NAME, COLUMN_TYPE, IS_NULLABLE, COLUMN_DEFAULT, COLUMN_KEY, EXTRA FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = ? AND TABLE_NAME = ? ORDER BY ORDINAL_POSITION LIMIT 1025", s.schema, table)
	if err != nil {
		return result, err
	}
	for rows.Next() {
		var c database.Column
		var nullable string
		var def sql.NullString
		if err = rows.Scan(&c.Name, &c.Type, &nullable, &def, &c.Key, &c.Extra); err != nil {
			break
		}
		c.Nullable = nullable == "YES"
		if def.Valid {
			c.Default = &def.String
		}
		result.Columns = append(result.Columns, c)
		if len(result.Columns) > maxColumns {
			err = errors.New("too many columns")
			cancel()
			break
		}
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return result, err
	}
	if len(result.Columns) == 0 {
		return result, fmt.Errorf("table %q not found or not accessible", table)
	}
	rows, err = s.db.QueryContext(ctx, "SELECT INDEX_NAME, COLUMN_NAME, NON_UNIQUE, SUB_PART FROM information_schema.STATISTICS WHERE TABLE_SCHEMA = ? AND TABLE_NAME = ? ORDER BY INDEX_NAME, SEQ_IN_INDEX LIMIT 10001", s.schema, table)
	if err != nil {
		return result, err
	}
	count := 0
	for rows.Next() {
		count++
		if count > maxMetadata {
			err = errors.New("too many index entries")
			cancel()
			break
		}
		var name string
		var column sql.NullString
		var nonUnique int
		var prefix sql.NullInt64
		if err = rows.Scan(&name, &column, &nonUnique, &prefix); err != nil {
			break
		}
		if len(result.Indexes) == 0 || result.Indexes[len(result.Indexes)-1].Name != name {
			result.Indexes = append(result.Indexes, database.Index{Name: name, Unique: nonUnique == 0, Columns: []string{}})
		}
		i := &result.Indexes[len(result.Indexes)-1]
		// Empty names represent functional index parts, which cannot be used for sorting.
		i.Columns = append(i.Columns, column.String)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return result, err
	}
	rows, err = s.db.QueryContext(ctx, "SELECT CONSTRAINT_NAME, COLUMN_NAME, REFERENCED_TABLE_SCHEMA, REFERENCED_TABLE_NAME, REFERENCED_COLUMN_NAME FROM information_schema.KEY_COLUMN_USAGE WHERE TABLE_SCHEMA = ? AND TABLE_NAME = ? AND REFERENCED_TABLE_NAME IS NOT NULL ORDER BY CONSTRAINT_NAME, ORDINAL_POSITION LIMIT 10001", s.schema, table)
	if err != nil {
		return result, err
	}
	defer rows.Close()
	for rows.Next() {
		if len(result.ForeignKeys) == maxMetadata {
			cancel()
			return result, errors.New("too many foreign keys")
		}
		var f database.ForeignKey
		if err := rows.Scan(&f.Name, &f.Column, &f.Database, &f.Table, &f.Target); err != nil {
			return result, err
		}
		result.ForeignKeys = append(result.ForeignKeys, f)
	}
	return result, rows.Err()
}

func quote(name string) string { return "`" + strings.ReplaceAll(name, "`", "``") + "`" }

func browseSQL(schema string, req database.BrowseRequest, meta database.Schema) (string, []any, string, error) {
	if req.Limit < 1 || req.Limit > maxRows || req.Offset < 0 {
		return "", nil, "", errors.New("limit must be 1..1000 and offset nonnegative")
	}
	if len(req.Filters) > 100 {
		return "", nil, "", errors.New("too many filters")
	}
	columns := map[string]database.Column{}
	names := []string{}
	for _, c := range meta.Columns {
		columns[c.Name] = c
		names = append(names, quote(c.Name))
	}
	if len(names) == 0 {
		return "", nil, "", errors.New("table has no accessible columns")
	}
	if req.Sort != "" {
		if _, ok := columns[req.Sort]; !ok {
			return "", nil, "", errors.New("unknown sort column")
		}
	}
	var key []string
	for _, idx := range meta.Indexes {
		valid := idx.Unique && len(idx.Columns) > 0
		for _, name := range idx.Columns {
			c, ok := columns[name]
			valid = valid && ok && !c.Nullable
		}
		if valid && (len(key) == 0 || idx.Name == "PRIMARY") {
			key = idx.Columns
		}
		if valid && idx.Name == "PRIMARY" {
			break
		}
	}
	notice := "Offset pagination may shift when data changes."
	if len(key) == 0 {
		key = []string{meta.Columns[0].Name}
		notice += " No non-null unique key; fallback ordering is not deterministic for ties."
	}
	order := []string{}
	seen := map[string]bool{}
	for _, name := range append([]string{req.Sort}, key...) {
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		direction := " ASC"
		if req.Desc {
			direction = " DESC"
		}
		order = append(order, quote(name)+direction)
	}
	where := []string{}
	args := []any{}
	for _, f := range req.Filters {
		if _, ok := columns[f.Column]; !ok {
			return "", nil, "", errors.New("unknown filter column")
		}
		if len(f.Value) > maxValueBytes {
			return "", nil, "", errors.New("filter value too large")
		}
		col := quote(f.Column)
		switch f.Op {
		case "is-null":
			where = append(where, col+" IS NULL")
		case "not-null":
			where = append(where, col+" IS NOT NULL")
		case "contains":
			where = append(where, col+" LIKE ? ESCAPE '!'")
			args = append(args, "%"+strings.NewReplacer("!", "!!", "%", "!%", "_", "!_").Replace(f.Value)+"%")
		case "eq", "ne", "gt", "gte", "lt", "lte":
			op := map[string]string{"eq": "=", "ne": "<>", "gt": ">", "gte": ">=", "lt": "<", "lte": "<="}[f.Op]
			where = append(where, col+" "+op+" ?")
			args = append(args, f.Value)
		default:
			return "", nil, "", fmt.Errorf("unsupported filter operator %q", f.Op)
		}
	}
	query := "SELECT " + strings.Join(names, ", ") + " FROM " + quote(schema) + "." + quote(req.Table)
	if len(where) > 0 {
		query += " WHERE " + strings.Join(where, " AND ")
	}
	query += " ORDER BY " + strings.Join(order, ", ") + " LIMIT ? OFFSET ?"
	args = append(args, req.Limit+1, req.Offset)
	return query, args, notice, nil
}

func (s *store) Browse(ctx context.Context, req database.BrowseRequest) (database.Result, error) {
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	meta, err := s.Schema(ctx, req.Table)
	if err != nil {
		return database.Result{}, err
	}
	query, args, notice, err := browseSQL(s.schema, req, meta)
	if err != nil {
		return database.Result{}, err
	}
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return database.Result{}, err
	}
	result, err := collect(rows, req.Limit, cancel)
	if err != nil {
		return database.Result{}, err
	}
	result.Notice = strings.TrimSpace(notice + " " + result.Notice)
	return result, err
}

// Query is not a SQL sandbox: read-only transactions do not prevent external
// effects of functions, locks, or temporary-table writes. Use a restricted DB user.
func (s *store) Query(ctx context.Context, query string) (database.Result, error) {
	if err := validateQuery(query); err != nil {
		return database.Result{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return database.Result{}, err
	}
	// Raw SQL can change session state or acquire locks. Never reuse this session.
	defer func() { _ = conn.Raw(func(any) error { return driver.ErrBadConn }); _ = conn.Close() }()
	tx, err := conn.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return database.Result{}, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, query)
	if err != nil {
		return database.Result{}, err
	}
	return collect(rows, maxRows, cancel)
}

// Execute runs a single raw statement. The caller MUST explicitly authorize writes.
func (s *store) Execute(ctx context.Context, query string) (database.Result, error) {
	if strings.TrimSpace(query) == "" || len(query) > maxResultBytes {
		return database.Result{}, errors.New("SQL is empty or too large")
	}
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return database.Result{}, err
	}
	defer func() { _ = conn.Raw(func(any) error { return driver.ErrBadConn }); _ = conn.Close() }()
	result, err := conn.ExecContext(ctx, query)
	if err != nil {
		return database.Result{}, err
	}
	affected, err := result.RowsAffected()
	return database.Result{Affected: affected}, err
}

func collect(rows *sql.Rows, limit int, cancel context.CancelFunc) (database.Result, error) {
	defer rows.Close()
	result := database.Result{Rows: [][]database.Value{}}
	var err error
	result.Columns, err = rows.Columns()
	if err != nil {
		cancel()
		return result, err
	}
	if len(result.Columns) > maxColumns {
		cancel()
		return result, errors.New("too many result columns")
	}
	types, err := rows.ColumnTypes()
	if err != nil {
		cancel()
		return result, err
	}
	raw := make([]sql.RawBytes, len(types))
	dest := make([]any, len(types))
	for i := range raw {
		dest[i] = &raw[i]
	}
	total := 0
	for rows.Next() {
		if len(result.Rows) == limit {
			result.HasMore = true
			cancel()
			return result, nil
		}
		if err := rows.Scan(dest...); err != nil {
			cancel()
			return result, err
		}
		values := make([]database.Value, len(raw))
		for i, b := range raw {
			v, truncated := value(b, types[i].DatabaseTypeName())
			if truncated {
				result.Notice = "Values truncated to 64 KiB of source bytes."
			}
			total += len(v.Text)
			if total > maxResultBytes {
				cancel()
				return database.Result{}, errors.New("result exceeds 8 MiB; reduce page size or select fewer/smaller values")
			}
			values[i] = v
		}
		result.Rows = append(result.Rows, values)
	}
	if err := rows.Err(); err != nil {
		return result, err
	}
	if rows.NextResultSet() {
		cancel()
		return database.Result{}, errors.New("multiple result sets are not supported")
	}
	return result, rows.Err()
}

func value(b []byte, typ string) (database.Value, bool) {
	if b == nil {
		return database.Value{Null: true}, false
	}
	binary := false
	switch strings.ToUpper(typ) {
	case "BINARY", "VARBINARY", "TINYBLOB", "BLOB", "MEDIUMBLOB", "LONGBLOB", "BIT", "GEOMETRY":
		binary = true
	}
	if !utf8.Valid(b) {
		binary = true
	}
	truncated := len(b) > maxValueBytes
	if truncated {
		b = b[:maxValueBytes]
		if !binary {
			for !utf8.Valid(b) {
				b = b[:len(b)-1]
			}
		}
	}
	if binary {
		return database.Value{Text: "0x" + hex.EncodeToString(b), Binary: true, Truncated: truncated}, truncated
	}
	return database.Value{Text: string(b), Truncated: truncated}, truncated
}

// Deliberately conservative: comments and backslash escapes are rejected so
// executable comments and SQL-mode-dependent tokenization cannot bypass the guard.
// The server's read-only transaction, not this guard, enforces ordinary DML safety.
func validateQuery(query string) error {
	if len(query) > maxResultBytes {
		return errors.New("SQL too large")
	}
	tokens := []string{}
	for i := 0; i < len(query); {
		c := query[i]
		if c == 0 || c == '\\' || c == '#' || (i+1 < len(query) && (query[i:i+2] == "/*" || query[i:i+2] == "--")) {
			return errors.New("Query does not support comments, NUL, or backslash escapes")
		}
		if c == ';' {
			if strings.TrimSpace(query[i+1:]) != "" {
				return errors.New("multiple statements are not supported")
			}
			break
		}
		if c == '\'' || c == '"' || c == '`' {
			quote := c
			i++
			closed := false
			for i < len(query) {
				if query[i] == '\\' || query[i] == 0 {
					return errors.New("Query does not support backslash escapes or NUL")
				}
				if query[i] == quote {
					i++
					if i < len(query) && query[i] == quote {
						i++
						continue
					}
					closed = true
					break
				}
				i++
			}
			if !closed {
				return errors.New("unterminated quoted value")
			}
			continue
		}
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || c == '_' {
			start := i
			for i < len(query) && ((query[i] >= 'a' && query[i] <= 'z') || (query[i] >= 'A' && query[i] <= 'Z') || (query[i] >= '0' && query[i] <= '9') || query[i] == '_') {
				i++
			}
			tokens = append(tokens, strings.ToUpper(query[start:i]))
			continue
		}
		i++
	}
	if len(tokens) == 0 {
		return errors.New("empty query")
	}
	switch tokens[0] {
	case "SELECT", "WITH", "SHOW", "DESCRIBE", "DESC", "EXPLAIN":
	default:
		return errors.New("Query only supports read statements; use explicitly authorized Execute for writes")
	}
	for _, token := range tokens {
		switch token {
		case "INSERT", "UPDATE", "DELETE", "REPLACE", "CREATE", "ALTER", "DROP", "TRUNCATE", "CALL", "DO", "SET", "COMMIT", "ROLLBACK", "START", "BEGIN", "GRANT", "REVOKE", "INTO", "LOAD", "LOCK", "UNLOCK", "FLUSH", "RESET", "RENAME", "ANALYZE", "OPTIMIZE", "REPAIR":
			return fmt.Errorf("Query does not support %s", token)
		}
	}
	return nil
}
