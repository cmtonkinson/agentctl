package core

import (
	"os"
	"testing"
)

// TestExistingInstructionPointer preserves an intentional client pointer.
func TestExistingInstructionPointer(t *testing.T) {
	e := newEnv(t)
	e.write(".agents/instructions/agents.md", "Follow general.md.\n")
	e.write(".codex/AGENTS.md", "Follow instructions at ~/.agents/instructions/agents.md now.\n")
	as, err := e.a.Store.Find("instructions/agents", "")
	if err != nil {
		t.Fatal(err)
	}
	e.a.Config.Assign("codex", "", as.Ref.String())
	u := e.a.Plan("codex", "", as, "")[0]
	if u.Method != MethodInPlace || e.a.Evaluate(u).State != StateOK {
		t.Fatalf("existing pointer should be recognized: %+v", u)
	}
	if _, err := e.a.Deploy(DeployOptions{All: true, Selection: Selection{Targets: []string{"codex"}}}); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(e.a.TargetPaths("codex")["instructions"])
	if err != nil || string(got) != "Follow instructions at ~/.agents/instructions/agents.md now.\n" {
		t.Fatalf("pointer was changed: %q, %v", got, err)
	}
}
