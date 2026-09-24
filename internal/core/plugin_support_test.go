package core

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestOpenAIPluginSupport distinguishes portable manifests from Claude-only ones.
func TestOpenAIPluginSupport(t *testing.T) {
	e := newEnv(t)
	dir := filepath.Join(e.home, ".agents", "plugins", "sample")
	if err := os.MkdirAll(filepath.Join(dir, ".claude-plugin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".claude-plugin", "plugin.json"), []byte(`{"name":"sample"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	asset := &Asset{Ref: Ref{Plugins, "sample"}, Path: dir}
	for _, target := range []string{"codex", "chatgpt"} {
		if got := e.a.Support(target, asset); got.Level != SupportUnsupported {
			t.Fatalf("%s should refuse Claude-only plugin, got %+v", target, got)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "plugin.json"), []byte(`{"name":"sample"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{"codex", "chatgpt"} {
		if got := e.a.Support(target, asset); got.Level != SupportManual {
			t.Fatalf("%s should support a portable plugin, got %+v", target, got)
		}
		units := e.a.Plan(target, "", asset, "")
		if len(units) != 1 || units[0].Method != MethodManual || !strings.Contains(strings.Join(units[0].Steps, "\n"), "./plugins/sample") {
			t.Fatalf("%s produced wrong marketplace steps: %+v", target, units)
		}
	}
	if err := os.Remove(filepath.Join(dir, ".claude-plugin", "plugin.json")); err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{"claude-code", "claude-chat"} {
		if got := e.a.Support(target, asset); got.Level != SupportUnsupported {
			t.Fatalf("%s should require a Claude manifest, got %+v", target, got)
		}
	}
}

// TestImportPortablePlugin accepts a plugin rooted at plugin.json.
func TestImportPortablePlugin(t *testing.T) {
	e := newEnv(t)
	manifest := e.write("source/my-plugin/plugin.json", `{"name":"my-plugin","version":"1.0.0"}`)
	res, err := e.a.Import(manifest, ImportOptions{})
	if err != nil || len(res) != 1 || res[0].Asset != "plugins/my-plugin" {
		t.Fatalf("portable plugin import failed: %+v, %v", res, err)
	}
	if _, err := e.a.Store.Find("plugins/my-plugin", ""); err != nil {
		t.Fatal(err)
	}
}
