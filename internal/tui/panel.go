package tui

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// composeOverlay draws a dialog over the current screen, keeping the table in
// view. Canvas trims trailing spaces, so restore the full terminal dimensions.
func (m *model) composeOverlay(background, panel string, x, y int) string {
	canvas := lipgloss.NewCanvas(m.width, m.height)
	canvas.Compose(lipgloss.NewCompositor(lipgloss.NewLayer(background), lipgloss.NewLayer(panel).X(x).Y(y)))
	lines := strings.Split(canvas.Render(), "\n")
	for i := range lines {
		lines[i] = fit(lines[i], m.width)
	}
	for len(lines) < m.height {
		lines = append(lines, fit("", m.width))
	}
	return strings.Join(lines[:m.height], "\n")
}

type panelColors struct {
	border, text, muted, accent, selected, danger lipgloss.Style
}

func (m *model) panelColors() panelColors {
	background, text, muted, accent, border, selection, danger := "#17202E", "#DCE8F2", "#91A7B9", "#80CCC3", "#75B8FF", "#345367", "#EDAB9D"
	if m.light {
		background, text, muted, accent, border, selection, danger = "#F7FAFC", "#243D51", "#5B7183", "#126D70", "#226888", "#C8E2EA", "#A43D36"
	}
	bg := lipgloss.Color(background)
	return panelColors{
		border:   lipgloss.NewStyle().Foreground(lipgloss.Color(border)).Background(bg),
		text:     lipgloss.NewStyle().Foreground(lipgloss.Color(text)).Background(bg),
		muted:    lipgloss.NewStyle().Foreground(lipgloss.Color(muted)).Background(bg),
		accent:   lipgloss.NewStyle().Foreground(lipgloss.Color(accent)).Background(bg).Bold(true),
		selected: lipgloss.NewStyle().Foreground(lipgloss.Color(text)).Background(lipgloss.Color(selection)),
		danger:   lipgloss.NewStyle().Foreground(lipgloss.Color(danger)).Background(bg),
	}
}

func (m *model) panelBounds() (x, y, width, height int) {
	limit := 88
	if m.modal == "inspect" {
		limit = 96
	}
	width = min(limit, max(1, m.width-4))
	if m.width < 48 {
		width = m.width
	}
	preferredHeight := 25
	if m.modal == "inspect" {
		count := len(m.snap.Result.Columns)
		valueWidth := max(1, (width-2)*2/3)
		if width-2 < 40 {
			count *= 2
			valueWidth = max(1, width-6)
		}
		for c := range m.snap.Result.Columns {
			if count >= 24 {
				break
			}
			value := m.selectedValue(m.row, c)
			count += len(value)/valueWidth + strings.Count(value, "\n")
		}
		preferredHeight = min(30, max(10, count+6))
	} else {
		rows := len(m.fields)
		if m.modal == "env" {
			rows += 5 // three section labels, two blank separators
		} else {
			rows += 7 // four section labels, three blank separators
		}
		if width-2 < 46 {
			rows += len(m.fields) // field labels move above their values
		}
		preferredHeight = min(25, max(14, rows+6))
	}
	height = min(preferredHeight, max(1, m.height-4))
	if m.height < 16 {
		height = m.height
	}
	return (m.width - width) / 2, (m.height - height) / 2, width, height
}

func (m *model) panelBodyHeight() int {
	_, _, _, height := m.panelBounds()
	return max(0, height-6)
}

type panelRow struct {
	content string
	field   int // -1 for headings, spacing and status
}

