package core

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cmtonkinson/agentctl/internal/fsx"
)

type env struct {
	t    *testing.T
	home string
	a    *App
}

func newEnv(t *testing.T) *env {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	for _, k := range []string{"CLAUDE_CONFIG_DIR", "CODEX_HOME", "XDG_CONFIG_HOME", "AGENTCTL_STORE"} {
		t.Setenv(k, "")
	}
	e := &env{t: t, home: home}
	e.reopen()
	return e
}

// reopen reloads config and state from disk, as a new invocation would.
func (e *env) reopen() {
	a, err := Open("~/.agents")
	if err != nil {
		e.t.Fatal(err)
	}
	a.GOOS = "linux"
	a.LookPath = func(string) (string, error) { return "", errors.New("not found") }
	a.Run = func(context.Context, string, string, ...string) ([]byte, error) {
		return nil, errors.New("commands disabled in tests")
	}
	e.a = a
}

func (e *env) write(rel, content string) string {
	e.t.Helper()
	p := filepath.Join(e.home, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		e.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		e.t.Fatal(err)
	}
	return p
}

func (e *env) skill(rel, name, body string) string {
	return filepath.Dir(e.write(filepath.Join(rel, "SKILL.md"), "---\nname: "+name+"\ndescription: "+name+" skill\n---\n"+body+"\n"))
}

func (e *env) status(target string) map[string]*Eval {
	e.t.Helper()
	res, err := e.a.Status(DeployOptions{Selection: Selection{Targets: []string{target}}})
	if err != nil {
		e.t.Fatal(err)
	}
	out := map[string]*Eval{}
	for _, ev := range res.Evals {
		out[ev.Asset] = ev
	}
	return out
}

func (e *env) deploy(opts DeployOptions) *DeployResult {
	e.t.Helper()
	res, err := e.a.Deploy(opts)
	if err != nil {
		e.t.Fatal(err)
	}
	return res
}

