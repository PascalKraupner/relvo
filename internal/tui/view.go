package tui

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

type theme struct {
	text, muted, accent, selected, active, danger                                 lipgloss.Style
	sidebar, sideMuted, sideAccent, surface, surfaceMuted, surfaceAccent, divider lipgloss.Style
}

func (m *model) theme() theme {
	fg, muted, accent, bg, side, surface, selection, danger := "#C4D2DF", "#8399AB", "#65BCB5", "#101C2A", "#172638", "#293B52", "#345367", "#EDAB9D"
	if m.light {
		fg, muted, accent, bg, side, surface, selection, danger = "#243D51", "#5B7183", "#126D70", "#EDF3F6", "#DDEAF0", "#F7FBFC", "#BEDBE3", "#A43D36"
	}
	if m.modal == "help" {
		fg, accent = muted, muted
	}
	return theme{
		text:          lipgloss.NewStyle().Foreground(lipgloss.Color(fg)),
		muted:         lipgloss.NewStyle().Foreground(lipgloss.Color(muted)),
		accent:        lipgloss.NewStyle().Foreground(lipgloss.Color(accent)).Bold(true),
		selected:      lipgloss.NewStyle().Foreground(lipgloss.Color(fg)).Background(lipgloss.Color(selection)),
		active:        lipgloss.NewStyle().Foreground(lipgloss.Color(accent)).Background(lipgloss.Color(bg)).Bold(true),
		danger:        lipgloss.NewStyle().Foreground(lipgloss.Color(danger)),
		sidebar:       lipgloss.NewStyle().Foreground(lipgloss.Color(fg)).Background(lipgloss.Color(side)),
		sideMuted:     lipgloss.NewStyle().Foreground(lipgloss.Color(muted)).Background(lipgloss.Color(side)),
		sideAccent:    lipgloss.NewStyle().Foreground(lipgloss.Color(accent)).Background(lipgloss.Color(side)).Bold(true),
		surface:       lipgloss.NewStyle().Foreground(lipgloss.Color(fg)).Background(lipgloss.Color(surface)),
		surfaceMuted:  lipgloss.NewStyle().Foreground(lipgloss.Color(muted)).Background(lipgloss.Color(surface)),
		surfaceAccent: lipgloss.NewStyle().Foreground(lipgloss.Color(accent)).Background(lipgloss.Color(surface)).Bold(true),
		divider:       lipgloss.NewStyle().Foreground(lipgloss.Color(muted)).Background(lipgloss.Color(bg)),
	}
}

// safe makes terminal controls visible rather than allowing database content to
// inject escape sequences. Unicode letters, emoji and combining marks survive.
func safe(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch r {
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			if unicode.IsControl(r) || (r >= 0x202a && r <= 0x202e) || (r >= 0x2066 && r <= 0x2069) {
				fmt.Fprintf(&b, `\u%04x`, r)
			} else {
				b.WriteRune(r)
			}
		}
	}
	return b.String()
}

// fit is also used for trusted, styled UI strings; untrusted strings go through
// safe first. Width is in terminal cells, not bytes or runes.
func fit(s string, width int) string {
	if width <= 0 {
		return ""
	}
	s = ansi.Truncate(s, width, "")
	return s + strings.Repeat(" ", max(0, width-ansi.StringWidth(s)))
}

func wrapSafe(s string, width int) []string {
	// Preserve line breaks in full-value documents while escaping other controls.
	var out []string
	for _, line := range strings.Split(s, "\n") {
		out = append(out, strings.Split(ansi.Hardwrap(safe(line), width, true), "\n")...)
	}
	return out
}

const previewBytes = 512

// Bound work before sanitization and width measurement, including pathological
// combining sequences. Full inspector/approval documents never use previews.
func previewText(s string) string {
	if len(s) <= previewBytes {
		return safe(s)
	}
	end := previewBytes
	for end > 0 && !utf8.RuneStart(s[end]) {
		end--
	}
	return safe(s[:end]) + "~"
}

func (m *model) cellPreview(r, c int) string {
	if r >= len(m.snap.Result.Rows) || c >= len(m.snap.Result.Rows[r]) {
		return ""
	}
	v := m.snap.Result.Rows[r][c]
	if v.Null {
		return "NULL"
	}
	value := previewText(v.Text)
	if v.Binary {
		value = "[binary] " + value
	}
	if v.Truncated {
		value = "[truncated] " + value
	}
	return value
}

