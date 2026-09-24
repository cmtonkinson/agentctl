package core

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/cmtonkinson/agentctl/internal/fsx"
)

// Item is an asset discovered in a target's files or account records.
type Item struct {
	ID          string `json:"id"`
	Target      string `json:"target"`
	Scope       string `json:"scope"`
	Project     string `json:"project,omitempty"`
	Kind        Kind   `json:"kind"`
	Name        string `json:"name"`
	Origin      string `json:"origin"`
	Path        string `json:"path,omitempty"`
	Key         string `json:"key,omitempty"` // config entry, for MCP servers
	Plugin      string `json:"plugin,omitempty"`
	Version     string `json:"version,omitempty"`
	Description string `json:"description,omitempty"`
	Hash        string `json:"hash,omitempty"`
	Cached      bool   `json:"cached,omitempty"`
	Status      string `json:"status"`              // managed | in-store | differs | unmanaged | recorded
	StoreRef    string `json:"store_ref,omitempty"` // matching store asset
	Note        string `json:"note,omitempty"`

	value      json.RawMessage // MCP entry
	importable bool
}

// Inventory statuses.
const (
	ItemManaged   = "managed"
	ItemInStore   = "in-store"
	ItemDiffers   = "differs"
	ItemUnmanaged = "unmanaged"
	ItemRecorded  = "recorded"
)

// InventoryOptions configures discovery.
type InventoryOptions struct {
	Selection
	Refresh      bool
	IncludeCache bool
}

// InventoryResult holds discovered items and notes about coverage.
type InventoryResult struct {
	Items []*Item  `json:"items"`
	Notes []string `json:"notes,omitempty"`
}

type collector struct {
	a        *App
	opts     InventoryOptions
	excludes []string
	items    []*Item
	notes    []string
	seen     map[string]bool
}

// Inventory discovers assets across targets.
func (a *App) Inventory(opts InventoryOptions) (*InventoryResult, error) {
	sel := opts.Selection
	if len(sel.Targets) == 0 {
		sel.AllTargets = true
	}
	targets, err := a.SelectedTargets(sel)
	if err != nil {
		return nil, err
	}
	c := &collector{a: a, opts: opts, excludes: a.Excludes(opts.Excludes), seen: map[string]bool{}}
	projects := c.projects()
	for _, t := range targets {
		switch t {
		case "claude-code":
			c.claudeCode(projects)
		case "codex":
			c.codex(projects)
		case "claude-chat":
			c.claudeChat()
		case "chatgpt":
			c.chatgpt()
		}
	}
	c.classify()
	var out []*Item
	for _, it := range c.items {
		if c.keep(it) {
			out = append(out, it)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		x, y := out[i], out[j]
		if x.Target != y.Target {
			return x.Target < y.Target
		}
		if x.Kind != y.Kind {
			return kindIndex(x.Kind) < kindIndex(y.Kind)
		}
		if x.Name != y.Name {
			return x.Name < y.Name
		}
		return x.Path < y.Path
	})
	return &InventoryResult{Items: out, Notes: c.notes}, nil
}

func kindIndex(k Kind) int {
	for i, x := range AllKinds {
		if x == k {
			return i
		}
	}
	return len(AllKinds)
}

func (c *collector) keep(it *Item) bool {
	o := c.opts
	if o.Kind != "" && it.Kind != o.Kind {
		return false
	}
	if o.Origin != "" && it.Origin != o.Origin {
		return false
	}
	if o.Scope != "" && it.Scope != o.Scope {
		return false
	}
	if it.Cached && !o.IncludeCache {
		return false
	}
	return !Excluded(it.Path, c.excludes)
}

// projects returns project directories to scan.
func (c *collector) projects() []string {
	a := c.a
	var out []string
	add := func(p string) {
		p = a.AbsPath(p)
		if !fsx.IsDir(p) || Excluded(p, c.excludes) || contains(out, p) || fsx.SamePath(p, a.Home) {
			return
		}
		out = append(out, p)
	}
	if c.opts.Project != "" {
		add(c.opts.Project)
		return out
	}
	if c.opts.Scope != ScopeProject {
		return nil
	}
	for _, p := range a.Config.ProjectPaths() {
		add(p)
	}
	if m, err := readJSONMap(a.TargetPaths("claude-code")["settings"]); err == nil {
		if projs, ok := m["projects"].(map[string]any); ok {
			for _, p := range sortedKeys(projs) {
				add(p)
			}
		}
	}
	return out
}

