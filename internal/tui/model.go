// Package tui provides the local, interactive database browser.
package tui

import (
	"context"
	"fmt"
	"strings"

	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"github.com/PascalKraupner/relvo/internal/app"
	"github.com/PascalKraupner/relvo/internal/connections"
	"github.com/PascalKraupner/relvo/internal/database"
)

type backend interface {
	Snapshot() app.Snapshot
	Events() <-chan app.Snapshot
	Dispatch(context.Context, app.Action) (app.Snapshot, error)
	Cancel()
	Approve(context.Context, string) (app.Snapshot, error)
	Reject(string) error
	Setup(context.Context, connections.Profile, bool) (app.Snapshot, error)
}

type resultMsg struct {
	snapshot app.Snapshot
	err      error
}
type eventMsg struct {
	snapshot app.Snapshot
	open     bool
}

type setupMsg struct {
	resultMsg
	formID uint64
	test   bool
}

type model struct {
	app                    backend
	events                 <-chan app.Snapshot
	snap                   app.Snapshot
	width, height          int
	light                  bool
	focus                  int // 0: tables, 1: results
	tab                    int // 0: data, 1: structure
	row, col, top, left    int
	table, tableTop        int
	search                 string
	g                      bool
	rowMode                bool
	structureTop           int
	modal                  string
	input                  textinput.Model
	sql                    textarea.Model
	queryDraft, writeDraft string
	desiredWidths          []int
	fields                 []textinput.Model
	formID                 uint64
	setupBusy              bool
	field                  int
	choice                 int
	step                   int
	filter                 database.Filter
	modalTop               int
	helpSearch             string
	helpFiltering          bool
	pending                *app.PendingWrite
	notice                 string
}

func newModel(a backend) *model {
	i := textinput.New()
	i.Prompt = "> "
	i.SetVirtualCursor(true)
	s := textarea.New()
	s.SetVirtualCursor(true)
	s.ShowLineNumbers = true
	s.CharLimit = 0
	s.MaxHeight = 0
	s.MaxWidth = 0
	m := &model{app: a, events: a.Events(), snap: a.Snapshot(), width: 100, height: 30, input: i, sql: s}
	m.resizeInputs()
	m.cacheWidths()
	if m.snap.Pending != nil {
		m.showPending(m.snap.Pending)
	}
	return m
}

func (m *model) Init() tea.Cmd {
	return tea.Batch(m.listen(), tea.RequestBackgroundColor, textinput.Blink)
}

func (m *model) listen() tea.Cmd {
	if m.events == nil {
		return nil
	}
	ch := m.events
	return func() tea.Msg { s, ok := <-ch; return eventMsg{s, ok} }
}

func (m *model) dispatch(a app.Action) tea.Cmd {
	if m.snap.Query {
		switch a.Type {
		case "filter", "sort", "next", "prev", "page-size":
			m.notice = "Raw query results: edit SQL to filter, sort or paginate"
			return nil
		}
	}
	m.notice = ""
	b := m.app
	return func() tea.Msg { s, err := b.Dispatch(context.Background(), a); return resultMsg{s, err} }
}

