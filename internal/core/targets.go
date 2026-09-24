package core

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/cmtonkinson/agentctl/internal/fsx"
)

// TargetDef describes an agent target.
type TargetDef struct {
	Name     string
	Title    string
	Local    bool // deploys into local files
	Account  bool // has account-side assets managed through a web UI
	PathKeys []PathKey
}

// PathKey is a configurable target path.
type PathKey struct {
	Key  string
	Help string
}

// HasPathKey reports whether key is a known path for the target.
func (t *TargetDef) HasPathKey(key string) bool {
	for _, k := range t.PathKeys {
		if k.Key == key {
			return true
		}
	}
	return false
}

// PathKeyNames lists the target's path keys.
func (t *TargetDef) PathKeyNames() []string {
	var out []string
	for _, k := range t.PathKeys {
		out = append(out, k.Key)
	}
	return out
}

// Targets lists supported targets in display order.
var Targets = []*TargetDef{
	{
		Name: "chatgpt", Title: "ChatGPT account and desktop", Account: true,
		PathKeys: []PathKey{{"app", "ChatGPT desktop app data directory"}},
	},
	{
		Name: "claude-chat", Title: "Claude account and desktop chat", Account: true,
		PathKeys: []PathKey{
			{"app", "Claude Desktop app data directory"},
			{"desktop-config", "Claude Desktop MCP configuration (claude_desktop_config.json)"},
			{"extensions", "Claude Desktop extensions directory"},
		},
	},
	{
		Name: "codex", Title: "Codex CLI", Local: true,
		PathKeys: []PathKey{
			{"home", "Codex home ($CODEX_HOME)"},
			{"instructions", "Global AGENTS.md"},
			{"skills", "User skills directory"},
			{"legacy-skills", "Older user skills directory (inventory only)"},
			{"config", "Codex config.toml (MCP servers; read only)"},
		},
	},
	{
		Name: "claude-code", Title: "Claude Code", Local: true,
		PathKeys: []PathKey{
			{"home", "Claude Code config directory ($CLAUDE_CONFIG_DIR)"},
			{"instructions", "User CLAUDE.md"},
			{"skills", "User skills directory"},
			{"plugins", "Directory for skills-dir plugins (loaded as <name>@skills-dir)"},
			{"plugin-data", "Installed plugin data (installed_plugins.json, cache)"},
			{"settings", "Claude Code state file with user MCP servers (.claude.json; read only)"},
		},
	},
}

// TargetByName returns a target definition or nil.
func TargetByName(name string) *TargetDef {
	for _, t := range Targets {
		if t.Name == name {
			return t
		}
	}
	return nil
}

// TargetNames lists target names.
func TargetNames() []string {
	var out []string
	for _, t := range Targets {
		out = append(out, t.Name)
	}
	return out
}

func (a *App) env(key, fallback string) string {
	if v := a.Getenv(key); v != "" {
		return a.AbsPath(v)
	}
	return fallback
}

func (a *App) claudeDesktopDir() string {
	switch a.GOOS {
	case "darwin":
		return filepath.Join(a.Home, "Library", "Application Support", "Claude")
	case "windows":
		return filepath.Join(a.env("APPDATA", filepath.Join(a.Home, "AppData", "Roaming")), "Claude")
	default:
		return filepath.Join(a.env("XDG_CONFIG_HOME", filepath.Join(a.Home, ".config")), "Claude")
	}
}

func (a *App) chatgptDir() string {
	switch a.GOOS {
	case "darwin":
		return filepath.Join(a.Home, "Library", "Application Support", "com.openai.chat")
	case "windows":
		base := a.env("LOCALAPPDATA", filepath.Join(a.Home, "AppData", "Local"))
		if m, _ := filepath.Glob(filepath.Join(base, "Packages", "OpenAI.ChatGPT-Desktop_*")); len(m) > 0 {
			return m[0]
		}
		return filepath.Join(base, "Packages", "OpenAI.ChatGPT-Desktop")
	default:
		return ""
	}
}

func (a *App) defaultPaths(target string) map[string]string {
	h := a.Home
	switch target {
	case "claude-code":
		home := a.env("CLAUDE_CONFIG_DIR", filepath.Join(h, ".claude"))
		settings := filepath.Join(h, ".claude.json")
		if a.Getenv("CLAUDE_CONFIG_DIR") != "" {
			settings = filepath.Join(home, ".claude.json")
		}
		return map[string]string{
			"home":         home,
			"instructions": filepath.Join(home, "CLAUDE.md"),
			"skills":       filepath.Join(home, "skills"),
			"plugins":      filepath.Join(home, "skills"),
			"plugin-data":  filepath.Join(home, "plugins"),
			"settings":     settings,
		}
	case "codex":
		home := a.env("CODEX_HOME", filepath.Join(h, ".codex"))
		return map[string]string{
			"home":          home,
			"instructions":  filepath.Join(home, "AGENTS.md"),
			"skills":        filepath.Join(h, ".agents", "skills"),
			"legacy-skills": filepath.Join(home, "skills"),
			"config":        filepath.Join(home, "config.toml"),
		}
	case "claude-chat":
		app := a.claudeDesktopDir()
		return map[string]string{
			"app":            app,
			"desktop-config": filepath.Join(app, "claude_desktop_config.json"),
			"extensions":     filepath.Join(app, "Claude Extensions"),
		}
	case "chatgpt":
		return map[string]string{"app": a.chatgptDir()}
	}
	return map[string]string{}
}

