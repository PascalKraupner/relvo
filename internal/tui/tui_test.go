package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"github.com/PascalKraupner/relvo/internal/app"
	"github.com/PascalKraupner/relvo/internal/connections"
	"github.com/PascalKraupner/relvo/internal/database"
	"github.com/charmbracelet/x/ansi"
)

type fakeApp struct {
	snapshot               app.Snapshot
	events                 chan app.Snapshot
	actions                []app.Action
	profiles               []connections.Profile
	setupProfiles          []connections.Profile
	setupTests             []bool
	setupErr               error
	approved, rejected     string
	cancels, subscriptions int
}

func (a *fakeApp) Snapshot() app.Snapshot      { return a.snapshot }
func (a *fakeApp) Events() <-chan app.Snapshot { a.subscriptions++; return a.events }
func (a *fakeApp) Dispatch(_ context.Context, action app.Action) (app.Snapshot, error) {
	a.actions = append(a.actions, action)
	return a.snapshot, nil
}
func (a *fakeApp) Cancel() { a.cancels++ }
func (a *fakeApp) Approve(_ context.Context, id string) (app.Snapshot, error) {
	a.approved = id
	return a.snapshot, nil
}
func (a *fakeApp) Reject(id string) error { a.rejected = id; return nil }
func (a *fakeApp) Setup(_ context.Context, p connections.Profile, test bool) (app.Snapshot, error) {
	a.setupProfiles = append(a.setupProfiles, p)
	a.setupTests = append(a.setupTests, test)
	if a.setupErr == nil && !test {
		a.profiles = append(a.profiles, p)
	}
	return a.snapshot, a.setupErr
}

func fixture() (*model, *fakeApp) {
	a := &fakeApp{events: make(chan app.Snapshot, 4), snapshot: app.Snapshot{
		Revision: 1, Connection: "local", Database: "demo", Connections: []string{"local", "staging"}, WritesEnabled: true,
		Tables: []database.Table{{Name: "users"}, {Name: "audit_events"}, {Name: "日本語"}},
		Browse: database.BrowseRequest{Table: "users", Limit: 50},
		Schema: database.Schema{Columns: []database.Column{{Name: "id", Type: "bigint", Key: "PRI"}, {Name: "name", Type: "text"}}},
	}}
	for c := 0; c < 12; c++ {
		a.snapshot.Result.Columns = append(a.snapshot.Result.Columns, fmt.Sprintf("column_%d", c))
	}
	for r := 0; r < 90; r++ {
		row := make([]database.Value, 12)
		for c := range row {
			row[c].Text = fmt.Sprintf("r%d c%d", r, c)
		}
		a.snapshot.Result.Rows = append(a.snapshot.Result.Rows, row)
	}
	return newModel(a), a
}

func press(m *model, key string) tea.Cmd {
	special := map[string]rune{"enter": tea.KeyEnter, "esc": tea.KeyEscape, "tab": tea.KeyTab, "up": tea.KeyUp, "down": tea.KeyDown, "left": tea.KeyLeft, "right": tea.KeyRight, "home": tea.KeyHome, "end": tea.KeyEnd, "pgup": tea.KeyPgUp, "pgdown": tea.KeyPgDown}
	k := tea.Key{}
	if strings.HasPrefix(key, "ctrl+") {
		k.Mod = tea.ModCtrl
		k.Code = []rune(strings.TrimPrefix(key, "ctrl+"))[0]
	} else if code, ok := special[key]; ok {
		k.Code = code
	} else {
		k.Code = []rune(key)[0]
		k.Text = key
	}
	_, cmd := m.Update(tea.KeyPressMsg(k))
	return cmd
}

func run(cmd tea.Cmd) {
	if cmd != nil {
		cmd()
	}
}

func TestNavigationAndColumnScrolling(t *testing.T) {
	m, a := fixture()
	press(m, "j")
	if m.table != 1 || m.row != 0 {
		t.Fatal("sidebar movement leaked into grid")
	}
	press(m, "tab")
	press(m, "j")
	if m.focus != 1 || m.row != 1 {
		t.Fatal("grid focus/navigation")
	}
	press(m, "G")
	if m.row != 89 || m.top+m.bodyHeight() != 90 {
		t.Fatalf("end viewport: row=%d top=%d", m.row, m.top)
	}
	press(m, "g")
	press(m, "g")
	if m.row != 0 || m.top != 0 {
		t.Fatal("gg did not move to first row")
	}
	press(m, "ctrl+d")
	if m.row != m.bodyHeight()/2 {
		t.Fatal("half page movement")
	}
	press(m, "pgdown")
	press(m, "pgup")
	press(m, "home")
	if m.row != 0 {
		t.Fatal("home")
	}
	for range 11 {
		press(m, "l")
	}
	if m.col != 11 || m.left == 0 || m.columnsWidth(m.left, m.col) > m.gridWidth() {
		t.Fatalf("column scrolling: col=%d left=%d", m.col, m.left)
	}
	if len(a.actions) != 0 {
		t.Fatal("local navigation performed I/O")
	}
	run(press(m, "]"))
	if a.actions[0].Type != "next" {
		t.Fatal("database pagination")
	}
}