func (c *collector) add(it *Item) *Item {
	if it.Origin == "" {
		it.Origin = OriginPersonal
	}
	if it.Scope != ScopeProject {
		it.Project = ""
	}
	h := sha1.Sum([]byte(strings.Join([]string{it.Target, it.Scope, it.Project, string(it.Kind), it.Name, it.Path, it.Key, it.Plugin}, "|")))
	it.ID = hex.EncodeToString(h[:])[:8]
	if c.seen[it.ID] {
		return it
	}
	c.seen[it.ID] = true
	if it.Hash == "" && it.Path != "" && it.value == nil {
		it.Hash, _ = fsx.Hash(it.Path)
	}
	if it.value != nil {
		it.Hash = canonicalHash(it.value)
	}
	if it.Scope != ScopeAccount {
		it.importable = true
	}
	c.items = append(c.items, it)
	return it
}

func (c *collector) note(format string, args ...any) {
	c.notes = append(c.notes, fmt.Sprintf(format, args...))
}

func scope(project string) string {
	if project != "" {
		return ScopeProject
	}
	return ScopeUser
}

func (c *collector) projectKey(project string) string { return c.a.ProjectKey(project) }

func (c *collector) instructionFile(target, project, path, name string) {
	if !fsx.Exists(path) || fsx.IsDir(path) {
		return
	}
	b, _ := os.ReadFile(path)
	c.add(&Item{Target: target, Scope: scope(project), Project: c.projectKey(project), Kind: Instructions, Name: name, Path: path, Description: firstHeading(b)})
}

// skillsDir scans a directory of skills (and skills-dir plugins).
func (c *collector) skillsDir(target, project, dir, origin string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		p := filepath.Join(dir, e.Name())
		if !fsx.IsDir(p) {
			continue
		}
		if strings.HasPrefix(e.Name(), ".") {
			if e.Name() == ".system" {
				c.skillsDir(target, project, p, OriginSystem)
			}
			continue
		}
		if fsx.Exists(filepath.Join(p, ".claude-plugin", "plugin.json")) {
			c.plugin(target, project, p, origin, "", false)
			continue
		}
		if fsx.Exists(filepath.Join(p, "SKILL.md")) {
			c.skill(target, project, p, origin, "")
		}
	}
}

func (c *collector) skill(target, project, dir, origin, plugin string) {
	fm, _, _ := ReadFrontmatter(filepath.Join(dir, "SKILL.md"))
	name := str(fm["name"])
	if name == "" || ValidName(name) != nil {
		name = filepath.Base(dir)
	}
	version := str(fm["version"])
	c.add(&Item{Target: target, Scope: scope(project), Project: c.projectKey(project), Kind: Skills, Name: name, Origin: origin, Path: dir, Plugin: plugin, Version: version, Description: str(fm["description"])})
}

func (c *collector) plugin(target, project, dir, origin, id string, cached bool) {
	m, err := readJSONMap(filepath.Join(dir, ".claude-plugin", "plugin.json"))
	name := filepath.Base(dir)
	var version, desc string
	if err == nil {
		if n := str(m["name"]); n != "" {
			name = n
		}
		version, desc = str(m["version"]), str(m["description"])
	}
	it := c.add(&Item{Target: target, Scope: scope(project), Project: c.projectKey(project), Kind: Plugins, Name: name, Origin: origin, Path: dir, Version: version, Description: desc, Cached: cached})
	if id != "" {
		it.Note = id
	}
	if cached || (fsx.IsSymlink(dir) && linksIntoStore(c.a, dir)) {
		return // cached copies and agentctl's own deployments are not expanded
	}
	// Skills and MCP servers the plugin provides.
	if entries, err := os.ReadDir(filepath.Join(dir, "skills")); err == nil {
		for _, e := range entries {
			p := filepath.Join(dir, "skills", e.Name())
			if fsx.Exists(filepath.Join(p, "SKILL.md")) {
				c.skill(target, project, p, origin, name)
			}
		}
	}
	if fsx.Exists(filepath.Join(dir, "SKILL.md")) {
		c.skill(target, project, dir, origin, name)
	}
	mcpFile := filepath.Join(dir, ".mcp.json")
	c.mcpServers(target, project, mcpFile, "mcpServers", origin, name)
}