func (m *model) cacheWidths() {
	m.desiredWidths = make([]int, len(m.snap.Result.Columns))
	for c, name := range m.snap.Result.Columns {
		w := ansi.StringWidth(previewText(name)) + 3
		for r := 0; r < min(40, len(m.snap.Result.Rows)) && w < 32; r++ {
			w = max(w, ansi.StringWidth(m.cellPreview(r, c))+2)
		}
		m.desiredWidths[c] = min(32, max(12, w))
	}
}

func (m *model) columnWidth(c int) int {
	return min(m.gridWidth(), m.desiredWidths[c])
}

type visibleColumn struct{ index, width int }

// Lay out as many complete columns as fit, then share remaining space between
// them. Preferred widths are still bounded on narrow screens so navigation can
// reach every column; no trailing blank region is left on wide screens.
func (m *model) visibleColumns() []visibleColumn {
	available := m.gridWidth()
	var cols []visibleColumn
	used := 0
	for c := m.left; c < len(m.snap.Result.Columns); c++ {
		preferred := m.columnWidth(c)
		if used+preferred > available && len(cols) > 0 {
			break
		}
		cols = append(cols, visibleColumn{c, preferred})
		used += preferred
	}
	if len(cols) == 0 {
		return nil
	}
	spare := available - used
	for i := range cols {
		cols[i].width += spare / len(cols)
		if i < spare%len(cols) {
			cols[i].width++
		}
	}
	return cols
}

func (m *model) columnsWidth(start, end int) int {
	w := 0
	for c := start; c <= end && c < len(m.snap.Result.Columns); c++ {
		w += m.columnWidth(c)
	}
	return w
}

func (m *model) View() tea.View {
	t := m.theme()
	connection := "Disconnected  /  c connections   n new   e environment file"
	if m.snap.Connection != "" {
		connection = safe(m.snap.Connection) + " / " + safe(m.snap.Database)
	}
	state := ""
	if m.snap.Busy {
		state = "  [working / Ctrl+c cancel]"
	}
	access := "Read-only"
	if m.snap.WritesEnabled {
		access = "Writes gated"
	}
	current := "No table"
	if m.snap.Browse.Table != "" {
		current = "Table: " + previewText(m.snap.Browse.Table)
	}
	if m.snap.Query {
		current = "Raw query"
	}
	header := " RELVO  [" + access + "]  " + current + "  |  " + connection + state
	lines := []string{t.active.Render(fit(header, m.width))}
	if m.modal != "" && m.modal != "help" {
		lines = append(lines, m.modalLines()...)
	} else {
		sw, gw := m.sidebarWidth(), m.gridWidth()
		sp := sw
		separator := ""
		if sw > 0 && sw < m.width {
			sp--
			separator = t.divider.Render("│")
		}
		sideTitle := " TABLES"
		if m.focus == 0 {
			sideTitle = " > TABLES"
		}
		tabs := " Data    Structure     SQL"
		if m.tab == 0 {
			tabs = " [Data]  Structure     SQL"
		} else {
			tabs = " Data    [Structure]   SQL"
		}
		if sw == m.width {
			tabs = ""
			gw = 0
		}
		lines = append(lines, t.sideAccent.Render(fit(sideTitle, sp))+separator+t.surfaceAccent.Render(fit(tabs, gw)))
		chips := "No filters  / add   i primary key"
		if m.snap.Query {
			chips = "RAW QUERY / filters, table sorting and pagination unavailable"
		} else if len(m.snap.Browse.Filters) > 0 {
			var values []string
			for _, f := range m.snap.Browse.Filters {
				values = append(values, safe(f.Column)+" "+safe(f.Op)+" "+safe(f.Value))
			}
			chips = strings.Join(values, "  AND  ") + "  / add  x clear"
		}
		search := fmt.Sprintf(" %d tables", len(m.tables()))
		if m.search != "" {
			search = " / " + safe(m.search)
		}
		lines = append(lines, t.sideMuted.Render(fit(search, sp))+separator+t.surfaceMuted.Render(fit(chips, gw)))
		header := ""
		if m.tab == 0 {
			header = m.gridHeader()
		} else {
			header = t.surfaceAccent.Render(fit(" Column / Type / Nullable / Key / Default", gw))
		}
		if gw == 0 {
			header = ""
		}
		lines = append(lines, t.sideMuted.Render(fit(" / find  Enter open", sp))+separator+header)
		ts := m.tables()
		structure := m.structureLines()
		for y := 0; y < m.bodyHeight(); y++ {
			side := ""
			idx := m.tableTop + y
			if idx < len(ts) {
				side = " " + safe(ts[idx].Name)
			}
			side = fit(side, sp)
			if idx == m.table && idx < len(ts) {
				if m.focus == 0 {
					side = t.active.Render(side)
				} else {
					side = t.selected.Render(side)
				}
			} else {
				side = t.sidebar.Render(side)
			}
			body := ""
			if gw > 0 {
				if m.tab == 1 {
					if y+m.structureTop < len(structure) {
						body = safe(structure[y+m.structureTop])
					}
					body = t.surface.Render(fit(body, gw))
				} else {
					body = m.gridRow(m.top + y)
				}
				if m.tab == 0 && len(m.snap.Result.Rows) == 0 && y == 1 {
					message := " No rows match. / filter  x clear  Q SQL"
					if m.snap.Browse.Table == "" && !m.snap.Query {
						message = " Select a table to browse. Tab focuses tables."
					}
					if m.snap.Connection == "" {
						message = " Connect to begin. c profiles  n manual  e .env"
					}
					body = t.surfaceMuted.Render(fit(message, gw))
				}
			}
			lines = append(lines, side+separator+body)
		}
	}
	// Every screen has exactly two footer rows, at stable mouse coordinates.
	available := max(0, m.height-2)
	if len(lines) > available {
		lines = lines[:available]
	}
	for len(lines) < available {
		lines = append(lines, fit("", m.width))
	}
	status := safe(m.snap.Status)
	if m.snap.Result.Notice != "" {
		status += "  " + safe(m.snap.Result.Notice)
	}
	if m.notice != "" {
		status = m.notice
	} else if m.snap.Error != "" {
		status = safe(m.snap.Error)
	}
	if status == "" {
		status = "Ready"
	}
	style := t.muted
	if m.notice != "" || m.snap.Error != "" {
		style = t.danger
	}
	lines = append(lines, style.Render(fit(" "+status, m.width)))
	footer := fmt.Sprintf(" [ Prev ]  [ Next ]  %d rows / size %d / offset %d  | Tab focus  / filter  Q SQL  ? help", len(m.snap.Result.Rows), m.snap.Browse.Limit, m.snap.Browse.Offset)
	if m.snap.Result.HasMore {
		footer += "  more >"
	}
	if m.snap.Pending != nil {
		footer = " [ Prev ]  [ Next ]  a REVIEW PENDING WRITE  |  ? help"
	}
	if m.modal != "" {
		footer = " Esc close  |  " + m.modalHint()
	}
	lines = append(lines, t.active.Render(fit(footer, m.width)))
	if len(lines) > m.height {
		lines = lines[:m.height]
	}
	for i := range lines {
		lines[i] = fit(lines[i], m.width)
	}
	content := strings.Join(lines, "\n")
	if m.modal == "help" {
		content = m.helpOverlay(content)
	}
	v := tea.NewView(content)
	v.AltScreen = true
	v.MouseMode = tea.MouseModeCellMotion
	v.WindowTitle = "relvo"
	return v
}