func (m *model) accept(s app.Snapshot) {
	if s.Revision < m.snap.Revision {
		return
	}
	tableChanged := s.Connection != m.snap.Connection || s.Database != m.snap.Database || s.Browse.Table != m.snap.Browse.Table
	changed := tableChanged || s.Browse.Offset != m.snap.Browse.Offset || s.Query != m.snap.Query
	m.snap = s
	m.cacheWidths()
	if tableChanged {
		if !fuzzy(m.search, s.Browse.Table) {
			m.search = ""
		}
		for i, table := range m.tables() {
			if table.Name == s.Browse.Table {
				m.table = i
				break
			}
		}
	}
	if changed {
		m.row, m.col, m.top, m.left, m.structureTop = 0, 0, 0, 0, 0
		if m.modal == "filter" || m.modal == "inspect" {
			m.closeModal()
		}
	}
	if s.Pending != nil && (m.pending == nil || *m.pending != *s.Pending) {
		// Do not discard a connection form while its asynchronous setup completes.
		if m.modal != "profile" && m.modal != "env" {
			m.showPending(s.Pending)
		}
	} else if s.Pending == nil && m.pending != nil {
		m.pending = nil
		if m.modal == "approve" {
			m.closeModal()
		}
	}
	m.clamp()
}

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch v := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = max(1, v.Width), max(1, v.Height)
		m.resizeInputs()
		m.clamp()
		return m, nil
	case tea.BackgroundColorMsg:
		m.light = !v.IsDark()
		m.resizeInputs()
		return m, nil
	case eventMsg:
		if !v.open {
			m.events = nil
			return m, nil
		}
		m.accept(v.snapshot)
		return m, m.listen()
	case resultMsg:
		if v.snapshot.Revision < m.snap.Revision {
			return m, nil
		}
		m.accept(v.snapshot)
		if v.err != nil {
			m.notice = safe(v.err.Error())
		}
		return m, nil
	case setupMsg:
		m.accept(v.snapshot)
		if v.formID != m.formID || (m.modal != "profile" && m.modal != "env") {
			return m, nil
		}
		m.setupBusy = false
		if v.err != nil {
			m.notice = "Connection setup failed: " + safe(v.err.Error())
		} else if v.test {
			m.notice = "Connection test succeeded. Draft not saved; Ctrl+s connects."
		} else {
			m.closeModal()
			m.notice = "Connected. Profile added in memory only."
		}
		return m, nil
	case tea.KeyPressMsg:
		if v.String() == "ctrl+c" {
			if m.snap.Busy || m.setupBusy {
				m.app.Cancel()
				m.notice = "Cancellation requested"
				return m, nil
			}
			return m, tea.Quit
		}
	}
	if m.modal != "" {
		return m, m.updateModal(msg)
	}
	switch v := msg.(type) {
	case tea.KeyPressMsg:
		return m, m.key(v.String())
	case tea.MouseClickMsg:
		return m, m.click(v.Mouse())
	case tea.MouseWheelMsg:
		delta := 3
		if v.Button == tea.MouseWheelUp {
			delta = -3
		}
		if v.Button == tea.MouseWheelLeft {
			m.col--
			m.clamp()
			return m, nil
		}
		if v.Button == tea.MouseWheelRight {
			m.col++
			m.clamp()
			return m, nil
		}
		if m.sidebarWidth() > 0 && v.X < m.sidebarWidth() {
			m.table += delta
		} else if m.tab == 1 {
			m.structureTop += delta
		} else {
			m.row += delta
		}
		m.clamp()
	}
	return m, nil
}

func (m *model) key(k string) tea.Cmd {
	gg := m.g && k == "g"
	m.g = k == "g" && !gg
	switch k {
	case "q":
		return tea.Quit
	case "esc":
		if m.snap.Busy {
			m.app.Cancel()
		}
		m.search = ""
		m.clamp()
	case "tab", "shift+tab":
		m.focus = 1 - m.focus
		m.clamp()
	case "1":
		m.tab = 0
		m.focus = 1
	case "2":
		m.tab = 1
		m.focus = 1
	case "3", "Q":
		return m.openSQL(false)
	case "W":
		if !m.snap.WritesEnabled {
			m.notice = "Writes are disabled. Start with write access to stage SQL."
			return nil
		}
		return m.openSQL(true)
	case "c":
		m.choice = 0
		return m.openInput("connections", "Find a connection")
	case "n":
		return m.openProfile(false)
	case "e":
		return m.openProfile(true)
	case "ctrl+p":
		m.choice = 0
		return m.openInput("palette", "Find a command")
	case "?":
		m.modal = "help"
		m.modalTop = 0
		m.helpSearch = ""
		m.helpFiltering = false
	case "/":
		if m.focus == 0 {
			return m.openInput("find", "Fuzzy table name")
		}
		return m.openFilter(false)
	case "find-table":
		m.focus = 0
		return m.openInput("find", "Fuzzy table name")
	case "inspect-row":
		m.focus, m.tab = 1, 0
		return m.key("enter")
	case "i":
		return m.openFilter(true)
	case "f":
		return m.followForeignKey()
	case "x":
		if m.focus == 1 {
			return m.dispatch(app.Action{Type: "filter"})
		}
		m.search = ""
		m.clamp()
	case "s":
		return m.sort()
	case "v":
		m.rowMode = !m.rowMode
	case "r":
		return m.dispatch(app.Action{Type: "refresh"})
	case "[":
		return m.dispatch(app.Action{Type: "prev"})
	case "]":
		return m.dispatch(app.Action{Type: "next"})
	case "+", "=":
		return m.dispatch(app.Action{Type: "page-size", PageSize: min(1000, max(25, m.snap.Browse.Limit+25))})
	case "-":
		return m.dispatch(app.Action{Type: "page-size", PageSize: max(1, m.snap.Browse.Limit-25)})
	case "enter":
		if m.focus == 0 {
			return m.openTable()
		}
		if len(m.snap.Result.Rows) > 0 && m.tab == 0 {
			m.modal = "inspect"
			m.modalTop = 0
		}
	case "a":
		if m.snap.Pending != nil {
			return m.showPending(m.snap.Pending)
		}
	case "j", "down":
		m.move(1)
	case "k", "up":
		m.move(-1)
	case "h", "left":
		if m.focus == 1 {
			m.col--
		}
	case "l", "right":
		if m.focus == 1 {
			m.col++
		}
	case "ctrl+d":
		m.move(max(1, m.bodyHeight()/2))
	case "ctrl+u":
		m.move(-max(1, m.bodyHeight()/2))
	case "pgdown":
		m.move(m.bodyHeight())
	case "pgup":
		m.move(-m.bodyHeight())
	case "home":
		m.move(-1 << 28)
	case "G", "end":
		m.move(1 << 28)
	case "g":
		if gg {
			m.move(-1 << 28)
		}
	}
	m.clamp()
	return nil
}

