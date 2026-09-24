package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func run(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	code := Execute("test", args, &out, &errb)
	return code, out.String(), errb.String()
}

func sandbox(t *testing.T) string {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PATH", "")
	for _, k := range []string{"CLAUDE_CONFIG_DIR", "CODEX_HOME", "XDG_CONFIG_HOME", "AGENTCTL_STORE"} {
		t.Setenv(k, "")
	}
	return home
}

func TestHelpAndVersion(t *testing.T) {
	sandbox(t)
	code, out, _ := run(t, "--help")
	if code != 0 || !strings.Contains(out, "Targets:") || !strings.Contains(out, "claude-code   Claude Code") {
		t.Fatalf("unexpected help (%d):\n%s", code, out)
	}
	if strings.Index(out, "inventory") > strings.Index(out, "doctor") {
		t.Fatal("commands should keep their documented order")
	}
	if code, out, _ := run(t, "--version"); code != 0 || strings.TrimSpace(out) != "agentctl test" {
		t.Fatalf("version: %d %q", code, out)
	}
	if code, out, _ := run(t, "help", "deploy"); code != 0 || !strings.Contains(out, "--dry-run") {
		t.Fatalf("help deploy: %d\n%s", code, out)
	}
}

func TestUsageErrorsExit2(t *testing.T) {
	sandbox(t)
	for _, args := range [][]string{{"frobnicate"}, {"deploy", "--bogus"}, {"deploy"}, {"status", "--only", "weird"}, {"remove", "x"}, {"inventory", "--kind", "widgets"}} {
		code, _, errOut := run(t, args...)
		if code != ExitUsage {
			t.Errorf("%v: exit %d, want %d (%s)", args, code, ExitUsage, errOut)
		}
	}
}

func TestEndToEndJSON(t *testing.T) {
	home := sandbox(t)
	skill := filepath.Join(home, "src", "tidy")
	os.MkdirAll(skill, 0o755)
	os.WriteFile(filepath.Join(skill, "SKILL.md"), []byte("---\nname: tidy\ndescription: Tidy up.\n---\nBody\n"), 0o644)

	if code, _, errOut := run(t, "import", skill, "-q"); code != 0 {
		t.Fatalf("import: %s", errOut)
	}
	if code, _, errOut := run(t, "target", "assign", "claude-code", "tidy", "-q"); code != 0 {
		t.Fatalf("assign: %s", errOut)
	}
	if code, _, _ := run(t, "status", "--check", "-q"); code != ExitCheck {
		t.Fatalf("status --check should report work to do, got %d", code)
	}
	if code, _, errOut := run(t, "deploy", "--all", "-q"); code != 0 {
		t.Fatalf("deploy: %s", errOut)
	}
	code, out, _ := run(t, "status", "--check", "--json")
	if code != 0 {
		t.Fatalf("status after deploy: %d\n%s", code, out)
	}
	var res struct {
		Results []struct {
			Asset, State, Method string
		} `json:"results"`
	}
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("bad JSON: %v\n%s", err, out)
	}
	if len(res.Results) != 1 || res.Results[0].State != "ok" || res.Results[0].Method != "link" {
		t.Fatalf("unexpected status: %+v", res.Results)
	}
	if code, out, _ := run(t, "list", "--json"); code != 0 || !strings.Contains(out, `"asset": "skills/tidy"`) {
		t.Fatalf("list: %s", out)
	}
	b, err := os.ReadFile(filepath.Join(home, ".agents", "agentctl.yaml"))
	if err != nil || !strings.Contains(string(b), "skills/tidy") {
		t.Fatalf("config not written as YAML: %v\n%s", err, b)
	}
}
