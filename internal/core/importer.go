package core

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/cmtonkinson/agentctl/internal/fsx"
)

// ImportOptions configures Import.
type ImportOptions struct {
	Selection
	Name       string
	From       string
	All        bool
	Version    string
	OnConflict string // fail | skip | rename
	DryRun     bool
}

// ImportResult describes one imported (or previewed) asset.
type ImportResult struct {
	Source   string   `json:"source"`
	Asset    string   `json:"asset"`
	Action   string   `json:"action"` // imported | would import | identical | skipped | conflict
	Path     string   `json:"path,omitempty"`
	Origin   string   `json:"origin,omitempty"`
	Version  string   `json:"version,omitempty"`
	License  string   `json:"license,omitempty"`
	Hash     string   `json:"hash,omitempty"`
	Requires []DepRef `json:"requires,omitempty"`
	Warnings []string `json:"warnings,omitempty"`
	Reason   string   `json:"reason,omitempty"`
}

// DepRef is a required asset and whether the store has it.
type DepRef struct {
	Ref     string `json:"ref"`
	InStore bool   `json:"in_store"`
	Hint    string `json:"hint,omitempty"`
}

// candidate is an importable asset located in some source.
type candidate struct {
	Kind     Kind
	Name     string
	Dir      string    // directory copied as-is
	File     string    // single instructions file or script
	Tool     *ToolSpec // generated tool.json
	Source   Source    // provenance
	Origin   string
	Version  string
	License  string
	Requires []string
	Warnings []string
	Label    string
	repoRoot string // for license lookup
}

// materialize writes the candidate in store layout at dst.
func (c *candidate) materialize(dst string) error {
	switch {
	case c.Dir != "":
		skipped, err := fsx.CopyTreeContained(c.Dir, dst)
		if len(skipped) > 0 {
			c.Warnings = append(c.Warnings, "skipped symlinks that point outside the asset: "+strings.Join(skipped, ", "))
		}
		return err
	case c.Kind == Instructions && c.File != "":
		return fsx.CopyFile(c.File, dst, 0o644)
	case c.Kind == Tools && c.File != "":
		base := filepath.Base(c.File)
		if err := fsx.CopyFile(c.File, filepath.Join(dst, base), 0o755); err != nil {
			return err
		}
		return writeToolJSON(dst, &ToolSpec{Bin: []string{base}})
	case c.Kind == Tools && c.Tool != nil:
		return writeToolJSON(dst, c.Tool)
	}
	return fmt.Errorf("nothing to import for %s", c.Label)
}

func writeToolJSON(dir string, spec *ToolSpec) error {
	b, err := json.MarshalIndent(spec, "", "  ")
	if err != nil {
		return err
	}
	return fsx.WriteFileAtomic(filepath.Join(dir, "tool.json"), append(b, '\n'), 0o644)
}

// stage materializes a candidate into a temp dir and returns its path and hash.
func stage(c *candidate) (string, string, func(), error) {
	tmp, err := os.MkdirTemp("", "agentctl-stage-")
	if err != nil {
		return "", "", func() {}, err
	}
	cleanup := func() { os.RemoveAll(tmp) }
	dst := filepath.Join(tmp, "asset")
	if err := c.materialize(dst); err != nil {
		cleanup()
		return "", "", func() {}, err
	}
	h, err := fsx.Hash(dst)
	if err != nil {
		cleanup()
		return "", "", func() {}, err
	}
	return dst, h, cleanup, nil
}