// mcpServers adds each entry of a JSON mcpServers object.
func (c *collector) mcpServers(target, project, file, top, origin, plugin string) {
	m, err := readJSONMap(file)
	if err != nil {
		return
	}
	servers, _ := m[top].(map[string]any)
	c.mcpMap(target, project, file, top, servers, origin, plugin)
}

func (c *collector) mcpMap(target, project, file, top string, servers map[string]any, origin, plugin string) {
	for _, name := range sortedKeys(servers) {
		raw, _ := json.Marshal(servers[name])
		desc := describeMCP(raw)
		c.add(&Item{Target: target, Scope: scope(project), Project: c.projectKey(project), Kind: Tools, Name: SanitizeName(name), Origin: origin, Path: file, Key: top + "." + name, Plugin: plugin, Description: desc, value: raw})
	}
}

func describeMCP(raw json.RawMessage) string {
	m, _ := mcpFromClient(raw)
	if m == nil {
		return "MCP server"
	}
	return describeServer(m)
}

func (c *collector) claudeCode(projects []string) {
	a := c.a
	t := "claude-code"
	p := a.TargetPaths(t)
	c.instructionFile(t, "", p["instructions"], t)
	c.skillsDir(t, "", p["skills"], OriginPersonal)
	if p["plugins"] != p["skills"] {
		c.skillsDir(t, "", p["plugins"], OriginPersonal)
	}
	installed := map[string]bool{}
	for _, ip := range readInstalledPlugins(filepath.Join(p["plugin-data"], "installed_plugins.json")) {
		installed[fsx.Real(ip.Path)] = true
		c.plugin(t, ip.Project, ip.Path, OriginThirdParty, ip.ID, false)
	}
	if c.opts.Refresh {
		if _, err := a.LookPath("claude"); err != nil {
			c.note("claude-code: --refresh needs the claude CLI on PATH")
		} else {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			out, err := a.Run(ctx, "", "claude", "plugin", "list", "--json")
			cancel()
			var list []struct {
				ID          string `json:"id"`
				Scope       string `json:"scope"`
				InstallPath string `json:"installPath"`
			}
			if err != nil || json.Unmarshal(out, &list) != nil {
				c.note("claude-code: could not read `claude plugin list --json`")
			}
			for _, pl := range list {
				if pl.InstallPath == "" || installed[fsx.Real(pl.InstallPath)] || strings.HasSuffix(pl.ID, "@skills-dir") {
					continue
				}
				installed[fsx.Real(pl.InstallPath)] = true
				c.plugin(t, "", pl.InstallPath, OriginThirdParty, pl.ID, false)
			}
		}
	}
	if c.opts.IncludeCache {
		cacheDirs, _ := filepath.Glob(filepath.Join(p["plugin-data"], "cache", "*", "*", "*"))
		for _, d := range cacheDirs {
			if installed[fsx.Real(d)] || !fsx.Exists(filepath.Join(d, ".claude-plugin", "plugin.json")) {
				continue
			}
			rel, _ := filepath.Rel(filepath.Join(p["plugin-data"], "cache"), d)
			parts := strings.Split(filepath.ToSlash(rel), "/")
			c.plugin(t, "", d, OriginThirdParty, parts[1]+"@"+parts[0]+" (cached)", true)
		}
	}
	if settings, err := readJSONMap(p["settings"]); err == nil {
		servers, _ := settings["mcpServers"].(map[string]any)
		c.mcpMap(t, "", p["settings"], "mcpServers", servers, OriginPersonal, "")
		if projs, ok := settings["projects"].(map[string]any); ok {
			for _, proj := range projects {
				if entry, ok := projs[proj].(map[string]any); ok {
					servers, _ := entry["mcpServers"].(map[string]any)
					c.mcpMap(t, proj, p["settings"], "projects."+proj+".mcpServers", servers, OriginPersonal, "")
				}
			}
		}
	}
	for _, proj := range projects {
		c.instructionFile(t, proj, filepath.Join(proj, "CLAUDE.md"), filepath.Base(proj))
		c.instructionFile(t, proj, filepath.Join(proj, ".claude", "CLAUDE.md"), filepath.Base(proj))
		c.skillsDir(t, proj, filepath.Join(proj, ".claude", "skills"), OriginPersonal)
		c.mcpServers(t, proj, filepath.Join(proj, ".mcp.json"), "mcpServers", OriginPersonal, "")
	}
}

