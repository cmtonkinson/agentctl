package core

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/cmtonkinson/agentctl/internal/fsx"
	"github.com/cmtonkinson/agentctl/internal/textdiff"
)

// UpdateOptions configures Update.
type UpdateOptions struct {
	Selection
	Assets  []string
	Check   bool // compare revisions only (no fetch for git sources)
	Version string
	DryRun  bool
	Apply   bool
}

// UpdateResult reports one asset's upstream status.
type UpdateResult struct {
	Asset     string       `json:"asset"`
	Source    string       `json:"source,omitempty"`
	Current   string       `json:"current,omitempty"`
	Available string       `json:"available,omitempty"`
	State     string       `json:"state"` // up-to-date | available | local-edits | conflict | updated | no-source | error
	Detail    string       `json:"detail,omitempty"`
	Changes   []FileChange `json:"changes,omitempty"`
}

// Update states.
const (
	UpdateCurrent   = "up-to-date"
	UpdateAvailable = "available"
	UpdateLocal     = "local-edits"
	UpdateConflict  = "conflict"
	UpdateApplied   = "updated"
	UpdateNoSource  = "no-source"
	UpdateError     = "error"
)

// fetchSource re-reads an asset's upstream and returns it as a candidate.
func (a *App) fetchSource(src *Source, kind Kind, version string) (*candidate, func(), error) {
	noop := func() {}
	pick := func(cands []*candidate) (*candidate, error) {
		for _, c := range cands {
			if src.Key != "" && c.Source.Key == src.Key {
				return c, nil
			}
		}
		for _, c := range cands {
			if c.Kind == kind && (src.Name == "" || c.Name == src.Name || len(cands) == 1) {
				return c, nil
			}
		}
		return nil, fmt.Errorf("the source no longer contains %s %s", kind, src.Name)
	}
	switch src.Type {
	case "path":
		if !fsx.Exists(src.Location) {
			return nil, noop, fmt.Errorf("%s no longer exists", a.Abbrev(src.Location))
		}
		if ref := a.managedBy(src.Location); ref != "" {
			return nil, noop, &managedSourceError{a.Abbrev(src.Location), ref}
		}
		cands, err := locate(src.Location, src.Name, kind)
		if err != nil {
			return nil, noop, err
		}
		c, err := pick(cands)
		return c, noop, err
	case "zip":
		cands, cleanup, err := a.zipCandidates(src.Location, kind)
		if err != nil {
			return nil, cleanup, err
		}
		for _, c := range cands {
			if c.Source.Subpath == src.Subpath {
				return c, cleanup, nil
			}
		}
		c, err := pick(cands)
		return c, cleanup, err
	case "git":
		ref := src.Ref
		if version != "" {
			ref = version
		}
		u := src.Location
		if src.Subpath != "" {
			u += "#" + src.Subpath
		}
		cands, cleanup, err := a.gitCandidates(u, ImportOptions{Version: ref, Selection: Selection{Kind: kind}})
		if err != nil {
			return nil, cleanup, err
		}
		c, err := pick(cands)
		return c, cleanup, err
	case "target":
		sel := Selection{Targets: []string{src.Target}, Kind: kind, Scope: src.Scope}
		if src.Project != "" {
			sel.Project = src.Project
		}
		inv, err := a.Inventory(InventoryOptions{Selection: sel, IncludeCache: true})
		if err != nil {
			return nil, noop, err
		}
		match := func(it *Item) bool {
			return it.Name == src.Name && it.Plugin == src.Plugin && (src.Key == "" || it.Key == src.Key) && !it.Cached && it.Status != ItemManaged
		}
		for _, it := range inv.Items {
			if match(it) && it.Path == src.Location {
				c, err := a.itemCandidate(it)
				return c, noop, err
			}
		}
		for _, it := range inv.Items {
			if match(it) {
				c, err := a.itemCandidate(it)
				return c, noop, err
			}
		}
		for _, it := range inv.Items {
			if it.Name == src.Name && it.Path == src.Location && it.Status == ItemManaged {
				return nil, noop, &managedSourceError{a.Abbrev(it.Path), it.StoreRef}
			}
		}
		return nil, noop, fmt.Errorf("%s no longer has %s %s", src.Target, kind, src.Name)
	}
	return nil, noop, fmt.Errorf("unknown source type %q", src.Type)
}

