package tui

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

type helpBinding struct{ keys, description string }
type helpSection struct {
	name     string
	bindings []helpBinding
}

var helpSections = []helpSection{
	{"Navigation", []helpBinding{
		{"tab / shift+tab", "Switch between tables and results"},
		{"h j k l / arrows", "Move through rows and columns"},
		{"gg / G", "First / last loaded row"},
		{"ctrl+d / ctrl+u", "Scroll half a screen"},
		{"pgdown / pgup", "Scroll a full screen"},
		{"[ / ]", "Previous / next database page"},
		{"+ / -", "Change page size by 25"},
		{"enter", "Open a table or inspect a row"},
	}},
	{"Explore data", []helpBinding{
		{"/", "Find a table or add an AND filter"},
		{"i", "Look up a primary key"},
		{"f", "Follow foreign key (same database)"},
		{"x", "Clear filters or table search"},
		{"s", "Sort by selected column"},
		{"v", "Toggle row / cell selection"},
		{"1 / 2 / 3", "Data / Structure / SQL"},
	}},
	{"Connections & SQL", []helpBinding{
		{"c", "Pick or test a connection"},
		{"n / e", "Add manually / map an .env file"},
		{"ctrl+s / ctrl+t", "Connect / test draft (test never saves)"},
		{"Q", "Read query · ctrl+s runs"},
		{"W", "Stage write · local approval required"},
		{"a", "Review write · type approve, then Enter"},
	}},
	{"Application", []helpBinding{
		{"ctrl+p", "Search commands"},
		{"?", "Open this keybind guide"},
		{"r", "Refresh the current table"},
		{"esc", "Close a dialog or clear table search"},
		{"ctrl+c", "Cancel running work / quit"},
		{"q", "Quit outside an input"},
	}},
}

var helpNotes = []string{
	"Mouse: click to select; click headers to sort; wheel to scroll.",
	"SQL drafts are separate and retained until you quit.",
	"Foreign keys require complete, non-NULL values in this database.",
}

// helpText remains a textual copy for search, accessibility and tests.
var helpText = func() string {
	var text strings.Builder
	for _, section := range helpSections {
		text.WriteString(section.name + "\n")
		for _, binding := range section.bindings {
			fmt.Fprintf(&text, "%s  %s\n", binding.keys, binding.description)
		}
	}
	for _, note := range helpNotes {
		text.WriteString(note + "\n")
	}
	return text.String()
}()

type helpRow struct {
	keys, description string
	section           bool
	muted             bool
}

func (m *model) helpRows() []helpRow {
	query := strings.ToLower(strings.TrimSpace(m.helpSearch))
	var rows []helpRow
	for _, section := range helpSections {
		var matches []helpBinding
		for _, binding := range section.bindings {
			if query == "" || strings.Contains(strings.ToLower(section.name+" "+binding.keys+" "+binding.description), query) {
				matches = append(matches, binding)
			}
		}
		if len(matches) == 0 {
			continue
		}
		if len(rows) > 0 {
			rows = append(rows, helpRow{})
		}
		rows = append(rows, helpRow{keys: section.name, section: true})
		for _, binding := range matches {
			rows = append(rows, helpRow{keys: binding.keys, description: binding.description})
		}
	}
	if query == "" {
		rows = append(rows, helpRow{})
		for _, note := range helpNotes {
			rows = append(rows, helpRow{keys: note, muted: true})
		}
	}
	if len(rows) == 0 {
		rows = append(rows, helpRow{keys: "No matching shortcuts", muted: true})
	}
	return rows
}

func (m *model) helpDisplayRows(inner int) []helpRow {
	rows := m.helpRows()
	if inner >= 46 {
		return rows
	}
	var expanded []helpRow
	for _, row := range rows {
		if row.description == "" {
			expanded = append(expanded, row)
			continue
		}
		expanded = append(expanded, helpRow{keys: row.keys, section: true}, helpRow{keys: "    " + row.description, muted: true})
	}
	return expanded
}

func (m *model) helpBounds() (x, y, width, height int) {
	width = min(96, max(1, m.width-4))
	if m.width < 8 {
		width = m.width
	}
	height = min(30, max(10, len(m.helpDisplayRows(max(0, width-2)))+6), max(1, m.height-4))
	if m.height < 14 {
		height = m.height
	}
	return (m.width - width) / 2, (m.height - height) / 2, width, height
}

func (m *model) helpBodyHeight() int { _, _, _, h := m.helpBounds(); return max(0, h-6) }

