package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/PascalKraupner/relvo/internal/database"
	"github.com/charmbracelet/x/ansi"
)

func TestGridDistributesWidthAndKeepsMouseColumnsAligned(t *testing.T) {
	m, a := fixture()
	m.snap.Result.Columns = []string{"id", "name", "status"}
	m.snap.Result.Rows = [][]database.Value{{{Text: "42"}, {Text: "Alice"}, {Text: "active"}}}
	m.cacheWidths()
	m.focus = 1
	for _, width := range []int{80, 120, 200} {
		m.Update(tea.WindowSizeMsg{Width: width, Height: 24})
		cols := m.visibleColumns()
		if len(cols) != 3 {
			t.Fatalf("%d columns at width %d", len(cols), width)
		}
		sum := 0
		for _, c := range cols {
			sum += c.width
		}
		if sum != m.gridWidth() {
			t.Fatalf("width %d: columns span %d / %d", width, sum, m.gridWidth())
		}
		if width >= 120 && cols[0].width <= m.desiredWidths[0] {
			t.Fatal("wide grid left unused space")
		}
		x := m.sidebarWidth() + cols[0].width + cols[1].width + 1
		m.click(tea.Mouse{X: x, Y: 5, Button: tea.MouseLeft})
		if m.col != 2 || m.row != 0 {
			t.Fatalf("click selected %d/%d at width %d", m.col, m.row, width)
		}
		for _, line := range strings.Split(m.View().Content, "\n") {
			if ansi.StringWidth(line) != width {
				t.Fatalf("line does not fill %d cells", width)
			}
		}
	}
	if len(a.actions) != 0 {
		t.Fatal("selection dispatched an action")
	}
	divider := m.sidebarWidth() - 1
	m.click(tea.Mouse{X: divider, Y: 5, Button: tea.MouseLeft})
	if len(a.actions) != 0 {
		t.Fatal("separator opened a table")
	}
}

func TestFloatingHelpKeepsContextAndFiltersBindings(t *testing.T) {
	m, _ := fixture()
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 38})
	base := ansi.Strip(m.View().Content)
	press(m, "?")
	view := ansi.Strip(m.View().Content)
	if !strings.Contains(view, "┌") || !strings.Contains(view, "└") || !strings.Contains(view, "keybinds") || !strings.Contains(view, "esc close") {
		t.Fatal("help was not rendered as a bordered overlay")
	}
	if !strings.Contains(view, "users") || !strings.Contains(base, "users") || !strings.Contains(view, "TABLES") {
		t.Fatal("help replaced the database view")
	}
	x, y, w, h := m.helpBounds()
	if x <= 0 || y <= 0 || w != 96 || h != 30 {
		t.Fatalf("help position: %d,%d %dx%d", x, y, w, h)
	}
	press(m, "/")
	for _, r := range "foreign" {
		press(m, string(r))
	}
	filtered := ansi.Strip(m.View().Content)
	if !strings.Contains(filtered, "Follow foreign key") || strings.Contains(filtered, "Previous / next database page") {
		t.Fatal("help search did not narrow the bindings")
	}
	press(m, "enter")
	if m.helpFiltering || m.helpSearch != "foreign" {
		t.Fatal("help search was not applied")
	}
	press(m, "/")
	press(m, "esc")
	if m.modal != "help" || m.helpSearch != "" {
		t.Fatal("escape in search should only clear search")
	}
	press(m, "/")
	m.Update(tea.PasteMsg{Content: "primary key"})
	if m.helpSearch != "primary key" || !strings.Contains(ansi.Strip(m.View().Content), "Look up a primary key") {
		t.Fatal("pasted help search did not update the overlay")
	}
	press(m, "enter")
	press(m, "/")
	press(m, "esc")
	press(m, "end")
	if m.modalTop == 0 {
		t.Fatal("help cannot scroll")
	}
	press(m, "esc")
	if m.modal != "" {
		t.Fatal("escape did not close help")
	}
}

func TestHelpOverlayFitsSmallTerminalAndMouseCloses(t *testing.T) {
	m, _ := fixture()
	for _, size := range [][2]int{{100, 24}, {50, 18}, {32, 10}, {12, 6}, {1, 1}} {
		m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		press(m, "?")
		view := m.View().Content
		lines := strings.Split(view, "\n")
		if len(lines) != size[1] {
			t.Fatalf("height %v = %d", size, len(lines))
		}
		for _, line := range lines {
			if ansi.StringWidth(line) != size[0] {
				t.Fatalf("width %v: %q", size, line)
			}
		}
		if size[0] == 32 {
			plain := ansi.Strip(view)
			if !strings.Contains(plain, "tab / shift+tab") || !strings.Contains(plain, "Switch between") {
				t.Fatal("narrow help should stack keys and descriptions")
			}
		}
		press(m, "esc")
	}
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 38})
	press(m, "?")
	x, y, w, _ := m.helpBounds()
	m.Update(tea.MouseClickMsg{X: x + w - 4, Y: y + 1, Button: tea.MouseLeft})
	if m.modal != "" {
		t.Fatal("help close button did not close modal")
	}
}