func (m *model) move(n int) {
	if m.focus == 0 {
		m.table += n
	} else if m.tab == 1 {
		m.structureTop += n
	} else {
		m.row += n
	}
}

func (m *model) tables() []database.Table {
	var out []database.Table
	for _, t := range m.snap.Tables {
		if fuzzy(m.search, t.Name) {
			out = append(out, t)
		}
	}
	return out
}

func fuzzy(query, value string) bool {
	q := []rune(strings.ToLower(query))
	i := 0
	for _, r := range strings.ToLower(value) {
		if i < len(q) && r == q[i] {
			i++
		}
	}
	return i == len(q)
}

func (m *model) openTable() tea.Cmd {
	ts := m.tables()
	if m.table >= len(ts) {
		return nil
	}
	m.focus, m.tab = 1, 0
	return m.dispatch(app.Action{Type: "open-table", Table: ts[m.table].Name})
}

func (m *model) sort() tea.Cmd {
	if m.snap.Query || len(m.snap.Result.Columns) == 0 {
		m.notice = "Sorting requires a table, not raw query results"
		return nil
	}
	column := m.snap.Result.Columns[m.col]
	return m.dispatch(app.Action{Type: "sort", Sort: column, Desc: m.snap.Browse.Sort == column && !m.snap.Browse.Desc})
}

func (m *model) sidebarWidth() int {
	if m.width < 64 {
		if m.focus == 0 {
			return m.width
		}
		return 0
	}
	return min(28, max(20, m.width/5))
}
func (m *model) bodyHeight() int { return max(1, m.height-6) }
func (m *model) gridWidth() int  { return max(1, m.width-m.sidebarWidth()) }

func (m *model) clamp() {
	m.table = bound(m.table, len(m.tables()))
	m.row = bound(m.row, len(m.snap.Result.Rows))
	m.col = bound(m.col, len(m.snap.Result.Columns))
	m.top = max(0, min(m.top, max(0, len(m.snap.Result.Rows)-m.bodyHeight())))
	if m.row < m.top {
		m.top = m.row
	}
	if m.row >= m.top+m.bodyHeight() {
		m.top = m.row - m.bodyHeight() + 1
	}
	m.tableTop = max(0, min(m.tableTop, max(0, len(m.tables())-m.bodyHeight())))
	if m.table < m.tableTop {
		m.tableTop = m.table
	}
	if m.table >= m.tableTop+m.bodyHeight() {
		m.tableTop = m.table - m.bodyHeight() + 1
	}
	m.left = min(m.left, m.col)
	width := m.columnsWidth(m.left, m.col)
	for m.left < m.col && width > m.gridWidth() {
		width -= m.columnWidth(m.left)
		m.left++
	}
	for m.left > 0 && width+m.columnWidth(m.left-1) <= m.gridWidth() {
		m.left--
		width += m.columnWidth(m.left)
	}
	if m.tab == 1 {
		m.structureTop = max(0, min(m.structureTop, max(0, len(m.structureLines())-m.bodyHeight())))
	}
}

func bound(n, length int) int { return max(0, min(n, length-1)) }

func (m *model) resizeInputs() {
	w := max(1, m.width-6)
	t := m.theme()
	inputStyles := textinput.DefaultStyles(!m.light)
	inputStyles.Focused.Text, inputStyles.Focused.Prompt, inputStyles.Focused.Placeholder = t.text, t.accent, t.muted
	m.input.SetStyles(inputStyles)
	sqlStyles := textarea.DefaultStyles(!m.light)
	sqlStyles.Focused.Text, sqlStyles.Focused.Prompt, sqlStyles.Focused.Placeholder = t.text, t.accent, t.muted
	sqlStyles.Focused.LineNumber, sqlStyles.Focused.CursorLineNumber = t.muted, t.accent
	m.sql.SetStyles(sqlStyles)
	m.input.SetWidth(max(1, w-3))
	m.sql.SetWidth(w)
	m.sql.SetHeight(max(1, m.height-9))
	_, _, panelWidth, _ := m.panelBounds()
	inner := max(1, panelWidth-2)
	for i := range m.fields {
		m.fields[i].SetStyles(inputStyles)
		if inner < 46 {
			m.fields[i].SetWidth(max(1, inner-4))
		} else {
			m.fields[i].SetWidth(max(1, inner-min(20, max(12, inner/4))-3))
		}
	}
}