func TestExternalTableChangeKeepsSidebarInSync(t *testing.T) {
	m, a := fixture()
	m.search = "users"
	s := a.snapshot
	s.Revision++
	s.Browse.Table = "audit_events"
	m.accept(s)
	if m.search != "" || m.tables()[m.table].Name != "audit_events" {
		t.Fatal("external table change left the sidebar focused on a different table")
	}
}

func BenchmarkWideGridView(b *testing.B) {
	m, _ := fixture()
	m.width, m.height, m.focus = 160, 40, 1
	for i := range m.snap.Result.Rows {
		for c := range m.snap.Result.Rows[i] {
			m.snap.Result.Rows[i][c].Text = strings.Repeat("long value ", 6000)
		}
	}
	m.cacheWidths()
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		m.View()
	}
}

func TestInputIsolation(t *testing.T) {
	for _, kind := range []string{"sql", "write", "profile", "env", "find", "connections", "palette", "filter", "approve"} {
		t.Run(kind, func(t *testing.T) {
			m, a := fixture()
			switch kind {
			case "sql":
				m.openSQL(false)
			case "write":
				m.openSQL(true)
			case "profile":
				m.openProfile(false)
			case "env":
				m.openProfile(true)
			case "filter":
				m.openFilter(true)
			case "approve":
				p := &app.PendingWrite{ID: "p", SQL: "DELETE FROM users"}
				m.snap.Pending = p
				m.showPending(p)
			default:
				m.openInput(kind, "")
			}
			for _, key := range []string{"q", "j", "k", "h", "l", "c", "n", "e", "x", "s", "r", "[", "]", "+", "-", "/", "?", "W", "f"} {
				press(m, key)
			}
			value := m.input.Value()
			if kind == "sql" || kind == "write" {
				value = m.sql.Value()
			}
			if kind == "profile" || kind == "env" {
				value = m.fields[m.field].Value()
			}
			if value != "qjkhlcnexsr[]+-/?Wf" {
				t.Fatalf("shortcut key was not inserted: %q", value)
			}
			if m.modal != kind || m.row != 0 || m.table != 0 || len(a.actions) > 0 || a.approved != "" {
				t.Fatal("typing triggered a global shortcut")
			}
		})
	}
}

func TestFilterStepsAndPrimaryKey(t *testing.T) {
	m, a := fixture()
	m.focus = 1
	press(m, "/")
	if m.modal != "filter" || m.step != 0 {
		t.Fatal("filter column step")
	}
	press(m, "enter")
	if m.step != 1 || m.filter.Column != "id" {
		t.Fatal("column selection")
	}
	press(m, "enter")
	if m.step != 2 {
		t.Fatal("operator selection")
	}
	press(m, "4")
	press(m, "2")
	run(press(m, "enter"))
	if got := a.actions[0].Filters; len(got) != 1 || got[0] != (database.Filter{Column: "id", Op: "eq", Value: "42"}) {
		t.Fatalf("filter: %+v", got)
	}
	m.snap.Browse.Filters = a.actions[0].Filters
	press(m, "i")
	if m.step != 2 || m.filter.Column != "id" {
		t.Fatal("primary key shortcut")
	}
	press(m, "7")
	run(press(m, "enter"))
	if len(a.actions[1].Filters) != 2 {
		t.Fatal("filters were not AND-appended")
	}
	run(press(m, "x"))
	if len(a.actions[2].Filters) != 0 {
		t.Fatal("clear filters")
	}
}

func TestFuzzyFindAndConnections(t *testing.T) {
	m, a := fixture()
	press(m, "/")
	for _, r := range "aev" {
		press(m, string(r))
	}
	press(m, "enter")
	if len(m.tables()) != 1 || m.tables()[0].Name != "audit_events" {
		t.Fatal("fuzzy table search")
	}
	run(press(m, "enter"))
	if a.actions[0].Table != "audit_events" {
		t.Fatal("open filtered table")
	}
	press(m, "c")
	press(m, "s")
	run(press(m, "ctrl+t"))
	if a.actions[1].Type != "test" || a.actions[1].Connection != "staging" {
		t.Fatal("connection test")
	}
}