func (m *model) helpOverlay(background string) string {
	x, y, w, h := m.helpBounds()
	if w < 3 || h < 3 {
		return background
	}
	inner := w - 2
	frame, main, muted, accent, key, closeButton, scroll := "#75B8FF", "#C4D2DF", "#8B9EB0", "#9EC5FF", "#CDD9E9", "#172638", "#75B8FF"
	panel := "#17202E"
	if m.light {
		frame, main, muted, accent, key, closeButton, scroll = "#226888", "#243D51", "#5B7183", "#126D70", "#243D51", "#D2E6F3", "#226888"
		panel = "#F7FAFC"
	}
	border := lipgloss.NewStyle().Foreground(lipgloss.Color(frame)).Background(lipgloss.Color(panel))
	plain := lipgloss.NewStyle().Foreground(lipgloss.Color(main)).Background(lipgloss.Color(panel))
	quiet := lipgloss.NewStyle().Foreground(lipgloss.Color(muted)).Background(lipgloss.Color(panel))
	heading := lipgloss.NewStyle().Foreground(lipgloss.Color(accent)).Background(lipgloss.Color(panel)).Bold(true)
	shortcut := lipgloss.NewStyle().Foreground(lipgloss.Color(key)).Background(lipgloss.Color(panel)).Bold(true)
	closeStyle := lipgloss.NewStyle().Foreground(lipgloss.Color(panel)).Background(lipgloss.Color(scroll)).Bold(true)
	if m.light {
		closeStyle = closeStyle.Background(lipgloss.Color(closeButton))
	}
	var lines []string
	lines = append(lines, border.Render("┌"+strings.Repeat("─", inner)+"┐"))
	line := func(content string) string { return border.Render("│") + content + border.Render("│") }
	label := " keybinds"
	close := closeStyle.Render(" esc close ")
	if inner < lipgloss.Width(label)+lipgloss.Width(close) {
		close = ""
	}
	lines = append(lines, line(heading.Render(fit(label, inner-lipgloss.Width(close)))+close))
	search := " /  search by command or shortcut"
	if m.helpFiltering {
		search = " /  " + safe(m.input.Value()) + "▌"
	} else if m.helpSearch != "" {
		search = " /  " + safe(m.helpSearch) + "    (press / to edit)"
	}
	lines = append(lines, line(quiet.Render(fit(search, inner))))
	lines = append(lines, line(plain.Render(strings.Repeat(" ", inner))))
	rows := m.helpDisplayRows(inner)
	body := m.helpBodyHeight()
	top := max(0, min(m.modalTop, max(0, len(rows)-body)))
	keyWidth := min(inner, 28, max(22, inner*2/5))
	for i := 0; i < body; i++ {
		idx := top + i
		indicatorWidth := 0
		if len(rows) > body && inner >= 4 {
			indicatorWidth = 1
		}
		row := helpRow{}
		if idx < len(rows) {
			row = rows[idx]
		}
		var contents string
		switch {
		case row.section:
			contents = heading.Render(fit("  "+row.keys, inner))
		case row.muted:
			contents = quiet.Render(fit("  "+row.keys, inner))
		default:
			descriptionWidth := max(0, inner-keyWidth-indicatorWidth)
			contents = shortcut.Render(fit("  "+row.keys, keyWidth)) + plain.Render(fit(ansi.Truncate(row.description, descriptionWidth, "…"), descriptionWidth))
		}
		if indicatorWidth != 0 {
			start := top * body / max(1, len(rows))
			length := max(1, body*body/max(1, len(rows)))
			indicator := "│"
			if i >= start && i < start+length {
				indicator = "┃"
			}
			contents = fit(contents, inner-1) + border.Render(indicator)
		}
		lines = append(lines, line(contents))
	}
	footer := "  / search  ·  j/k ↑↓ scroll  ·  pgup/pgdn page  ·  esc close"
	if inner < 64 {
		footer = "  / search  ·  j/k scroll  ·  esc close"
	}
	if m.helpFiltering {
		footer = "  enter apply filter  ·  esc clear search"
	}
	lines = append(lines, line(quiet.Render(fit(footer, inner))))
	lines = append(lines, border.Render("└"+strings.Repeat("─", inner)+"┘"))
	for len(lines) > h {
		lines = lines[:h]
	}
	for len(lines) < h {
		lines = append(lines, line(plain.Render(strings.Repeat(" ", inner))))
	}
	canvas := lipgloss.NewCanvas(m.width, m.height)
	canvas.Compose(lipgloss.NewCompositor(lipgloss.NewLayer(background), lipgloss.NewLayer(strings.Join(lines, "\n")).X(x).Y(y)))
	composed := strings.Split(canvas.Render(), "\n")
	for i := range composed {
		composed[i] = fit(composed[i], m.width)
	}
	for len(composed) < m.height {
		composed = append(composed, fit("", m.width))
	}
	return strings.Join(composed[:m.height], "\n")
}
