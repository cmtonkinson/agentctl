package core

import (
	"bufio"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/cmtonkinson/agentctl/internal/fsx"
)

// Deps lists what an asset needs.
type Deps struct {
	Assets   []DepRef     `json:"assets,omitempty"`
	Commands []CommandDep `json:"commands,omitempty"`
	Notes    []string     `json:"notes,omitempty"`
}

// CommandDep is a runtime or command an asset uses.
type CommandDep struct {
	Name  string   `json:"name"`
	Found bool     `json:"found"`
	Path  string   `json:"path,omitempty"`
	From  []string `json:"from"` // files or fields that imply it
}

var extRuntime = map[string]string{
	".py": "python3", ".js": "node", ".mjs": "node", ".cjs": "node",
	".rb": "ruby", ".pl": "perl", ".ps1": "pwsh", ".php": "php", ".lua": "lua",
}

// shells every system has; not worth reporting.
var baseline = map[string]bool{"sh": true, "env": true}

// Dependencies reports required assets and inferred commands.
func (a *App) Dependencies(as *Asset) *Deps {
	d := &Deps{}
	if as.Meta != nil && as.Meta.Requires != nil {
		for _, r := range as.Meta.Requires.Assets {
			pr, err := ParseRef(r)
			d.Assets = append(d.Assets, DepRef{Ref: r, InStore: err == nil && fsx.IsDir(a.Store.Path(pr))})
		}
	}
	cmds := map[string][]string{}
	add := func(name, from string) {
		name = filepath.Base(strings.TrimSpace(name))
		if name == "" || name == "." || baseline[name] || strings.Contains(name, "$") {
			return
		}
		if !contains(cmds[name], from) {
			cmds[name] = append(cmds[name], from)
		}
	}
	if as.Meta != nil && as.Meta.Requires != nil {
		for _, c := range as.Meta.Requires.Commands {
			add(c, "meta")
		}
	}
	if as.Tool != nil {
		for _, r := range as.Tool.Requires {
			add(r, "tool.json requires")
		}
		if m := as.Tool.MCP; m != nil && m.Command != "" {
			add(m.Command, "tool.json mcp.command")
		}
	}
	if as.Kind == Plugins {
		if m, err := readJSONMap(filepath.Join(as.Path, ".mcp.json")); err == nil {
			servers, _ := m["mcpServers"].(map[string]any)
			for _, n := range sortedKeys(servers) {
				if s, ok := servers[n].(map[string]any); ok {
					add(str(s["command"]), ".mcp.json "+n)
				}
			}
		}
	}
	if as.Frontmatter != nil {
		if c := str(as.Frontmatter["compatibility"]); c != "" {
			d.Notes = append(d.Notes, "compatibility: "+c)
		}
		if t := as.Frontmatter["allowed-tools"]; t != nil {
			d.Notes = append(d.Notes, "allowed-tools: "+str(t))
		}
	}
	entries, _ := fsx.Walk(as.Path)
	if len(entries) > 2000 {
		entries = entries[:2000]
	}
	for _, e := range entries {
		if e.Link != "" || strings.HasSuffix(strings.ToLower(e.Rel), ".md") || e.Size > 4<<20 {
			continue
		}
		if interp := shebang(e.Abs); interp != "" {
			add(interp, e.Rel)
			continue
		}
		if rt, ok := extRuntime[strings.ToLower(filepath.Ext(e.Rel))]; ok {
			add(rt, e.Rel)
		}
	}
	for _, name := range sortedKeys(cmds) {
		cd := CommandDep{Name: name, From: cmds[name]}
		if p, err := a.LookPath(name); err == nil {
			cd.Found, cd.Path = true, p
		}
		d.Commands = append(d.Commands, cd)
	}
	return d
}

// shebang returns the interpreter named on a script's #! line.
func shebang(p string) string {
	f, err := os.Open(p)
	if err != nil {
		return ""
	}
	defer f.Close()
	line, _ := bufio.NewReader(f).ReadString('\n')
	if !strings.HasPrefix(line, "#!") {
		return ""
	}
	fields := strings.Fields(strings.TrimPrefix(line, "#!"))
	if len(fields) == 0 {
		return ""
	}
	if filepath.Base(fields[0]) != "env" {
		return filepath.Base(fields[0])
	}
	for _, f := range fields[1:] {
		if strings.HasPrefix(f, "-") || strings.Contains(f, "=") {
			continue
		}
		return f
	}
	return ""
}

// FileInfo describes a packaged file.
type FileInfo struct {
	Path       string `json:"path"`
	Size       int64  `json:"size"`
	Executable bool   `json:"executable,omitempty"`
	Link       string `json:"link,omitempty"`
}

// Files lists an asset's files.
func (a *App) Files(as *Asset) []FileInfo {
	entries, _ := fsx.Walk(as.Path)
	var out []FileInfo
	for _, e := range entries {
		out = append(out, FileInfo{Path: e.Rel, Size: e.Size, Executable: e.Link == "" && e.Mode&0o111 != 0, Link: e.Link})
	}
	return out
}

