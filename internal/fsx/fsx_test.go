package fsx

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func write(t *testing.T, p, content string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
}

func TestHashIgnoresJunkAndTracksExecBit(t *testing.T) {
	a, b := t.TempDir(), t.TempDir()
	write(t, filepath.Join(a, "SKILL.md"), "x", 0o644)
	write(t, filepath.Join(b, "SKILL.md"), "x", 0o644)
	write(t, filepath.Join(b, ".DS_Store"), "junk", 0o644)
	write(t, filepath.Join(b, ".git", "HEAD"), "ref", 0o644)
	ha, _ := Hash(a)
	hb, _ := Hash(b)
	if ha != hb {
		t.Fatalf("ignored files changed the hash")
	}
	os.Chmod(filepath.Join(b, "SKILL.md"), 0o755)
	if hb2, _ := Hash(b); hb2 == ha {
		t.Fatalf("exec bit not hashed")
	}
}

func TestMatchHash(t *testing.T) {
	full := HashBytes([]byte("hello"))
	if !MatchHash(full, Short(full)) || !MatchHash(full, full) {
		t.Fatal("expected match")
	}
	if MatchHash(full, "abc") || MatchHash(full, "0000000") {
		t.Fatal("unexpected match")
	}
}

func TestZipIsDeterministicAndRoundTrips(t *testing.T) {
	src := t.TempDir()
	write(t, filepath.Join(src, "SKILL.md"), "body", 0o644)
	write(t, filepath.Join(src, "scripts", "run.sh"), "#!/bin/sh\n", 0o755)
	out := t.TempDir()
	z1, z2 := filepath.Join(out, "1.zip"), filepath.Join(out, "2.zip")
	if err := ZipTree(src, z1, "demo"); err != nil {
		t.Fatal(err)
	}
	if err := ZipTree(src, z2, "demo"); err != nil {
		t.Fatal(err)
	}
	b1, _ := os.ReadFile(z1)
	b2, _ := os.ReadFile(z2)
	if !bytes.Equal(b1, b2) {
		t.Fatal("zip output is not deterministic")
	}
	dst := t.TempDir()
	if err := Unzip(z1, dst); err != nil {
		t.Fatal(err)
	}
	h1, _ := Hash(src)
	h2, _ := Hash(filepath.Join(dst, "demo"))
	if h1 != h2 {
		t.Fatal("round trip changed content")
	}
}

func TestUnzipRejectsTraversal(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, _ := zw.Create("../evil.txt")
	w.Write([]byte("x"))
	zw.Close()
	p := filepath.Join(t.TempDir(), "bad.zip")
	os.WriteFile(p, buf.Bytes(), 0o644)
	if err := Unzip(p, t.TempDir()); err == nil {
		t.Fatal("expected traversal to be rejected")
	}
}

func TestSameLocationDoesNotFollowFinalLink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "store", "skill")
	os.MkdirAll(target, 0o755)
	link := filepath.Join(dir, "deploy", "skill")
	os.MkdirAll(filepath.Dir(link), 0o755)
	os.Symlink(target, link)
	if SameLocation(link, target) {
		t.Fatal("a link is not the same location as its target")
	}
	if !SamePath(link, target) {
		t.Fatal("SamePath should resolve links")
	}
}