// managedSourceError reports an upstream path that is now one of
// agentctl's own deployments, so reading it would feed the store to itself.
type managedSourceError struct{ path, ref string }

func (e *managedSourceError) Error() string {
	return e.path + " is now deployed by agentctl from " + e.ref + "; the store is its source"
}

func describeSource(a *App, s *Source) string {
	switch s.Type {
	case "git":
		if s.Subpath != "" {
			return s.Location + "#" + s.Subpath
		}
		return s.Location
	case "target":
		loc := s.Target
		if s.Plugin != "" {
			loc += " (plugin " + s.Plugin + ")"
		}
		return loc + " " + a.Abbrev(s.Location)
	case "zip":
		return a.Abbrev(s.Location) + ":" + s.Subpath
	}
	return a.Abbrev(s.Location)
}

// Update checks or applies upstream changes to imported assets. With
// Apply, nothing is changed if any selected asset with an update has
// local edits.
func (a *App) Update(o UpdateOptions) ([]*UpdateResult, error) {
	var assets []*Asset
	if len(o.Assets) > 0 {
		for _, ref := range o.Assets {
			as, err := a.Store.Find(ref, o.Kind)
			if err != nil {
				return nil, err
			}
			assets = append(assets, as)
		}
	} else {
		all, err := a.Store.List()
		if err != nil {
			return nil, err
		}
		for _, as := range all {
			if o.MatchAsset(as) {
				assets = append(assets, as)
			}
		}
	}
	type pending struct {
		as      *Asset
		res     *UpdateResult
		c       *candidate
		staged  string
		hash    string
		cleanup []func()
		merge   *merge
	}
	var results []*UpdateResult
	var todo []*pending
	defer func() {
		for _, p := range todo {
			for _, f := range p.cleanup {
				f()
			}
		}
	}()
	for _, as := range assets {
		res := &UpdateResult{Asset: as.Ref.String()}
		results = append(results, res)
		if as.Meta == nil || as.Meta.Source == nil {
			res.State, res.Detail = UpdateNoSource, "no recorded upstream"
			continue
		}
		src := as.Meta.Source
		res.Source = describeSource(a, src)
		res.Current = as.Meta.Version
		if src.Type == "git" {
			res.Current = shortRev(src.Revision)
			if o.Check && !o.Apply {
				rev, err := a.gitRemoteRevision(src.Location, firstNonEmpty(o.Version, src.Ref))
				if err != nil {
					res.State, res.Detail = UpdateError, err.Error()
					continue
				}
				res.Available = shortRev(rev)
				if rev == src.Revision {
					res.State = UpdateCurrent
				} else {
					res.State, res.Detail = UpdateAvailable, "new upstream revision"
				}
				continue
			}
		}
		c, cleanup, err := a.fetchSource(src, as.Kind, o.Version)
		p := &pending{as: as, res: res, cleanup: []func(){cleanup}}
		todo = append(todo, p)
		var managed *managedSourceError
		if errors.As(err, &managed) {
			res.State, res.Detail = UpdateNoSource, err.Error()
			continue
		}
		if err != nil {
			res.State, res.Detail = UpdateError, err.Error()
			continue
		}
		dir, h, clean, err := stage(c)
		if err == nil && as.Kind == Instructions && fsx.IsDir(as.Path) && c.File != "" {
			clean()
			dir, h, clean, err = stageLegacyInstruction(c.File)
		}
		p.cleanup = append(p.cleanup, clean)
		if err != nil {
			res.State, res.Detail = UpdateError, err.Error()
			continue
		}
		p.c, p.staged, p.hash = c, dir, h
		if c.Source.Revision != "" {
			res.Available = shortRev(c.Source.Revision)
		} else {
			res.Available = c.Version
		}
		p.merge, err = planMerge(as, dir)
		if err != nil {
			res.State, res.Detail = UpdateError, err.Error()
			continue
		}
		mg := p.merge
		switch {
		case !mg.upstreamChanged:
			res.State = UpdateCurrent
			if mg.localChanged {
				res.Detail = "store has local edits"
			}
		case len(mg.conflicts) > 0:
			res.State = UpdateLocal
			res.Detail = "upstream and local edits both changed " + strings.Join(mg.conflicts, ", ") + "; compare with: agentctl diff " + as.Ref.String() + " --against origin"
		default:
			res.State, res.Detail = UpdateAvailable, "upstream content changed"
			if mg.localChanged {
				res.Detail += "; local edits will be kept"
			}
		}
		res.Changes = mg.changes
	}
	if !o.Apply {
		return results, nil
	}
	for _, p := range todo {
		if p.res.State == UpdateLocal {
			return results, fmt.Errorf("stopped: %s has local edits and an upstream change; nothing was updated", p.as.Ref)
		}
	}
	for _, p := range todo {
		if p.c == nil {
			continue
		}
		src := p.as.Meta.Source
		revChanged := p.c.Source.Revision != "" && p.c.Source.Revision != src.Revision
		if p.res.State != UpdateAvailable && !revChanged {
			continue
		}
		if p.res.State == UpdateAvailable {
			if err := applyMerge(p.as.Path, p.staged, p.merge); err != nil {
				p.res.State, p.res.Detail = UpdateError, err.Error()
				continue
			}
			p.res.State = UpdateApplied
		}
		now := a.Now().UTC()
		m := p.as.Meta
		m.SourceHash, m.ImportedHash = p.hash, p.hash
		m.Files, _ = fsx.FileHashes(p.staged)
		if p.as.Kind == Instructions && !fsx.IsDir(p.as.Path) {
			m.Files = nil
		}
		m.UpdatedAt = &now
		if p.c.Version != "" {
			m.Version = p.c.Version
		}
		if p.c.Source.Revision != "" {
			m.Source.Revision = p.c.Source.Revision
			if o.Version != "" {
				m.Source.Ref = o.Version
			}
		}
		if err := a.Store.SaveMeta(p.as.Ref, m); err != nil {
			return results, err
		}
	}
	return results, nil
}