// Compat is a target's support for an asset plus its current state.
type Compat struct {
	Support
	Enabled bool   `json:"enabled"`
	State   string `json:"state,omitempty"`
	Detail  string `json:"detail,omitempty"`
}

// Compatibility reports each target's support for an asset.
func (a *App) Compatibility(as *Asset) []Compat {
	var out []Compat
	for _, t := range Targets {
		c := Compat{Support: a.Support(t.Name, as), Enabled: a.Config.Enabled(t.Name)}
		if contains(a.Config.Assignments(t.Name, ""), as.Ref.String()) {
			for _, u := range a.Plan(t.Name, "", as, "") {
				e := a.Evaluate(u)
				if c.State == "" || severity(e.State) > severity(c.State) {
					c.State, c.Detail = e.State, e.Detail
				}
			}
		}
		out = append(out, c)
	}
	return out
}

func severity(s string) int {
	switch s {
	case StateOK:
		return 0
	case StateManual:
		return 1
	case StateMissing, StateOutdated:
		return 2
	case StateBlocked:
		return 3
	case StateConflict:
		return 4
	}
	return 0
}

// Provenance summarizes where an asset came from and whether it changed.
type Provenance struct {
	*Meta
	LocalEdits  bool   `json:"local_edits"`
	CurrentHash string `json:"current_hash"`
	SourceLabel string `json:"source_label,omitempty"`
}

// ProvenanceOf returns an asset's provenance.
func (a *App) ProvenanceOf(as *Asset) *Provenance {
	h, _ := as.Hash()
	p := &Provenance{Meta: as.Meta, CurrentHash: h}
	if p.Meta == nil {
		p.Meta = &Meta{Origin: OriginPersonal}
	}
	if p.Meta.ImportedHash != "" && h != p.Meta.ImportedHash {
		p.LocalEdits = true
	}
	if p.Meta.Source != nil {
		p.SourceLabel = describeSource(a, p.Meta.Source)
	}
	return p
}

// Check is one doctor finding.
type Check struct {
	Status  string `json:"status"` // ok | warn | error
	Area    string `json:"area"`
	Message string `json:"message"`
	Hint    string `json:"hint,omitempty"`
}

