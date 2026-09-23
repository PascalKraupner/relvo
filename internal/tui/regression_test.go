package tui

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"github.com/PascalKraupner/relvo/internal/app"
	"github.com/PascalKraupner/relvo/internal/database"
	"github.com/charmbracelet/x/ansi"
)

func TestNewFactoryStartsDisconnected(t *testing.T) {
	opened := false
	a, err := app.New(app.Options{Open: func(context.Context, database.Config) (database.Store, error) {
		opened = true
		return nil, errors.New("unexpected database access")
	}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := a.Close(); err != nil {
			t.Error(err)
		}
	})
	m := New(a)
	if opened || !strings.Contains(ansi.Strip(m.View().Content), "Disconnected") {
		t.Fatal("factory connected or omitted disconnected state")
	}
}

func TestSQLDraftsSurviveCloseSubmissionAndApproval(t *testing.T) {
	m, a := fixture()
	query := "SELECT '東京 e\u0301 👩‍💻';\nSELECT 2;"
	write := "UPDATE users SET name = '日本語' WHERE id = 42;"
	m.openSQL(false)
	m.sql.SetValue(query)
	press(m, "esc")
	m.openSQL(true)
	m.sql.SetValue(write)
	press(m, "esc")
	m.openSQL(false)
	if m.sql.Value() != query {
		t.Fatal("query draft lost on close or overwritten by write")
	}
	cmd := press(m, "ctrl+s")
	if m.queryDraft != query || len(a.actions) != 0 {
		t.Fatal("draft lost before asynchronous dispatch")
	}
	m.Update(cmd())
	if a.actions[0].SQL != query {
		t.Fatal("query dispatch altered Unicode")
	}
	m.openSQL(true)
	if m.sql.Value() != write {
		t.Fatal("write draft lost")
	}
	write += "\n-- unfinished next statement"
	m.sql.SetValue(write)
	s := m.snap
	s.Revision++
	s.Pending = &app.PendingWrite{ID: "remote", Connection: "local", Database: "demo", SQL: "DELETE FROM other_table;"}
	m.accept(s)
	if m.modal != "approve" || m.writeDraft != write {
		t.Fatal("incoming approval discarded the active SQL draft")
	}
	press(m, "esc")
	m.openSQL(true)
	if m.sql.Value() != write {
		t.Fatal("approval replaced write draft with pending SQL")
	}
	cmd = press(m, "ctrl+s")
	m.Update(resultMsg{snapshot: m.snap, err: errors.New("write rejected")})
	run(cmd)
	m.openSQL(true)
	if m.sql.Value() != write || a.actions[1].SQL != write || a.actions[1].Type != "write" {
		t.Fatal("write draft lost after submission/failure")
	}
	m.openSQL(false)
	if m.sql.Value() != query {
		t.Fatal("switching editors lost separate drafts")
	}
}

func TestSetupKeepsFailedAndTestedForm(t *testing.T) {
	for _, kind := range []string{"profile", "env"} {
		for _, test := range []bool{false, true} {
			t.Run(kind+map[bool]string{false: "-connect", true: "-test"}[test], func(t *testing.T) {
				m, a := fixture()
				m.openProfile(kind == "env")
				m.fields[0].SetValue("draft")
				if kind == "profile" {
					m.fields[4].SetValue("private-password")
				}
				before := make([]string, len(m.fields))
				for i := range m.fields {
					before[i] = m.fields[i].Value()
				}
				key := "ctrl+s"
				if test {
					key = "ctrl+t"
				}
				a.setupErr = errors.New("host unreachable")
				cmd := press(m, key)
				if !m.setupBusy || m.modal != kind || len(a.setupProfiles) != 0 {
					t.Fatal("setup was not asynchronous or discarded form")
				}
				if press(m, key) != nil {
					t.Fatal("duplicate setup allowed")
				}
				press(m, "q")
				m.Update(cmd())
				if m.setupBusy || m.modal != kind || !strings.Contains(m.notice, "host unreachable") || len(a.profiles) != 0 || len(a.actions) != 0 {
					t.Fatal("failed setup lifecycle")
				}
				for i := range m.fields {
					if m.fields[i].Value() != before[i] {
						t.Fatal("failed setup changed form fields")
					}
				}
				a.setupErr = nil
				m.Update(press(m, "ctrl+t")())
				if m.modal != kind || len(a.profiles) != 0 || !strings.Contains(m.notice, "test succeeded") {
					t.Fatal("successful test saved profile or closed form")
				}
				for i := range m.fields {
					if m.fields[i].Value() != before[i] {
						t.Fatal("test changed form fields")
					}
				}
				m.Update(press(m, "ctrl+s")())
				if m.modal != "" || m.fields != nil || len(a.profiles) != 1 || len(a.actions) != 0 {
					t.Fatal("successful setup did not close and release form")
				}
			})
		}
	}
}

