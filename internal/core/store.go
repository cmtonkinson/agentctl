package core

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/cmtonkinson/agentctl/internal/fsx"
)

// Store is the authoritative asset store (default ~/.agents).
//
//	<root>/instructions/<name>.md                     shared rules
//	<root>/instructions/<name>/AGENTS.md             shared rules with adapters
//	<root>/instructions/<name>/targets/<target>.md   optional target adapter
//	<root>/skills/<name>/SKILL.md
//	<root>/plugins/<name>/.claude-plugin/plugin.json
//	<root>/tools/<name>/tool.json
//	<root>/agentctl.yaml                             configuration
//	<root>/.agentctl/                                meta, state, packages
type Store struct {
	Root string
}

// Internal returns a path under <root>/.agentctl.
func (s *Store) Internal(parts ...string) string {
	return filepath.Join(append([]string{s.Root, ".agentctl"}, parts...)...)
}

// Path returns an asset's path. Instructions use a Markdown file unless an
// adapter-bearing directory with the same name already exists.
func (s *Store) Path(r Ref) string {
	if r.Kind == Instructions {
		dir := filepath.Join(s.Root, string(r.Kind), r.Name)
		if fsx.IsDir(dir) {
			return dir
		}
		return dir + ".md"
	}
	return filepath.Join(s.Root, string(r.Kind), r.Name)
}

// Exists reports whether the store directory exists.
func (s *Store) Exists() bool { return fsx.IsDir(s.Root) }

// Asset is one asset in the store.
type Asset struct {
	Ref
	Path        string
	Description string
	Version     string
	License     string
	Meta        *Meta
	Frontmatter map[string]any // skills
	Manifest    map[string]any // plugins
	Tool        *ToolSpec      // tools
	Adapters    []string       // instructions: targets with an adapter file
	Problems    []string
}

// Origin returns the recorded origin, defaulting to personal.
func (a *Asset) Origin() string {
	if a.Meta != nil && a.Meta.Origin != "" {
		return a.Meta.Origin
	}
	return OriginPersonal
}

// MainFile returns the path of the file that defines the asset.
func (a *Asset) MainFile() string {
	if a.Kind == Instructions && !fsx.IsDir(a.Path) {
		return a.Path
	}
	if a.Kind == Plugins {
		for _, name := range []string{"plugin.json", ".codex-plugin/plugin.json", ".claude-plugin/plugin.json"} {
			p := filepath.Join(a.Path, filepath.FromSlash(name))
			if fsx.Exists(p) {
				return p
			}
		}
	}
	return filepath.Join(a.Path, filepath.FromSlash(a.Kind.MainFile()))
}

// Hash returns the asset's content hash.
func (a *Asset) Hash() (string, error) { return fsx.Hash(a.Path) }

// HasAdapter reports whether an instructions asset has an adapter for target.
func (a *Asset) HasAdapter(target string) bool {
	for _, t := range a.Adapters {
		if t == target {
			return true
		}
	}
	return false
}

// ToolSpec is the content of a tool's tool.json.
type ToolSpec struct {
	Description string     `json:"description,omitempty"`
	MCP         *MCPServer `json:"mcp,omitempty"`
	Bin         []string   `json:"bin,omitempty"`
	Requires    []string   `json:"requires,omitempty"`
}

// MCPServer is a portable MCP server definition. Credential values belong
// in each client's own store; tool.json holds ${VAR} placeholders instead.
type MCPServer struct {
	Type    string            `json:"type,omitempty"` // stdio (default with command) | http | sse
	Command string            `json:"command,omitempty"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
	URL     string            `json:"url,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
}

// Remote reports whether the server is reached over the network.
func (m *MCPServer) Remote() bool { return m.URL != "" }

// Transport names the MCP transport.
func (m *MCPServer) Transport() string {
	if m.Type != "" {
		return m.Type
	}
	if m.Remote() {
		return "http"
	}
	return "stdio"
}

// Load reads one asset from the store.
func (s *Store) Load(r Ref) (*Asset, error) {
	path := s.Path(r)
	if !fsx.IsDir(path) && !(r.Kind == Instructions && fsx.Exists(path)) {
		return nil, fmt.Errorf("%s is not in the store", r)
	}
	a := &Asset{Ref: r, Path: path}
	if err := a.load(); err != nil {
		return nil, err
	}
	m, err := s.LoadMeta(r)
	if err != nil {
		a.Problems = append(a.Problems, "unreadable meta: "+err.Error())
	}
	a.Meta = m
	if m != nil {
		if a.Version == "" {
			a.Version = m.Version
		}
		if a.License == "" {
			a.License = m.License
		}
	}
	return a, nil
}