// stageLegacyInstruction preserves the directory layout of an existing
// instruction asset while checking its upstream file.
func stageLegacyInstruction(file string) (string, string, func(), error) {
	tmp, err := os.MkdirTemp("", "agentctl-instruction-")
	if err != nil {
		return "", "", func() {}, err
	}
	clean := func() { os.RemoveAll(tmp) }
	dir := filepath.Join(tmp, "asset")
	if err := fsx.CopyFile(file, filepath.Join(dir, "AGENTS.md"), 0o644); err != nil {
		clean()
		return "", "", func() {}, err
	}
	h, err := fsx.Hash(dir)
	if err != nil {
		clean()
		return "", "", func() {}, err
	}
	return dir, h, clean, nil
}

// merge is a file-level three-way merge of upstream changes into the store.
type merge struct {
	take, del, conflicts          []string
	upstreamChanged, localChanged bool
	changes                       []FileChange
}

// planMerge compares the last imported files (base), the store (local),
// and a fresh upstream copy. Upstream changes to files untouched locally
// are taken; local changes are kept; files changed on both sides conflict.
func planMerge(as *Asset, upstream string) (*merge, error) {
	if as.Kind == Instructions && !fsx.IsDir(as.Path) {
		local, err := fsx.Hash(as.Path)
		if err != nil {
			return nil, err
		}
		up, err := fsx.Hash(upstream)
		if err != nil {
			return nil, err
		}
		base := as.Meta.ImportedHash
		m := &merge{localChanged: local != base, upstreamChanged: up != base}
		if up != local && up != base {
			status := "modified"
			if local != base {
				status = "conflict"
				m.conflicts = []string{filepath.Base(as.Path)}
			} else {
				m.take = []string{filepath.Base(as.Path)}
			}
			m.changes = []FileChange{fileChange(filepath.Base(as.Path), status, as.Path, upstream)}
		}
		return m, nil
	}
	local, err := fsx.FileHashes(as.Path)
	if err != nil {
		return nil, err
	}
	up, err := fsx.FileHashes(upstream)
	if err != nil {
		return nil, err
	}
	base := as.Meta.Files
	if base == nil {
		// Older records lack per-file hashes; an unedited store is the base.
		if h, _ := as.Hash(); h == as.Meta.ImportedHash {
			base = local
		}
	}
	keys := map[string]bool{}
	for _, m := range []map[string]string{base, local, up} {
		for k := range m {
			keys[k] = true
		}
	}
	m := &merge{}
	for _, rel := range sortedKeys(keys) {
		b, hb := base[rel]
		l, hl := local[rel]
		u, hu := up[rel]
		same := func(x string, hx bool, y string, hy bool) bool { return hx == hy && x == y }
		if !same(b, hb, l, hl) {
			m.localChanged = true
		}
		if !same(b, hb, u, hu) {
			m.upstreamChanged = true
		}
		switch {
		case same(l, hl, u, hu), same(b, hb, u, hu):
			continue
		case same(b, hb, l, hl) && hu:
			m.take = append(m.take, rel)
			status := "modified"
			if !hl {
				status = "added"
			}
			m.changes = append(m.changes, fileChange(rel, status, filepath.Join(as.Path, rel), filepath.Join(upstream, rel)))
		case same(b, hb, l, hl):
			m.del = append(m.del, rel)
			m.changes = append(m.changes, fileChange(rel, "removed", filepath.Join(as.Path, rel), ""))
		default:
			m.conflicts = append(m.conflicts, rel)
			m.changes = append(m.changes, fileChange(rel, "conflict", filepath.Join(as.Path, rel), filepath.Join(upstream, rel)))
		}
	}
	return m, nil
}

