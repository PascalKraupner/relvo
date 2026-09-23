// Package demo provides a deterministic, read-only ecommerce database.
package demo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/PascalKraupner/relvo/internal/database"
)

type table struct {
	schema database.Schema
	rows   [][]database.Value
}

type store struct {
	tables map[string]table
	closed atomic.Bool
}

var _ database.Store = (*store)(nil)

// Open creates an independent seed. Config is accepted for opener compatibility;
// no credentials or network connections are used.
func Open(ctx context.Context, cfg database.Config) (database.Store, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s := &store{tables: map[string]table{}}
	users := table{schema: database.Schema{
		Columns: []database.Column{
			{Name: "id", Type: "bigint unsigned", Key: "PRI"},
			{Name: "uuid", Type: "char(36)", Key: "UNI"},
			{Name: "name", Type: "varchar(255)"},
			{Name: "email", Type: "varchar(255)", Key: "UNI"},
			{Name: "balance", Type: "decimal(30,8)"},
			{Name: "notes", Type: "text", Nullable: true},
			{Name: "metadata", Type: "json"},
		},
		Indexes:     []database.Index{{Name: "PRIMARY", Columns: []string{"id"}, Unique: true}, {Name: "users_uuid", Columns: []string{"uuid"}, Unique: true}, {Name: "users_email", Columns: []string{"email"}, Unique: true}},
		ForeignKeys: []database.ForeignKey{},
	}}
	for i := 1; i <= 250; i++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		name := fmt.Sprintf("Customer %03d", i)
		if i == 1 {
			name = "Zo\u00eb \u6771\u4eac"
		}
		metadata, _ := json.Marshal(map[string]any{"loyalty_points": 9007199254740993 + int64(i), "preferences": []string{"email", "\u65e5\u672c\u8a9e"}, "history": strings.Repeat("Purchased coffee, tea, and gifts. ", 128)})
		row := values(strconv.Itoa(i), fmt.Sprintf("00000000-0000-4000-8000-%012d", i), name, fmt.Sprintf("customer%03d@example.test", i), fmt.Sprintf("9007199254740993.%08d", i), "Prefers 100% cotton_ products", string(metadata))
		if i%3 == 0 {
			row[5] = database.Value{Null: true}
		} else if i%3 == 2 {
			row[5] = database.Value{}
		}
		users.rows = append(users.rows, row)
	}
	products := table{schema: database.Schema{
		Columns:     []database.Column{{Name: "id", Type: "bigint unsigned", Key: "PRI"}, {Name: "name", Type: "varchar(255)"}, {Name: "price", Type: "decimal(20,8)"}, {Name: "stock", Type: "int"}, {Name: "description", Type: "text", Nullable: true}},
		Indexes:     []database.Index{{Name: "PRIMARY", Columns: []string{"id"}, Unique: true}, {Name: "products_name", Columns: []string{"name"}}},
		ForeignKeys: []database.ForeignKey{},
	}}
	for i := 1; i <= 80; i++ {
		products.rows = append(products.rows, values(strconv.FormatUint(18446744073709551000+uint64(i), 10), fmt.Sprintf("Product %03d", i), fmt.Sprintf("%d.%08d", i%20+1, i), strconv.Itoa(i*3), strings.Repeat("Carefully made for everyday use. ", 64)))
	}
	orders := table{schema: database.Schema{
		Columns:     []database.Column{{Name: "id", Type: "bigint unsigned", Key: "PRI"}, {Name: "user_id", Type: "bigint unsigned", Key: "MUL"}, {Name: "product_id", Type: "bigint unsigned", Key: "MUL"}, {Name: "quantity", Type: "int"}, {Name: "total", Type: "decimal(30,8)"}, {Name: "status", Type: "varchar(32)"}},
		Indexes:     []database.Index{{Name: "PRIMARY", Columns: []string{"id"}, Unique: true}, {Name: "orders_user", Columns: []string{"user_id"}}, {Name: "orders_product", Columns: []string{"product_id"}}, {Name: "orders_status_id", Columns: []string{"status", "id"}}},
		ForeignKeys: []database.ForeignKey{{Name: "orders_user_fk", Column: "user_id", Table: "users", Target: "id"}, {Name: "orders_product_fk", Column: "product_id", Table: "products", Target: "id"}},
	}}
	for i := 1; i <= 600; i++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		product := products.rows[(i-1)%len(products.rows)]
		quantity := int64(i%4 + 1)
		price, _ := new(big.Rat).SetString(product[2].Text)
		total := new(big.Rat).Mul(price, new(big.Rat).SetInt64(quantity)).FloatString(8)
		orders.rows = append(orders.rows, values(strconv.Itoa(i), strconv.Itoa((i-1)%250+1), product[0].Text, strconv.FormatInt(quantity, 10), total, []string{"pending", "paid", "shipped", "cancelled"}[i%4]))
	}
	s.tables["users"], s.tables["products"], s.tables["orders"] = users, products, orders
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return s, nil
}

func values(texts ...string) []database.Value {
	row := make([]database.Value, len(texts))
	for i, text := range texts {
		row[i].Text = text
	}
	return row
}

func (s *store) check(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.closed.Load() {
		return errors.New("demo database is closed")
	}
	return nil
}

func (s *store) Close() error { s.closed.Store(true); return nil }

func (s *store) Tables(ctx context.Context) ([]database.Table, error) {
	if err := s.check(ctx); err != nil {
		return nil, err
	}
	return []database.Table{{Name: "orders", Kind: "BASE TABLE"}, {Name: "products", Kind: "BASE TABLE"}, {Name: "users", Kind: "BASE TABLE"}}, nil
}