func TestLateSetupDoesNotCloseNewForm(t *testing.T) {
	m, a := fixture()
	m.openProfile(false)
	m.fields[0].SetValue("old")
	cmd := press(m, "ctrl+s")
	press(m, "ctrl+c")
	if a.cancels != 1 {
		t.Fatal("setup cancellation not available before busy snapshot")
	}
	press(m, "esc")
	m.openProfile(false)
	m.fields[0].SetValue("new")
	m.Update(cmd())
	if m.modal != "profile" || m.fields[0].Value() != "new" {
		t.Fatal("late setup completion closed newer form")
	}
}

func TestPendingDoesNotDiscardSetupForm(t *testing.T) {
	m, _ := fixture()
	m.openProfile(false)
	m.fields[0].SetValue("draft")
	s := m.snap
	s.Revision++
	s.Pending = &app.PendingWrite{ID: "pending", SQL: "DELETE FROM users"}
	m.accept(s)
	if m.modal != "profile" || m.fields[0].Value() != "draft" {
		t.Fatal("pending request discarded connection form")
	}
	press(m, "esc")
	press(m, "a")
	if m.modal != "approve" || m.pending.ID != "pending" {
		t.Fatal("deferred approval unavailable")
	}
}

func TestCachedWidthsAndBoundedPreviews(t *testing.T) {
	m, _ := fixture()
	old := m.columnWidth(0)
	// Mutation without accept is deliberately a test probe: width reads must not
	// revisit the source records. Production snapshots are immutable.
	m.snap.Result.Rows[0][0].Text = strings.Repeat("東京\x1b", 16*1024)
	if m.columnWidth(0) != old {
		t.Fatal("width read rescanned row data")
	}
	m.gridRow(0)
	m.clamp()
	m.View()
	if m.columnWidth(0) != old {
		t.Fatal("frame/navigation recomputed sampled widths")
	}
	s := m.snap
	s.Revision++
	m.accept(s)
	if m.columnWidth(0) != 32 {
		t.Fatal("accepted snapshot did not invalidate widths")
	}
	m.Update(tea.WindowSizeMsg{Width: 20, Height: 15})
	m.focus = 1
	m.clamp()
	if m.columnWidth(0) != 20 || m.desiredWidths[0] != 32 {
		t.Fatal("resize corrupted desired widths")
	}
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	if m.columnWidth(0) != 32 {
		t.Fatal("resize did not restore desired widths")
	}
	text := strings.Repeat("a", previewBytes-1) + "東京" + strings.Repeat("z", 64*1024)
	preview := previewText(text)
	if !utf8.ValidString(preview) || len(preview) > previewBytes+1 || !strings.HasSuffix(preview, "~") {
		t.Fatalf("unbounded or split UTF-8 preview: %q", preview)
	}
	if strings.Contains(previewText(strings.Repeat("\x1b", 64*1024)), "\x1b") {
		t.Fatal("preview allowed terminal controls")
	}
}

func foreignFixture() (*model, *fakeApp) {
	m, a := fixture()
	m.snap.Schema.ForeignKeys = []database.ForeignKey{
		{Name: "fk_account", Column: "column_0", Database: "demo", Table: "audit_events", Target: "tenant_id"},
		{Name: "fk_account", Column: "column_1", Database: "demo", Table: "audit_events", Target: "id"},
		{Name: "unrelated", Column: "column_2", Database: "demo", Table: "users", Target: "id"},
	}
	m.snap.Result.Rows[0][0].Text = "00042"
	m.snap.Result.Rows[0][1].Text = "東京 ' \n exact"
	m.focus = 1
	return m, a
}

func TestFollowCompositeForeignKey(t *testing.T) {
	for _, selected := range []int{0, 1} {
		m, a := foreignFixture()
		m.col = selected
		run(press(m, "f"))
		if len(a.actions) != 1 {
			t.Fatalf("FK not dispatched: %s", m.notice)
		}
		want := app.Action{Type: "open-table", Table: "audit_events", Filters: []database.Filter{
			{Column: "tenant_id", Op: "eq", Value: "00042"}, {Column: "id", Op: "eq", Value: "東京 ' \n exact"},
		}}
		if !reflect.DeepEqual(a.actions[0], want) {
			t.Fatalf("foreign key filters changed: %+v", a.actions[0])
		}
	}
}