// Import copies assets into the store. Originals are never moved, and
// existing store assets are never overwritten.
func (a *App) Import(source string, o ImportOptions) ([]*ImportResult, error) {
	if o.OnConflict == "" {
		o.OnConflict = "fail"
	}
	if !contains([]string{"fail", "skip", "rename"}, o.OnConflict) {
		return nil, fmt.Errorf("--on-conflict must be fail, skip, or rename")
	}
	cands, cleanup, err := a.resolveImport(source, o)
	defer cleanup()
	if err != nil {
		return nil, err
	}
	if len(cands) == 0 {
		return nil, fmt.Errorf("nothing importable matched")
	}
	if len(cands) > 1 && !o.All {
		var names []string
		for _, c := range cands {
			names = append(names, "  "+string(c.Kind)+"/"+c.Name+"  "+c.Label)
		}
		return nil, fmt.Errorf("%d assets matched; pass --all to import them all, or narrow the source:\n%s", len(cands), strings.Join(names, "\n"))
	}
	if o.Name != "" && len(cands) > 1 {
		return nil, fmt.Errorf("--name needs a single asset, but %d matched", len(cands))
	}

	type staged struct {
		c    *candidate
		ref  Ref
		dir  string
		hash string
		res  *ImportResult
	}
	var plan []*staged
	var conflicts []string
	taken := map[string]bool{}
	for _, c := range cands {
		name := c.Name
		if o.Name != "" {
			name = o.Name
		}
		if err := ValidName(name); err != nil {
			name = SanitizeName(name)
		}
		if o.Origin != "" {
			c.Origin = o.Origin
		}
		if c.Origin == "" {
			c.Origin = OriginPersonal
		}
		dir, h, clean, err := stage(c)
		if err != nil {
			return nil, err
		}
		defer clean()
		if dc := dirCandidate(dir, name); dc != nil {
			c.License = firstNonEmpty(c.License, dc.License)
			c.Version = firstNonEmpty(c.Version, dc.Version)
		}
		if c.License == "" {
			c.License = detectLicense(dir, c.repoRoot)
		}
		ref := Ref{c.Kind, name}
		res := &ImportResult{Source: c.Label, Origin: c.Origin, Version: c.Version, License: c.License, Hash: h, Warnings: c.Warnings}
		exists := func(r Ref) bool { return taken[r.String()] || fsx.Exists(a.Store.Path(r)) }
		if exists(ref) {
			existing, _ := fsx.Hash(a.Store.Path(ref))
			switch {
			case existing == h:
				res.Asset, res.Action, res.Reason = ref.String(), "identical", "already in the store"
				plan = append(plan, &staged{c, ref, "", h, res})
				continue
			case o.OnConflict == "skip":
				res.Asset, res.Action, res.Reason = ref.String(), "skipped", "a different asset with this name is in the store"
				plan = append(plan, &staged{c, ref, "", h, res})
				continue
			case o.OnConflict == "rename":
				for i := 2; exists(ref); i++ {
					ref.Name = fmt.Sprintf("%s-%d", name, i)
				}
			default:
				conflicts = append(conflicts, fmt.Sprintf("  %s (from %s)", ref, c.Label))
				continue
			}
		}
		taken[ref.String()] = true
		res.Asset = ref.String()
		res.Path = a.Store.Path(ref)
		plan = append(plan, &staged{c, ref, dir, h, res})
	}
	for _, st := range plan {
		for _, req := range st.c.Requires {
			r, _ := ParseRef(req)
			in := fsx.IsDir(a.Store.Path(r)) || taken[req]
			d := DepRef{Ref: req, InStore: in}
			if !in {
				d.Hint = "import it too, or deployment stays blocked"
				if st.c.Source.Target != "" {
					d.Hint = fmt.Sprintf("import it: agentctl import %s --from %s --kind %s", r.Name, st.c.Source.Target, r.Kind)
				}
			}
			st.res.Requires = append(st.res.Requires, d)
		}
	}
	if len(conflicts) > 0 {
		return nil, fmt.Errorf("the store already has different assets with these names (nothing was imported):\n%s\nuse --on-conflict skip|rename, or --name", strings.Join(conflicts, "\n"))
	}
	var results []*ImportResult
	for _, s := range plan {
		results = append(results, s.res)
		if s.dir == "" {
			continue
		}
		if o.DryRun {
			s.res.Action = "would import"
			continue
		}
		dest := a.Store.Path(s.ref)
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return results, err
		}
		if err := fsx.CopyTree(s.dir, dest); err != nil {
			return results, err
		}
		now := a.Now().UTC()
		src := s.c.Source
		files, _ := fsx.FileHashes(s.dir)
		if s.c.Kind == Instructions {
			files = nil
		}
		m := &Meta{Origin: s.c.Origin, Source: &src, License: s.c.License, Version: s.c.Version, ImportedAt: &now, ImportedHash: s.hash, SourceHash: s.hash, Files: files}
		if len(s.c.Requires) > 0 {
			m.Requires = &Requires{Assets: s.c.Requires}
		}
		if err := a.Store.SaveMeta(s.ref, m); err != nil {
			return results, err
		}
		s.res.Action = "imported"
	}
	return results, nil
}

