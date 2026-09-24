package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/PascalKraupner/relvo/internal/database"
	"github.com/charmbracelet/x/ansi"
)

func TestConnectionPanelShowsFieldsAndMasksPassword(t *testing.T) {
	m, _ := fixture()
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 36})
	press(m, "n")
	m.fields[4].SetValue("private-value")
	view := ansi.Strip(m.View().Content)
	for _, expected := range []string{"Add connection", "PROFILE", "ENDPOINT", "ACCESS", "DATABASE", "Host", "Port", "User", "Password", "TLS (true / off)", "esc close"} {
		if !strings.Contains(view, expected) {
			t.Fatalf("connection form missing %q", expected)
		}
	}
	if strings.Contains(view, "private-value") || !strings.Contains(view, "••••••••") {
		t.Fatal("inactive password field is not masked")
	}
	if !strings.Contains(view, "users") || !strings.Contains(view, "┌") {
		t.Fatal("connection form did not float over the current table")
	}
	for range 4 {
		press(m, "tab")
	}
	if m.field != 4 || strings.Contains(m.View().Content, "private-value") {
		t.Fatal("focused password exposed its value")
	}
	press(m, "up")
	if m.field != 3 {
		t.Fatal("up did not focus the previous field")
	}
	_, y, width, _ := m.panelBounds()
	rows := m.formRows(width - 2)
	start := m.formViewportStart(rows)
	for i, row := range rows[start:] {
		if row.field == 5 {
			m.Update(tea.MouseClickMsg{X: m.width / 2, Y: y + 4 + i, Button: tea.MouseLeft})
			if m.field != 5 {
				t.Fatal("click did not focus database field")
			}
			break
		}
	}
	press(m, "esc")
	if m.fields != nil {
		t.Fatal("closing panel retained password-bearing fields")
	}
	press(m, "e")
	if view := ansi.Strip(m.View().Content); !strings.Contains(view, "VARIABLE MAPPING") || !strings.Contains(view, "DB_HOST") {
		t.Fatal("environment mapping form is not visible")
	}
	m.Update(tea.WindowSizeMsg{Width: 36, Height: 13})
	m.field = len(m.fields) - 1
	m.notice = "Connection setup failed: host unreachable"
	if view := ansi.Strip(m.View().Content); !strings.Contains(view, "Connection setup failed") {
		t.Fatal("setup error disappeared when the form scrolled")
	}
}

func TestInspectorPanelKeepsFullValueAndScrolls(t *testing.T) {
	m, _ := fixture()
	m.Update(tea.WindowSizeMsg{Width: 110, Height: 32})
	m.focus = 1
	m.snap.Result.Rows[0][0].Text = "東京 e\u0301" + strings.Repeat(" hello", 350) + "\x1b[31m"
	press(m, "enter")
	view := ansi.Strip(m.View().Content)
	if !strings.Contains(view, "Row 1") || !strings.Contains(view, "column_0") || !strings.Contains(view, "東京 e\u0301") || !strings.Contains(view, "TABLES") {
		t.Fatal("inspector did not display full values above the table")
	}
	_, _, width, _ := m.panelBounds()
	full := strings.Join(m.detailLines(width-2), "\n")
	if strings.Contains(full, "\x1b[31m") || !strings.Contains(ansi.Strip(full), `\u`) || !strings.Contains(ansi.Strip(full), "001b[31m") {
		t.Fatal("inspector failed to escape database control sequences")
	}
	press(m, "end")
	if m.modalTop == 0 {
		t.Fatal("inspector cannot scroll long values")
	}
	press(m, "home")
	if m.modalTop != 0 {
		t.Fatal("inspector home did not reset scroll")
	}
}

func TestStructureColumnsAndNumberedTabs(t *testing.T) {
	m, a := fixture()
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 28})
	m.snap.Schema = database.Schema{
		Columns: []database.Column{
			{Name: "id", Type: "bigint unsigned", Key: "PRI"},
			{Name: "email", Type: "varchar(255)", Nullable: true, Default: ptr("none\x1b[31m")},
		},
		Indexes:     []database.Index{{Name: "PRIMARY", Columns: []string{"id"}, Unique: true}},
		ForeignKeys: []database.ForeignKey{{Name: "fk_owner", Column: "id", Table: "owners", Target: "id"}},
	}
	m.click(tea.Mouse{X: m.sidebarWidth() + len(tabLabels[0]) + 1, Y: 1, Button: tea.MouseLeft})
	if m.tab != 1 {
		t.Fatal("numbered Structure tab did not respond to mouse")
	}
	view := m.View().Content
	plain := ansi.Strip(view)
	for _, expected := range []string{"1 Data", "2 Structure", "3 SQL", "SCHEMA", "COLUMNS", "NAME", "TYPE", "NULL", "KEY", "DEFAULT", "INDEXES", "RELATIONSHIPS", "owners.id"} {
		if !strings.Contains(plain, expected) {
			t.Fatalf("schema missing %q", expected)
		}
	}
	if strings.Contains(view, "\x1b[31m") || !strings.Contains(plain, `\u001b`) {
		t.Fatal("schema displayed unsafe default")
	}
	m.Update(tea.WindowSizeMsg{Width: 52, Height: 20})
	plain = ansi.Strip(m.View().Content)
	if !strings.Contains(plain, "NOT NULL") || !strings.Contains(plain, "NULLABLE") || strings.Contains(plain, "NO NULL") {
		t.Fatal("stacked structure metadata is hard to read")
	}
	m.click(tea.Mouse{X: len(tabLabels[0]) + len(tabLabels[1]) + 1, Y: 1, Button: tea.MouseLeft})
	if m.modal != "sql" {
		t.Fatal("numbered SQL tab did not open editor")
	}
	if len(a.actions) != 0 {
		t.Fatal("view switch dispatched database work")
	}
}

func ptr(s string) *string { return &s }
