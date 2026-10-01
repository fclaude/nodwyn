package binding

import (
	"bytes"
	"errors"
	"os"
	"slices"
	"sync"
)

// Authority is the read-only binding surface used by policy checkers. Table is
// an immutable snapshot; Live supplies the session's latest validated snapshot.
type Authority interface {
	Lookup(class, host, method, position string, scopes ...string) (bool, string)
	HasDestination(host string) bool
}

// Live owns one session's policy snapshot. The execution goroutine refreshes it
// before egress; frontend inspectors can read copies concurrently.
type Live struct {
	mu     sync.RWMutex
	path   string
	raw    []byte
	exists bool
	table  Table
}

// Open loads the bindings file at path. With no path, initial is the table
// for the whole session (tests, embedders); with one, the file alone is
// authoritative and initial is ignored.
func Open(path string, initial Table) (*Live, error) {
	p := &Live{path: path, table: cloneTable(initial)}
	// A configured file is authoritative, including its absence. Never restore
	// a daemon-startup snapshot after the user deletes the policy.
	if path != "" {
		p.table = Table{}
		p.exists = true
	}
	if err := p.Refresh(); err != nil {
		return nil, err
	}
	return p, nil
}

// Refresh rereads the file if it changed. A file that no longer parses
// leaves the previous table in place and returns the error.
func (p *Live) Refresh() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.path == "" {
		return nil
	}
	raw, err := os.ReadFile(p.path)
	exists := err == nil
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if exists == p.exists && bytes.Equal(raw, p.raw) {
		return nil
	}
	table, err := parse(raw, p.path)
	if err != nil {
		return err
	}
	p.raw, p.exists, p.table = raw, exists, table
	return nil
}

// Snapshot returns a copy of the current table.
func (p *Live) Snapshot() Table {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return cloneTable(p.table)
}

// Lookup is Table.Lookup on the current table.
func (p *Live) Lookup(class, host, method, position string, scopes ...string) (bool, string) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.table.Lookup(class, host, method, position, scopes...)
}

// HasDestination is Table.HasDestination on the current table.
func (p *Live) HasDestination(host string) bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.table.HasDestination(host)
}

func cloneTable(t Table) Table {
	out := Table{Bind: make([]Binding, len(t.Bind))}
	for i, row := range t.Bind {
		out.Bind[i] = row
		out.Bind[i].Methods = slices.Clone(row.Methods)
		out.Bind[i].Positions = slices.Clone(row.Positions)
		out.Bind[i].Scopes = slices.Clone(row.Scopes)
	}
	return out
}
