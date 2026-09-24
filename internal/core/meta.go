package core

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/cmtonkinson/agentctl/internal/fsx"
)

// Meta is agentctl's record for one asset: where it came from and what it
// needs. It lives in <store>/.agentctl/meta/<kind>/<name>.json so asset
// directories stay exactly as authored.
type Meta struct {
	Origin       string     `json:"origin,omitempty"`
	Source       *Source    `json:"source,omitempty"`
	License      string     `json:"license,omitempty"`
	Version      string     `json:"version,omitempty"`
	ImportedAt   *time.Time `json:"imported_at,omitempty"`
	UpdatedAt    *time.Time `json:"updated_at,omitempty"`
	ImportedHash string     `json:"imported_hash,omitempty"` // store content right after import or update
	SourceHash   string     `json:"source_hash,omitempty"`   // upstream content at import or update
	Requires     *Requires  `json:"requires,omitempty"`
	// Files maps each file to its hash as last imported, for three-way
	// merges on update.
	Files map[string]string `json:"files,omitempty"`
}

// Source records an asset's upstream.
type Source struct {
	Type     string `json:"type"` // path | zip | git | target
	Location string `json:"location,omitempty"`
	Subpath  string `json:"subpath,omitempty"`
	Ref      string `json:"ref,omitempty"`      // requested git ref or version
	Revision string `json:"revision,omitempty"` // resolved git commit
	Target   string `json:"target,omitempty"`
	Scope    string `json:"scope,omitempty"`
	Project  string `json:"project,omitempty"`
	Plugin   string `json:"plugin,omitempty"`
	Kind     string `json:"kind,omitempty"`
	Name     string `json:"name,omitempty"`
	Key      string `json:"key,omitempty"` // entry in a config file, for MCP servers
}

// Requires lists declared dependencies.
type Requires struct {
	Assets   []string `json:"assets,omitempty"`
	Commands []string `json:"commands,omitempty"`
}

func (s *Store) metaPath(r Ref) string {
	return s.Internal("meta", string(r.Kind), r.Name+".json")
}

// LoadMeta returns an asset's meta, or nil when none is recorded.
func (s *Store) LoadMeta(r Ref) (*Meta, error) {
	b, err := os.ReadFile(s.metaPath(r))
	if fsx.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var m Meta
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	return &m, nil
}

// SaveMeta writes an asset's meta.
func (s *Store) SaveMeta(r Ref, m *Meta) error {
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return fsx.WriteFileAtomic(s.metaPath(r), append(b, '\n'), 0o644)
}

// RemoveMeta deletes an asset's meta file.
func (s *Store) RemoveMeta(r Ref) error {
	err := os.Remove(s.metaPath(r))
	if fsx.IsNotExist(err) {
		return nil
	}
	return err
}

// MetaFiles lists refs that have meta files (used to find orphans).
func (s *Store) MetaFiles() []Ref {
	var out []Ref
	for _, k := range AllKinds {
		matches, _ := filepath.Glob(s.Internal("meta", string(k), "*.json"))
		for _, m := range matches {
			name := filepath.Base(m)
			out = append(out, Ref{k, name[:len(name)-len(".json")]})
		}
	}
	return out
}