// TargetPaths returns a target's user-scope paths with config overrides.
func (a *App) TargetPaths(target string) map[string]string {
	p := a.defaultPaths(target)
	if t := a.Config.target(target, false); t != nil {
		for k, v := range t.Paths {
			p[k] = a.AbsPath(v)
		}
	}
	return p
}

// ProjectPaths returns a local target's project-scope paths.
func ProjectPaths(target, project string) map[string]string {
	switch target {
	case "claude-code":
		return map[string]string{
			"instructions": filepath.Join(project, "CLAUDE.md"),
			"skills":       filepath.Join(project, ".claude", "skills"),
			"mcp":          filepath.Join(project, ".mcp.json"),
		}
	case "codex":
		return map[string]string{
			"instructions": filepath.Join(project, "AGENTS.md"),
			"skills":       filepath.Join(project, ".agents", "skills"),
		}
	}
	return nil
}

// Detection reports whether a target's local installation was found.
type Detection struct {
	Found  bool   `json:"found"`
	Detail string `json:"detail"`
}

// Detect looks for a target's local installation.
func (a *App) Detect(target string) Detection {
	p := a.TargetPaths(target)
	switch target {
	case "claude-code", "codex":
		bin := map[string]string{"claude-code": "claude", "codex": "codex"}[target]
		var parts []string
		if fsx.IsDir(p["home"]) {
			parts = append(parts, a.Abbrev(p["home"]))
		}
		if path, err := a.LookPath(bin); err == nil {
			parts = append(parts, path)
		}
		if len(parts) == 0 {
			return Detection{false, fmt.Sprintf("no %s and no %s on PATH", a.Abbrev(p["home"]), bin)}
		}
		return Detection{true, joinComma(parts)}
	case "claude-chat", "chatgpt":
		if p["app"] != "" && fsx.IsDir(p["app"]) {
			return Detection{true, "desktop app data at " + a.Abbrev(p["app"])}
		}
		return Detection{false, "desktop app not found; account assets are managed through the web UI"}
	}
	return Detection{}
}

// Support describes how a target handles an asset.
type Support struct {
	Target  string   `json:"target"`
	Level   string   `json:"support"` // native | package | manual | unsupported
	Methods []string `json:"methods,omitempty"`
	Scopes  []string `json:"scopes,omitempty"`
	Notes   []string `json:"notes,omitempty"`
}

// Support levels.
const (
	SupportNative      = "native"
	SupportPackage     = "package"
	SupportManual      = "manual"
	SupportUnsupported = "unsupported"
)

func unsupported(target, why string) Support {
	return Support{Target: target, Level: SupportUnsupported, Notes: []string{why}}
}

// Support reports how target can take asset.
func (a *App) Support(target string, as *Asset) Support {
	user := []string{ScopeUser}
	both := []string{ScopeUser, ScopeProject}
	account := []string{ScopeAccount}
	switch target {
	case "claude-code":
		switch as.Kind {
		case Instructions:
			return Support{target, SupportNative, []string{"link", "copy"}, both, []string{
				"Deployed as CLAUDE.md; several instruction assets, or a targets/claude-code.md adapter, are composed into one generated file."}}
		case Skills:
			return Support{target, SupportNative, []string{"link", "copy"}, both, nil}
		case Plugins:
			if !fsx.Exists(filepath.Join(as.Path, ".claude-plugin", "plugin.json")) {
				return unsupported(target, "Claude Code needs .claude-plugin/plugin.json for a skills-directory plugin; add that adapter to the canonical plugin")
			}
			return Support{target, SupportNative, []string{"link", "copy"}, user, []string{
				"Placed in the skills directory, where Claude Code loads it as " + as.Name + "@skills-dir."}}
		case Tools:
			return a.toolSupport(target, as)
		}
	case "codex":
		switch as.Kind {
		case Instructions:
			return Support{target, SupportNative, []string{"link", "copy"}, both, []string{
				"Deployed as AGENTS.md; several instruction assets, or a targets/codex.md adapter, are composed into one generated file."}}
		case Skills:
			s := Support{target, SupportNative, []string{"link", "copy"}, both, nil}
			if fsx.SamePath(filepath.Dir(a.TargetPaths(target)["skills"]), a.Store.Root) {
				s.Notes = append(s.Notes, "Codex reads skills directly from the store; every stored skill is visible to Codex whether assigned or not.")
			}
			return s
		case Plugins:
			return a.openAIPluginSupport(target, as)
		case Tools:
			return a.toolSupport(target, as)
		}
	case "claude-chat":
		switch as.Kind {
		case Instructions:
			return Support{target, SupportPackage, []string{"package"}, account, []string{
				"Written to a text file to paste into claude.ai profile preferences; confirm with `target acknowledge`."}}
		case Skills:
			return Support{target, SupportPackage, []string{"package"}, account, []string{
				"Packaged as a ZIP for upload in claude.ai Settings → Capabilities → Skills; confirm with `target acknowledge`."}}
		case Plugins:
			if !fsx.Exists(filepath.Join(as.Path, ".claude-plugin", "plugin.json")) {
				return unsupported(target, "Claude Desktop plugin upload needs a Claude plugin manifest; add .claude-plugin/plugin.json to the canonical plugin")
			}
			return Support{target, SupportPackage, []string{"package"}, account, []string{
				"Packaged as a ZIP for upload in the Claude desktop app; confirm with `target acknowledge`."}}
		case Tools:
			return a.toolSupport(target, as)
		}
	case "chatgpt":
		switch as.Kind {
		case Instructions:
			return Support{target, SupportPackage, []string{"package"}, account, []string{
				"Written to a text file to paste into ChatGPT custom instructions (1,500 characters per field)."}}
		case Skills:
			return Support{target, SupportPackage, []string{"package"}, account, []string{
				"Packaged as a ZIP for a supported ChatGPT workspace Skills flow; for ChatGPT Chat and Work, bundle it in a plugin. Confirm only after installation."}}
		case Plugins:
			return a.openAIPluginSupport(target, as)
		case Tools:
			return a.toolSupport(target, as)
		}
	}
	return unsupported(target, "unknown target or kind")
}

