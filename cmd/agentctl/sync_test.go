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
	out, stderr, code = run("status", "kept", "codex")
	if code != 0 || !strings.Contains(out, "kept\n  codex       different") || strings.Contains(out, "claude-code") || strings.Contains(out, "new") {
		t.Fatalf("selected status: %s %s", out, stderr)
	}
}

// TestDeployAndPull verifies link preference, conflict protection, and backups.
func TestDeployAndPull(t *testing.T) {
	home, run := fixture(t)
	canonical := filepath.Join(home, ".agents/skills/example/SKILL.md")
	live := filepath.Join(home, ".claude/skills/example/SKILL.md")
	writeFixture(t, canonical, "original")
	_, stderr, code := run("deploy", "example", "claude-code")
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
	_, stderr, code = run("deploy", "example", "claude-code")
	if code == 0 || !strings.Contains(stderr, "differs") {
		t.Fatalf("expected conflict: %s", stderr)
	}
	_, stderr, code = run("pull", "example", "claude-code")
	if code == 0 || !strings.Contains(stderr, "--replace") {
		t.Fatalf("expected pull conflict: %s", stderr)
	}
	out, stderr, code = run("pull", "example", "claude-code", "--replace")
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

// TestDeployNameDefaultsToAllLocalTargets verifies asset-first global deployment.
func TestDeployNameDefaultsToAllLocalTargets(t *testing.T) {
	home, run := fixture(t)
	writeFixture(t, filepath.Join(home, ".agents/skills/example/SKILL.md"), "skill")
	out, stderr, code := run("deploy", "example", "--dry-run")
	if code != 0 || !strings.Contains(out, "codex/example: direct") || !strings.Contains(out, "would link ~/.agents/skills/example -> ~/.claude/skills/example") {
		t.Fatalf("global deploy preview: %s %s", out, stderr)
	}
	_, stderr, code = run("deploy", "example")
	if code != 0 {
		t.Fatal(stderr)
	}
	info, err := os.Lstat(filepath.Join(home, ".claude/skills/example"))
	if err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("global deploy missed Claude: %v", err)
	}
	out, stderr, code = run("deploy", "--target=claude-code", "--dry-run")
	if code != 0 || strings.Contains(out, "codex/") || !strings.Contains(out, "claude-code/example") {
		t.Fatalf("targeted bulk preview: %s %s", out, stderr)
	}
}

