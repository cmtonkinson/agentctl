package main

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fixture creates a private home so synchronization tests never touch live assets.
func fixture(t *testing.T) (string, func(...string) (string, string, int)) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AGENTCTL_STORE", "")
	t.Setenv("CODEX_HOME", "")
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	run := func(args ...string) (string, string, int) {
		t.Helper()
		var out, err bytes.Buffer
		code := execute("test", args, &out, &err)
		return out.String(), err.String(), code
	}
	return home, run
}

// writeFixture writes a skill file in one of the private test locations.
func writeFixture(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

// TestStatusDiscoversOnlyKnownPaths verifies new installs and direct Codex use.
func TestStatusDiscoversOnlyKnownPaths(t *testing.T) {
	home, run := fixture(t)
	writeFixture(t, filepath.Join(home, ".agents/skills/kept/SKILL.md"), "canonical")
	writeFixture(t, filepath.Join(home, ".claude/skills/new/SKILL.md"), "new")
	writeFixture(t, filepath.Join(home, ".claude/plugins/cache/noise/SKILL.md"), "noise")
	writeFixture(t, filepath.Join(home, ".codex/skills/kept/SKILL.md"), "changed")
	out, stderr, code := run("status")
	if code != 0 {
		t.Fatalf("status: %s", stderr)
	}
	for _, expected := range []string{"kept\n  codex       different", "new\n  codex       missing\n  claude-code new"} {
		if !strings.Contains(out, expected) {
			t.Errorf("missing %q in:\n%s", expected, out)
		}
	}
	if strings.Contains(out, "noise") {
		t.Errorf("plugin cache appeared: %s", out)
	}
}

// TestDeployAndPull verifies link preference, conflict protection, and backups.
func TestDeployAndPull(t *testing.T) {
	home, run := fixture(t)
	canonical := filepath.Join(home, ".agents/skills/example/SKILL.md")
	live := filepath.Join(home, ".claude/skills/example/SKILL.md")
	writeFixture(t, canonical, "original")
	_, stderr, code := run("deploy", "claude-code", "example")
	if code != 0 {
		t.Fatal(stderr)
	}
	info, err := os.Lstat(filepath.Dir(live))
	if err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("expected symlink: %v", err)
	}
	out, _, _ := run("status", "claude-code")
	if !strings.Contains(out, "linked") {
		t.Fatal(out)
	}
	if err := os.Remove(filepath.Dir(live)); err != nil {
		t.Fatal(err)
	}
	writeFixture(t, live, "live change")
	_, stderr, code = run("deploy", "claude-code", "example")
	if code == 0 || !strings.Contains(stderr, "differs") {
		t.Fatalf("expected conflict: %s", stderr)
	}
	_, stderr, code = run("pull", "claude-code", "example")
	if code == 0 || !strings.Contains(stderr, "--replace") {
		t.Fatalf("expected pull conflict: %s", stderr)
	}
	out, stderr, code = run("pull", "claude-code", "example", "--replace")
	if code != 0 {
		t.Fatal(stderr)
	}
	if string(mustRead(t, canonical)) != "live change" || !strings.Contains(out, "backup:") {
		t.Fatal(out)
	}
	if string(mustRead(t, live)) != "live change" {
		t.Fatal("pull changed live source")
	}
	backups, err := filepath.Glob(filepath.Join(home, ".local/state/agentctl/backups/*/example/SKILL.md"))
	if err != nil || len(backups) != 1 || string(mustRead(t, backups[0])) != "original" {
		t.Fatalf("backup: %v %v", backups, err)
	}
}

// TestDeployCopyAndDirectInstructions checks copy mode and existing pointers.
func TestDeployCopyAndDirectInstructions(t *testing.T) {
	home, run := fixture(t)
	writeFixture(t, filepath.Join(home, ".agents/instructions/agents.md"), "rules")
	writeFixture(t, filepath.Join(home, ".claude/CLAUDE.md"), "Follow instructions at ~/.agents/instructions/agents.md now.\n")
	writeFixture(t, filepath.Join(home, ".agents/skills/example/SKILL.md"), "original")
	out, stderr, code := run("deploy", "claude-code", "--copy")
	if code != 0 {
		t.Fatal(stderr)
	}
	if !strings.Contains(out, "instructions: pointer") {
		t.Fatal(out)
	}
	path := filepath.Join(home, ".claude/skills/example")
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("expected directory copy: %v", err)
	}
	writeFixture(t, filepath.Join(home, ".agents/skills/example/SKILL.md"), "changed")
	out, _, _ = run("status", "claude-code")
	if !strings.Contains(out, "different") {
		t.Fatal(out)
	}
}

