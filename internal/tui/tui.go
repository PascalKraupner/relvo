package tui

import (
	tea "charm.land/bubbletea/v2"
	"github.com/PascalKraupner/relvo/internal/app"
)

// New creates a model. It does not connect or perform database I/O.
func New(a *app.App) tea.Model { return newModel(a) }