func TestImportPreservesOriginalAndRecordsProvenance(t *testing.T) {
	e := newEnv(t)
	orig := e.skill(".claude/skills/prune", "prune", "Cut words.")
	e.write(".claude/skills/prune/LICENSE", "MIT License\n")
	res, err := e.a.Import(orig, ImportOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 || res[0].Action != "imported" || res[0].Asset != "skills/prune" || res[0].License != "MIT" {
		t.Fatalf("unexpected result: %+v", res[0])
	}
	if !fsx.Exists(filepath.Join(orig, "SKILL.md")) {
		t.Fatal("original was moved")
	}
	as, err := e.a.Store.Find("prune", "")
	if err != nil {
		t.Fatal(err)
	}
	if as.Meta == nil || as.Meta.Source.Type != "path" || as.Meta.Source.Location != orig || len(as.Meta.Files) != 2 {
		t.Fatalf("meta not recorded: %+v", as.Meta)
	}
	// Re-importing identical content is a no-op; different content conflicts.
	res, err = e.a.Import(orig, ImportOptions{})
	if err != nil || res[0].Action != "identical" {
		t.Fatalf("expected identical, got %+v %v", res, err)
	}
	other := e.skill("elsewhere/prune", "prune", "Different.")
	if _, err := e.a.Import(other, ImportOptions{}); err == nil || !strings.Contains(err.Error(), "already has different") {
		t.Fatalf("expected conflict, got %v", err)
	}
	res, err = e.a.Import(other, ImportOptions{OnConflict: "rename"})
	if err != nil || res[0].Asset != "skills/prune-2" {
		t.Fatalf("expected rename, got %+v %v", res, err)
	}
	res, err = e.a.Import(other, ImportOptions{OnConflict: "skip"})
	if err != nil || res[0].Action != "skipped" {
		t.Fatalf("expected skipped, got %+v %v", res, err)
	}
}

func TestDeployLinksAndNeverOverwritesUnmanaged(t *testing.T) {
	e := newEnv(t)
	orig := e.skill(".claude/skills/prune", "prune", "Cut words.")
	if _, err := e.a.Import(orig, ImportOptions{}); err != nil {
		t.Fatal(err)
	}
	e.a.Config.Assign("claude-code", "", "skills/prune")

	// The original is still in place and identical: a conflict, not an overwrite.
	st := e.status("claude-code")["skills/prune"]
	if st.State != StateConflict || !st.adoptable {
		t.Fatalf("expected adoptable conflict, got %s (%s)", st.State, st.Detail)
	}
	res := e.deploy(DeployOptions{Assets: []string{"prune"}, Selection: Selection{Targets: []string{"claude-code"}}})
	if !res.Failed() || !fsx.IsDir(orig) || fsx.IsSymlink(orig) {
		t.Fatal("deploy overwrote an unmanaged directory")
	}
	// --adopt replaces the identical copy with a link and keeps the original in trash.
	e.deploy(DeployOptions{Assets: []string{"prune"}, Adopt: true, Selection: Selection{Targets: []string{"claude-code"}}})
	if !fsx.IsSymlink(orig) {
		t.Fatal("expected a symlink after adopt")
	}
	if st := e.status("claude-code")["skills/prune"]; st.State != StateOK {
		t.Fatalf("expected ok, got %s (%s)", st.State, st.Detail)
	}
	trash, _ := filepath.Glob(e.a.Store.Internal("trash", "*", "adopted"))
	if len(trash) != 1 {
		t.Fatal("adopted original was not kept in trash")
	}

	// Removing the deployment removes only the link.
	e.reopen()
	acts, err := e.a.Remove("prune", RemoveOptions{Targets: []string{"claude-code"}})
	if err != nil {
		t.Fatal(err)
	}
	if fsx.Exists(orig) || acts[0].Action != "removed" {
		t.Fatalf("link not removed: %+v", acts[0])
	}
	if !fsx.IsDir(e.a.Store.Path(Ref{Skills, "prune"})) {
		t.Fatal("store copy removed")
	}
}

func TestCopyDetectsIndependentModification(t *testing.T) {
	e := newEnv(t)
	src := e.skill("src/fmt", "fmt", "v1")
	e.a.Import(src, ImportOptions{})
	opts := DeployOptions{Assets: []string{"fmt"}, Method: MethodCopy, Selection: Selection{Targets: []string{"claude-code"}}}
	e.deploy(opts)
	dest := filepath.Join(e.home, ".claude", "skills", "fmt")
	if fsx.IsSymlink(dest) || !fsx.IsDir(dest) {
		t.Fatal("expected a copy")
	}
	// A store change makes the copy outdated and redeploy updates it.
	os.WriteFile(filepath.Join(e.a.Store.Path(Ref{Skills, "fmt"}), "extra.md"), []byte("x"), 0o644)
	e.a.Config.Deploy.Method = MethodCopy
	if st := e.status("claude-code")["skills/fmt"]; st.State != StateOutdated {
		t.Fatalf("expected outdated, got %s", st.State)
	}
	e.deploy(opts)
	if !fsx.Exists(filepath.Join(dest, "extra.md")) {
		t.Fatal("copy not updated")
	}
	// Editing the deployed copy is a conflict; deploy and remove refuse.
	os.WriteFile(filepath.Join(dest, "SKILL.md"), []byte("hand edit"), 0o644)
	if st := e.status("claude-code")["skills/fmt"]; st.State != StateConflict {
		t.Fatalf("expected conflict, got %s", st.State)
	}
	if res := e.deploy(opts); !res.Failed() {
		t.Fatal("deploy should refuse a modified copy")
	}
	if _, err := e.a.Remove("fmt", RemoveOptions{Targets: []string{"claude-code"}}); err == nil {
		t.Fatal("remove should refuse a modified copy")
	}
	if b, _ := os.ReadFile(filepath.Join(dest, "SKILL.md")); string(b) != "hand edit" {
		t.Fatal("modified copy was touched")
	}
}

func TestInstructionsComposeWithAdapter(t *testing.T) {
	e := newEnv(t)
	e.a.Import(e.write("a/AGENTS.md", "# Core\nBe terse."), ImportOptions{Name: "core"})
	e.a.Import(e.write("b/AGENTS.md", "# Style\nNo emoji."), ImportOptions{Name: "style"})
	os.MkdirAll(filepath.Join(e.a.Store.Path(Ref{Instructions, "style"}), "targets"), 0o755)
	os.WriteFile(filepath.Join(e.a.Store.Path(Ref{Instructions, "style"}), "targets", "codex.md"), []byte("Codex only."), 0o644)

	// One asset without adapter: a link.
	e.a.Config.Assign("claude-code", "", "instructions/core")
	e.deploy(DeployOptions{All: true, Selection: Selection{Targets: []string{"claude-code"}}})
	if !fsx.IsSymlink(filepath.Join(e.home, ".claude", "CLAUDE.md")) {
		t.Fatal("expected CLAUDE.md link")
	}
	// Two assets plus an adapter: a generated file per target.
	e.a.Config.Assign("codex", "", "instructions/core", "instructions/style")
	e.deploy(DeployOptions{All: true, Selection: Selection{Targets: []string{"codex"}}})
	b, err := os.ReadFile(filepath.Join(e.home, ".codex", "AGENTS.md"))
	if err != nil {
		t.Fatal(err)
	}
	got := string(b)
	for _, want := range []string{"Generated by agentctl", "# Core\nBe terse.", "# Style\nNo emoji.\n\nCodex only."} {
		if !strings.Contains(got, want) {
			t.Fatalf("AGENTS.md missing %q:\n%s", want, got)
		}
	}
	// Removing one asset rewrites the file from the rest.
	if _, err := e.a.Remove("style", RemoveOptions{Targets: []string{"codex"}}); err != nil {
		t.Fatal(err)
	}
	if !fsx.IsSymlink(filepath.Join(e.home, ".codex", "AGENTS.md")) {
		t.Fatal("expected AGENTS.md to become a link to the remaining asset")
	}
}

func TestPackageNeedsAcknowledgement(t *testing.T) {
	e := newEnv(t)
	e.a.Import(e.skill("src/brief", "brief", "Morning brief."), ImportOptions{})
	opts := DeployOptions{Assets: []string{"brief"}, Selection: Selection{Targets: []string{"claude-chat"}}}
	res := e.deploy(opts)
	ev := res.Evals[0]
	if ev.Method != MethodPackage || ev.State != StateManual || !fsx.Exists(ev.Dest) {
		t.Fatalf("expected a package awaiting upload, got %s %s", ev.Method, ev.State)
	}
	if len(ev.Steps) != 2 || !strings.Contains(ev.Steps[1], "acknowledge") {
		t.Fatalf("missing manual steps: %v", ev.Steps)
	}
	if _, _, err := e.a.Acknowledge("claude-chat", "", "brief", "0000000000"); err == nil {
		t.Fatal("wrong hash accepted")
	}
	if _, _, err := e.a.Acknowledge("claude-chat", "", "brief", fsx.Short(ev.Want)); err != nil {
		t.Fatal(err)
	}
	if st := e.status("claude-chat")["skills/brief"]; st.State != StateOK {
		t.Fatalf("expected ok after ack, got %s", st.State)
	}
	// A store change makes the acknowledged upload outdated.
	os.WriteFile(filepath.Join(e.a.Store.Path(Ref{Skills, "brief"}), "more.md"), []byte("x"), 0o644)
	if st := e.status("claude-chat")["skills/brief"]; st.State != StateOutdated {
		t.Fatalf("expected outdated, got %s", st.State)
	}
}

func TestMCPImportScrubsCredentialsAndMergesDesktopConfig(t *testing.T) {
	e := newEnv(t)
	e.write(".claude.json", `{"mcpServers": {"gh": {"command": "npx", "args": ["gh-mcp"], "env": {"TOKEN": "secret-value"}}}}`)
	res, err := e.a.Import("gh", ImportOptions{From: "claude-code"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res[0].Warnings) == 0 {
		t.Fatal("expected a credential warning")
	}
	b, _ := os.ReadFile(filepath.Join(e.a.Store.Path(Ref{Tools, "gh"}), "tool.json"))
	if strings.Contains(string(b), "secret-value") || !strings.Contains(string(b), "${TOKEN}") {
		t.Fatalf("credential leaked into the store:\n%s", b)
	}
	// Claude Code's own entry verifies as configured (placeholders match any value).
	e.a.Config.Assign("claude-code", "", "tools/gh")
	if st := e.status("claude-code")["tools/gh"]; st.State != StateOK {
		t.Fatalf("expected verified, got %s (%s)", st.State, st.Detail)
	}
	// Desktop config: merged in place, preserving other keys, order, and an existing credential.
	cfg := e.write(".config/Claude/claude_desktop_config.json", "{\n  \"zzz\": true,\n  \"mcpServers\": {\n    \"gh\": {\"command\": \"old\", \"env\": {\"TOKEN\": \"desktop-secret\"}}\n  }\n}\n")
	e.reopen()
	e.a.Config.Assign("claude-chat", "", "tools/gh")
	st := e.status("claude-chat")["tools/gh"]
	if st.State != StateConflict {
		t.Fatalf("expected conflict with an unmanaged entry, got %s", st.State)
	}
	os.WriteFile(cfg, []byte("{\n  \"zzz\": true\n}\n"), 0o644)
	e.deploy(DeployOptions{All: true, Selection: Selection{Targets: []string{"claude-chat"}}})
	out, _ := os.ReadFile(cfg)
	if !strings.HasPrefix(string(out), "{\n  \"zzz\": true,\n  \"mcpServers\"") {
		t.Fatalf("key order not preserved:\n%s", out)
	}
	// The user puts the real credential in; a redeploy keeps it.
	os.WriteFile(cfg, []byte(strings.Replace(string(out), "${TOKEN}", "desktop-secret", 1)), 0o644)
	if st := e.status("claude-chat")["tools/gh"]; st.State != StateOK {
		t.Fatalf("expected ok, got %s (%s)", st.State, st.Detail)
	}
	if _, err := e.a.Remove("gh", RemoveOptions{Targets: []string{"claude-chat"}}); err != nil {
		t.Fatal(err)
	}
	var m map[string]map[string]any
	b, _ = os.ReadFile(cfg)
	json.Unmarshal(b, &m)
	if _, ok := m["mcpServers"]["gh"]; ok {
		t.Fatal("entry not removed")
	}
}

func TestPluginSkillDependencyBlocksUntilAssigned(t *testing.T) {
	e := newEnv(t)
	p := ".claude/plugins/cache/mkt/power/1.0.0"
	e.write(p+"/.claude-plugin/plugin.json", `{"name": "power", "version": "1.0.0", "license": "MIT"}`)
	e.skill(p+"/skills/think", "think", "Think.")
	e.write(".claude/plugins/installed_plugins.json", `{"version": 2, "plugins": {"power@mkt": [{"scope": "user", "installPath": "`+filepath.Join(e.home, p)+`"}]}}`)
	res, err := e.a.Import("think", ImportOptions{From: "claude-code"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res[0].Requires) != 1 || res[0].Requires[0].InStore {
		t.Fatalf("expected a missing plugin dependency: %+v", res[0].Requires)
	}
	e.a.Config.Assign("claude-code", "", "skills/think")
	if st := e.status("claude-code")["skills/think"]; st.State != StateBlocked || !strings.Contains(st.Detail, "not in the store") {
		t.Fatalf("expected blocked, got %s (%s)", st.State, st.Detail)
	}
	res, err = e.a.Import("power", ImportOptions{From: "claude-code", Selection: Selection{Kind: Plugins}})
	if err != nil || res[0].License != "MIT" || res[0].Origin != OriginThirdParty {
		t.Fatalf("plugin import: %+v %v", res, err)
	}
	if st := e.status("claude-code")["skills/think"]; st.State != StateBlocked || !strings.Contains(st.Detail, "assign it") {
		t.Fatalf("expected blocked on assignment, got %s (%s)", st.State, st.Detail)
	}
	e.a.Config.Assign("claude-code", "", "plugins/power")
	if st := e.status("claude-code")["skills/think"]; st.State != StateMissing {
		t.Fatalf("expected deployable, got %s (%s)", st.State, st.Detail)
	}
	if _, err := e.a.Remove("power", RemoveOptions{Store: true}); err == nil {
		t.Fatal("removing a required plugin from the store should fail")
	}
}

func TestUpdateMergesAndStopsOnConflicts(t *testing.T) {
	e := newEnv(t)
	up := e.skill("upstream/doc", "doc", "v1")
	e.write("upstream/doc/ref.md", "ref v1\n")
	e.a.Import(up, ImportOptions{})
	store := e.a.Store.Path(Ref{Skills, "doc"})

	res, _ := e.a.Update(UpdateOptions{})
	if res[0].State != UpdateCurrent {
		t.Fatalf("expected up-to-date, got %s", res[0].State)
	}
	// Upstream changes SKILL.md; the store adds a local file: they merge.
	e.skill("upstream/doc", "doc", "v2")
	os.WriteFile(filepath.Join(store, "local.md"), []byte("mine"), 0o644)
	e.reopen()
	res, err := e.a.Update(UpdateOptions{Apply: true})
	if err != nil || res[0].State != UpdateApplied {
		t.Fatalf("expected update, got %+v %v", res, err)
	}
	b, _ := os.ReadFile(filepath.Join(store, "SKILL.md"))
	if !strings.Contains(string(b), "v2") || !fsx.Exists(filepath.Join(store, "local.md")) {
		t.Fatal("merge lost upstream or local changes")
	}
	// Both sides change the same file: nothing is applied.
	e.write("upstream/doc/ref.md", "ref v2\n")
	os.WriteFile(filepath.Join(store, "ref.md"), []byte("ref local\n"), 0o644)
	e.reopen()
	res, err = e.a.Update(UpdateOptions{Apply: true})
	if err == nil || res[0].State != UpdateLocal {
		t.Fatalf("expected a local-edit stop, got %+v %v", res, err)
	}
	if b, _ := os.ReadFile(filepath.Join(store, "ref.md")); string(b) != "ref local\n" {
		t.Fatal("conflicting local edit was overwritten")
	}
}

func TestInventoryExcludesAndClassifies(t *testing.T) {
	e := newEnv(t)
	e.skill(".claude/skills/one", "one", "1")
	e.skill("work/client/.claude/skills/secret", "secret", "s")
	e.skill("src/app/.claude/skills/two", "two", "2")
	e.write(".claude.json", `{"projects": {"`+filepath.Join(e.home, "work/client")+`": {}, "`+filepath.Join(e.home, "src/app")+`": {}}}`)
	e.a.Config.AddExclude("~/work")
	inv, err := e.a.Inventory(InventoryOptions{Selection: Selection{Targets: []string{"claude-code"}, Scope: ScopeProject}})
	if err != nil {
		t.Fatal(err)
	}
	if len(inv.Items) != 1 || inv.Items[0].Name != "two" || inv.Items[0].Status != ItemUnmanaged {
		t.Fatalf("unexpected items: %+v", inv.Items)
	}
	e.a.Import(inv.Items[0].ID, ImportOptions{})
	inv, _ = e.a.Inventory(InventoryOptions{Selection: Selection{Targets: []string{"claude-code"}, Scope: ScopeProject}})
	if inv.Items[0].Status != ItemInStore {
		t.Fatalf("expected in-store, got %s", inv.Items[0].Status)
	}
}

func TestConfigRoundTrip(t *testing.T) {
	e := newEnv(t)
	c := e.a.Config
	if err := c.Set("deploy.method", "copy"); err != nil {
		t.Fatal(err)
	}
	if err := c.Set("targets.codex.paths.skills", "~/.codex/skills"); err != nil {
		t.Fatal(err)
	}
	if err := c.Set("targets.codex.paths.nope", "x"); err == nil {
		t.Fatal("unknown path key accepted")
	}
	c.SetEnabled("chatgpt", false)
	c.Assign("claude-code", "~/src/app", "skills/x")
	if err := e.a.SaveConfig(); err != nil {
		t.Fatal(err)
	}
	e.reopen()
	if e.a.Config.Deploy.Method != "copy" || e.a.Config.Enabled("chatgpt") || e.a.TargetPaths("codex")["skills"] != filepath.Join(e.home, ".codex", "skills") {
		t.Fatalf("config did not round-trip: %+v", e.a.Config)
	}
	if got := e.a.Config.Assignments("claude-code", "~/src/app"); len(got) != 1 {
		t.Fatalf("project assignment lost: %v", got)
	}
	b, _ := os.ReadFile(e.a.ConfigPath())
	if !strings.Contains(string(b), "method: copy") {
		t.Fatalf("expected YAML config:\n%s", b)
	}
}

// Regression tests from review.

func TestSymlinksOutsideAssetAreNotImportedOrPackaged(t *testing.T) {
	e := newEnv(t)
	secret := e.write(".ssh/id_rsa", "PRIVATE KEY")
	dir := e.skill("src/evil", "evil", "x")
	os.Symlink(secret, filepath.Join(dir, "abs.txt"))
	os.Symlink("SKILL.md", filepath.Join(dir, "inside.md"))
	res, err := e.a.Import(dir, ImportOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res[0].Warnings) == 0 || !strings.Contains(res[0].Warnings[0], "abs.txt") {
		t.Fatalf("expected a skipped-link warning: %v", res[0].Warnings)
	}
	store := e.a.Store.Path(Ref{Skills, "evil"})
	if fsx.Exists(filepath.Join(store, "abs.txt")) || !fsx.IsSymlink(filepath.Join(store, "inside.md")) {
		t.Fatal("escaping link imported or internal link dropped")
	}
	// Even a link added to the store later is never packaged.
	os.Symlink("../../../.ssh/id_rsa", filepath.Join(store, "rel.txt"))
	ev := e.deploy(DeployOptions{Assets: []string{"evil"}, Selection: Selection{Targets: []string{"claude-chat"}}}).Evals[0]
	out := t.TempDir()
	if err := fsx.Unzip(ev.Dest, out); err != nil {
		t.Fatal(err)
	}
	if fsx.Exists(filepath.Join(out, "evil", "rel.txt")) || !fsx.Exists(filepath.Join(out, "evil", "inside.md")) {
		t.Fatal("package contents wrong")
	}
}

func TestMCPArgsAndURLSecretsAreScrubbed(t *testing.T) {
	m, withheld := mcpFromClient(json.RawMessage(`{"command":"srv","args":["--api-key","sk-ARGSECRET","--token=ghp_XYZ","--verbose","ghp_bare"]}`))
	joined := strings.Join(m.Args, " ")
	for _, leak := range []string{"sk-ARGSECRET", "ghp_XYZ", "ghp_bare"} {
		if strings.Contains(joined, leak) {
			t.Fatalf("secret %s leaked: %v", leak, m.Args)
		}
	}
	if m.Args[3] != "--verbose" || len(withheld) != 3 {
		t.Fatalf("args = %v, withheld = %v", m.Args, withheld)
	}
	r, _ := mcpFromClient(json.RawMessage(`{"type":"http","url":"https://mcp.example.com/api/mcp/s/a1b2c3d4e5f6g7h8i9j0k1l2m3/mcp?api_key=sk-live-abc&mode=fast"}`))
	if strings.Contains(r.URL, "a1b2c3d4") || strings.Contains(r.URL, "sk-live") || !strings.Contains(r.URL, "mode=fast") {
		t.Fatalf("url not scrubbed: %s", r.URL)
	}
	// Scrubbed definitions still match the client's real entry.
	want := clientValue(m, false)
	have := json.RawMessage(`{"type":"stdio","command":"srv","args":["--api-key","sk-ARGSECRET","--token=ghp_XYZ","--verbose","ghp_bare"]}`)
	if !mcpMatch(want, have) {
		t.Fatal("placeholder args should match real values")
	}
}

func TestUpdateIgnoresSourceThatIsNowAManagedDeployment(t *testing.T) {
	e := newEnv(t)
	claudeMD := e.write(".claude/CLAUDE.md", "# Mine\n")
	e.a.Import(claudeMD, ImportOptions{Name: "mine"})
	e.a.Import(e.write("x/AGENTS.md", "# Other\n"), ImportOptions{Name: "other"})
	e.deploy(DeployOptions{Assets: []string{"mine"}, Adopt: true, Selection: Selection{Targets: []string{"claude-code"}}})
	e.a.Config.Assign("claude-code", "", "instructions/other")
	e.deploy(DeployOptions{All: true, Selection: Selection{Targets: []string{"claude-code"}}})
	if b, _ := os.ReadFile(claudeMD); !strings.Contains(string(b), "Generated by agentctl") {
		t.Fatalf("expected a generated CLAUDE.md:\n%s", b)
	}
	e.reopen()
	res, err := e.a.Update(UpdateOptions{Apply: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range res {
		if r.Asset == "instructions/mine" && r.State != UpdateNoSource {
			t.Fatalf("expected no-source, got %s (%s)", r.State, r.Detail)
		}
	}
	b, _ := os.ReadFile(filepath.Join(e.a.Store.Path(Ref{Instructions, "mine"}), "AGENTS.md"))
	if string(b) != "# Mine\n" {
		t.Fatalf("generated file leaked into the store:\n%s", b)
	}
}

func TestSharedBinLinkSurvivesRemovalFromOneTarget(t *testing.T) {
	e := newEnv(t)
	dir := filepath.Join(e.home, "src", "mytool")
	e.write("src/mytool/mytool", "#!/bin/sh\necho hi\n")
	os.Chmod(filepath.Join(dir, "mytool"), 0o755)
	if _, err := e.a.Import(dir, ImportOptions{Selection: Selection{Kind: Tools}}); err != nil {
		t.Fatal(err)
	}
	e.deploy(DeployOptions{Assets: []string{"mytool"}, Selection: Selection{Targets: []string{"codex", "claude-code"}}})
	bin := filepath.Join(e.home, ".local", "bin", "mytool")
	if !fsx.IsSymlink(bin) {
		t.Fatal("bin not linked")
	}
	if _, err := e.a.Remove("mytool", RemoveOptions{Targets: []string{"codex"}}); err != nil {
		t.Fatal(err)
	}
	if !fsx.IsSymlink(bin) {
		t.Fatal("shared link removed while claude-code still uses it")
	}
	if st := e.status("claude-code")["tools/mytool"]; st.State != StateOK {
		t.Fatalf("claude-code: %s", st.State)
	}
}

func TestDifferentAssetsCannotShareADestination(t *testing.T) {
	e := newEnv(t)
	e.a.Import(e.skill("src/skills/foo", "foo", "skill"), ImportOptions{})
	e.write("src/plugins/foo/.claude-plugin/plugin.json", `{"name":"foo"}`)
	e.a.Import(filepath.Join(e.home, "src/plugins/foo"), ImportOptions{})
	e.deploy(DeployOptions{Assets: []string{"skills/foo"}, Selection: Selection{Targets: []string{"claude-code"}}})
	res := e.deploy(DeployOptions{Assets: []string{"plugins/foo"}, Selection: Selection{Targets: []string{"claude-code"}}})
	if res.Evals[0].State != StateConflict || !strings.Contains(res.Evals[0].Detail, "skills/foo") {
		t.Fatalf("expected an occupied conflict, got %s (%s)", res.Evals[0].State, res.Evals[0].Detail)
	}
	if st := e.status("claude-code")["skills/foo"]; st.State != StateOK {
		t.Fatalf("skills/foo disturbed: %s", st.State)
	}
}

func TestProjectMCPUserEditIsAConflictNotReverted(t *testing.T) {
	e := newEnv(t)
	proj := filepath.Join(e.home, "src", "app")
	os.MkdirAll(proj, 0o755)
	e.write("t/tool.json", `{"mcp":{"command":"srv","env":{"LOG_LEVEL":"info","TOKEN":"${TOKEN}"}}}`)
	e.a.Import(filepath.Join(e.home, "t"), ImportOptions{Name: "srv"})
	sel := Selection{Targets: []string{"claude-code"}, Project: proj}
	e.deploy(DeployOptions{Assets: []string{"srv"}, Selection: sel})
	cfg := filepath.Join(proj, ".mcp.json")
	b, _ := os.ReadFile(cfg)
	// A credential filled in is fine; a changed literal value is the user's edit.
	os.WriteFile(cfg, []byte(strings.Replace(string(b), "${TOKEN}", "real", 1)), 0o644)
	e.reopen()
	res := e.deploy(DeployOptions{Assets: []string{"srv"}, Selection: sel})
	if res.Evals[0].State != StateOK {
		t.Fatalf("credential fill-in should still be ok: %s (%s)", res.Evals[0].State, res.Evals[0].Detail)
	}
	b, _ = os.ReadFile(cfg)
	os.WriteFile(cfg, []byte(strings.Replace(string(b), `"info"`, `"debug"`, 1)), 0o644)
	e.reopen()
	res = e.deploy(DeployOptions{Assets: []string{"srv"}, Selection: sel})
	if res.Evals[0].State != StateConflict {
		t.Fatalf("expected conflict, got %s", res.Evals[0].State)
	}
	if b, _ := os.ReadFile(cfg); !strings.Contains(string(b), "debug") || !strings.Contains(string(b), "real") {
		t.Fatalf("user edit reverted:\n%s", b)
	}
}

func TestGitDirectoryInDeployedCopyIsProtected(t *testing.T) {
	e := newEnv(t)
	e.a.Import(e.skill("src/fmt", "fmt", "v1"), ImportOptions{})
	sel := Selection{Targets: []string{"claude-code"}}
	e.deploy(DeployOptions{Assets: []string{"fmt"}, Method: MethodCopy, Selection: sel})
	dest := filepath.Join(e.home, ".claude", "skills", "fmt")
	e.write(".claude/skills/fmt/.git/HEAD", "ref: main")
	os.WriteFile(filepath.Join(e.a.Store.Path(Ref{Skills, "fmt"}), "new.md"), []byte("x"), 0o644)
	e.reopen()
	if res := e.deploy(DeployOptions{Assets: []string{"fmt"}, Selection: sel}); res.Evals[0].State != StateConflict {
		t.Fatalf("expected conflict, got %s", res.Evals[0].State)
	}
	if _, err := e.a.Remove("fmt", RemoveOptions{Targets: []string{"claude-code"}}); err == nil {
		t.Fatal("remove should refuse")
	}
	if !fsx.Exists(filepath.Join(dest, ".git", "HEAD")) {
		t.Fatal(".git deleted")
	}
}

func TestRepointedSymlinkIsAConflictAndCopyMethodIsRemembered(t *testing.T) {
	e := newEnv(t)
	e.a.Import(e.write("r/AGENTS.md", "# Rules\n"), ImportOptions{Name: "rules"})
	sel := Selection{Targets: []string{"claude-code"}}
	e.deploy(DeployOptions{Assets: []string{"rules"}, Selection: sel})
	dest := filepath.Join(e.home, ".claude", "CLAUDE.md")
	mine := e.write("dotfiles/CLAUDE.md", "mine")
	os.Remove(dest)
	os.Symlink(mine, dest)
	if st := e.status("claude-code")["instructions/rules"]; st.State != StateConflict {
		t.Fatalf("expected conflict, got %s", st.State)
	}
	os.Remove(dest)
	e.a.Import(e.skill("src/s", "s", "x"), ImportOptions{})
	e.deploy(DeployOptions{Assets: []string{"s"}, Method: MethodCopy, Selection: sel})
	e.reopen()
	res := e.deploy(DeployOptions{All: true, Selection: sel})
	for _, ev := range res.Evals {
		if ev.Asset == "skills/s" && ev.Method != MethodCopy {
			t.Fatalf("copy method forgotten: %s", ev.Method)
		}
	}
}

func TestManualMCPUpdatePathReachesOutdated(t *testing.T) {
	e := newEnv(t)
	e.write("t/tool.json", `{"mcp":{"command":"srv","args":["v1"]}}`)
	e.a.Import(filepath.Join(e.home, "t"), ImportOptions{Name: "srv"})
	sel := Selection{Targets: []string{"claude-code"}}
	e.deploy(DeployOptions{Assets: []string{"srv"}, Selection: sel})
	// Simulate what `claude mcp add-json` writes (including "type").
	e.write(".claude.json", `{"mcpServers":{"srv":{"type":"stdio","command":"srv","args":["v1"],"env":{}}}}`)
	if st := e.status("claude-code")["tools/srv"]; st.State != StateOK {
		t.Fatalf("expected ok, got %s (%s)", st.State, st.Detail)
	}
	os.WriteFile(filepath.Join(e.a.Store.Path(Ref{Tools, "srv"}), "tool.json"), []byte(`{"mcp":{"command":"srv","args":["v2"]}}`), 0o644)
	e.reopen()
	res := e.deploy(DeployOptions{Assets: []string{"srv"}, Selection: sel})
	ev := res.Evals[0]
	if ev.State != StateOutdated || !strings.Contains(strings.Join(ev.Steps, "\n"), "claude mcp remove") {
		t.Fatalf("expected outdated with remove+add steps, got %s %v", ev.State, ev.Steps)
	}
	e.reopen()
	if st := e.status("claude-code")["tools/srv"]; st.State != StateOutdated {
		t.Fatalf("should stay outdated until replaced, got %s", st.State)
	}
}