type installedPlugin struct {
	ID, Path, Project string
}

func readInstalledPlugins(file string) []installedPlugin {
	b, err := os.ReadFile(file)
	if err != nil {
		return nil
	}
	var raw struct {
		Plugins map[string]json.RawMessage `json:"plugins"`
	}
	if json.Unmarshal(b, &raw) != nil {
		return nil
	}
	type entry struct {
		InstallPath string `json:"installPath"`
		ProjectPath string `json:"projectPath"`
	}
	var out []installedPlugin
	for _, id := range sortedKeys(raw.Plugins) {
		var list []entry
		if json.Unmarshal(raw.Plugins[id], &list) != nil {
			var one entry
			if json.Unmarshal(raw.Plugins[id], &one) != nil {
				continue
			}
			list = []entry{one}
		}
		for _, e := range list {
			if e.InstallPath != "" && fsx.IsDir(e.InstallPath) {
				out = append(out, installedPlugin{id, e.InstallPath, e.ProjectPath})
			}
		}
	}
	return out
}

func (c *collector) codex(projects []string) {
	a := c.a
	t := "codex"
	p := a.TargetPaths(t)
	c.instructionFile(t, "", p["instructions"], t)
	c.instructionFile(t, "", filepath.Join(p["home"], "AGENTS.override.md"), "codex-override")
	if fsx.SamePath(p["skills"], filepath.Join(a.Store.Root, string(Skills))) {
		c.note("codex: reads skills from the store (%s) directly", a.Abbrev(p["skills"]))
	} else {
		c.skillsDir(t, "", p["skills"], OriginPersonal)
	}
	if !fsx.SamePath(p["legacy-skills"], p["skills"]) {
		c.skillsDir(t, "", p["legacy-skills"], OriginPersonal)
	}
	servers, err := readCodexServers(p["config"])
	if err != nil {
		c.note("codex: %v", err)
	}
	for _, name := range sortedKeys(servers) {
		c.add(&Item{Target: t, Scope: ScopeUser, Kind: Tools, Name: SanitizeName(name), Path: p["config"], Key: "mcp_servers." + name, Description: describeMCP(servers[name]), value: servers[name]})
	}
	for _, proj := range projects {
		c.instructionFile(t, proj, filepath.Join(proj, "AGENTS.md"), filepath.Base(proj))
		c.skillsDir(t, proj, filepath.Join(proj, ".agents", "skills"), OriginPersonal)
		c.skillsDir(t, proj, filepath.Join(proj, ".codex", "skills"), OriginPersonal)
	}
}

func (c *collector) accountRecords(target string) int {
	n := 0
	for _, ack := range c.a.State.Acks {
		if ack.Target != target {
			continue
		}
		r, err := ParseRef(ack.Asset)
		if err != nil {
			continue
		}
		c.add(&Item{Target: target, Scope: ScopeAccount, Project: ack.Project, Kind: r.Kind, Name: r.Name, Hash: ack.Hash, Note: "acknowledged " + ack.At.Format("2006-01-02")})
		n++
	}
	return n
}

func (c *collector) claudeChat() {
	a := c.a
	t := "claude-chat"
	p := a.TargetPaths(t)
	n := c.accountRecords(t)
	if c.opts.Refresh || contains(c.opts.Targets, t) {
		c.note("claude-chat: claude.ai has no API for listing account skills or connectors; showing %d acknowledged upload(s). Download skill ZIPs from claude.ai and import them with `agentctl import FILE.zip`.", n)
	}
	c.mcpServers(t, "", p["desktop-config"], "mcpServers", OriginPersonal, "")
	entries, _ := os.ReadDir(p["extensions"])
	for _, e := range entries {
		dir := filepath.Join(p["extensions"], e.Name())
		m, err := readJSONMap(filepath.Join(dir, "manifest.json"))
		if err != nil {
			continue
		}
		name := str(m["name"])
		if name == "" {
			name = e.Name()
		}
		it := c.add(&Item{Target: t, Scope: ScopeUser, Kind: Tools, Name: SanitizeName(name), Origin: OriginThirdParty, Path: dir, Version: str(m["version"]), Description: str(m["description"]), Note: "desktop extension"})
		it.importable = false
	}
}

func (c *collector) chatgpt() {
	n := c.accountRecords("chatgpt")
	if c.opts.Refresh || contains(c.opts.Targets, "chatgpt") {
		c.note("chatgpt: ChatGPT has no API for listing account instructions, skills, or connectors; showing %d acknowledged upload(s).", n)
	}
}

