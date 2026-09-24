package core

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestClaudeAccountSnapshot verifies the two Claude Desktop cache layouts and
// keeps account-managed copies out of the import path.
func TestClaudeAccountSnapshot(t *testing.T) {
	e := newEnv(t)
	base := ".config/Claude/local-agent-mode-sessions"
	old := filepath.Join(base, "skills-plugin", "old-a", "old-b")
	e.write(filepath.Join(old, "manifest.json"), `{"lastUpdated":1,"skills":[{"name":"old-skill","creatorType":"user","enabled":true}]}`)
	e.skill(filepath.Join(old, "skills/old-skill"), "old-skill", "old")
	current := filepath.Join(base, "skills-plugin", "session-a", "account-b")
	e.write(filepath.Join(current, "manifest.json"), `{"lastUpdated":2,"skills":[{"name":"mine","creatorType":"user","enabled":true},{"name":"built-in","creatorType":"anthropic","enabled":true},{"name":"disabled","creatorType":"user","enabled":false}]}`)
	e.skill(filepath.Join(current, "skills/mine"), "mine", "mine")
	e.skill(filepath.Join(current, "skills/built-in"), "built-in", "built-in")
	e.skill(filepath.Join(current, "skills/disabled"), "disabled", "disabled")
	rpm := filepath.Join(base, "account-b", "session-a", "rpm", "plugin_123")
	e.write(filepath.Join(rpm, ".claude-plugin/plugin.json"), `{"name":"engineering","version":"1.0"}`)
	e.skill(filepath.Join(rpm, "skills/code-review"), "code-review", "review")

	inv, err := e.a.Inventory(InventoryOptions{Selection: Selection{Targets: []string{"claude-chat"}}})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]*Item{}
	for _, it := range inv.Items {
		got[string(it.Kind)+"/"+it.Name] = it
	}
	if len(got) != 4 || got["skills/old-skill"] != nil || got["skills/disabled"] != nil {
		t.Fatalf("unexpected account snapshot: %+v", got)
	}
	for _, key := range []string{"skills/mine", "skills/built-in", "skills/code-review", "plugins/engineering"} {
		it := got[key]
		if it == nil || it.Scope != ScopeAccount || it.Status != ItemSnapshot || !it.Cached || it.importable {
			t.Fatalf("%s: unexpected item: %+v", key, it)
		}
	}
	if got["skills/mine"].Origin != OriginPersonal || got["skills/built-in"].Origin != OriginSystem || got["skills/code-review"].Plugin != "engineering" {
		t.Fatal("snapshot origin or plugin relationship was lost")
	}
	if _, err := e.a.Import(got["skills/mine"].ID, ImportOptions{}); err == nil || !strings.Contains(err.Error(), "not importable") {
		t.Fatalf("account snapshot import should be refused: %v", err)
	}
}

// TestMrsBPOIsExcludedByDefault protects the user-owned source boundary for
// both inventory and explicit imports.
func TestMrsBPOIsExcludedByDefault(t *testing.T) {
	e := newEnv(t)
	path := e.skill("mrs-bpo/.claude/skills/private", "private", "private")
	e.write(".claude.json", `{"projects":{"`+filepath.Join(e.home, "mrs-bpo")+`":{}}}`)
	inv, err := e.a.Inventory(InventoryOptions{Selection: Selection{Targets: []string{"claude-code"}, Scope: ScopeProject}})
	if err != nil {
		t.Fatal(err)
	}
	if len(inv.Items) != 0 {
		t.Fatalf("excluded project appeared in inventory: %+v", inv.Items)
	}
	if _, err := e.a.Import(path, ImportOptions{}); err == nil || !strings.Contains(err.Error(), "excluded") {
		t.Fatalf("explicit import from excluded project should fail: %v", err)
	}
}

// TestManifestlessInstalledPluginUsesInstallID keeps version folders from
// appearing as multiple unnamed plugins in inventory.
func TestManifestlessInstalledPluginUsesInstallID(t *testing.T) {
	e := newEnv(t)
	dir := filepath.Join(e.home, ".claude", "plugins", "cache", "official", "pyright-lsp", "1.0.0")
	e.write(".claude/plugins/cache/official/pyright-lsp/1.0.0/README.md", "LSP plugin")
	e.write(".claude/plugins/installed_plugins.json", `{"plugins":{"pyright-lsp@official":[{"installPath":"`+dir+`"}]}}`)
	inv, err := e.a.Inventory(InventoryOptions{Selection: Selection{Targets: []string{"claude-code"}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(inv.Items) != 1 || inv.Items[0].Name != "pyright-lsp" || inv.Items[0].importable {
		t.Fatalf("manifestless installed plugin was misidentified: %+v", inv.Items)
	}
}

// TestCodexCacheInventory lists plugin content as read-only cache evidence.
func TestCodexCacheInventory(t *testing.T) {
	e := newEnv(t)
	base := ".codex/plugins/cache/marketplace/my-plugin/1.0.0"
	e.write(filepath.Join(base, ".codex-plugin/plugin.json"), `{"name":"my-plugin","version":"1.0.0"}`)
	e.skill(filepath.Join(base, "skills/hello"), "hello", "hello")
	inv, err := e.a.Inventory(InventoryOptions{Selection: Selection{Targets: []string{"codex"}}, IncludeCache: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(inv.Items) != 2 || inv.Items[0].importable || inv.Items[1].importable {
		t.Fatalf("Codex cache was not listed read-only: %+v", inv.Items)
	}
}
