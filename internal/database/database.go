package database

import "context"

type Config struct {
	Name     string `json:"name"`
	Host     string `json:"host"`
	Port     string `json:"port"`
	User     string `json:"user"`
	Password string `json:"-"`
	Database string `json:"database"`
	Socket   string `json:"socket,omitempty"`
	TLS      string `json:"tls,omitempty"`
}

type Table struct {
	Name string `json:"name"`
	Kind string `json:"kind"`
}

type Column struct {
	Name     string  `json:"name"`
	Type     string  `json:"type"`
	Nullable bool    `json:"nullable"`
	Default  *string `json:"default"`
	Key      string  `json:"key"`
	Extra    string  `json:"extra"`
}

type Index struct {
	Name    string   `json:"name"`
	Columns []string `json:"columns"`
	Unique  bool     `json:"unique"`
}

type ForeignKey struct {
	Database string `json:"database,omitempty"`
	Name     string `json:"name"`
	Column   string `json:"column"`
	Table    string `json:"table"`
	Target   string `json:"target"`
}

type Schema struct {
	Columns     []Column     `json:"columns"`
	Indexes     []Index      `json:"indexes"`
	ForeignKeys []ForeignKey `json:"foreign_keys"`
}

// Values stay textual so decimals and 64-bit identifiers survive JSON transport.
type Value struct {
	Truncated bool   `json:"truncated,omitempty"`
	Text      string `json:"text"`
	Null      bool   `json:"null,omitempty"`
	Binary    bool   `json:"binary,omitempty"`
}

type Filter struct {
	Column string `json:"column"`
	Op     string `json:"op"`
	Value  string `json:"value"`
}

type BrowseRequest struct {
	Table   string   `json:"table"`
	Filters []Filter `json:"filters,omitempty"`
	Sort    string   `json:"sort,omitempty"`
	Desc    bool     `json:"desc,omitempty"`
	Limit   int      `json:"limit"`
	Offset  int      `json:"offset"`
}

type Result struct {
	Columns  []string  `json:"columns"`
	Rows     [][]Value `json:"rows"`
	HasMore  bool      `json:"has_more"`
	Affected int64     `json:"affected,omitempty"`
	Notice   string    `json:"notice,omitempty"`
}

type Store interface {
	Tables(context.Context) ([]Table, error)
	Schema(context.Context, string) (Schema, error)
	Browse(context.Context, BrowseRequest) (Result, error)
	Query(context.Context, string) (Result, error)
	Execute(context.Context, string) (Result, error)
	Close() error
}