// classify compares each item with the store.
func (c *collector) classify() {
	a := c.a
	assets, _ := a.Store.List()
	byHash := map[string]string{}
	var mcps []*Asset
	for _, as := range assets {
		switch as.Kind {
		case Instructions:
			if h, err := fsx.Hash(as.MainFile()); err == nil {
				byHash[h] = as.Ref.String()
			}
		case Tools:
			if as.Tool != nil && as.Tool.MCP != nil {
				mcps = append(mcps, as)
			}
		}
		if h, err := as.Hash(); err == nil {
			byHash[h] = as.Ref.String()
		}
	}
	for _, it := range c.items {
		if it.Scope == ScopeAccount {
			it.Status = ItemRecorded
			it.StoreRef = string(it.Kind) + "/" + it.Name
			continue
		}
		if it.Path != "" && it.value == nil {
			if fsx.IsSymlink(it.Path) {
				if t, err := fsx.LinkTarget(it.Path); err == nil && fsx.Within(a.Store.Root, t) {
					it.Status = ItemManaged
					it.StoreRef = storeRefFor(a, t)
					continue
				}
			}
			if ds := a.State.ForDest(it.Path, ""); len(ds) > 0 {
				it.Status, it.StoreRef = ItemManaged, ds[0].Asset
				continue
			}
		}
		if it.value != nil {
			for _, as := range mcps {
				if mcpMatch(clientValue(as.Tool.MCP, false), it.value) {
					it.Status, it.StoreRef = ItemInStore, as.Ref.String()
					break
				}
			}
			if it.Status != "" {
				continue
			}
		} else if ref, ok := byHash[it.Hash]; ok {
			it.Status, it.StoreRef = ItemInStore, ref
			continue
		}
		if fsx.IsDir(a.Store.Path(Ref{it.Kind, it.Name})) {
			it.Status, it.StoreRef = ItemDiffers, string(it.Kind)+"/"+it.Name
			continue
		}
		it.Status = ItemUnmanaged
	}
}

func storeRefFor(a *App, p string) string {
	rel, err := filepath.Rel(a.Store.Root, p)
	if err != nil {
		return ""
	}
	parts := strings.Split(filepath.ToSlash(rel), "/")
	if len(parts) >= 2 {
		return parts[0] + "/" + parts[1]
	}
	return ""
}

// DupGroup is a name found in more than one place.
type DupGroup struct {
	Kind    Kind      `json:"kind"`
	Name    string    `json:"name"`
	Copies  []DupCopy `json:"copies"`
	Differs bool      `json:"differs"`
}

// DupCopy is one location of a duplicated name.
type DupCopy struct {
	Where string `json:"where"`
	Path  string `json:"path,omitempty"`
	Hash  string `json:"hash,omitempty"`
}

// Duplicates groups items (and store assets) that share a kind and name.
func (a *App) Duplicates(items []*Item) []DupGroup {
	groups := map[string]*DupGroup{}
	var order []string
	addCopy := func(k Kind, name string, cp DupCopy) {
		key := string(k) + "/" + name
		g := groups[key]
		if g == nil {
			g = &DupGroup{Kind: k, Name: name}
			groups[key] = g
			order = append(order, key)
		}
		g.Copies = append(g.Copies, cp)
	}
	for _, it := range items {
		if it.Status == ItemManaged || it.Scope == ScopeAccount {
			continue
		}
		where := it.Target + ":" + it.Scope
		if it.Plugin != "" {
			where += " (plugin " + it.Plugin + ")"
		}
		addCopy(it.Kind, it.Name, DupCopy{Where: where, Path: it.Path, Hash: it.Hash})
	}
	assets, _ := a.Store.List()
	for _, as := range assets {
		if g := groups[as.Ref.String()]; g != nil {
			h, _ := as.Hash()
			addCopy(as.Kind, as.Name, DupCopy{Where: "store", Path: as.Path, Hash: h})
		}
	}
	var out []DupGroup
	for _, key := range order {
		g := groups[key]
		if len(g.Copies) < 2 {
			continue
		}
		for _, cp := range g.Copies[1:] {
			if cp.Hash != g.Copies[0].Hash {
				g.Differs = true
			}
		}
		out = append(out, *g)
	}
	return out
}
