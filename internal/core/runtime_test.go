package core

import (
	"os"
	"path/filepath"
	"testing"
)

// TestRuntimeStateLivesOutsideStore keeps host records out of dotfiles source.
func TestRuntimeStateLivesOutsideStore(t *testing.T) {
	e := newEnv(t)
	if got := e.a.statePath(); filepath.HasPrefix(got, e.a.Store.Root) {
		t.Fatalf("state is inside source store: %s", got)
	}
	if got := e.a.OutputDir(); filepath.HasPrefix(got, e.a.Store.Root) {
		t.Fatalf("packages are inside source store: %s", got)
	}
	e.a.State.AddAck(&Ack{Target: "claude-chat", Asset: "skills/example", Hash: "abc", At: e.a.Now().UTC()})
	if err := e.a.SaveState(); err != nil {
		t.Fatal(err)
	}
	e.reopen()
	if e.a.State.LatestAck("claude-chat", "", "skills/example") == nil {
		t.Fatal("machine-local state did not persist")
	}
	if _, err := os.Stat(filepath.Join(e.a.Store.Root, ".agentctl", "state.json")); !os.IsNotExist(err) {
		t.Fatalf("source store acquired a state file: %v", err)
	}
}