func (m *model) click(mouse tea.Mouse) tea.Cmd {
	if mouse.Button != tea.MouseLeft || mouse.X < 0 || mouse.X >= m.width || mouse.Y < 0 || mouse.Y >= m.height {
		return nil
	}
	x, y := mouse.X, mouse.Y
	if y == m.height-1 {
		if x < 10 {
			return m.dispatch(app.Action{Type: "prev"})
		}
		if x < 20 {
			return m.dispatch(app.Action{Type: "next"})
		}
		return nil
	}
	sw := m.sidebarWidth()
	if sw > 0 && sw < m.width && x == sw-1 {
		return nil
	}
	if x < sw {
		m.focus = 0
		if y >= 4 && y < 4+m.bodyHeight() {
			i := m.tableTop + y - 4
			if i < len(m.tables()) {
				m.table = i
				return m.openTable()
			}
		}
		return nil
	}
	m.focus = 1
	if y == 1 {
		pos := 0
		for tab, label := range tabLabels {
			pos += len(label)
			if x-sw < pos {
				if tab == 2 {
					return m.openSQL(false)
				}
				m.tab = tab
				m.clamp()
				return nil
			}
		}
		return nil
	}
	if m.tab != 0 {
		return nil
	}
	if y == 3 {
		left, right := m.hiddenColumns()
		switch {
		case left > 0 && x == sw:
			m.col = left - 1
			m.clamp()
			return nil
		case right > 0 && x == m.width-1:
			visible := m.visibleColumns()
			m.col = visible[len(visible)-1].index + 1
			m.clamp()
			return nil
		}
	}
	pos := sw
	for _, column := range m.visibleColumns() {
		c, w := column.index, column.width
		if x >= pos && x < pos+w {
			m.col = c
			if y == 3 {
				return m.sort()
			}
			if y >= 4 && y < 4+m.bodyHeight() {
				m.row = m.top + y - 4
			}
			break
		}
		pos += w
	}
	m.clamp()
	return nil
}

func (m *model) selectedValue(r, c int) string {
	if r >= len(m.snap.Result.Rows) || c >= len(m.snap.Result.Rows[r]) {
		return ""
	}
	v := m.snap.Result.Rows[r][c]
	if v.Null {
		return "NULL"
	}
	if v.Binary {
		return fmt.Sprintf("[binary] %s", v.Text)
	}
	if v.Truncated {
		return "[truncated] " + v.Text
	}
	return v.Text
}

func (m *model) followForeignKey() tea.Cmd {
	if m.snap.Query || m.snap.Browse.Table == "" || m.col >= len(m.snap.Result.Columns) || m.row >= len(m.snap.Result.Rows) {
		m.notice = "Foreign-key navigation requires a selected table row"
		return nil
	}
	var selected *database.ForeignKey
	for i := range m.snap.Schema.ForeignKeys {
		fk := &m.snap.Schema.ForeignKeys[i]
		if fk.Column != m.snap.Result.Columns[m.col] {
			continue
		}
		if selected != nil && selected.Name != fk.Name {
			m.notice = "Multiple foreign keys on this column; inspect Structure to choose a target"
			return nil
		}
		selected = fk
	}
	if selected == nil || selected.Name == "" {
		m.notice = "No foreign-key constraint available for this column"
		return nil
	}
	var filters []database.Filter
	for _, fk := range m.snap.Schema.ForeignKeys {
		if fk.Name != selected.Name {
			continue
		}
		if fk.Database != "" && fk.Database != m.snap.Database {
			m.notice = "Cross-schema foreign-key navigation is not supported: " + safe(fk.Database)
			return nil
		}
		if fk.Table != selected.Table || fk.Target == "" {
			m.notice = "Foreign-key target metadata is unavailable or inconsistent"
			return nil
		}
		column := -1
		for c, name := range m.snap.Result.Columns {
			if name == fk.Column {
				column = c
				break
			}
		}
		if column < 0 || column >= len(m.snap.Result.Rows[m.row]) {
			m.notice = "Foreign-key value unavailable: " + safe(fk.Column)
			return nil
		}
		value := m.snap.Result.Rows[m.row][column]
		reason := ""
		switch {
		case value.Null:
			reason = "NULL"
		case value.Binary:
			reason = "binary"
		case value.Truncated:
			reason = "truncated"
		}
		if reason != "" {
			m.notice = "Cannot follow " + reason + " foreign-key value: " + safe(fk.Column)
			return nil
		}
		filters = append(filters, database.Filter{Column: fk.Target, Op: "eq", Value: value.Text})
	}
	for _, table := range m.snap.Tables {
		if table.Name == selected.Table {
			m.focus, m.tab = 1, 0
			return m.dispatch(app.Action{Type: "open-table", Table: selected.Table, Filters: filters})
		}
	}
	m.notice = "Referenced table unavailable in the current database: " + safe(selected.Table)
	return nil
}