func (a *Asset) load() error {
	switch a.Kind {
	case Skills:
		fm, _, err := ReadFrontmatter(a.MainFile())
		if err != nil {
			a.Problems = append(a.Problems, err.Error())
			return nil
		}
		a.Frontmatter = fm
		a.Description = str(fm["description"])
		a.Version = str(fm["version"])
		if md, ok := fm["metadata"].(map[string]any); ok && a.Version == "" {
			a.Version = str(md["version"])
		}
		a.License = str(fm["license"])
		a.Problems = append(a.Problems, skillProblems(a.Name, fm)...)
	case Plugins:
		m, err := readJSONMap(a.MainFile())
		if err != nil {
			a.Problems = append(a.Problems, err.Error())
			return nil
		}
		a.Manifest = m
		a.Description = str(m["description"])
		a.Version = str(m["version"])
		a.License = str(m["license"])
		if n := str(m["name"]); n == "" {
			a.Problems = append(a.Problems, "plugin.json has no name")
		}
	case Instructions:
		b, err := os.ReadFile(a.MainFile())
		if err != nil {
			a.Problems = append(a.Problems, "missing instruction file")
		} else {
			a.Description = firstHeading(b)
		}
		matches, _ := filepath.Glob(filepath.Join(a.Path, "targets", "*.md"))
		for _, m := range matches {
			t := strings.TrimSuffix(filepath.Base(m), ".md")
			if TargetByName(t) == nil {
				a.Problems = append(a.Problems, fmt.Sprintf("adapter targets/%s.md names an unknown target", t))
				continue
			}
			a.Adapters = append(a.Adapters, t)
		}
	case Tools:
		spec := &ToolSpec{}
		b, err := os.ReadFile(a.MainFile())
		if err == nil {
			if err := json.Unmarshal(b, spec); err != nil {
				a.Problems = append(a.Problems, "tool.json: "+err.Error())
			}
		}
		if spec.MCP == nil && len(spec.Bin) == 0 {
			spec.Bin = findExecutables(a.Path)
		}
		for _, bin := range spec.Bin {
			if !fsx.IsExecutable(filepath.Join(a.Path, filepath.FromSlash(bin))) {
				a.Problems = append(a.Problems, fmt.Sprintf("bin %s is missing or not executable", bin))
			}
		}
		if spec.MCP != nil && spec.MCP.Command == "" && spec.MCP.URL == "" {
			a.Problems = append(a.Problems, "mcp definition needs a command or a url")
		}
		if spec.MCP == nil && len(spec.Bin) == 0 {
			a.Problems = append(a.Problems, "no tool.json MCP definition and no executables")
		}
		a.Tool = spec
		a.Description = spec.Description
	}
	return nil
}

func skillProblems(dir string, fm map[string]any) []string {
	var p []string
	name := str(fm["name"])
	if name == "" {
		p = append(p, "SKILL.md frontmatter has no name")
	} else if name != dir {
		p = append(p, fmt.Sprintf("SKILL.md name %q differs from directory %q", name, dir))
	}
	desc := str(fm["description"])
	if desc == "" {
		p = append(p, "SKILL.md frontmatter has no description")
	} else if len(desc) > 1024 {
		p = append(p, fmt.Sprintf("description is %d characters (limit 1024)", len(desc)))
	}
	return p
}

func findExecutables(dir string) []string {
	var out []string
	for _, sub := range []string{"", "bin"} {
		entries, _ := os.ReadDir(filepath.Join(dir, sub))
		for _, e := range entries {
			if e.IsDir() || fsx.Ignored(e.Name()) {
				continue
			}
			rel := e.Name()
			if sub != "" {
				rel = sub + "/" + e.Name()
			}
			if fsx.IsExecutable(filepath.Join(dir, filepath.FromSlash(rel))) {
				out = append(out, rel)
			}
		}
	}
	return out
}

