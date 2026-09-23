package app

import (
	"context"
	"time"

	"github.com/PascalKraupner/relvo/internal/connections"
	"github.com/PascalKraupner/relvo/internal/database"
)

type Action struct {
	Type       string            `json:"type"`
	Connection string            `json:"connection,omitempty"`
	Table      string            `json:"table,omitempty"`
	Filters    []database.Filter `json:"filters,omitempty"`
	Sort       string            `json:"sort,omitempty"`
	Desc       bool              `json:"desc,omitempty"`
	PageSize   int               `json:"page_size,omitempty"`
	SQL        string            `json:"sql,omitempty"`
}

type PendingWrite struct {
	ID         string    `json:"id"`
	Connection string    `json:"connection"`
	Database   string    `json:"database"`
	SQL        string    `json:"sql"`
	Expires    time.Time `json:"expires"`
}

type Snapshot struct {
	Revision      uint64                 `json:"revision"`
	Connection    string                 `json:"connection"`
	Database      string                 `json:"database"`
	Connections   []string               `json:"connections"`
	Tables        []database.Table       `json:"tables"`
	Schema        database.Schema        `json:"schema"`
	Browse        database.BrowseRequest `json:"browse"`
	Result        database.Result        `json:"result"`
	Busy          bool                   `json:"busy"`
	Error         string                 `json:"error,omitempty"`
	Status        string                 `json:"status"`
	Query         bool                   `json:"query"`
	Pending       *PendingWrite          `json:"pending,omitempty"`
	WritesEnabled bool                   `json:"writes_enabled"`
}

type OpenFunc func(context.Context, database.Config) (database.Store, error)

type Options struct {
	Profiles    []connections.Profile
	Open        OpenFunc
	AllowWrites bool
}