func (m *model) gridHeader() string {
	t := m.theme()
	var b strings.Builder
	width := 0
	for _, col := range m.visibleColumns() {
		c, w := col.index, col.width
		name := previewText(m.snap.Result.Columns[c])
		if m.snap.Browse.Sort == m.snap.Result.Columns[c] {
			if m.snap.Browse.Desc {
				name += " v"
			} else {
				name += " ^"
			}
		}
		cell := fit(" "+name, w)
		if c == m.col && m.focus == 1 {
			b.WriteString(t.active.Render(cell))
		} else {
			b.WriteString(t.surfaceAccent.Render(cell))
		}
		width += w
	}
	return t.surface.Render(fit(b.String(), m.gridWidth()))
}

func (m *model) gridRow(r int) string {
	if r >= len(m.snap.Result.Rows) {
		return m.theme().surface.Render(fit("", m.gridWidth()))
	}
	t := m.theme()
	var b strings.Builder
	width := 0
	for _, col := range m.visibleColumns() {
		c, w := col.index, col.width
		value := ansi.Truncate(m.cellPreview(r, c), max(0, w-2), "~")
		cell := fit(" "+value, w)
		style := t.surface
		if r == m.row {
			style = t.selected
			if m.focus == 1 && (m.rowMode || c == m.col) {
				style = t.active
			}
		}
		b.WriteString(style.Render(cell))
		width += w
	}
	return t.surface.Render(fit(b.String(), m.gridWidth()))
}