var inventoryIDRe = regexp.MustCompile(`^[0-9a-f]{8}$`)

func (a *App) resolveImport(source string, o ImportOptions) ([]*candidate, func(), error) {
	noop := func() {}
	if o.From != "" {
		if TargetByName(o.From) == nil {
			return nil, noop, fmt.Errorf("unknown target %q", o.From)
		}
		if source == "" && !o.All {
			return nil, noop, fmt.Errorf("name an asset to import from %s, or pass --all", o.From)
		}
		sel := o.Selection
		sel.Targets = []string{o.From}
		if source == "" && o.All && sel.Origin == "" {
			sel.Origin = OriginPersonal
		}
		inv, err := a.Inventory(InventoryOptions{Selection: sel})
		if err != nil {
			return nil, noop, err
		}
		var items []*Item
		for _, it := range inv.Items {
			if source != "" && it.ID != source && it.Name != source {
				continue
			}
			if source == "" && (it.Status == ItemManaged || it.Status == ItemInStore || it.Status == ItemRecorded) {
				continue
			}
			items = append(items, it)
		}
		if len(items) == 0 {
			if source != "" {
				return nil, noop, fmt.Errorf("%s: nothing named %q found (see: agentctl inventory --target %s)", o.From, source, o.From)
			}
			return nil, noop, fmt.Errorf("%s: no unmanaged assets match the selection", o.From)
		}
		if source != "" && len(items) > 1 && !o.All {
			var lines []string
			for _, it := range items {
				lines = append(lines, fmt.Sprintf("  %s  %s/%s  %s  %s", it.ID, it.Kind, it.Name, it.Scope, a.Abbrev(it.Path)))
			}
			return nil, noop, fmt.Errorf("%q matches %d items; import one by ID:\n%s", source, len(items), strings.Join(lines, "\n"))
		}
		return a.itemCandidates(items)
	}
	if source == "" {
		return nil, noop, fmt.Errorf("import needs a SOURCE (inventory ID, path, ZIP, or repository URL) or --from TARGET")
	}
	path := a.AbsPath(source)
	if inventoryIDRe.MatchString(source) && !fsx.Exists(path) {
		for _, scope := range []string{"", ScopeProject} {
			sel := o.Selection
			sel.Targets, sel.Scope, sel.Kind, sel.Origin = nil, scope, "", ""
			inv, err := a.Inventory(InventoryOptions{Selection: sel, IncludeCache: true})
			if err != nil {
				return nil, noop, err
			}
			for _, it := range inv.Items {
				if it.ID == source {
					return a.itemCandidates([]*Item{it})
				}
			}
		}
		return nil, noop, fmt.Errorf("no inventory item with ID %s (see: agentctl inventory)", source)
	}
	if isGitURL(source) {
		return a.gitCandidates(source, o)
	}
	if !fsx.Exists(path) {
		return nil, noop, fmt.Errorf("%s: no such file, inventory ID, or repository", source)
	}
	if Excluded(path, a.Excludes(o.Excludes)) {
		return nil, noop, fmt.Errorf("%s is excluded by configuration", a.Abbrev(path))
	}
	if strings.EqualFold(filepath.Ext(path), ".zip") && !fsx.IsDir(path) {
		return a.zipCandidates(path, o.Kind)
	}
	if fsx.Within(a.Store.Root, path) {
		return nil, noop, fmt.Errorf("%s is already inside the store", a.Abbrev(path))
	}
	if ref := a.managedBy(path); ref != "" {
		return nil, noop, fmt.Errorf("%s was deployed by agentctl from %s; it is not a source", a.Abbrev(path), ref)
	}
	cands, err := locate(path, "", o.Kind)
	for _, c := range cands {
		c.Source = Source{Type: "path", Location: firstNonEmpty(c.Dir, c.File, c.Source.Location), Key: c.Source.Key, Kind: string(c.Kind), Name: c.Name}
		c.Label = a.Abbrev(c.Source.Location)
	}
	return cands, noop, err
}