// Doctor checks the store, configuration, targets, deployments, and
// dependencies.
func (a *App) Doctor() []Check {
	var out []Check
	ok := func(area, msg string) { out = append(out, Check{"ok", area, msg, ""}) }
	warn := func(area, msg, hint string) { out = append(out, Check{"warn", area, msg, hint}) }
	fail := func(area, msg, hint string) { out = append(out, Check{"error", area, msg, hint}) }

	// Store.
	if !a.Store.Exists() {
		warn("store", "store "+a.Abbrev(a.Store.Root)+" does not exist yet", "agentctl import creates it")
	} else {
		probe := filepath.Join(a.Store.Root, ".agentctl-write-test")
		if err := os.WriteFile(probe, nil, 0o644); err != nil {
			fail("store", "store is not writable: "+err.Error(), "")
		} else {
			os.Remove(probe)
			ok("store", "store at "+a.Abbrev(a.Store.Root))
		}
	}
	assets, err := a.Store.List()
	if err != nil {
		fail("store", "cannot read store: "+err.Error(), "")
	}
	byRef := map[string]*Asset{}
	for _, as := range assets {
		byRef[as.Ref.String()] = as
		for _, p := range as.Problems {
			warn("asset", as.Ref.String()+": "+p, "agentctl edit "+as.Ref.String())
		}
	}
	if len(assets) > 0 {
		ok("store", pluralize(len(assets), "asset")+" in the store")
	}
	for _, r := range a.Store.MetaFiles() {
		if byRef[r.String()] == nil {
			warn("store", "meta for "+r.String()+" has no asset", "delete "+a.Abbrev(a.Store.metaPath(r)))
		}
	}
	for _, as := range assets {
		if as.Meta == nil || as.Meta.Requires == nil {
			continue
		}
		for _, req := range as.Meta.Requires.Assets {
			if byRef[req] == nil {
				fail("dependencies", as.Ref.String()+" requires "+req+", which is not in the store", "import "+req)
			}
		}
	}

	// Configuration.
	for name := range a.Config.Targets {
		if TargetByName(name) == nil {
			warn("config", "unknown target "+name+" in "+ConfigFile, "")
		}
	}
	scopes := []string{""}
	scopes = append(scopes, a.Config.ProjectPaths()...)
	for _, p := range scopes {
		if p != "" && !fsx.IsDir(a.AbsPath(p)) {
			warn("config", "project "+p+" does not exist", "")
		}
		for _, t := range Targets {
			for _, ref := range a.Config.Assignments(t.Name, p) {
				if byRef[ref] == nil {
					fail("config", Scoped{t.Name, p}.String()+" is assigned "+ref+", which is not in the store", "agentctl target unassign "+t.Name+" "+ref)
				}
			}
		}
	}
	for _, e := range a.Config.Exclude {
		if !fsx.Exists(a.AbsPath(e)) {
			warn("config", "excluded path "+e+" does not exist", "")
		}
	}

	// Targets.
	for _, t := range Targets {
		if !a.Config.Enabled(t.Name) {
			ok("target", t.Name+": disabled")
			continue
		}
		d := a.Detect(t.Name)
		if d.Found {
			ok("target", t.Name+": "+d.Detail)
		} else if t.Local {
			warn("target", t.Name+": "+d.Detail, "install it, or: agentctl target disable "+t.Name)
		} else {
			ok("target", t.Name+": "+d.Detail)
		}
		if t.Name == "codex" {
			if _, err := readCodexServers(a.TargetPaths("codex")["config"]); err != nil {
				warn("target", "codex: "+err.Error(), "")
			}
			if fsx.SamePath(filepath.Dir(a.TargetPaths("codex")["skills"]), a.Store.Root) {
				ok("target", "codex: reads skills directly from the store; unassigned skills are visible to Codex too")
			}
		}
	}

	// Deployments.
	for _, dep := range a.State.Deployments {
		where := Scoped{dep.Target, dep.Project}.String()
		if byRef[dep.Asset] == nil {
			warn("deploy", where+": deployment record for "+dep.Asset+", which is no longer in the store", "")
			continue
		}
		if !contains(a.Config.Assignments(dep.Target, dep.Project), dep.Asset) {
			warn("deploy", where+": "+dep.Asset+" is deployed but not assigned", "agentctl remove "+dep.Asset+" --target "+dep.Target)
		}
		switch dep.Method {
		case MethodLink:
			if !fsx.Exists(dep.Dest) {
				warn("deploy", where+": "+a.Abbrev(dep.Dest)+" was deleted outside agentctl", "agentctl deploy "+dep.Asset+" --target "+dep.Target)
			} else if _, err := os.Stat(dep.Dest); err != nil {
				fail("deploy", where+": broken link "+a.Abbrev(dep.Dest), "agentctl deploy "+dep.Asset+" --target "+dep.Target)
			}
		case MethodCopy, MethodGenerate:
			if !fsx.Exists(dep.Dest) {
				warn("deploy", where+": "+a.Abbrev(dep.Dest)+" was deleted outside agentctl", "agentctl deploy "+dep.Asset+" --target "+dep.Target)
			}
		}
	}
	for _, t := range Targets {
		if !t.Local {
			continue
		}
		for _, key := range []string{"skills", "plugins"} {
			dir := a.TargetPaths(t.Name)[key]
			entries, _ := os.ReadDir(dir)
			for _, e := range entries {
				p := filepath.Join(dir, e.Name())
				if fsx.IsSymlink(p) && linksIntoStore(a, p) {
					if _, err := os.Stat(p); err != nil {
						fail("deploy", t.Name+": broken link "+a.Abbrev(p)+" into the store", "remove it, or deploy the asset again")
					}
				}
			}
		}
	}

	// Dependencies of assigned assets on local targets.
	missing := map[string][]string{}
	needBin := false
	gitSources := false
	for _, as := range assets {
		if as.Meta != nil && as.Meta.Source != nil && as.Meta.Source.Type == "git" {
			gitSources = true
		}
		local := false
		for _, s := range a.Assigned(as.Ref.String()) {
			if TargetByName(s.Target).Local {
				local = true
			}
		}
		if !local {
			continue
		}
		if as.Kind == Tools && as.Tool != nil && len(as.Tool.Bin) > 0 {
			needBin = true
		}
		for _, c := range a.Dependencies(as).Commands {
			if !c.Found {
				missing[c.Name] = append(missing[c.Name], as.Ref.String())
			}
		}
	}
	for _, name := range sortedKeys(missing) {
		warn("dependencies", name+" not found on PATH (needed by "+strings.Join(missing[name], ", ")+")", "")
	}
	if gitSources {
		if _, err := a.LookPath("git"); err != nil {
			warn("dependencies", "git not found; updates from repositories need it", "")
		}
	}
	if needBin && !onPath(a, a.BinDir()) {
		warn("dependencies", a.Abbrev(a.BinDir())+" is not on PATH; linked script tools will not be found", "add it to PATH, or: agentctl config set bin-dir DIR")
	}
	sort.SliceStable(out, func(i, j int) bool { return areaOrder(out[i].Area) < areaOrder(out[j].Area) })
	return out
}

func areaOrder(a string) int {
	for i, x := range []string{"store", "asset", "config", "target", "deploy", "dependencies"} {
		if x == a {
			return i
		}
	}
	return 99
}

func onPath(a *App, dir string) bool {
	for _, p := range filepath.SplitList(a.Getenv("PATH")) {
		if p != "" && fsx.SamePath(p, dir) {
			return true
		}
	}
	return false
}

func pluralize(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return strconv.Itoa(n) + " " + word + "s"
}