func (m *model) structureLines() []string {
	var out []string
	for _, c := range m.snap.Schema.Columns {
		def := "NULL"
		if c.Default != nil {
			def = *c.Default
		}
		out = append(out, fmt.Sprintf(" %s  %s  nullable:%t  %s  default:%s  %s", c.Name, c.Type, c.Nullable, c.Key, def, c.Extra))
	}
	out = append(out, "", " INDEXES")
	for _, i := range m.snap.Schema.Indexes {
		out = append(out, fmt.Sprintf(" %s (%s) unique:%t", i.Name, strings.Join(i.Columns, ", "), i.Unique))
	}
	out = append(out, "", " FOREIGN KEYS")
	for _, fk := range m.snap.Schema.ForeignKeys {
		target := fk.Table + "." + fk.Target
		if fk.Database != "" {
			target = fk.Database + "." + target
		}
		out = append(out, fmt.Sprintf(" %s: %s -> %s", fk.Name, fk.Column, target))
	}
	return out
}

func (m *model) modalHint() string {
	switch m.modal {
	case "sql", "write":
		return "Ctrl+s / Ctrl+Enter submit  |  Enter newline  |  draft retained"
	case "profile", "env":
		if m.setupBusy {
			return "Checking connection; Ctrl+c cancels. Fields retained on failure."
		}
		return "Tab next field  Shift+Tab previous  Ctrl+s connect  Ctrl+t test (not saved)"
	case "connections":
		return "Up/Down select  Enter connect  Ctrl+t test"
	case "approve":
		return "PgUp/PgDown review SQL  |  type approve + Enter execute  Ctrl+r reject"
	case "inspect", "help":
		return "j/k or PgUp/PgDown scroll  Home/End"
	default:
		return "Up/Down select  Enter confirm"
	}
}

func (m *model) modalLines() []string {
	t := m.theme()
	title := map[string]string{"sql": "SQL / read query", "write": "SQL / stage write", "connections": "Connections", "profile": "New connection / memory only, never saved", "env": "Environment file / custom variable mapping", "find": "Find table / fuzzy subsequence", "palette": "Commands", "inspect": fmt.Sprintf("Row %d / full values", m.row+1), "help": "Help", "approve": "WRITE CONFIRMATION / local approval only"}[m.modal]
	if m.modal == "filter" {
		title = m.filterTitle()
	}
	lines := []string{t.accent.Render(" " + title), ""}
	switch m.modal {
	case "sql", "write":
		lines = append(lines, strings.Split(m.sql.View(), "\n")...)
	case "profile", "env":
		labels := profileLabels
		if m.modal == "env" {
			labels = envLabels
		}
		// A compact field summary keeps the active input visible on small screens.
		lines = append(lines, t.muted.Render(fmt.Sprintf(" Field %d / %d: %s", m.field+1, len(labels), labels[m.field])), " "+m.fields[m.field].View(), "")
		room := max(0, m.height-11)
		start := max(0, min(m.field-room/2, len(labels)-room))
		for i := start; i < min(len(labels), start+room); i++ {
			value := safe(m.fields[i].Value())
			if m.modal == "profile" && i == 4 && value != "" {
				value = "********"
			}
			prefix := "   "
			if i == m.field {
				prefix = " > "
			}
			lines = append(lines, t.muted.Render(prefix+labels[i]+": "+value))
		}
	case "inspect", "help", "approve":
		doc := m.documentLines()
		h := max(1, m.height-6)
		if m.modal == "approve" {
			h = max(1, m.height-9)
		}
		start := min(m.modalTop, max(0, len(doc)-h))
		end := min(len(doc), start+h)
		for _, line := range doc[start:end] {
			lines = append(lines, " "+line)
		}
		if m.modal == "approve" {
			lines = append(lines, t.muted.Render(fmt.Sprintf(" SQL review %d-%d / %d lines", start+1, end, len(doc))), " "+m.input.View())
		}
	case "find":
		lines = append(lines, " "+m.input.View(), "")
		for _, table := range m.snap.Tables {
			if fuzzy(m.input.Value(), table.Name) && len(lines) < m.height-4 {
				lines = append(lines, " "+safe(table.Name))
			}
		}
	default:
		if m.modal == "filter" {
			lines = append(lines, t.muted.Render(" "+safe(m.filter.Column)+" "+m.filter.Op))
			if m.step == 2 {
				lines = append(lines, " "+m.input.View())
				break
			}
		} else {
			lines = append(lines, " "+m.input.View())
		}
		list := m.choices()
		room := max(1, m.height-9)
		selected := bound(m.choice, len(list))
		start := max(0, selected-room+1)
		if len(list) == 0 {
			lines = append(lines, t.muted.Render(" No matches. Esc to return; n creates a connection."))
		}
		for i := start; i < min(len(list), start+room); i++ {
			line := fit("   "+safe(list[i]), m.width)
			if i == selected {
				line = t.active.Render(fit(" > "+safe(list[i]), m.width))
			} else {
				line = t.text.Render(line)
			}
			lines = append(lines, line)
		}
	}
	return lines
}