// List returns every asset in the store, sorted by kind then name.
func (s *Store) List() ([]*Asset, error) {
	var out []*Asset
	seen := map[Ref]bool{}
	for _, k := range AllKinds {
		entries, err := os.ReadDir(filepath.Join(s.Root, string(k)))
		if fsx.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		for _, e := range entries {
			name := e.Name()
			if k == Instructions && strings.HasSuffix(name, ".md") && !e.IsDir() {
				name = strings.TrimSuffix(name, ".md")
			}
			if strings.HasPrefix(name, ".") || ValidName(name) != nil {
				continue
			}
			if !e.IsDir() && !(k == Instructions && strings.HasSuffix(e.Name(), ".md")) {
				continue
			}
			ref := Ref{k, name}
			if seen[ref] {
				continue
			}
			a, err := s.Load(ref)
			if err != nil {
				return nil, err
			}
			seen[ref] = true
			out = append(out, a)
		}
	}
	return out, nil
}

// Find resolves "kind/name" or a bare name (optionally restricted to kind).
func (s *Store) Find(ref string, kind Kind) (*Asset, error) {
	r, err := ParseRef(ref)
	if err != nil {
		return nil, err
	}
	if r.Kind == "" && kind != "" {
		r.Kind = kind
	}
	if r.Kind != "" {
		return s.Load(r)
	}
	var found []Ref
	for _, k := range AllKinds {
		if fsx.Exists(s.Path(Ref{k, r.Name})) {
			found = append(found, Ref{k, r.Name})
		}
	}
	switch len(found) {
	case 0:
		return nil, fmt.Errorf("no asset named %q in the store (see: agentctl list)", r.Name)
	case 1:
		return s.Load(found[0])
	}
	var names []string
	for _, f := range found {
		names = append(names, f.String())
	}
	return nil, fmt.Errorf("%q is ambiguous: %s", r.Name, strings.Join(names, ", "))
}

// ReadFrontmatter parses YAML frontmatter from a Markdown file.
func ReadFrontmatter(p string) (map[string]any, string, error) {
	b, err := os.ReadFile(p)
	if err != nil {
		if fsx.IsNotExist(err) {
			return nil, "", fmt.Errorf("missing %s", filepath.Base(p))
		}
		return nil, "", err
	}
	return ParseFrontmatter(b)
}

// ParseFrontmatter splits "---\nyaml\n---\nbody".
func ParseFrontmatter(b []byte) (map[string]any, string, error) {
	s := strings.TrimPrefix(string(b), "\ufeff")
	s = strings.ReplaceAll(s, "\r\n", "\n")
	if !strings.HasPrefix(s, "---\n") {
		return map[string]any{}, s, nil
	}
	rest := s[4:]
	end := strings.Index(rest, "\n---")
	if end < 0 {
		return nil, "", fmt.Errorf("unterminated frontmatter")
	}
	fm := map[string]any{}
	if err := yaml.Unmarshal([]byte(rest[:end]), &fm); err != nil {
		return nil, "", fmt.Errorf("frontmatter: %w", err)
	}
	body := rest[end+4:]
	if i := strings.Index(body, "\n"); i >= 0 {
		body = body[i+1:]
	} else {
		body = ""
	}
	return fm, body, nil
}

func readJSONMap(p string) (map[string]any, error) {
	b, err := os.ReadFile(p)
	if err != nil {
		if fsx.IsNotExist(err) {
			return nil, fmt.Errorf("missing %s", filepath.Base(p))
		}
		return nil, err
	}
	m := map[string]any{}
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("%s: %w", filepath.Base(p), err)
	}
	return m, nil
}

func firstHeading(b []byte) string {
	for _, line := range bytes.Split(b, []byte("\n")) {
		t := strings.TrimSpace(string(line))
		if strings.HasPrefix(t, "#") {
			return strings.TrimSpace(strings.TrimLeft(t, "#"))
		}
		if t != "" && !strings.HasPrefix(t, "<!--") {
			return truncate(t, 80)
		}
	}
	return ""
}

func str(v any) string {
	switch t := v.(type) {
	case string:
		return strings.TrimSpace(t)
	case nil:
		return ""
	default:
		return strings.TrimSpace(fmt.Sprint(t))
	}
}

func truncate(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len([]rune(s)) <= n {
		return s
	}
	return string([]rune(s)[:n-1]) + "…"
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
