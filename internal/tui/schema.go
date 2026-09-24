package tui

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

func (m *model) structureHeader() string {
	w := m.gridWidth()
	t := m.theme()
	label := fmt.Sprintf(" SCHEMA  ·  %d columns  ·  %d indexes  ·  %d relationships", len(m.snap.Schema.Columns), len(m.snap.Schema.Indexes), len(m.snap.Schema.ForeignKeys))
	return t.surfaceAccent.Render(fit(label, w))
}

func (m *model) structureLines() []string {
	w := m.gridWidth()
	t := m.theme()
	var out []string
	line := func(content string, style lipgloss.Style) {
		out = append(out, style.Render(fit(content, w)))
	}
	section := func(name string, count int) {
		if len(out) > 0 {
			line("", t.surface)
		}
		line(fmt.Sprintf("  %s  ·  %d", name, count), t.surfaceAccent)
	}
	section("COLUMNS", len(m.snap.Schema.Columns))
	wide := w >= 78
	nameWidth, typeWidth := min(26, max(16, w/4)), min(32, max(18, w/4))
	if wide {
		header := t.surfaceMuted.Render(fit("  NAME", nameWidth)) + t.surfaceMuted.Render(fit("TYPE", typeWidth)) + t.surfaceMuted.Render(fit("NULL", 7)) + t.surfaceMuted.Render(fit("KEY", 9)) + t.surfaceMuted.Render(fit("DEFAULT", max(0, w-nameWidth-typeWidth-16)))
		line(header, t.surface)
	}
	if len(m.snap.Schema.Columns) == 0 {
		line("  No columns available", t.surfaceMuted)
	}
	for _, c := range m.snap.Schema.Columns {
		name, dataType := safe(c.Name), safe(c.Type)
		defaultValue := "—"
		if c.Default != nil {
			defaultValue = safe(*c.Default)
		}
		key := "—"
		switch c.Key {
		case "PRI":
			key = "PRIMARY"
		case "UNI":
			key = "UNIQUE"
		case "MUL":
			key = "INDEX"
		case "":
		default:
			key = safe(c.Key)
		}
		nullability := "NO"
		if c.Nullable {
			nullability = "YES"
		}
		if wide {
			remaining := max(0, w-nameWidth-typeWidth-16)
			row := t.surfaceAccent.Render(fit(ansi.Truncate("  "+name, nameWidth, "…"), nameWidth)) +
				t.surface.Render(fit(ansi.Truncate(dataType, typeWidth, "…"), typeWidth)) +
				t.surfaceMuted.Render(fit(nullability, 7)) +
				t.surfaceAccent.Render(fit(key, 9)) +
				t.surfaceMuted.Render(fit(ansi.Truncate(defaultValue, remaining, "…"), remaining))
			line(row, t.surface)
		} else {
			nameWidth := min(max(14, w/2), max(1, w-1))
			row := t.surfaceAccent.Render(fit(ansi.Truncate("  "+name, nameWidth, "…"), nameWidth)) +
				t.surface.Render(fit(ansi.Truncate(dataType, max(0, w-nameWidth), "…"), max(0, w-nameWidth)))
			line(row, t.surface)
			attributes := []string{}
			if key != "—" {
				attributes = append(attributes, key)
			}
			if c.Nullable {
				attributes = append(attributes, "NULLABLE")
			} else {
				attributes = append(attributes, "NOT NULL")
			}
			if c.Default != nil {
				attributes = append(attributes, "default "+defaultValue)
			}
			if c.Extra != "" {
				attributes = append(attributes, safe(c.Extra))
			}
			info := "    " + strings.Join(attributes, "  ·  ")
			for _, part := range wrapSafe(info, max(1, w-2)) {
				line("  "+part, t.surfaceMuted)
			}
		}
	}
	section("INDEXES", len(m.snap.Schema.Indexes))
	if len(m.snap.Schema.Indexes) == 0 {
		line("  No indexes", t.surfaceMuted)
	}
	for _, index := range m.snap.Schema.Indexes {
		kind := "INDEX"
		if index.Unique {
			kind = "UNIQUE"
		}
		label := "  " + safe(index.Name) + "   " + kind + "   " + safe(strings.Join(index.Columns, ", "))
		for _, part := range wrapSafe(label, max(1, w)) {
			line(part, t.surface)
		}
	}
	section("RELATIONSHIPS", len(m.snap.Schema.ForeignKeys))
	if len(m.snap.Schema.ForeignKeys) == 0 {
		line("  No foreign keys", t.surfaceMuted)
	}
	for _, fk := range m.snap.Schema.ForeignKeys {
		target := fk.Table + "." + fk.Target
		if fk.Database != "" && fk.Database != m.snap.Database {
			target = fk.Database + "." + target
		}
		label := "  " + safe(fk.Column) + "  →  " + safe(target) + "   [" + safe(fk.Name) + "]"
		for _, part := range wrapSafe(label, max(1, w)) {
			line(part, t.surface)
		}
	}
	return out
}
