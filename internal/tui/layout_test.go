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

func TestHiddenColumnsShowBothEdgesAndRespondToClicks(t *testing.T) {
	m, a := fixture()
	m.focus = 1
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	left, right := m.hiddenColumns()
	if left != 0 || right == 0 {
		t.Fatalf("expected hidden columns on right: left=%d right=%d", left, right)
	}
	first := ansi.Strip(m.gridHeader())
	if !strings.HasSuffix(first, "›") || strings.HasPrefix(first, "‹") {
		t.Fatalf("right overflow indicator missing: %q", first)
	}
	if !strings.Contains(ansi.Strip(m.View().Content), "more ›") {
		t.Fatal("top bar did not show hidden-column count")
	}
	// The last cell of the header is the chevron; clicking it navigates, not sorts.
	m.click(tea.Mouse{X: m.width - 1, Y: 3, Button: tea.MouseLeft})
	left, right = m.hiddenColumns()
	if left == 0 || right == 0 || m.col != left+len(m.visibleColumns())-1 || len(a.actions) != 0 {
		t.Fatalf("right edge did not reveal a column: col=%d left=%d right=%d actions=%v", m.col, left, right, a.actions)
	}
	both := ansi.Strip(m.gridHeader())
	if !strings.HasPrefix(both, "‹") || !strings.HasSuffix(both, "›") {
		t.Fatalf("expected indicators on both sides: %q", both)
	}
	if !strings.Contains(ansi.Strip(m.View().Content), "‹ 1 more") {
		t.Fatal("top bar did not show left-side count")
	}
	m.click(tea.Mouse{X: m.sidebarWidth(), Y: 3, Button: tea.MouseLeft})
	if m.left != 0 || m.col != 0 || len(a.actions) != 0 {
		t.Fatal("left indicator did not scroll back without sorting")
	}
	m.Update(tea.WindowSizeMsg{Width: 220, Height: 24})
	left, right = m.hiddenColumns()
	if left != 0 || right != 0 || strings.Contains(ansi.Strip(m.gridHeader()), "›") {
		t.Fatal("overflow indicator remained when all columns fit")
	}
}

func TestOverflowIndicatorsAdaptToNarrowGrid(t *testing.T) {
	m, _ := fixture()
	m.focus = 1
	for _, width := range []int{32, 12, 3, 1} {
		m.Update(tea.WindowSizeMsg{Width: width, Height: 12})
		m.col = min(len(m.snap.Result.Columns)-1, width/12+1)
		m.clamp()
		view := m.View().Content
		for _, line := range strings.Split(view, "\n") {
			if ansi.StringWidth(line) != width {
				t.Fatalf("width %d: misaligned line", width)
			}
		}
		if width >= 3 && !strings.Contains(ansi.Strip(m.gridHeader()), "‹") {
			t.Fatalf("narrow grid lost left indicator at width %d", width)
		}
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
