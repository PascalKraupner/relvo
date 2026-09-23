package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"github.com/PascalKraupner/relvo/internal/app"
	"github.com/PascalKraupner/relvo/internal/connections"
	"github.com/PascalKraupner/relvo/internal/database"
)

var operators = []string{"eq", "ne", "contains", "gt", "gte", "lt", "lte", "is-null", "not-null"}
var profileLabels = []string{"Name", "Host", "Port", "User", "Password", "Database", "TLS (true / off)"}
var envLabels = []string{"Name", "Environment file path", "Host variable", "Port variable", "User variable", "Password variable", "Database variable", "TLS variable"}
var commands = []struct{ name, key string }{
	{"Connections", "c"}, {"New connection (memory only)", "n"}, {"Environment file and mapping", "e"},
	{"SQL query", "Q"}, {"Stage SQL write for approval", "W"}, {"Add filter", "/"}, {"Primary key lookup", "i"},
	{"Clear filters", "x"}, {"Sort selected column", "s"}, {"Refresh", "r"}, {"Previous database page", "["},
	{"Next database page", "]"}, {"Increase page size", "+"}, {"Decrease page size", "-"},
	{"Data view", "1"}, {"Structure view", "2"}, {"Toggle row / cell selection", "v"},
	{"Review pending write", "a"}, {"Help", "?"},
	{"Find table", "find-table"}, {"Inspect selected row", "inspect-row"},
	{"Follow foreign key (selected column)", "f"},
	{"Cancel current operation", "esc"}, {"Quit", "q"},
}

func (m *model) closeModal() {
	if m.modal == "sql" {
		m.queryDraft = m.sql.Value()
	}
	if m.modal == "write" {
		m.writeDraft = m.sql.Value()
	}
	if m.modal == "profile" || m.modal == "env" {
		m.formID++
		m.setupBusy = false
	}
	m.modal = ""
	m.input.Blur()
	m.sql.Blur()
	// Password-bearing forms are dropped on dismissal; SQL drafts stay in memory.
	m.fields = nil
	m.input.Reset()
	m.sql.Reset()
	m.modalTop = 0
}

func (m *model) openInput(kind, placeholder string) tea.Cmd {
	m.modal, m.modalTop = kind, 0
	m.input.Reset()
	m.input.Placeholder = placeholder
	return m.input.Focus()
}

func (m *model) openSQL(write bool) tea.Cmd {
	m.closeModal()
	m.modal = "sql"
	if write {
		m.modal = "write"
	}
	draft := m.queryDraft
	if write {
		draft = m.writeDraft
	}
	m.sql.SetValue(draft)
	m.sql.Placeholder = "SELECT ..."
	if write {
		m.sql.Placeholder = "UPDATE ... (staged, never executed without local approval)"
	}
	return m.sql.Focus()
}

func (m *model) showPending(p *app.PendingWrite) tea.Cmd {
	m.closeModal()
	copy := *p
	m.pending = &copy
	return m.openInput("approve", "Type approve, then Enter; Ctrl+r rejects")
}

func (m *model) openProfile(env bool) tea.Cmd {
	m.closeModal()
	m.formID++
	m.modal, m.field = "profile", 0
	labels := profileLabels
	if env {
		m.modal = "env"
		labels = envLabels
	}
	m.fields = make([]textinput.Model, len(labels))
	for i := range labels {
		m.fields[i] = textinput.New()
		m.fields[i].Prompt = "> "
		m.fields[i].SetVirtualCursor(true)
	}
	if env {
		for i, v := range []string{"", ".env", "DB_HOST", "DB_PORT", "DB_USERNAME", "DB_PASSWORD", "DB_DATABASE", "DB_TLS"} {
			m.fields[i].SetValue(v)
		}
	} else {
		m.fields[1].SetValue("127.0.0.1")
		m.fields[2].SetValue("3306")
		m.fields[6].SetValue("true")
		m.fields[4].EchoMode = textinput.EchoPassword
	}
	m.resizeInputs()
	return m.fields[0].Focus()
}