func TestMouseHitTesting(t *testing.T) {
	m, a := fixture()
	sw := m.sidebarWidth()
	run(m.click(tea.Mouse{X: 2, Y: 5, Button: tea.MouseLeft}))
	if a.actions[0].Table != "audit_events" {
		t.Fatal("sidebar click")
	}
	m.click(tea.Mouse{X: sw + 2, Y: 7, Button: tea.MouseLeft})
	if m.row != 3 || m.col != 0 {
		t.Fatal("cell click")
	}
	run(m.click(tea.Mouse{X: sw + 2, Y: 3, Button: tea.MouseLeft}))
	if a.actions[1].Type != "sort" {
		t.Fatal("header sort")
	}
	m.click(tea.Mouse{X: sw + 12, Y: 1, Button: tea.MouseLeft})
	if m.tab != 1 {
		t.Fatal("structure tab")
	}
	m.click(tea.Mouse{X: sw + 2, Y: 1, Button: tea.MouseLeft})
	if m.tab != 0 {
		t.Fatal("data tab")
	}
	run(m.click(tea.Mouse{X: 12, Y: m.height - 1, Button: tea.MouseLeft}))
	if a.actions[2].Type != "next" {
		t.Fatal("footer next")
	}
	m.Update(tea.MouseWheelMsg{X: sw + 2, Y: 5, Button: tea.MouseWheelDown})
	if m.row != 6 {
		t.Fatal("wheel")
	}
	m.openSQL(false)
	m.Update(tea.MouseClickMsg{X: 2, Y: 5, Button: tea.MouseLeft})
	if len(a.actions) != 3 {
		t.Fatal("modal did not capture mouse")
	}
}

func TestSanitizeAndUnicodeClipping(t *testing.T) {
	input := "東京 e\u0301 👩‍💻\x1b[31m\x00\x7f\u009b\u009d\u0085\u202e\n"
	got := safe(input)
	for _, r := range got {
		if unicode.IsControl(r) {
			t.Fatalf("control survived: %U", r)
		}
	}
	if !strings.Contains(got, "東京 e\u0301 👩‍💻") || !strings.Contains(got, `\u009b`) {
		t.Fatalf("sanitization lost Unicode or failed C1: %q", got)
	}
	for width := 1; width <= 20; width++ {
		if w := ansi.StringWidth(fit(got, width)); w != width {
			t.Fatalf("width %d got %d", width, w)
		}
	}
	if got := fit("東京", 3); got != "東 " {
		t.Fatalf("split a wide character: %q", got)
	}
	for _, line := range wrapSafe("東京東京\nhello\x1b", 4) {
		if ansi.StringWidth(line) > 4 {
			t.Fatal("wrapped overflow")
		}
	}
}

func TestResizedLayoutAndModalBounds(t *testing.T) {
	m, _ := fixture()
	for _, size := range [][2]int{{120, 40}, {80, 24}, {63, 18}, {32, 10}, {12, 6}, {1, 1}} {
		m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		for _, kind := range []string{"", "sql", "profile", "env", "filter", "inspect", "help", "approve"} {
			m.closeModal()
			switch kind {
			case "sql":
				m.openSQL(false)
			case "profile":
				m.openProfile(false)
			case "env":
				m.openProfile(true)
			case "filter":
				m.openFilter(false)
			case "approve":
				m.showPending(&app.PendingWrite{ID: "p", SQL: strings.Repeat("SELECT 東京;\n", 30)})
			default:
				m.modal = kind
			}
			for focus := 0; focus < 2; focus++ {
				m.focus = focus
				m.clamp()
				view := m.View()
				lines := strings.Split(view.Content, "\n")
				if len(lines) != size[1] {
					t.Fatalf("%v %s height=%d", size, kind, len(lines))
				}
				for _, line := range lines {
					if w := ansi.StringWidth(line); w != size[0] {
						t.Fatalf("%v %s width=%d line=%q", size, kind, w, line)
					}
				}
				if !view.AltScreen || view.MouseMode == tea.MouseModeNone {
					t.Fatal("v2 view configuration")
				}
			}
		}
	}
	m.closeModal()
	m.Update(tea.WindowSizeMsg{Width: 40, Height: 20})
	m.focus = 0
	if m.sidebarWidth() != 40 {
		t.Fatal("narrow sidebar")
	}
	press(m, "tab")
	if m.sidebarWidth() != 0 {
		t.Fatal("narrow grid")
	}
}