// TestDeployCopyAndDirectInstructions checks copy mode and existing pointers.
func TestDeployCopyAndDirectInstructions(t *testing.T) {
	home, run := fixture(t)
	writeFixture(t, filepath.Join(home, ".agents/instructions/agents.md"), "rules")
	writeFixture(t, filepath.Join(home, ".claude/CLAUDE.md"), "Follow instructions at ~/.agents/instructions/agents.md now.\n")
	writeFixture(t, filepath.Join(home, ".agents/skills/example/SKILL.md"), "original")
	out, stderr, code := run("deploy", "--copy")
	if code != 0 {
		t.Fatal(stderr)
	}
	if !strings.Contains(out, "claude-code/instructions: pointer") {
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
	out, stderr, code := run("deploy", "instructions", "claude-code")
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

// TestAdoptCopiesNewSkillAndDeploysOthers verifies the one-command local workflow.
func TestAdoptCopiesNewSkillAndDeploysOthers(t *testing.T) {
	home, run := fixture(t)
	live := filepath.Join(home, ".claude/skills/example/SKILL.md")
	writeFixture(t, live, "new skill")
	before, err := fingerprint(home)
	if err != nil {
		t.Fatal(err)
	}
	out, stderr, code := run("adopt", "example", "--dry-run")
	if code != 0 || !strings.Contains(out, "would copy") || !strings.Contains(out, "codex: would use the store directly") {
		t.Fatalf("adopt preview: %s %s", out, stderr)
	}
	after, err := fingerprint(home)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("adopt preview changed files: %v", err)
	}
	out, stderr, code = run("adopt", "example")
	if code != 0 || !strings.Contains(out, "pulled example from claude-code") {
		t.Fatalf("adopt: %s %s", out, stderr)
	}
	if string(mustRead(t, filepath.Join(home, ".agents/skills/example/SKILL.md"))) != "new skill" || string(mustRead(t, live)) != "new skill" {
		t.Fatal("adopt did not preserve source and create canonical skill")
	}
	out, stderr, code = run("status")
	if code != 0 || !strings.Contains(out, "codex       direct") || !strings.Contains(out, "claude-code same copy") {
		t.Fatalf("adopt status: %s %s", out, stderr)
	}
}

// TestAdoptPreflightsOtherTarget prevents a partial pull on destination conflict.
func TestAdoptPreflightsOtherTarget(t *testing.T) {
	home, run := fixture(t)
	writeFixture(t, filepath.Join(home, ".claude/skills/example/SKILL.md"), "claude")
	writeFixture(t, filepath.Join(home, ".codex/skills/example/SKILL.md"), "codex")
	_, stderr, code := run("adopt", "example")
	if code == 0 || !strings.Contains(stderr, "multiple local sources") {
		t.Fatalf("expected source ambiguity: %s", stderr)
	}
	_, stderr, code = run("adopt", "example", "claude-code")
	if code == 0 || !strings.Contains(stderr, "differs") || exists(filepath.Join(home, ".agents/skills/example")) {
		t.Fatalf("expected preflight conflict: %s", stderr)
	}
	out, stderr, code := run("adopt", "example", "claude-code", "--replace", "--dry-run")
	if code != 0 || !strings.Contains(out, "would backup") || exists(filepath.Join(home, ".agents/skills/example")) {
		t.Fatalf("adopt replacement preview: %s %s", out, stderr)
	}
	out, stderr, code = run("adopt", "example", "claude-code", "--replace")
	if code != 0 || !strings.Contains(out, "backup:") || string(mustRead(t, filepath.Join(home, ".agents/skills/example/SKILL.md"))) != "claude" {
		t.Fatalf("adopt replacement: %s %s", out, stderr)
	}
	backups, err := filepath.Glob(filepath.Join(home, ".local/state/agentctl/backups/*/example/SKILL.md"))
	if err != nil || len(backups) != 1 || string(mustRead(t, backups[0])) != "codex" {
		t.Fatalf("adopt backup: %v %v", backups, err)
	}
}

// TestAdoptFromCodexDeploysClaude verifies the other source direction.
func TestAdoptFromCodexDeploysClaude(t *testing.T) {
	home, run := fixture(t)
	writeFixture(t, filepath.Join(home, ".codex/skills/example/SKILL.md"), "codex skill")
	out, stderr, code := run("adopt", "example")
	if code != 0 || !strings.Contains(out, "pulled example from codex") {
		t.Fatalf("adopt: %s %s", out, stderr)
	}
	if string(mustRead(t, filepath.Join(home, ".agents/skills/example/SKILL.md"))) != "codex skill" {
		t.Fatal("missing canonical skill")
	}
	info, err := os.Lstat(filepath.Join(home, ".claude/skills/example"))
	if err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("Claude skill was not linked: %v", err)
	}
}

// TestAdoptInstructions creates the central entrypoint and other agent pointer.
func TestAdoptInstructions(t *testing.T) {
	home, run := fixture(t)
	writeFixture(t, filepath.Join(home, ".codex/AGENTS.md"), "new rules")
	out, stderr, code := run("adopt", "agents.md")
	if code != 0 || !strings.Contains(out, "pulled instructions from codex") {
		t.Fatalf("adopt instructions: %s %s", out, stderr)
	}
	if string(mustRead(t, filepath.Join(home, ".agents/instructions/agents.md"))) != "new rules" {
		t.Fatal("missing central instructions")
	}
	if string(mustRead(t, filepath.Join(home, ".claude/CLAUDE.md"))) != "Follow instructions at ~/.agents/instructions/agents.md now.\n" {
		t.Fatal("missing Claude instruction pointer")
	}
}