func (m *model) openFilter(primary bool) tea.Cmd {
	if m.snap.Query || len(m.snap.Schema.Columns) == 0 || m.snap.Browse.Table == "" {
		m.notice = "Open a table to add filters"
		return nil
	}
	m.step, m.choice = 0, min(m.col, len(m.snap.Schema.Columns)-1)
	m.filter = database.Filter{Op: "eq"}
	if primary {
		found := false
		for _, c := range m.snap.Schema.Columns {
			if c.Key == "PRI" {
				m.filter.Column = c.Name
				found = true
				break
			}
		}
		if !found {
			m.notice = "This table has no primary key"
			return nil
		}
		m.step = 2
	}
	return m.openInput("filter", "Value")
}

func (m *model) choices() []string {
	var items []string
	switch m.modal {
	case "connections":
		for _, n := range m.snap.Connections {
			if fuzzy(m.input.Value(), n) {
				items = append(items, n)
			}
		}
	case "palette":
		for _, c := range commands {
			if fuzzy(m.input.Value(), c.name) {
				items = append(items, c.name)
			}
		}
	case "filter":
		if m.step == 0 {
			for _, c := range m.snap.Schema.Columns {
				items = append(items, c.Name)
			}
		}
		if m.step == 1 {
			items = operators
		}
	}
	return items
}