func TestRevisionGuardAndSingleEventChain(t *testing.T) {
	m, a := fixture()
	s := m.snap
	s.Revision = 10
	s.Status = "new"
	a.events <- s
	_, next := m.Update(m.listen()())
	if m.snap.Status != "new" || next == nil {
		t.Fatal("event chain")
	}
	m.Update(resultMsg{snapshot: a.snapshot, err: errors.New("stale")})
	if m.snap.Revision != 10 || m.notice != "" {
		t.Fatal("stale completion applied")
	}
	if a.subscriptions != 1 {
		t.Fatal("multiple event subscriptions")
	}
	close(a.events)
	_, cmd := m.Update(next())
	if cmd != nil || m.events != nil {
		t.Fatal("closed event channel loops")
	}
}

func TestLocalApprovalBoundToPendingWrite(t *testing.T) {
	m, a := fixture()
	s := m.snap
	s.Revision++
	s.Pending = &app.PendingWrite{ID: "one", Connection: "prod", Database: "billing", SQL: "DELETE FROM invoices;", Expires: time.Now().Add(time.Minute)}
	m.accept(s)
	if m.modal != "approve" || a.approved != "" {
		t.Fatal("snapshot must only prompt, not approve")
	}
	doc := strings.Join(m.documentLines(), "\n")
	for _, want := range []string{"prod", "billing", "DELETE FROM invoices;"} {
		if !strings.Contains(doc, want) {
			t.Fatalf("confirmation missing %s", want)
		}
	}
	press(m, "enter")
	if a.approved != "" {
		t.Fatal("empty approval executed")
	}
	for _, r := range "approve" {
		press(m, string(r))
	}
	s.Revision++
	p := *s.Pending
	p.ID = "two"
	p.SQL = "DROP TABLE invoices;"
	s.Pending = &p
	m.accept(s)
	if m.input.Value() != "" {
		t.Fatal("new pending request inherited typed approval")
	}
	press(m, "enter")
	if a.approved != "" {
		t.Fatal("new pending request approved accidentally")
	}
	for _, r := range "approve" {
		press(m, string(r))
	}
	run(press(m, "enter"))
	if a.approved != "two" {
		t.Fatal("local approval")
	}
	m.showPending(s.Pending)
	run(press(m, "ctrl+r"))
	if a.rejected != "two" {
		t.Fatal("local reject")
	}
}

func TestProfileIsMemoryOnlyAndPasswordMasked(t *testing.T) {
	m, a := fixture()
	m.openProfile(false)
	m.fields[0].SetValue("manual")
	m.fields[3].SetValue("user")
	m.fields[4].SetValue("very-secret-password")
	m.fields[5].SetValue("demo")
	if strings.Contains(m.View().Content, "very-secret-password") {
		t.Fatal("password visible")
	}
	m.Update(press(m, "ctrl+s")())
	if len(a.profiles) != 1 || a.profiles[0].Config.Password != "very-secret-password" || len(a.actions) != 0 || m.fields != nil || a.setupTests[0] {
		t.Fatal("manual connection lifecycle")
	}
	m.openProfile(true)
	m.fields[0].SetValue("env")
	m.fields[1].SetValue("/tmp/demo.env")
	m.fields[4].SetValue("CUSTOM_USER")
	m.Update(press(m, "ctrl+t")())
	p := a.setupProfiles[1]
	if p.EnvFile != "/tmp/demo.env" || p.Mapping["user"] != "CUSTOM_USER" || len(p.Mapping) != 1 || !a.setupTests[1] || len(a.profiles) != 1 || m.modal != "env" {
		t.Fatalf("env mapping: %+v", p)
	}
}

func TestCancelAndRawQueryGuards(t *testing.T) {
	m, a := fixture()
	m.snap.Busy = true
	press(m, "ctrl+c")
	if a.cancels != 1 {
		t.Fatal("busy ctrl+c must cancel")
	}
	m.snap.Query = true
	m.focus = 1
	for _, k := range []string{"/", "i", "s", "x", "[", "]", "+", "-", "f"} {
		run(press(m, k))
	}
	if len(a.actions) != 0 || m.modal != "" {
		t.Fatal("table actions dispatched for raw query")
	}
}

func TestInspectorScrollAndSQLSubmission(t *testing.T) {
	m, a := fixture()
	m.focus = 1
	m.snap.Result.Rows[0][0].Text = strings.Repeat("long full value 東京\n", 50)
	press(m, "enter")
	press(m, "G")
	if m.modalTop == 0 {
		t.Fatal("inspector did not scroll")
	}
	press(m, "home")
	if m.modalTop != 0 {
		t.Fatal("inspector home")
	}
	press(m, "esc")
	press(m, "Q")
	m.sql.SetValue("SELECT 'q; x; /';\nSELECT 2;")
	run(press(m, "ctrl+s"))
	if len(a.actions) != 1 || a.actions[0].Type != "query" || a.actions[0].SQL != "SELECT 'q; x; /';\nSELECT 2;" {
		t.Fatal("SQL altered or wrong action")
	}
}