func firstNonEmpty(s ...string) string {
	for _, x := range s {
		if x != "" {
			return x
		}
	}
	return ""
}

func (a *App) itemCandidates(items []*Item) ([]*candidate, func(), error) {
	var out []*candidate
	for _, it := range items {
		c, err := a.itemCandidate(it)
		if err != nil {
			return nil, func() {}, err
		}
		out = append(out, c)
	}
	return out, func() {}, nil
}

func (a *App) itemCandidate(it *Item) (*candidate, error) {
	if !it.importable {
		why := "account assets cannot be downloaded by agentctl; download the file from the web UI and import it"
		if it.Note == "desktop extension" {
			why = "desktop extensions are managed by Claude Desktop; add the underlying MCP server as a tool instead"
		}
		return nil, fmt.Errorf("%s (%s/%s) is not importable: %s", it.ID, it.Kind, it.Name, why)
	}
	c := &candidate{
		Kind: it.Kind, Name: it.Name, Origin: it.Origin, Version: it.Version,
		Source: Source{Type: "target", Target: it.Target, Scope: it.Scope, Project: it.Project, Plugin: it.Plugin, Kind: string(it.Kind), Name: it.Name, Location: it.Path, Key: it.Key},
		Label:  fmt.Sprintf("%s %s %s", it.ID, it.Target, a.Abbrev(it.Path)),
	}
	switch {
	case it.value != nil:
		m, withheld := mcpFromClient(it.value)
		if m == nil {
			return nil, fmt.Errorf("%s: unreadable MCP entry", it.ID)
		}
		c.Tool = &ToolSpec{Description: describeServer(m), MCP: m}
		if len(withheld) > 0 {
			c.Warnings = append(c.Warnings, "credential values ("+strings.Join(withheld, ", ")+") were replaced with ${VAR} placeholders; they stay in "+it.Target+"'s config")
		}
	case it.Kind == Instructions:
		c.File = it.Path
	default:
		c.Dir = it.Path
	}
	if it.Plugin != "" && it.Kind != Plugins {
		c.Requires = []string{"plugins/" + SanitizeName(it.Plugin)}
	}
	return c, nil
}