// TestDeployInstructionPointer preserves references inside the canonical directory.
func TestDeployInstructionPointer(t *testing.T) {
	home, run := fixture(t)
	writeFixture(t, filepath.Join(home, ".agents/instructions/agents.md"), "Follow general.md")
	out, stderr, code := run("deploy", "claude-code", "instructions")
	if code != 0 || !strings.Contains(out, "points to") {
		t.Fatalf("deploy: %s %s", out, stderr)
	}
	path := filepath.Join(home, ".claude/CLAUDE.md")
	if string(mustRead(t, path)) != "Follow instructions at ~/.agents/instructions/agents.md now.\n" {
		t.Fatal("wrong pointer")
	}
	out, stderr, code = run("status", "claude-code")
	if code != 0 || !strings.Contains(out, "agents.md\n  claude-code pointer") {
		t.Fatalf("status: %s %s", out, stderr)
	}
}

// TestExportAndValidation verifies account package contents and path boundaries.
func TestExportAndValidation(t *testing.T) {
	home, run := fixture(t)
	writeFixture(t, filepath.Join(home, ".agents/instructions/agents.md"), "rules")
	writeFixture(t, filepath.Join(home, ".agents/instructions/coding.md"), "coding rules")
	writeFixture(t, filepath.Join(home, ".agents/skills/example/SKILL.md"), "skill")
	out, stderr, code := run("export", "claude-chat")
	if code != 0 {
		t.Fatal(stderr)
	}
	if !strings.Contains(out, "cannot inspect account versions") {
		t.Fatal(out)
	}
	archive := filepath.Join(home, ".local/state/agentctl/exports/claude-chat/example.zip")
	reader, err := zip.OpenReader(archive)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	if len(reader.File) != 1 || reader.File[0].Name != "example/SKILL.md" {
		t.Fatalf("zip entries: %+v", reader.File)
	}
	if string(mustRead(t, filepath.Join(home, ".local/state/agentctl/exports/claude-chat/instructions/agents.md"))) != "rules" || string(mustRead(t, filepath.Join(home, ".local/state/agentctl/exports/claude-chat/instructions/coding.md"))) != "coding rules" {
		t.Fatal("instruction export")
	}
	_, stderr, code = run("pull", "claude-code", "../escape")
	if code == 0 || !strings.Contains(stderr, "invalid asset name") {
		t.Fatal(stderr)
	}
}