func (m *model) updateModal(msg tea.Msg) tea.Cmd {
	if m.modal == "help" {
		if click, ok := msg.(tea.MouseClickMsg); ok && click.Button == tea.MouseLeft {
			x, y, w, h := m.helpBounds()
			if click.X < x || click.X >= x+w || click.Y < y || click.Y >= y+h || (click.Y == y+1 && click.X >= x+w-14) {
				m.closeModal()
			}
			return nil
		}
	}
	if wheel, ok := msg.(tea.MouseWheelMsg); ok {
		delta := 3
		if wheel.Button == tea.MouseWheelUp {
			delta = -3
		}
		m.scrollModal(delta)
		return nil
	}
	if m.modal == "help" && m.helpFiltering {
		if _, ok := msg.(tea.PasteMsg); ok {
			var cmd tea.Cmd
			m.input, cmd = m.input.Update(msg)
			m.helpSearch, m.modalTop = m.input.Value(), 0
			return cmd
		}
	}
	if key, ok := msg.(tea.KeyPressMsg); ok {
		k := key.String()
		if m.modal == "help" {
			if m.helpFiltering {
				switch k {
				case "esc":
					m.helpFiltering = false
					m.helpSearch = ""
					m.modalTop = 0
					m.input.Blur()
					m.input.Reset()
					return nil
				case "enter":
					m.helpFiltering = false
					m.input.Blur()
					return nil
				default:
					before := m.input.Value()
					var cmd tea.Cmd
					m.input, cmd = m.input.Update(msg)
					if before != m.input.Value() {
						m.helpSearch = m.input.Value()
						m.modalTop = 0
					}
					return cmd
				}
			}
			if k == "/" {
				m.helpFiltering = true
				m.input.SetValue(m.helpSearch)
				return m.input.Focus()
			}
		}
		if k == "esc" {
			if m.setupBusy {
				m.app.Cancel()
			}
			m.closeModal()
			return nil
		}
		if m.modal == "inspect" || m.modal == "help" {
			page := max(1, m.height-6)
			if m.modal == "help" {
				page = max(1, m.helpBodyHeight())
			}
			switch k {
			case "j", "down":
				m.scrollModal(1)
			case "k", "up":
				m.scrollModal(-1)
			case "pgdown", "ctrl+d":
				m.scrollModal(page)
			case "pgup", "ctrl+u":
				m.scrollModal(-page)
			case "home", "g":
				m.modalTop = 0
			case "end", "G":
				m.scrollModal(1 << 28)
			}
			return nil
		}
		if m.modal == "approve" {
			if k == "pgup" {
				m.scrollModal(-max(1, m.height-9))
				return nil
			}
			if k == "pgdown" {
				m.scrollModal(max(1, m.height-9))
				return nil
			}
			if k == "ctrl+r" && m.pending != nil {
				id, b := m.pending.ID, m.app
				m.closeModal()
				return func() tea.Msg { err := b.Reject(id); return resultMsg{b.Snapshot(), err} }
			}
			if k == "enter" {
				if m.pending == nil || m.snap.Pending == nil || *m.pending != *m.snap.Pending {
					m.notice = "Pending write changed; review it again"
					return nil
				}
				if !m.pending.Expires.IsZero() && time.Now().After(m.pending.Expires) {
					m.notice = "Pending write expired"
					return nil
				}
				if m.input.Value() != "approve" {
					m.notice = "Type exactly approve to execute this write"
					return nil
				}
				// Approval is reachable only from this local key event, never an app action.
				id, b := m.pending.ID, m.app
				m.closeModal()
				return func() tea.Msg { s, err := b.Approve(context.Background(), id); return resultMsg{s, err} }
			}
		}
		if m.modal == "sql" || m.modal == "write" {
			if k == "ctrl+enter" || k == "ctrl+s" {
				sql, kind := m.sql.Value(), "query"
				if m.modal == "write" {
					kind = "write"
				}
				if strings.TrimSpace(sql) == "" {
					return nil
				}
				m.closeModal()
				m.focus, m.tab = 1, 0
				return m.dispatch(app.Action{Type: kind, SQL: sql})
			}
			var cmd tea.Cmd
			m.sql, cmd = m.sql.Update(msg)
			return cmd
		}
		if m.modal == "profile" || m.modal == "env" {
			if m.setupBusy {
				return nil
			}
			if k == "ctrl+s" || k == "ctrl+t" {
				return m.submitProfile(k == "ctrl+t")
			}
			if k == "tab" || k == "enter" || k == "shift+tab" {
				m.fields[m.field].Blur()
				delta := 1
				if k == "shift+tab" {
					delta = -1
				}
				m.field = (m.field + delta + len(m.fields)) % len(m.fields)
				return m.fields[m.field].Focus()
			}
		}
		if m.modal == "find" && k == "enter" {
			m.search = m.input.Value()
			m.table, m.tableTop = 0, 0
			m.closeModal()
			m.clamp()
			return nil
		}
		list := m.choices()
		if len(list) > 0 {
			switch k {
			case "up", "ctrl+k":
				m.choice = bound(m.choice-1, len(list))
				return nil
			case "down", "ctrl+j":
				m.choice = bound(m.choice+1, len(list))
				return nil
			case "enter", "ctrl+t":
				m.choice = bound(m.choice, len(list))
				selected := list[m.choice]
				switch m.modal {
				case "connections":
					kind := "connect"
					if k == "ctrl+t" {
						kind = "test"
					}
					m.closeModal()
					return m.dispatch(app.Action{Type: kind, Connection: selected})
				case "palette":
					if k != "enter" {
						return nil
					}
					m.closeModal()
					for _, c := range commands {
						if c.name == selected {
							if c.key == "/" || c.key == "x" {
								m.focus = 1
							}
							return m.key(c.key)
						}
					}
				case "filter":
					if k != "enter" {
						return nil
					}
					if m.step == 0 {
						m.filter.Column = selected
						m.step = 1
						m.choice = 0
						return nil
					}
					m.filter.Op = selected
					m.step = 2
					if selected == "is-null" || selected == "not-null" {
						return m.submitFilter()
					}
					return m.input.Focus()
				}
			}
		}
		if m.modal == "filter" {
			if m.step < 2 {
				return nil
			}
			if k == "enter" {
				m.filter.Value = m.input.Value()
				return m.submitFilter()
			}
		}
	}
	var cmd tea.Cmd
	switch m.modal {
	case "sql", "write":
		m.sql, cmd = m.sql.Update(msg)
	case "profile", "env":
		if !m.setupBusy {
			m.fields[m.field], cmd = m.fields[m.field].Update(msg)
		}
	case "filter":
		if m.step == 2 {
			m.input, cmd = m.input.Update(msg)
		}
	case "inspect", "help":
	default:
		before := m.input.Value()
		m.input, cmd = m.input.Update(msg)
		if before != m.input.Value() {
			m.choice = 0
		}
	}
	return cmd
}