func (s *store) Schema(ctx context.Context, name string) (database.Schema, error) {
	if err := s.check(ctx); err != nil {
		return database.Schema{}, err
	}
	t, ok := s.tables[name]
	if !ok {
		return database.Schema{}, fmt.Errorf("unknown table %q", name)
	}
	meta := t.schema
	meta.Columns = slices.Clone(meta.Columns)
	meta.Indexes = slices.Clone(meta.Indexes)
	for i := range meta.Indexes {
		meta.Indexes[i].Columns = slices.Clone(meta.Indexes[i].Columns)
	}
	meta.ForeignKeys = slices.Clone(meta.ForeignKeys)
	return meta, nil
}

func (s *store) Query(ctx context.Context, _ string) (database.Result, error) {
	if err := s.check(ctx); err != nil {
		return database.Result{}, err
	}
	return database.Result{}, errors.New("demo supports browsing only; connect MySQL to run SQL")
}

func (s *store) Execute(ctx context.Context, sql string) (database.Result, error) {
	return s.Query(ctx, sql)
}

func numeric(c database.Column) bool {
	return strings.HasPrefix(c.Type, "bigint") || c.Type == "int" || strings.HasPrefix(c.Type, "decimal")
}

func compare(a, b database.Value, number bool) int {
	if a.Null && b.Null {
		return 0
	}
	if a.Null {
		return -1
	}
	if b.Null {
		return 1
	}
	if number {
		x, _ := new(big.Rat).SetString(a.Text)
		y, _ := new(big.Rat).SetString(b.Text)
		return x.Cmp(y)
	}
	return strings.Compare(a.Text, b.Text)
}

// Browse applies AND filters before ordering and paging. Text comparisons are
// case-sensitive; contains is literal, not LIKE. NULL sorts first ascending.
func (s *store) Browse(ctx context.Context, req database.BrowseRequest) (database.Result, error) {
	if err := s.check(ctx); err != nil {
		return database.Result{}, err
	}
	t, ok := s.tables[req.Table]
	if !ok {
		return database.Result{}, fmt.Errorf("unknown table %q", req.Table)
	}
	if req.Limit < 1 || req.Limit > 1000 || req.Offset < 0 {
		return database.Result{}, errors.New("limit must be 1..1000 and offset nonnegative")
	}
	if len(req.Filters) > 100 {
		return database.Result{}, errors.New("too many filters")
	}
	columns := make(map[string]int, len(t.schema.Columns))
	result := database.Result{Rows: [][]database.Value{}, Notice: "Read-only demo. Text filters are case-sensitive; contains matches literal text."}
	for i, c := range t.schema.Columns {
		columns[c.Name] = i
		result.Columns = append(result.Columns, c.Name)
	}
	sortColumn := 0
	if req.Sort != "" {
		var found bool
		sortColumn, found = columns[req.Sort]
		if !found {
			return database.Result{}, errors.New("unknown sort column")
		}
	}
	for _, f := range req.Filters {
		i, found := columns[f.Column]
		if !found {
			return database.Result{}, errors.New("unknown filter column")
		}
		if len(f.Value) > 64*1024 {
			return database.Result{}, errors.New("filter value too large")
		}
		switch f.Op {
		case "is-null", "not-null", "contains":
		case "eq", "ne", "gt", "gte", "lt", "lte":
			if numeric(t.schema.Columns[i]) {
				if _, ok := new(big.Rat).SetString(f.Value); !ok {
					return database.Result{}, fmt.Errorf("invalid numeric filter for %q", f.Column)
				}
			}
		default:
			return database.Result{}, fmt.Errorf("unsupported filter operator %q", f.Op)
		}
	}
	rows := make([][]database.Value, 0, len(t.rows))
	for _, row := range t.rows {
		if err := ctx.Err(); err != nil {
			return database.Result{}, err
		}
		match := true
		for _, f := range req.Filters {
			i := columns[f.Column]
			v := row[i]
			pass := false
			switch f.Op {
			case "is-null":
				pass = v.Null
			case "not-null":
				pass = !v.Null
			case "contains":
				pass = !v.Null && strings.Contains(v.Text, f.Value)
			default:
				if !v.Null {
					cmp := compare(v, database.Value{Text: f.Value}, numeric(t.schema.Columns[i]))
					switch f.Op {
					case "eq":
						pass = cmp == 0
					case "ne":
						pass = cmp != 0
					case "gt":
						pass = cmp > 0
					case "gte":
						pass = cmp >= 0
					case "lt":
						pass = cmp < 0
					case "lte":
						pass = cmp <= 0
					}
				}
			}
			if !pass {
				match = false
				break
			}
		}
		if match {
			rows = append(rows, row)
		}
	}
	sort.SliceStable(rows, func(i, j int) bool {
		cmp := compare(rows[i][sortColumn], rows[j][sortColumn], numeric(t.schema.Columns[sortColumn]))
		// Every seeded table has a numeric primary key in its first column.
		if cmp == 0 {
			cmp = compare(rows[i][0], rows[j][0], true)
		}
		if req.Desc {
			return cmp > 0
		}
		return cmp < 0
	})
	if err := ctx.Err(); err != nil {
		return database.Result{}, err
	}
	start := min(req.Offset, len(rows))
	end := start + min(req.Limit, len(rows)-start)
	result.HasMore = end < len(rows)
	for _, row := range rows[start:end] {
		result.Rows = append(result.Rows, slices.Clone(row))
	}
	return result, nil
}