// openAIPluginSupport checks for a manifest Codex and ChatGPT can load.
func (a *App) openAIPluginSupport(target string, as *Asset) Support {
	for _, name := range []string{"plugin.json", filepath.Join(".codex-plugin", "plugin.json")} {
		if info, err := os.Stat(filepath.Join(as.Path, name)); err == nil && !info.IsDir() {
			return Support{target, SupportManual, []string{MethodManual}, []string{ScopeUser}, []string{
				"Add the plugin to the personal marketplace, then install it in the ChatGPT Plugins Directory. Record the completed installation with target acknowledge.",
			}}
		}
	}
	return unsupported(target, "this plugin only has a Claude manifest; add a portable root plugin.json or .codex-plugin/plugin.json before assigning it to Codex or ChatGPT")
}

func (a *App) toolSupport(target string, as *Asset) Support {
	spec := as.Tool
	if spec == nil || (spec.MCP == nil && len(spec.Bin) == 0) {
		return unsupported(target, "the tool has no MCP definition or executables to deploy")
	}
	user := []string{ScopeUser}
	if spec.MCP != nil {
		m := spec.MCP
		switch target {
		case "claude-code":
			return Support{target, SupportManual, []string{"manual"}, []string{ScopeUser, ScopeProject}, []string{
				"User scope: agentctl prints a `claude mcp add-json` command and verifies ~/.claude.json afterwards.",
				"Project scope: merged into the project's .mcp.json."}}
		case "codex":
			return Support{target, SupportManual, []string{"manual"}, user, []string{
				"agentctl prints a `codex mcp add` command and verifies config.toml afterwards."}}
		case "claude-chat":
			if m.Remote() {
				return Support{target, SupportManual, []string{"manual"}, []string{ScopeAccount}, []string{
					"Remote servers are added as custom connectors in claude.ai settings; confirm with `target acknowledge`."}}
			}
			return Support{target, SupportNative, []string{"config"}, user, []string{
				"Merged into claude_desktop_config.json; restart Claude Desktop to load it."}}
		case "chatgpt":
			if !m.Remote() {
				return unsupported(target, "ChatGPT connects only to remote (HTTP) MCP servers.")
			}
			return Support{target, SupportManual, []string{"manual"}, []string{ScopeAccount}, []string{
				"Added as a connector in ChatGPT settings (developer mode); confirm with `target acknowledge`."}}
		}
	}
	if TargetByName(target).Local {
		return Support{target, SupportNative, []string{"link", "copy"}, user, []string{
			"Executables are placed in " + a.Abbrev(a.BinDir()) + ", shared by local targets."}}
	}
	return unsupported(target, "script tools cannot run in "+target+".")
}

// Assigned returns (target, project) pairs where ref is assigned.
func (a *App) Assigned(ref string) []Scoped {
	var out []Scoped
	for _, t := range Targets {
		if contains(a.Config.Assignments(t.Name, ""), ref) {
			out = append(out, Scoped{t.Name, ""})
		}
	}
	for _, p := range a.Config.ProjectPaths() {
		for _, t := range Targets {
			if contains(a.Config.Assignments(t.Name, p), ref) {
				out = append(out, Scoped{t.Name, p})
			}
		}
	}
	return out
}

// Scoped is a target in user scope (Project "") or a project.
type Scoped struct {
	Target  string `json:"target"`
	Project string `json:"project,omitempty"`
}

func (s Scoped) String() string {
	if s.Project == "" {
		return s.Target
	}
	return s.Target + "@" + s.Project
}

func joinComma(s []string) string {
	sort.Strings(s)
	out := ""
	for i, x := range s {
		if i > 0 {
			out += ", "
		}
		out += x
	}
	return out
}