// TestPullLinkedExternalSkill copies the actual skill tree, not an empty link.
func TestPullLinkedExternalSkill(t *testing.T) {
	home, run := fixture(t)
	external := filepath.Join(home, "external/skill")
	writeFixture(t, filepath.Join(external, "SKILL.md"), "external skill")
	live := filepath.Join(home, ".claude/skills/example")
	if err := os.MkdirAll(filepath.Dir(live), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(external, live); err != nil {
		t.Fatal(err)
	}
	_, stderr, code := run("pull", "claude-code", "example")
	if code != 0 {
		t.Fatal(stderr)
	}
	got := mustRead(t, filepath.Join(home, ".agents/skills/example/SKILL.md"))
	if string(got) != "external skill" {
		t.Fatalf("copied %q", got)
	}
}

// TestGeneratedFilesIgnored keeps local runtimes out of portable skill drift.
func TestGeneratedFilesIgnored(t *testing.T) {
	home, run := fixture(t)
	writeFixture(t, filepath.Join(home, ".agents/skills/example/SKILL.md"), "skill")
	writeFixture(t, filepath.Join(home, ".claude/skills/example/SKILL.md"), "skill")
	writeFixture(t, filepath.Join(home, ".claude/skills/example/scripts/.venv/bin/python"), "runtime")
	writeFixture(t, filepath.Join(home, ".claude/skills/example/scripts/__pycache__/cache.pyc"), "cache")
	out, stderr, code := run("status", "claude-code")
	if code != 0 || !strings.Contains(out, "same copy") {
		t.Fatalf("status: %s %s", out, stderr)
	}
	out, stderr, code = run("diff", "claude-code", "example")
	if code != 0 || out != "" {
		t.Fatalf("generated files appeared in diff: %s %s", out, stderr)
	}
	writeFixture(t, filepath.Join(home, ".claude/skills/example/SKILL.md"), "live change")
	out, stderr, code = run("diff", "claude-code", "example")
	if code != 0 || strings.TrimSpace(out) != "SKILL.md" {
		t.Fatalf("missing file diff: %s %s", out, stderr)
	}
	out, stderr, code = run("diff", "claude-code", "example", "--verbose")
	if code != 0 || !strings.Contains(out, "+live change") {
		t.Fatalf("missing verbose content diff: %s %s", out, stderr)
	}
	writeFixture(t, filepath.Join(home, ".claude/skills/example/SKILL.md"), "skill")
	out, stderr, code = run("deploy", "claude-code", "example")
	if code != 0 || !strings.Contains(out, "backup:") {
		t.Fatalf("deploy: %s %s", out, stderr)
	}
	backups, err := filepath.Glob(filepath.Join(home, ".local/state/agentctl/backups/*/example/scripts/.venv/bin/python"))
	if err != nil || len(backups) != 1 || string(mustRead(t, backups[0])) != "runtime" {
		t.Fatalf("raw backup: %v %v", backups, err)
	}
}

// mustRead reads a fixture file or fails its test.
func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// TestInventoryOutput locks grouping, alignment, paths, and informational files.
func TestInventoryOutput(t *testing.T) {
	home, run := fixture(t)
	writeFixture(t, filepath.Join(home, ".agents/instructions/agents.md"), "rules")
	writeFixture(t, filepath.Join(home, ".agents/instructions/coding.md"), "coding")
	writeFixture(t, filepath.Join(home, ".agents/skills/asd-ste100/SKILL.md"), "skill")
	writeFixture(t, filepath.Join(home, ".codex/AGENTS.md"), "Follow instructions at ~/.agents/instructions/agents.md now.\n")
	if err := os.MkdirAll(filepath.Join(home, ".claude/skills"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(home, ".agents/skills/asd-ste100"), filepath.Join(home, ".claude/skills/asd-ste100")); err != nil {
		t.Fatal(err)
	}
	out, stderr, code := run("list")
	expected := "instruction agents.md\ninstruction coding.md\nskill       asd-ste100\n"
	if code != 0 || out != expected {
		t.Fatalf("list: %q %s", out, stderr)
	}
	expected = "Instructions:\nagents.md\n  codex       pointer\n  claude-code missing\ncoding.md\n\nSkills:\nasd-ste100\n  codex       direct\n  claude-code linked\n"
	out, stderr, code = run("status")
	if code != 0 || out != expected {
		t.Fatalf("status: %q %s", out, stderr)
	}
	for _, flag := range []string{"-v", "--verbose"} {
		out, stderr, code = run("list", flag)
		expected = "instruction ~/.agents/instructions/agents.md\ninstruction ~/.agents/instructions/coding.md\nskill       ~/.agents/skills/asd-ste100\n"
		if code != 0 || out != expected {
			t.Fatalf("verbose list: %q %s", out, stderr)
		}
		out, stderr, code = run("status", flag)
		for _, text := range []string{"~/.agents/instructions/agents.md\n  codex       pointer (~/.codex/AGENTS.md)", "~/.agents/instructions/coding.md\n\nSkills:", "~/.agents/skills/asd-ste100\n  codex       direct\n  claude-code linked (~/.claude/skills/asd-ste100)"} {
			if code != 0 || !strings.Contains(out, text) {
				t.Fatalf("verbose status missing %q: %s %s", text, out, stderr)
			}
		}
		out, stderr, code = run("status", flag, "codex")
		if code != 0 || strings.Contains(out, "claude-code") {
			t.Fatalf("filtered status: %s %s", out, stderr)
		}
	}
}

// TestBareDiff includes changed copies and one-sided additions and deletions.
func TestBareDiff(t *testing.T) {
	home, run := fixture(t)
	writeFixture(t, filepath.Join(home, ".agents/skills/changed/SKILL.md"), "canonical content\n")
	writeFixture(t, filepath.Join(home, ".claude/skills/changed/SKILL.md"), "changed content\n")
	writeFixture(t, filepath.Join(home, ".claude/skills/changed/scripts/helper.sh"), "new helper\n")
	writeFixture(t, filepath.Join(home, ".claude/skills/new/SKILL.md"), "new content\n")
	writeFixture(t, filepath.Join(home, ".agents/skills/missing/SKILL.md"), "missing content\n")
	out, stderr, code := run("diff")
	for _, expected := range []string{"Skills:", "changed\n  claude-code different", "    SKILL.md", "    scripts/helper.sh", "new\n  claude-code new", "missing\n  claude-code missing"} {
		if code != 0 || !strings.Contains(out, expected) {
			t.Fatalf("missing %q: %s %s", expected, out, stderr)
		}
	}
	if strings.Contains(out, "content") || strings.Contains(out, "diff --git") {
		t.Fatalf("default diff showed changed lines: %s", out)
	}
	out, stderr, code = run("diff", "-v")
	for _, expected := range []string{"+changed content", "+new content", "-missing content"} {
		if code != 0 || !strings.Contains(out, expected) {
			t.Fatalf("verbose diff missing %q: %s %s", expected, out, stderr)
		}
	}
}

// TestDiffEntrypointAlias keeps both entrypoint names and informational files separate.
func TestDiffEntrypointAlias(t *testing.T) {
	home, run := fixture(t)
	writeFixture(t, filepath.Join(home, ".agents/instructions/agents.md"), "canonical rules\n")
	writeFixture(t, filepath.Join(home, ".agents/instructions/coding.md"), "informational only\n")
	writeFixture(t, filepath.Join(home, ".codex/AGENTS.md"), "live rules\n")
	for _, args := range [][]string{{"diff"}, {"diff", "codex", "agents.md"}, {"diff", "codex", "instructions"}} {
		out, stderr, code := run(args...)
		if code != 0 || !strings.Contains(out, "agents.md") || strings.Contains(out, "informational only") || strings.Contains(out, "+live rules") {
			t.Fatalf("diff: %s %s", out, stderr)
		}
	}
	out, stderr, code := run("diff", "codex", "instructions", "-v")
	if code != 0 || !strings.Contains(out, "+live rules") || strings.Contains(out, "informational only") {
		t.Fatalf("verbose diff: %s %s", out, stderr)
	}
}

// TestDryRunsDoNotWrite verifies creation, replacement, backups, and conflicts.
func TestDryRunsDoNotWrite(t *testing.T) {
	home, run := fixture(t)
	writeFixture(t, filepath.Join(home, ".agents/instructions/agents.md"), "rules")
	writeFixture(t, filepath.Join(home, ".agents/skills/example/SKILL.md"), "canonical")
	writeFixture(t, filepath.Join(home, ".claude/skills/example/SKILL.md"), "live")
	writeFixture(t, filepath.Join(home, ".claude/skills/new/SKILL.md"), "new")
	before, err := fingerprint(home)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		args  []string
		wants []string
		code  int
	}{
		{[]string{"deploy", "claude-code", "--dry-run"}, []string{"would pointer", "conflict:"}, 1},
		{[]string{"deploy", "claude-code", "--dry-run", "--replace"}, []string{"would pointer", "would backup", "would link"}, 0},
		{[]string{"deploy", "claude-code", "--copy", "--dry-run", "--replace"}, []string{"would copy", "would backup"}, 0},
		{[]string{"pull", "claude-code", "example", "--dry-run"}, []string{}, 1},
		{[]string{"pull", "claude-code", "example", "--replace", "--dry-run"}, []string{"would backup", "would copy"}, 0},
		{[]string{"pull", "--dry-run", "claude-code", "new"}, []string{"would copy"}, 0},
	}
	for _, c := range cases {
		out, stderr, code := run(c.args...)
		if code != c.code {
			t.Fatalf("%v: %s %s", c.args, out, stderr)
		}
		for _, want := range c.wants {
			if !strings.Contains(out, want) {
				t.Fatalf("%v missing %q: %s", c.args, want, out)
			}
		}
		after, err := fingerprint(home)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(before, after) {
			t.Fatalf("%v changed files", c.args)
		}
		if exists(filepath.Join(home, ".local/state/agentctl")) || exists(filepath.Join(home, ".claude/CLAUDE.md")) || exists(filepath.Join(home, ".agents/skills/new")) {
			t.Fatalf("%v created paths", c.args)
		}
	}
	_, _, code := run("deploy", "claude-code")
	if code != 1 || exists(filepath.Join(home, ".claude/CLAUDE.md")) {
		t.Fatal("bulk conflict caused partial deployment")
	}
}

// TestBareDiffWithoutDrift gives a clear result for directly used store assets.
func TestBareDiffWithoutDrift(t *testing.T) {
	home, run := fixture(t)
	writeFixture(t, filepath.Join(home, ".agents/skills/example/SKILL.md"), "skill")
	if _, stderr, code := run("deploy", "claude-code", "example"); code != 0 {
		t.Fatal(stderr)
	}
	out, stderr, code := run("diff")
	if code != 0 || out != "No differences.\n" {
		t.Fatalf("diff: %q %s", out, stderr)
	}
}