// locate finds importable assets at root (a file or directory).
// rootName names an asset found at root itself (default: its base name).
func locate(root, rootName string, kind Kind) ([]*candidate, error) {
	if rootName == "" {
		rootName = filepath.Base(root)
	}
	var out []*candidate
	keep := func(c *candidate) {
		if kind == "" || c.Kind == kind {
			out = append(out, c)
		}
	}
	if !fsx.IsDir(root) {
		base := strings.ToLower(filepath.Base(root))
		switch {
		case base == "skill.md":
			if c := dirCandidate(filepath.Dir(root), filepath.Base(filepath.Dir(root))); c != nil {
				keep(c)
			}
		case strings.HasSuffix(base, ".md"):
			name := strings.TrimSuffix(filepath.Base(root), filepath.Ext(root))
			if base == "agents.md" || base == "claude.md" {
				name = strings.TrimPrefix(filepath.Base(filepath.Dir(root)), ".")
			}
			keep(&candidate{Kind: Instructions, Name: SanitizeName(name), File: root})
		case strings.HasSuffix(base, ".json"):
			m, err := readJSONMap(root)
			if err != nil {
				return nil, err
			}
			if servers, ok := m["mcpServers"].(map[string]any); ok {
				for _, n := range sortedKeys(servers) {
					raw, _ := json.Marshal(servers[n])
					spec, withheld := mcpFromClient(raw)
					if spec == nil {
						continue
					}
					c := &candidate{Kind: Tools, Name: SanitizeName(n), Tool: &ToolSpec{Description: describeServer(spec), MCP: spec}, Source: Source{Key: "mcpServers." + n, Location: root}}
					if len(withheld) > 0 {
						c.Warnings = append(c.Warnings, "credential values ("+strings.Join(withheld, ", ")+") were replaced with ${VAR} placeholders")
					}
					keep(c)
				}
			} else if base == "tool.json" || base == "plugin.json" {
				dir := filepath.Dir(root)
				if base == "plugin.json" && (filepath.Base(dir) == ".claude-plugin" || filepath.Base(dir) == ".codex-plugin") {
					dir = filepath.Dir(dir)
				}
				if c := dirCandidate(dir, filepath.Base(dir)); c != nil {
					keep(c)
				}
			} else {
				return nil, fmt.Errorf("%s: no mcpServers to import", filepath.Base(root))
			}
		case fsx.IsExecutable(root):
			keep(&candidate{Kind: Tools, Name: SanitizeName(strings.TrimSuffix(filepath.Base(root), filepath.Ext(root))), File: root})
		default:
			return nil, fmt.Errorf("%s: not a recognizable asset (expected SKILL.md, a Markdown instructions file, an MCP JSON file, or an executable)", root)
		}
		return out, nil
	}
	if c := dirCandidate(root, rootName); c != nil {
		keep(c)
		return out, nil
	}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || !d.IsDir() || p == root {
			return nil
		}
		n := d.Name()
		if fsx.Ignored(n) || n == "node_modules" || (strings.HasPrefix(n, ".") && n != ".claude" && n != ".agents" && n != ".codex") {
			return filepath.SkipDir
		}
		if rel, _ := filepath.Rel(root, p); strings.Count(rel, string(filepath.Separator)) >= 4 {
			return filepath.SkipDir
		}
		if c := dirCandidate(p, n); c != nil {
			keep(c)
			return filepath.SkipDir
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(out) == 0 && len(findExecutables(root)) > 0 {
		keep(&candidate{Kind: Tools, Name: SanitizeName(rootName), Dir: root})
	}
	if len(out) == 0 {
		for _, f := range []string{"AGENTS.md", "CLAUDE.md"} {
			if fsx.Exists(filepath.Join(root, f)) {
				keep(&candidate{Kind: Instructions, Name: SanitizeName(strings.TrimPrefix(rootName, ".")), File: filepath.Join(root, f)})
				break
			}
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no skills, plugins, tools, or instructions found in %s", root)
	}
	return out, nil
}

// dirCandidate recognizes a directory that is itself an asset.
func dirCandidate(dir, name string) *candidate {
	for _, manifest := range []string{"plugin.json", ".codex-plugin/plugin.json", ".claude-plugin/plugin.json"} {
		file := filepath.Join(dir, filepath.FromSlash(manifest))
		if !fsx.Exists(file) {
			continue
		}
		c := &candidate{Kind: Plugins, Name: SanitizeName(name), Dir: dir}
		if m, err := readJSONMap(file); err == nil {
			if n := str(m["name"]); n != "" {
				c.Name = SanitizeName(n)
			}
			c.Version, c.License = str(m["version"]), str(m["license"])
		}
		return c
	}
	switch {
	case fsx.Exists(filepath.Join(dir, "SKILL.md")):
		c := &candidate{Kind: Skills, Name: SanitizeName(name), Dir: dir}
		if fm, _, err := ReadFrontmatter(filepath.Join(dir, "SKILL.md")); err == nil {
			if n := str(fm["name"]); n != "" && ValidName(n) == nil {
				c.Name = n
			}
			c.Version, c.License = str(fm["version"]), str(fm["license"])
		}
		return c
	case fsx.Exists(filepath.Join(dir, "tool.json")):
		return &candidate{Kind: Tools, Name: SanitizeName(name), Dir: dir}
	}
	return nil
}

func (a *App) zipCandidates(zipPath string, kind Kind) ([]*candidate, func(), error) {
	tmp, err := os.MkdirTemp("", "agentctl-zip-")
	if err != nil {
		return nil, func() {}, err
	}
	cleanup := func() { os.RemoveAll(tmp) }
	if err := fsx.Unzip(zipPath, tmp); err != nil {
		return nil, cleanup, fmt.Errorf("%s: %w", zipPath, err)
	}
	root, rootName := tmp, strings.TrimSuffix(filepath.Base(zipPath), filepath.Ext(zipPath))
	if entries, _ := os.ReadDir(tmp); len(entries) == 1 && entries[0].IsDir() {
		root, rootName = filepath.Join(tmp, entries[0].Name()), entries[0].Name()
	}
	cands, err := locate(root, rootName, kind)
	for _, c := range cands {
		sub, _ := filepath.Rel(tmp, firstNonEmpty(c.Dir, c.File))
		c.Source = Source{Type: "zip", Location: zipPath, Subpath: filepath.ToSlash(sub), Kind: string(c.Kind), Name: c.Name}
		c.Label = filepath.Base(zipPath) + ":" + filepath.ToSlash(sub)
		c.repoRoot = root
	}
	return cands, cleanup, err
}

var githubTreeRe = regexp.MustCompile(`^https://github\.com/([^/]+)/([^/]+)/(?:tree|blob)/([^/]+)(?:/(.*))?$`)

func isGitURL(s string) bool {
	for _, p := range []string{"https://", "http://", "git@", "ssh://", "git://", "file://"} {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return strings.HasSuffix(s, ".git")
}

// parseRepoURL splits a repository URL into clone URL, ref, and subpath.
// It accepts URL#subpath and GitHub /tree/<ref>/<path> links.
func parseRepoURL(s string) (url, ref, sub string) {
	if i := strings.Index(s, "#"); i >= 0 {
		s, sub = s[:i], s[i+1:]
	}
	if m := githubTreeRe.FindStringSubmatch(s); m != nil {
		return "https://github.com/" + m[1] + "/" + strings.TrimSuffix(m[2], ".git") + ".git", m[3], strings.Trim(m[4], "/")
	}
	return s, "", strings.Trim(sub, "/")
}

func repoName(url string) string {
	base := strings.TrimSuffix(filepath.Base(strings.TrimRight(url, "/")), ".git")
	if i := strings.LastIndex(base, ":"); i >= 0 {
		base = base[i+1:]
	}
	return SanitizeName(base)
}

// gitCheckout clones url at ref (branch, tag, or commit) into dst and
// returns the resolved commit.
func (a *App) gitCheckout(url, ref, dst string) (string, error) {
	if _, err := a.LookPath("git"); err != nil {
		return "", fmt.Errorf("importing from a repository needs git on PATH")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	args := []string{"clone", "--quiet", "--depth", "1"}
	if ref != "" {
		args = append(args, "--branch", ref)
	}
	if _, err := a.Run(ctx, "", "git", append(args, url, dst)...); err != nil {
		if ref == "" {
			return "", err
		}
		// A commit hash cannot be cloned by name; clone fully and check out.
		os.RemoveAll(dst)
		if _, err := a.Run(ctx, "", "git", "clone", "--quiet", url, dst); err != nil {
			return "", err
		}
		if _, err := a.Run(ctx, dst, "git", "checkout", "--quiet", ref); err != nil {
			return "", fmt.Errorf("revision %s not found: %w", ref, err)
		}
	}
	out, err := a.Run(ctx, dst, "git", "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// gitRemoteRevision resolves ref (default HEAD) on the remote without cloning.
func (a *App) gitRemoteRevision(url, ref string) (string, error) {
	if ref == "" {
		ref = "HEAD"
	}
	if regexp.MustCompile(`^[0-9a-f]{7,40}$`).MatchString(ref) {
		return ref, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	out, err := a.Run(ctx, "", "git", "ls-remote", url, ref, "refs/tags/"+ref+"^{}")
	if err != nil {
		return "", err
	}
	rev := ""
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		f := strings.Fields(line)
		if len(f) != 2 {
			continue
		}
		if rev == "" || strings.HasSuffix(f[1], "^{}") {
			rev = f[0] // peeled tags point at the commit
		}
	}
	if rev == "" {
		return "", fmt.Errorf("%s: ref %s not found", url, ref)
	}
	return rev, nil
}

func (a *App) gitCandidates(source string, o ImportOptions) ([]*candidate, func(), error) {
	url, ref, sub := parseRepoURL(source)
	if o.Version != "" {
		ref = o.Version
	}
	tmp, err := os.MkdirTemp("", "agentctl-git-")
	if err != nil {
		return nil, func() {}, err
	}
	cleanup := func() { os.RemoveAll(tmp) }
	repo := filepath.Join(tmp, "repo")
	rev, err := a.gitCheckout(url, ref, repo)
	if err != nil {
		return nil, cleanup, err
	}
	root := filepath.Join(repo, filepath.FromSlash(sub))
	if !fsx.Within(repo, root) || !fsx.Exists(root) {
		return nil, cleanup, fmt.Errorf("%s not found in %s", sub, url)
	}
	rootName := repoName(url)
	if sub != "" {
		rootName = filepath.Base(root)
	}
	cands, err := locate(root, rootName, o.Kind)
	for _, c := range cands {
		rel, _ := filepath.Rel(repo, firstNonEmpty(c.Dir, c.File))
		rel = filepath.ToSlash(rel)
		if rel == "." {
			rel = ""
		}
		c.Source = Source{Type: "git", Location: url, Subpath: rel, Ref: ref, Revision: rev, Kind: string(c.Kind), Name: c.Name}
		c.Origin = OriginThirdParty
		if c.Version == "" {
			c.Version = firstNonEmpty(ref, rev[:min(12, len(rev))])
		}
		c.Label = url
		if rel != "" {
			c.Label += "#" + rel
		}
		c.repoRoot = repo
	}
	return cands, cleanup, err
}

// detectLicense identifies a license file in dir or, failing that, root.
func detectLicense(dir, root string) string {
	for _, d := range []string{dir, root} {
		if d == "" {
			continue
		}
		matches, _ := filepath.Glob(filepath.Join(d, "LICEN[CS]E*"))
		matches2, _ := filepath.Glob(filepath.Join(d, "COPYING*"))
		for _, m := range append(matches, matches2...) {
			b, err := os.ReadFile(m)
			if err != nil {
				continue
			}
			return identifyLicense(string(b))
		}
	}
	return ""
}

func identifyLicense(text string) string {
	head := text
	if len(head) > 2000 {
		head = head[:2000]
	}
	u := strings.ToUpper(head)
	switch {
	case strings.Contains(u, "MIT LICENSE") || strings.Contains(u, "PERMISSION IS HEREBY GRANTED, FREE OF CHARGE"):
		return "MIT"
	case strings.Contains(u, "APACHE LICENSE"):
		return "Apache-2.0"
	case strings.Contains(u, "GNU AFFERO GENERAL PUBLIC LICENSE"):
		return "AGPL-3.0"
	case strings.Contains(u, "GNU LESSER GENERAL PUBLIC LICENSE"):
		return "LGPL"
	case strings.Contains(u, "GNU GENERAL PUBLIC LICENSE") && strings.Contains(u, "VERSION 3"):
		return "GPL-3.0"
	case strings.Contains(u, "GNU GENERAL PUBLIC LICENSE"):
		return "GPL"
	case strings.Contains(u, "MOZILLA PUBLIC LICENSE"):
		return "MPL-2.0"
	case strings.Contains(u, "ISC LICENSE"):
		return "ISC"
	case strings.Contains(u, "BSD"):
		return "BSD"
	case strings.Contains(u, "UNLICENSE") || strings.Contains(u, "PUBLIC DOMAIN"):
		return "Unlicense"
	case strings.Contains(u, "CREATIVE COMMONS"):
		return "CC"
	}
	return "see LICENSE"
}
