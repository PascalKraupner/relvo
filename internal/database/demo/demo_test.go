package demo

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/PascalKraupner/relvo/internal/database"
)

func openTest(t *testing.T) database.Store {
	t.Helper()
	s, err := Open(context.Background(), database.Config{Name: "demo"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func browseTest(t *testing.T, s database.Store, r database.BrowseRequest) database.Result {
	t.Helper()
	if r.Table == "" {
		r.Table = "users"
	}
	if r.Limit == 0 {
		r.Limit = 1000
	}
	result, err := s.Browse(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestSeedAndMetadata(t *testing.T) {
	s := openTest(t)
	tables, err := s.Tables(context.Background())
	if err != nil || len(tables) != 3 {
		t.Fatalf("tables: %v %v", tables, err)
	}
	for name, count := range map[string]int{"users": 250, "products": 80, "orders": 600} {
		r := browseTest(t, s, database.BrowseRequest{Table: name})
		if len(r.Rows) != count || r.HasMore {
			t.Fatalf("%s: %d rows, more=%v", name, len(r.Rows), r.HasMore)
		}
		meta, err := s.Schema(context.Background(), name)
		if err != nil {
			t.Fatal(err)
		}
		if len(meta.Columns) != len(r.Columns) || len(meta.Indexes) == 0 {
			t.Fatalf("bad schema: %+v", meta)
		}
		for _, fk := range meta.ForeignKeys {
			column := -1
			for i, c := range r.Columns {
				if c == fk.Column {
					column = i
				}
			}
			for _, row := range r.Rows {
				target := browseTest(t, s, database.BrowseRequest{Table: fk.Table, Filters: []database.Filter{{Column: fk.Target, Op: "eq", Value: row[column].Text}}})
				if len(target.Rows) != 1 {
					t.Fatalf("dangling FK: %+v", fk)
				}
			}
		}
	}
	r := browseTest(t, s, database.BrowseRequest{})
	if !strings.Contains(r.Rows[0][2].Text, "\u6771\u4eac") || len(r.Rows[0][1].Text) != 36 || len(r.Rows[0][6].Text) < 4000 || !json.Valid([]byte(r.Rows[0][6].Text)) {
		t.Fatal("missing rich seed samples")
	}
}

func TestFilters(t *testing.T) {
	s := openTest(t)
	for _, tc := range []struct {
		name, column, op, value string
		count                   int
		first                   string
	}{
		{"id", "id", "eq", "42", 1, "42"},
		{"numeric gt", "id", "gt", "248", 2, "249"},
		{"numeric gte", "id", "gte", "249", 2, "249"},
		{"numeric lt", "id", "lt", "3", 2, "1"},
		{"numeric lte", "id", "lte", "2", 2, "1"},
		{"numeric ne", "id", "ne", "1", 249, "2"},
		{"decimal exact", "balance", "eq", "9007199254740993.00000002", 1, "2"},
		{"decimal order", "balance", "lt", "9007199254740993.00000003", 2, "1"},
		{"large integer", "id", "eq", "18446744073709551002", 0, ""},
		{"null", "notes", "is-null", "", 83, "3"},
		{"not null", "notes", "not-null", "", 167, "1"},
		{"contains empty excludes null", "notes", "contains", "", 167, "1"},
		{"literal contains", "notes", "contains", "100% cotton_", 84, "1"},
		{"unicode", "name", "contains", "\u6771\u4eac", 1, "1"},
		{"empty differs from null", "notes", "eq", "", 83, "2"},
		{"ne excludes null", "notes", "ne", "", 84, "1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := browseTest(t, s, database.BrowseRequest{Filters: []database.Filter{{Column: tc.column, Op: tc.op, Value: tc.value}}})
			if len(r.Rows) != tc.count || (tc.count > 0 && r.Rows[0][0].Text != tc.first) {
				t.Fatalf("got %d rows: %v", len(r.Rows), r.Rows)
			}
		})
	}
	r := browseTest(t, s, database.BrowseRequest{Table: "products", Filters: []database.Filter{{Column: "id", Op: "eq", Value: "18446744073709551002"}}})
	if len(r.Rows) != 1 || r.Rows[0][0].Text != "18446744073709551002" {
		t.Fatal("large integer lost precision")
	}
	r = browseTest(t, s, database.BrowseRequest{Filters: []database.Filter{{Column: "id", Op: "gt", Value: "10"}, {Column: "id", Op: "lt", Value: "12"}}})
	if len(r.Rows) != 1 || r.Rows[0][0].Text != "11" {
		t.Fatal("filters must be ANDed")
	}
}

func TestSortingPagination(t *testing.T) {
	s := openTest(t)
	for _, desc := range []bool{false, true} {
		for _, column := range []string{"id", "balance"} {
			var ids []string
			for offset := 0; offset < 250; offset += 37 {
				r := browseTest(t, s, database.BrowseRequest{Sort: column, Desc: desc, Limit: 37, Offset: offset})
				if r.HasMore != (offset+len(r.Rows) < 250) {
					t.Fatal("incorrect HasMore")
				}
				for _, row := range r.Rows {
					ids = append(ids, row[0].Text)
				}
			}
			for i, id := range ids {
				want := i + 1
				if desc {
					want = 250 - i
				}
				if id != strconv.Itoa(want) {
					t.Fatalf("%s desc=%v: at %d got %s", column, desc, i, id)
				}
			}
		}
	}
	full := browseTest(t, s, database.BrowseRequest{Table: "orders", Sort: "status", Desc: true})
	var paged [][]database.Value
	for offset := 0; offset < 600; offset += 17 {
		r := browseTest(t, s, database.BrowseRequest{Table: "orders", Sort: "status", Desc: true, Offset: offset, Limit: 17})
		paged = append(paged, r.Rows...)
	}
	if !reflect.DeepEqual(full.Rows, paged) {
		t.Fatal("unstable ties across pages")
	}
	r := browseTest(t, s, database.BrowseRequest{Offset: int(^uint(0) >> 1)})
	if len(r.Rows) != 0 || r.HasMore {
		t.Fatal("offset past end")
	}
	r = browseTest(t, s, database.BrowseRequest{Sort: "notes", Limit: 1})
	if !r.Rows[0][5].Null {
		t.Fatal("NULL must sort first ascending")
	}
	r = browseTest(t, s, database.BrowseRequest{Filters: []database.Filter{{Column: "id", Op: "gt", Value: "245"}}, Offset: 3, Limit: 2})
	if len(r.Rows) != 2 || r.Rows[0][0].Text != "249" || r.HasMore {
		t.Fatal("pagination must follow filtering")
	}
}

func TestInvalidRequests(t *testing.T) {
	s := openTest(t)
	for _, req := range []database.BrowseRequest{
		{Table: "missing", Limit: 1}, {Table: "users", Limit: 0}, {Table: "users", Limit: 1001}, {Table: "users", Limit: 1, Offset: -1},
		{Table: "users", Limit: 1, Sort: "missing"},
		{Table: "users", Limit: 1, Filters: []database.Filter{{Column: "missing", Op: "eq"}}},
		{Table: "users", Limit: 1, Filters: []database.Filter{{Column: "id", Op: "unknown"}}},
		{Table: "users", Limit: 1, Filters: []database.Filter{{Column: "id", Op: "eq", Value: "bad"}}},
		{Table: "users", Limit: 1, Filters: []database.Filter{{Column: "id", Op: "eq", Value: "0"}, {Column: "name", Op: "unknown"}}},
	} {
		if _, err := s.Browse(context.Background(), req); err == nil {
			t.Fatalf("accepted %+v", req)
		}
	}
	if _, err := s.Schema(context.Background(), "missing"); err == nil {
		t.Fatal("accepted unknown schema")
	}
}

func TestReadOnlyAndOwnership(t *testing.T) {
	s := openTest(t)
	for _, run := range []func(context.Context, string) (database.Result, error){s.Query, s.Execute} {
		for _, sql := range []string{"SELECT * FROM users", "DELETE FROM users", ""} {
			if _, err := run(context.Background(), sql); err == nil || err.Error() != "demo supports browsing only; connect MySQL to run SQL" {
				t.Fatalf("SQL: %v", err)
			}
		}
	}
	r := browseTest(t, s, database.BrowseRequest{Limit: 1})
	r.Rows[0][0].Text = "broken"
	r.Columns[0] = "broken"
	meta, _ := s.Schema(context.Background(), "users")
	meta.Columns[0].Name = "broken"
	meta.Indexes[0].Columns[0] = "broken"
	r = browseTest(t, s, database.BrowseRequest{Limit: 1})
	meta, _ = s.Schema(context.Background(), "users")
	if r.Rows[0][0].Text != "1" || r.Columns[0] != "id" || meta.Columns[0].Name != "id" || meta.Indexes[0].Columns[0] != "id" {
		t.Fatal("caller mutated seed")
	}
}

func TestCancellationAndClose(t *testing.T) {
	s := openTest(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Open(ctx, database.Config{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("Open: %v", err)
	}
	checks := []func(context.Context) error{
		func(ctx context.Context) error { _, err := s.Tables(ctx); return err },
		func(ctx context.Context) error { _, err := s.Schema(ctx, "users"); return err },
		func(ctx context.Context) error {
			_, err := s.Browse(ctx, database.BrowseRequest{Table: "users", Limit: 10})
			return err
		},
		func(ctx context.Context) error { _, err := s.Query(ctx, "SELECT 1"); return err },
		func(ctx context.Context) error { _, err := s.Execute(ctx, "DELETE FROM users"); return err },
	}
	for _, check := range checks {
		if err := check(ctx); !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation: %v", err)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	for _, check := range checks {
		if err := check(context.Background()); err == nil {
			t.Fatal("accepted operation after Close")
		}
	}
}
