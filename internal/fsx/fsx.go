// Package fsx holds filesystem helpers: content hashing, tree copies,
// deterministic ZIP packaging, and path handling.
package fsx

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Ignored reports whether a file or directory name is excluded from hashing,
// copying, and packaging.
func Ignored(name string) bool {
	switch name {
	case ".git", ".DS_Store", "__pycache__", "Thumbs.db", "__MACOSX":
		return true
	}
	return strings.HasSuffix(name, ".pyc")
}

// Entry is one file (or symlink) inside a tree.
type Entry struct {
	Rel  string // slash-separated path relative to the root
	Abs  string
	Mode fs.FileMode
	Size int64
	Link string // symlink target when the entry is a symlink
}

// Walk lists the files under root in sorted order. If root is a file, the
// result is a single entry named after it. Symlinks inside the tree are
// reported, not followed; a symlinked root is resolved first.
func Walk(root string) ([]Entry, error) {
	info, err := os.Stat(root)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return []Entry{{Rel: filepath.Base(root), Abs: root, Mode: info.Mode(), Size: info.Size()}}, nil
	}
	real, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil, err
	}
	var out []Entry
	err = filepath.WalkDir(real, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p == real {
			return nil
		}
		if Ignored(d.Name()) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(real, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if d.Type()&fs.ModeSymlink != 0 {
			t, err := os.Readlink(p)
			if err != nil {
				return err
			}
			out = append(out, Entry{Rel: rel, Abs: p, Mode: fs.ModeSymlink, Link: t})
			return nil
		}
		if d.IsDir() {
			return nil
		}
		fi, err := d.Info()
		if err != nil {
			return err
		}
		out = append(out, Entry{Rel: rel, Abs: p, Mode: fi.Mode(), Size: fi.Size()})
		return nil
	})
	sort.Slice(out, func(i, j int) bool { return out[i].Rel < out[j].Rel })
	return out, err
}

// HashBytes returns the sha256 content hash of b.
func HashBytes(b []byte) string {
	s := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(s[:])
}

// Hash returns a content hash for a file or directory tree. A file hashes to
// HashBytes of its content. A directory hash covers relative paths, the
// executable bit, and file contents, so it is stable across machines.
func Hash(p string) (string, error) {
	info, err := os.Stat(p)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		b, err := os.ReadFile(p)
		if err != nil {
			return "", err
		}
		return HashBytes(b), nil
	}
	entries, err := Walk(p)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	for _, e := range entries {
		if e.Link != "" {
			fmt.Fprintf(h, "L %s %s\n", e.Rel, e.Link)
			continue
		}
		f, err := os.Open(e.Abs)
		if err != nil {
			return "", err
		}
		fh := sha256.New()
		_, err = io.Copy(fh, f)
		f.Close()
		if err != nil {
			return "", err
		}
		x := "-"
		if e.Mode&0o111 != 0 {
			x = "x"
		}
		fmt.Fprintf(h, "F %s %s %x\n", e.Rel, x, fh.Sum(nil))
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil)), nil
}

// Short abbreviates a hash for display.
func Short(h string) string {
	h = strings.TrimPrefix(h, "sha256:")
	if len(h) > 12 {
		return h[:12]
	}
	return h
}

// MatchHash reports whether given (full or an abbreviation of at least seven
// hex digits, with or without the sha256: prefix) identifies full.
func MatchHash(full, given string) bool {
	f := strings.TrimPrefix(full, "sha256:")
	g := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(given), "sha256:"))
	return f != "" && len(g) >= 7 && strings.HasPrefix(f, g)
}

// Exists reports whether p exists (without following a final symlink).
func Exists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}

// IsDir reports whether p is (or links to) a directory.
func IsDir(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}

// IsSymlink reports whether p is a symlink.
func IsSymlink(p string) bool {
	fi, err := os.Lstat(p)
	return err == nil && fi.Mode()&fs.ModeSymlink != 0
}

// LinkTarget returns the absolute target of symlink p (resolved relative to
// its directory, but not evaluated further).
func LinkTarget(p string) (string, error) {
	t, err := os.Readlink(p)
	if err != nil {
		return "", err
	}
	if !filepath.IsAbs(t) {
		t = filepath.Join(filepath.Dir(p), t)
	}
	return filepath.Clean(t), nil
}

// Real returns p with symlinks evaluated when possible, else a cleaned p.
func Real(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return filepath.Clean(p)
}

// SamePath reports whether a and b name the same location.
func SamePath(a, b string) bool {
	if filepath.Clean(a) == filepath.Clean(b) {
		return true
	}
	return Real(a) == Real(b)
}

