package textdiff

import (
	"strings"
	"testing"
)

func TestUnified(t *testing.T) {
	a := "one\ntwo\nthree\nfour\n"
	b := "one\n2\nthree\nfour\nfive\n"
	got := Unified("a/f", "b/f", a, b, 3)
	want := "--- a/f\n+++ b/f\n@@ -1,4 +1,5 @@\n one\n-two\n+2\n three\n four\n+five\n"
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
	if Unified("a", "b", a, a, 3) != "" {
		t.Fatal("equal inputs should produce no diff")
	}
}

func TestSeparateHunks(t *testing.T) {
	var a, b []string
	for i := 0; i < 30; i++ {
		a = append(a, "line")
		b = append(b, "line")
	}
	b[2], b[25] = "changed", "changed"
	got := Unified("a", "b", strings.Join(a, "\n")+"\n", strings.Join(b, "\n")+"\n", 3)
	if n := strings.Count(got, "@@ -"); n != 2 {
		t.Fatalf("expected 2 hunks, got %d:\n%s", n, got)
	}
	added, removed := Stat(strings.Join(a, "\n"), strings.Join(b, "\n"))
	if added != 2 || removed != 2 {
		t.Fatalf("stat = +%d -%d", added, removed)
	}
}

func TestNoTrailingNewline(t *testing.T) {
	got := Unified("a", "b", "x", "y", 3)
	if !strings.Contains(got, `\ No newline at end of file`) {
		t.Fatalf("missing marker:\n%s", got)
	}
}