func fileChange(rel, status, oldPath, newPath string) FileChange {
	var ob, nb []byte
	if oldPath != "" {
		ob, _ = os.ReadFile(oldPath)
	}
	if newPath != "" {
		nb, _ = os.ReadFile(newPath)
	}
	fc := FileChange{Path: rel, Status: status}
	if !isBinary(ob) && !isBinary(nb) {
		fc.Added, fc.Removed = textdiff.Stat(string(ob), string(nb))
	}
	return fc
}

// applyMerge applies the planned file changes to the store in place, so
// files outside the merge (including a .git directory) are left alone.
// Every upstream file is already staged, so only local I/O can fail here.
func applyMerge(dst, upstream string, m *merge) error {
	if !fsx.IsDir(dst) {
		if len(m.take) == 0 {
			return nil
		}
		b, err := os.ReadFile(upstream)
		if err != nil {
			return err
		}
		return fsx.WriteFileAtomic(dst, b, 0o644)
	}
	for _, rel := range m.del {
		if err := os.Remove(filepath.Join(dst, filepath.FromSlash(rel))); err != nil && !fsx.IsNotExist(err) {
			return err
		}
	}
	for _, rel := range m.take {
		src := filepath.Join(upstream, filepath.FromSlash(rel))
		out := filepath.Join(dst, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
			return err
		}
		if fsx.IsSymlink(src) {
			t, err := os.Readlink(src)
			if err != nil {
				return err
			}
			os.Remove(out)
			if err := os.Symlink(t, out); err != nil {
				return err
			}
			continue
		}
		b, err := os.ReadFile(src)
		if err != nil {
			return err
		}
		fi, err := os.Stat(src)
		if err != nil {
			return err
		}
		if fsx.IsSymlink(out) {
			os.Remove(out)
		}
		if err := fsx.WriteFileAtomic(out, b, fi.Mode().Perm()); err != nil {
			return err
		}
	}
	return nil
}

func shortRev(r string) string {
	if len(r) > 12 {
		return r[:12]
	}
	return r
}

// trashAsset moves a store asset and its meta into machine-local trash.
func (a *App) trashAsset(as *Asset) (string, error) {
	dst := filepath.Join(a.TrashDir(), a.Now().UTC().Format("20060102T150405Z"), string(as.Kind), as.Name)
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return "", err
	}
	if err := os.Rename(as.Path, dst); err != nil {
		return "", err
	}
	if b, err := os.ReadFile(a.Store.metaPath(as.Ref)); err == nil {
		os.WriteFile(dst+".meta.json", b, 0o644)
	}
	return dst, a.Store.RemoveMeta(as.Ref)
}