// TestExportAndValidation verifies account package contents and path boundaries.
func TestExportAndValidation(t *testing.T) {
	home, run := fixture(t)
	writeFixture(t, filepath.Join(home, ".agents/instructions/agents.md"), "rules")
	writeFixture(t, filepath.Join(home, ".agents/instructions/coding.md"), "coding rules")
	writeFixture(t, filepath.Join(home, ".agents/skills/example/SKILL.md"), "skill")
	out, stderr, code := run("export")
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
	if !exists(filepath.Join(home, ".local/state/agentctl/exports/chatgpt/example.zip")) {
		t.Fatal("default export missed chatgpt")
	}
	out, stderr, code = run("export", "example", "chatgpt")
	if code != 0 || !strings.Contains(out, "chatgpt/example.zip") || strings.Contains(out, "claude-chat") {
		t.Fatalf("selected export: %s %s", out, stderr)
	}
	_, stderr, code = run("pull", "../escape", "claude-code")
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
	_, stderr, code := run("pull", "example", "claude-code")
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
	out, stderr, code = run("diff", "example", "claude-code")
	if code != 0 || !strings.Contains(out, "claude-code same copy") || strings.Contains(out, "runtime") || strings.Contains(out, "cache") {
		t.Fatalf("generated files appeared in diff: %s %s", out, stderr)
	}
	writeFixture(t, filepath.Join(home, ".claude/skills/example/SKILL.md"), "live change")
	out, stderr, code = run("diff", "example", "claude-code")
	if code != 0 || !strings.Contains(out, "claude-code different\n    SKILL.md") || strings.Contains(out, "live change") {
		t.Fatalf("missing file diff: %s %s", out, stderr)
	}
	out, stderr, code = run("diff", "example", "claude-code", "--verbose")
	if code != 0 || !strings.Contains(out, "+live change") {
		t.Fatalf("missing verbose content diff: %s %s", out, stderr)
	}
	writeFixture(t, filepath.Join(home, ".claude/skills/example/SKILL.md"), "skill")
	out, stderr, code = run("deploy", "example", "claude-code")
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
	writeFixture(t, filepath.Join(home, ".agents/skills/changed/references/removed.md"), "removed\n")
	writeFixture(t, filepath.Join(home, ".claude/skills/changed/SKILL.md"), "changed content\n")
	writeFixture(t, filepath.Join(home, ".claude/skills/changed/scripts/helper.sh"), "new helper\n")
	writeFixture(t, filepath.Join(home, ".claude/skills/new/SKILL.md"), "new content\n")
	writeFixture(t, filepath.Join(home, ".agents/skills/missing/SKILL.md"), "missing content\n")
	out, stderr, code := run("diff")
	for _, expected := range []string{
		"Skills:",
		"changed\n  claude-code different\n    SKILL.md\n    references/removed.md\n    scripts/helper.sh",
		"new\n  claude-code new (files in claude-code)\n    SKILL.md",
		"missing\n  claude-code missing (files in store)\n    SKILL.md",
	} {
		if code != 0 || !strings.Contains(out, expected) {
			t.Fatalf("missing %q: %s %s", expected, out, stderr)
		}
	}
	if strings.Contains(out, "/dev/null") || strings.Contains(out, "content") || strings.Contains(out, "diff --git") {
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
	for _, args := range [][]string{{"diff"}, {"diff", "agents.md", "codex"}, {"diff", "instructions", "codex"}} {
		out, stderr, code := run(args...)
		if code != 0 || !strings.Contains(out, "agents.md") || strings.Contains(out, "informational only") || strings.Contains(out, "+live rules") {
			t.Fatalf("diff: %s %s", out, stderr)
		}
	}
	out, stderr, code := run("diff", "instructions", "codex", "-v")
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
		{[]string{"deploy", "--dry-run"}, []string{"would pointer", "conflict:"}, 1},
		{[]string{"deploy", "--dry-run", "--replace"}, []string{"would pointer", "would backup", "would link"}, 0},
		{[]string{"deploy", "--copy", "--dry-run", "--replace"}, []string{"would copy", "would backup"}, 0},
		{[]string{"pull", "example", "claude-code", "--dry-run"}, []string{}, 1},
		{[]string{"pull", "example", "claude-code", "--replace", "--dry-run"}, []string{"would backup", "would copy"}, 0},
		{[]string{"pull", "--dry-run", "new"}, []string{"would copy"}, 0},
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
	_, _, code := run("deploy")
	if code != 1 || exists(filepath.Join(home, ".claude/CLAUDE.md")) || exists(filepath.Join(home, ".codex/AGENTS.md")) {
		t.Fatal("bulk conflict caused partial deployment")
	}
}

// TestBareDiffWithoutDrift gives a clear result for directly used store assets.
func TestBareDiffWithoutDrift(t *testing.T) {
	home, run := fixture(t)
	writeFixture(t, filepath.Join(home, ".agents/skills/example/SKILL.md"), "skill")
	if _, stderr, code := run("deploy", "example", "claude-code"); code != 0 {
		t.Fatal(stderr)
	}
	out, stderr, code := run("diff")
	if code != 0 || out != "No differences.\n" {
		t.Fatalf("diff: %q %s", out, stderr)
	}
}