func (m *model) panelOverlay(background string) string {
	x, y, width, height := m.panelBounds()
	if width < 3 || height < 6 {
		return background
	}
	inner := width - 2
	styles := m.panelColors()
	borderLine := func(left, middle, right string) string {
		return styles.border.Render(left + strings.Repeat(middle, inner) + right)
	}
	line := func(content string) string {
		return styles.border.Render("│") + styles.text.Render(fit(content, inner)) + styles.border.Render("│")
	}
	name, subtitle, footer := m.panelLabels()
	closeLabel := styles.selected.Render(" esc close ")
	if inner < 22 {
		closeLabel = ""
	}
	titleWidth := inner - ansi.StringWidth(closeLabel)
	var rows []panelRow
	start := m.modalTop
	if m.modal == "inspect" {
		for _, content := range m.detailLines(inner) {
			rows = append(rows, panelRow{content: content, field: -1})
		}
	} else {
		rows = m.formRows(inner)
		start = m.formViewportStart(rows)
	}
	body := m.panelBodyHeight()
	start = max(0, min(start, max(0, len(rows)-body)))
	var lines []string
	lines = append(lines, borderLine("┌", "─", "┐"))
	lines = append(lines, line(styles.accent.Render(fit(ansi.Truncate("  "+name, titleWidth, "…"), titleWidth))+closeLabel))
	subtitleStyle := styles.muted
	if m.modal != "inspect" && m.notice != "" {
		subtitleStyle = styles.accent
		if strings.Contains(m.notice, "failed") || strings.Contains(m.notice, "required") {
			subtitleStyle = styles.danger
		}
	}
	lines = append(lines, line(subtitleStyle.Render(fit(ansi.Truncate("  "+subtitle, inner, "…"), inner))))
	lines = append(lines, borderLine("├", "─", "┤"))
	for i := 0; i < body; i++ {
		index := start + i
		content := ""
		if index < len(rows) {
			content = rows[index].content
		}
		if len(rows) > body && inner > 3 {
			track := "│"
			thumb := max(1, body*body/len(rows))
			position := start * max(0, body-thumb) / max(1, len(rows)-body)
			if i >= position && i < position+thumb {
				track = "┃"
			}
			content = fit(content, inner-1) + styles.border.Render(track)
		}
		lines = append(lines, line(content))
	}
	lines = append(lines, line(styles.muted.Render(fit("  "+footer, inner))))
	lines = append(lines, borderLine("└", "─", "┘"))
	return m.composeOverlay(background, strings.Join(lines, "\n"), x, y)
}

func (m *model) formViewportStart(rows []panelRow) int {
	// Keep the currently edited field visible while Tab or mouse changes focus.
	for i, row := range rows {
		if row.field == m.field {
			return max(0, min(i-m.panelBodyHeight()/2, max(0, len(rows)-m.panelBodyHeight())))
		}
	}
	return 0
}

func (m *model) panelLabels() (title, subtitle, footer string) {
	switch m.modal {
	case "inspect":
		title = fmt.Sprintf("Row %d  ·  %s", m.snap.Browse.Offset+m.row+1, safe(m.snap.Browse.Table))
		if m.snap.Query {
			title = fmt.Sprintf("Result row %d", m.row+1)
		}
		subtitle = fmt.Sprintf("%s / %s   ·   %d fields", safe(m.snap.Connection), safe(m.snap.Database), len(m.snap.Result.Columns))
		footer = "j/k or ↑↓ scroll   ·   pgup/pgdn page   ·   esc close"
	case "env":
		title = "Add connection  ·  environment file"
		subtitle = "Use variable names from the selected .env; nothing is executed"
		footer = "tab next  ·  ctrl+t test  ·  ctrl+s connect  ·  esc close"
	default:
		title = "Add connection  ·  MySQL / MariaDB"
		subtitle = "Set up a connection for this session"
		footer = "tab next  ·  ctrl+t test  ·  ctrl+s connect  ·  esc close"
	}
	if m.modal != "inspect" && m.setupBusy {
		footer = "Checking connection…   ·   ctrl+c cancel"
	}
	if m.modal != "inspect" && m.notice != "" {
		subtitle = m.notice
	}
	_, _, panelWidth, _ := m.panelBounds()
	if panelWidth < 70 && !m.setupBusy {
		if m.modal == "inspect" {
			footer = "j/k scroll  ·  pgup/pgdn page  ·  esc close"
		} else {
			footer = "tab next  ·  ctrl+t test  ·  ctrl+s connect"
		}
	}
	if panelWidth < 40 && !m.setupBusy {
		if m.modal == "inspect" {
			footer = "j/k scroll  ·  esc close"
		} else {
			footer = "tab next  ·  ctrl+s connect"
		}
	}
	return
}