// Within reports whether p is base or lies inside base.
func Within(base, p string) bool {
	rel, err := filepath.Rel(filepath.Clean(base), filepath.Clean(p))
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

// Expand replaces a leading ~ with home.
func Expand(p, home string) string {
	if p == "~" {
		return home
	}
	if strings.HasPrefix(p, "~/") || strings.HasPrefix(p, `~\`) {
		return filepath.Join(home, p[2:])
	}
	return p
}

// Abbrev replaces a home prefix with ~ for display.
func Abbrev(p, home string) string {
	if p == "" || home == "" {
		return p
	}
	if p == home {
		return "~"
	}
	if Within(home, p) {
		rel, _ := filepath.Rel(home, p)
		return "~" + string(filepath.Separator) + rel
	}
	return p
}

// CopyFile copies one regular file, preserving permission bits.
func CopyFile(src, dst string, mode fs.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode.Perm()|0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// CopyTree copies a file or directory tree to dst, which must not exist.
// Ignored names are skipped; symlinks inside the tree are recreated as-is.
func CopyTree(src, dst string) error {
	if Exists(dst) {
		return fmt.Errorf("%s already exists", dst)
	}
	info, err := os.Stat(src)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return CopyFile(src, dst, info.Mode())
	}
	entries, err := Walk(src)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	for _, e := range entries {
		target := filepath.Join(dst, filepath.FromSlash(e.Rel))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		if e.Link != "" {
			if err := os.Symlink(e.Link, target); err != nil {
				return err
			}
			continue
		}
		if err := CopyFile(e.Abs, target, e.Mode); err != nil {
			return err
		}
	}
	return nil
}

// ReplaceTree copies src over dst by staging a copy next to dst and swapping
// it in, so a failed copy leaves dst untouched.
func ReplaceTree(src, dst string) error {
	stage := dst + ".agentctl-new"
	old := dst + ".agentctl-old"
	os.RemoveAll(stage)
	os.RemoveAll(old)
	if err := CopyTree(src, stage); err != nil {
		os.RemoveAll(stage)
		return err
	}
	if Exists(dst) {
		if err := os.Rename(dst, old); err != nil {
			os.RemoveAll(stage)
			return err
		}
	}
	if err := os.Rename(stage, dst); err != nil {
		os.Rename(old, dst)
		return err
	}
	return os.RemoveAll(old)
}

// WriteFileAtomic writes data to a temp file beside path and renames it.
func WriteFileAtomic(p string, data []byte, perm fs.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(p), "."+filepath.Base(p)+".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Chmod(tmp, perm); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, p)
}

var zipEpoch = time.Date(1980, 1, 1, 0, 0, 0, 0, time.UTC)

// ZipTree packages src into a ZIP at dst with every entry under prefix/.
// Entries are sorted and timestamped at a fixed epoch, so the same content
// always produces the same bytes. Symlinks are resolved to their content.
func ZipTree(src, dst, prefix string) error {
	entries, err := Walk(src)
	if err != nil {
		return err
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, e := range entries {
		abs := e.Abs
		mode := e.Mode
		if e.Link != "" {
			fi, err := os.Stat(abs)
			if err != nil || fi.IsDir() {
				continue // dangling or directory links are not packaged
			}
			mode = fi.Mode()
		}
		name := e.Rel
		if prefix != "" {
			name = path.Join(prefix, e.Rel)
		}
		hdr := &zip.FileHeader{Name: name, Method: zip.Deflate, Modified: zipEpoch}
		hdr.SetMode(mode.Perm())
		w, err := zw.CreateHeader(hdr)
		if err != nil {
			return err
		}
		f, err := os.Open(abs)
		if err != nil {
			return err
		}
		_, err = io.Copy(w, f)
		f.Close()
		if err != nil {
			return err
		}
	}
	if err := zw.Close(); err != nil {
		return err
	}
	return WriteFileAtomic(dst, buf.Bytes(), 0o644)
}

// Unzip extracts src into dst, rejecting entries that escape dst and
// skipping symlinks and macOS resource forks.
func Unzip(src, dst string) error {
	r, err := zip.OpenReader(src)
	if err != nil {
		return err
	}
	defer r.Close()
	for _, f := range r.File {
		name := filepath.FromSlash(f.Name)
		first := strings.SplitN(filepath.ToSlash(name), "/", 2)[0]
		if Ignored(first) {
			continue
		}
		target := filepath.Join(dst, name)
		if !Within(dst, target) {
			return fmt.Errorf("zip entry %q escapes the destination", f.Name)
		}
		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
			continue
		}
		if f.Mode()&fs.ModeSymlink != 0 {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, f.Mode().Perm()|0o600)
		if err != nil {
			rc.Close()
			return err
		}
		_, err = io.Copy(out, rc)
		rc.Close()
		if cerr := out.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// IsExecutable reports whether p is a regular file with an execute bit.
func IsExecutable(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.Mode().IsRegular() && fi.Mode()&0o111 != 0
}

// IsNotExist unwraps err and reports whether it is a not-exist error.
func IsNotExist(err error) bool { return errors.Is(err, fs.ErrNotExist) }

// SameLocation reports whether a and b name the same directory entry,
// resolving symlinks in their parents but not a final symlink itself.
func SameLocation(a, b string) bool {
	return filepath.Base(a) == filepath.Base(b) && Real(filepath.Dir(a)) == Real(filepath.Dir(b))
}

// FileHashes maps each file in a tree (relative slash path) to a hash of
// its content and executable bit; symlinks map to their target.
func FileHashes(root string) (map[string]string, error) {
	entries, err := Walk(root)
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, e := range entries {
		if e.Link != "" {
			out[e.Rel] = "link:" + e.Link
			continue
		}
		b, err := os.ReadFile(e.Abs)
		if err != nil {
			return nil, err
		}
		h := HashBytes(b)
		if e.Mode&0o111 != 0 {
			h += "+x"
		}
		out[e.Rel] = h
	}
	return out, nil
}
