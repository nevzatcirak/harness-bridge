package dashboard

import "context"

// Session is one row the dashboard may show. Slice 1 defines the type only.
type Session struct {
	Harness string
	ID      string
	Title   string
	Root    string
	State   string
}

// Source is the dashboard's only path to data, and it is read-only: every
// method is a reader and none can mutate (ADR-023 T11). A test pins the method
// set. Slices 2 to 5 add readers here; nothing in this package depends on the
// execution manager or the MCP server.
type Source interface {
	Sessions(ctx context.Context) ([]Session, error)
}

// NullSource is the slice 1 source: no data source is connected.
type NullSource struct{}

// Sessions returns no rows.
func (NullSource) Sessions(context.Context) ([]Session, error) { return nil, nil }