func (m *model) formRows(width int) []panelRow {
	styles := m.panelColors()
	labels := profileLabels
	groups := map[int]string{0: "PROFILE", 1: "ENDPOINT", 3: "ACCESS", 5: "DATABASE"}
	if m.modal == "env" {
		labels = envLabels
		groups = map[int]string{0: "PROFILE", 1: "SOURCE", 2: "VARIABLE MAPPING"}
	}
	rows := make([]panelRow, 0, len(labels)*2+8)
	compact := width < 46
	labelWidth := min(20, max(12, width/4))
	for i, label := range labels {
		if group, ok := groups[i]; ok {
			if len(rows) > 0 {
				rows = append(rows, panelRow{field: -1})
			}
			rows = append(rows, panelRow{content: styles.accent.Render(fit("  "+group, width)), field: -1})
		}
		value := safe(m.fields[i].Value())
		if m.modal == "profile" && i == 4 && value != "" {
			value = "••••••••"
		}
		if compact {
			rows = append(rows, panelRow{content: styles.muted.Render(fit("  "+label, width)), field: -1})
			if i == m.field {
				rows = append(rows, panelRow{content: styles.selected.Render(fit("  "+m.fields[i].View(), width)), field: i})
			} else {
				rows = append(rows, panelRow{content: styles.text.Render(fit("    "+value, width)), field: i})
			}
			continue
		}
		prefix := styles.muted.Render(fit("  "+label, labelWidth))
		if i == m.field {
			prefix = styles.accent.Render(fit("  "+label, labelWidth))
			rows = append(rows, panelRow{content: styles.selected.Render(fit(prefix+m.fields[i].View(), width)), field: i})
		} else {
			rows = append(rows, panelRow{content: prefix + styles.text.Render(fit(value, width-labelWidth)), field: i})
		}
	}
	return rows
}

func (m *model) detailLines(width int) []string {
	styles := m.panelColors()
	if width <= 0 {
		return nil
	}
	labelWidth := min(26, max(14, width/3))
	if width < 40 {
		labelWidth = width
	}
	var lines []string
	for col, name := range m.snap.Result.Columns {
		value := m.selectedValue(m.row, col)
		key := "  " + safe(name)
		if col == m.col {
			key = "› " + safe(name)
		}
		nameStyle := styles.muted
		if col == m.col {
			nameStyle = styles.accent
		}
		valueStyle := styles.text
		if m.row < len(m.snap.Result.Rows) && col < len(m.snap.Result.Rows[m.row]) && m.snap.Result.Rows[m.row][col].Null {
			valueStyle = styles.muted
		}
		if width < 40 {
			lines = append(lines, nameStyle.Render(fit(key, width)))
			for _, part := range wrapSafe(value, max(1, width-4)) {
				lines = append(lines, valueStyle.Render(fit("    "+part, width)))
			}
		} else {
			valueWidth := max(1, width-labelWidth)
			wrapped := wrapSafe(value, valueWidth)
			if len(wrapped) == 0 {
				wrapped = []string{""}
			}
			lines = append(lines, nameStyle.Render(fit(key, labelWidth))+valueStyle.Render(fit(wrapped[0], valueWidth)))
			for _, part := range wrapped[1:] {
				lines = append(lines, styles.text.Render(fit("", labelWidth))+valueStyle.Render(fit(part, valueWidth)))
			}
		}
	}
	return lines
}