func TestForeignKeyGuards(t *testing.T) {
	cases := []struct {
		name, notice string
		change       func(*model)
	}{
		{"cross schema", "Cross-schema", func(m *model) { m.snap.Schema.ForeignKeys[1].Database = "other" }},
		{"missing target", "unavailable", func(m *model) { m.snap.Tables = nil }},
		{"null component", "NULL", func(m *model) { m.snap.Result.Rows[0][1].Null = true }},
		{"binary component", "binary", func(m *model) { m.snap.Result.Rows[0][1].Binary = true }},
		{"truncated component", "truncated", func(m *model) { m.snap.Result.Rows[0][1].Truncated = true }},
		{"missing component", "unavailable", func(m *model) { m.snap.Schema.ForeignKeys[1].Column = "absent" }},
		{"ragged row", "unavailable", func(m *model) { m.snap.Result.Rows[0] = m.snap.Result.Rows[0][:1] }},
		{"empty rows", "selected table row", func(m *model) { m.snap.Result.Rows = nil }},
		{"raw query", "selected table row", func(m *model) { m.snap.Query = true }},
		{"no constraint", "No foreign-key", func(m *model) { m.snap.Schema.ForeignKeys = nil }},
		{"ambiguous constraint", "Multiple", func(m *model) { m.snap.Schema.ForeignKeys[2].Column = "column_0" }},
		{"inconsistent target", "inconsistent", func(m *model) { m.snap.Schema.ForeignKeys[1].Table = "users" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, a := foreignFixture()
			tc.change(m)
			run(press(m, "f"))
			if len(a.actions) != 0 || !strings.Contains(m.notice, tc.notice) {
				t.Fatalf("guard failed: actions=%+v notice=%s", a.actions, m.notice)
			}
		})
	}
}

func TestHeaderHelpAndForeignKeyPalette(t *testing.T) {
	m, a := foreignFixture()
	header := strings.Split(ansi.Strip(m.View().Content), "\n")[0]
	if !strings.Contains(header, "Table: users") || !strings.Contains(header, "Writes gated") {
		t.Fatalf("missing table/write state: %s", header)
	}
	m.snap.WritesEnabled = false
	if !strings.Contains(strings.Split(ansi.Strip(m.View().Content), "\n")[0], "Read-only") {
		t.Fatal("missing read-only state")
	}
	if !strings.Contains(helpText, "Follow foreign key") || !strings.Contains(helpText, "drafts are separate") || !strings.Contains(helpText, "test never saves") {
		t.Fatal("help does not describe new behavior")
	}
	press(m, "ctrl+p")
	m.input.SetValue("follow foreign")
	run(press(m, "enter"))
	if len(a.actions) != 1 || a.actions[0].Type != "open-table" {
		t.Fatal("foreign-key palette action unavailable")
	}
}

func TestApprovalUnicodeAndMouseIsolation(t *testing.T) {
	m, a := fixture()
	sql := "UPDATE `東京` SET name = 'e\u0301 👩‍💻'; -- \u009b escaped control"
	s := m.snap
	s.Revision++
	s.Pending = &app.PendingWrite{ID: "unicode", Connection: "local", Database: "demo", SQL: sql}
	m.accept(s)
	doc := strings.Join(m.documentLines(), "\n")
	if !strings.Contains(doc, "`東京`") || !strings.Contains(doc, "e\u0301 👩‍💻") || !strings.Contains(doc, `\u009b`) || m.pending.SQL != sql {
		t.Fatal("confirmation altered SQL or Unicode")
	}
	m.input.SetValue("approve")
	for y := 0; y < m.height; y++ {
		_, cmd := m.Update(tea.MouseClickMsg{X: 2, Y: y, Button: tea.MouseLeft})
		run(cmd)
	}
	if a.approved != "" || len(a.actions) != 0 {
		t.Fatal("mouse approved or dispatched a write")
	}
	run(press(m, "enter"))
	if a.approved != "unicode" || m.pending.SQL != sql {
		t.Fatal("approval did not preserve exact request")
	}
}

func BenchmarkGridWidthCached(b *testing.B) {
	m, _ := fixture()
	for r := range m.snap.Result.Rows {
		for c := range m.snap.Result.Rows[r] {
			m.snap.Result.Rows[r][c].Text = strings.Repeat("x", 64*1024)
		}
	}
	m.cacheWidths()
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		m.columnsWidth(0, len(m.snap.Result.Columns)-1)
	}
}

func BenchmarkGridFrame64KiB(b *testing.B) {
	m, _ := fixture()
	value := strings.Repeat("東京\x1b", 16*1024)
	for r := range m.snap.Result.Rows {
		for c := range m.snap.Result.Rows[r] {
			m.snap.Result.Rows[r][c].Text = value
		}
	}
	m.cacheWidths()
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		m.View()
	}
}