func (m *model) submitFilter() tea.Cmd {
	filters := append([]database.Filter(nil), m.snap.Browse.Filters...)
	filters = append(filters, m.filter)
	m.closeModal()
	return m.dispatch(app.Action{Type: "filter", Filters: filters})
}

func (m *model) submitProfile(test bool) tea.Cmd {
	if m.setupBusy {
		return nil
	}
	values := make([]string, len(m.fields))
	for i := range m.fields {
		values[i] = m.fields[i].Value()
	}
	p := connections.Profile{Name: strings.TrimSpace(values[0])}
	if p.Name == "" {
		m.notice = "A connection name is required"
		return nil
	}
	if m.modal == "env" {
		p.EnvFile = values[1]
		if strings.TrimSpace(p.EnvFile) == "" {
			m.notice = "An environment file path is required"
			return nil
		}
		p.Mapping = map[string]string{}
		defaults := []string{"DB_HOST", "DB_PORT", "DB_USERNAME", "DB_PASSWORD", "DB_DATABASE", "DB_TLS"}
		for i, field := range []string{"host", "port", "user", "password", "database", "tls"} {
			if v := strings.TrimSpace(values[i+2]); v != "" && v != defaults[i] {
				p.Mapping[field] = v
			}
		}
	} else {
		p.Config = database.Config{Name: p.Name, Host: values[1], Port: values[2], User: values[3], Password: values[4], Database: values[5], TLS: values[6]}
	}
	b := m.app
	id := m.formID
	m.setupBusy = true
	m.notice = "Checking connection... Ctrl+c cancels"
	return func() tea.Msg {
		s, err := b.Setup(context.Background(), p, test)
		return setupMsg{resultMsg{s, err}, id, test}
	}
}

func (m *model) scrollModal(delta int) {
	if m.modal == "help" {
		_, _, w, _ := m.helpBounds()
		m.modalTop = max(0, min(m.modalTop+delta, max(0, len(m.helpDisplayRows(max(0, w-2)))-m.helpBodyHeight())))
		return
	}
	lines := m.documentLines()
	h := max(1, m.height-6)
	if m.modal == "approve" {
		h = max(1, m.height-9)
	}
	m.modalTop = max(0, min(m.modalTop+delta, max(0, len(lines)-h)))
}

func (m *model) documentLines() []string {
	var lines []string
	switch m.modal {
	case "help":
		for _, row := range m.helpRows() {
			lines = append(lines, row.keys+"  "+row.description)
		}
	case "inspect":
		for c, name := range m.snap.Result.Columns {
			lines = append(lines, name, m.selectedValue(m.row, c), "")
		}
	case "approve":
		if m.pending != nil {
			lines = []string{"Connection: " + m.pending.Connection, "Database: " + m.pending.Database, "Request: " + m.pending.ID, "Expires: " + m.pending.Expires.Format(time.RFC3339), "", m.pending.SQL}
		}
	}
	var wrapped []string
	for _, line := range lines {
		wrapped = append(wrapped, wrapSafe(line, max(1, m.width-6))...)
	}
	return wrapped
}

func (m *model) filterTitle() string {
	return fmt.Sprintf("Add AND filter / %d of 3: %s", m.step+1, []string{"Column", "Operator", "Value"}[m.step])
}
