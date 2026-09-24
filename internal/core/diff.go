package core

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/cmtonkinson/agentctl/internal/fsx"
	"github.com/cmtonkinson/agentctl/internal/textdiff"
)

// FileChange is one differing file between two trees.
type FileChange struct {
	Path    string `json:"path"`
	Status  string `json:"status"` // added | removed | modified | mode
	Added   int    `json:"added"`
	Removed int    `json:"removed"`
	Diff    string `json:"diff,omitempty"`
}

// CompareTrees compares old against new (files or directories). With
// withDiff, modified text files carry a unified diff.
func CompareTrees(oldRoot, newRoot string, withDiff bool) ([]FileChange, error) {
	if !fsx.IsDir(oldRoot) && !fsx.IsDir(newRoot) {
		ob, err1 := os.ReadFile(oldRoot)
		nb, err2 := os.ReadFile(newRoot)
		if err1 != nil && err2 != nil {
			return nil, err1
		}
		return compareBytes(filepath.Base(newRoot), ob, nb, err1 == nil, err2 == nil, withDiff), nil
	}
	index := func(root string) map[string]fsx.Entry {
		m := map[string]fsx.Entry{}
		if !fsx.IsDir(root) {
			return m
		}
		entries, _ := fsx.Walk(root)
		for _, e := range entries {
			m[e.Rel] = e
		}
		return m
	}
	om, nm := index(oldRoot), index(newRoot)
	var names []string
	for k := range om {
		names = append(names, k)
	}
	for k := range nm {
		if _, ok := om[k]; !ok {
			names = append(names, k)
		}
	}
	sort.Strings(names)
	var out []FileChange
	for _, n := range names {
		o, hasO := om[n]
		nw, hasN := nm[n]
		var ob, nb []byte
		if hasO {
			ob = readEntry(o)
		}
		if hasN {
			nb = readEntry(nw)
		}
		changes := compareBytes(n, ob, nb, hasO, hasN, withDiff)
		if len(changes) == 0 && hasO && hasN && (o.Mode&0o111 != 0) != (nw.Mode&0o111 != 0) {
			changes = []FileChange{{Path: n, Status: "mode"}}
		}
		out = append(out, changes...)
	}
	return out, nil
}

func readEntry(e fsx.Entry) []byte {
	if e.Link != "" {
		return []byte("symlink → " + e.Link + "\n")
	}
	b, _ := os.ReadFile(e.Abs)
	return b
}

func isBinary(b []byte) bool { return bytes.IndexByte(b, 0) >= 0 }

func compareBytes(name string, ob, nb []byte, hasO, hasN, withDiff bool) []FileChange {
	if hasO && hasN && bytes.Equal(ob, nb) {
		return nil
	}
	fc := FileChange{Path: name, Status: "modified"}
	switch {
	case !hasO:
		fc.Status = "added"
	case !hasN:
		fc.Status = "removed"
	}
	if isBinary(ob) || isBinary(nb) {
		if withDiff {
			fc.Diff = "Binary files differ\n"
		}
		return []FileChange{fc}
	}
	fc.Added, fc.Removed = textdiff.Stat(string(ob), string(nb))
	if withDiff {
		oldName, newName := "a/"+name, "b/"+name
		if !hasO {
			oldName = "/dev/null"
		}
		if !hasN {
			newName = "/dev/null"
		}
		fc.Diff = textdiff.Unified(oldName, newName, string(ob), string(nb), 3)
	}
	return []FileChange{fc}
}

// DiffOptions configures Diff.
type DiffOptions struct {
	Selection
	Asset   string
	Against string // origin | target
	Stat    bool
}

// DiffResult compares the store with an origin or a deployed copy.
// Changes read from the other copy (a/) to the store (b/).
type DiffResult struct {
	Asset   string       `json:"asset"`
	Against string       `json:"against"`
	Target  string       `json:"target,omitempty"`
	Project string       `json:"project,omitempty"`
	Other   string       `json:"other,omitempty"`
	State   string       `json:"state"` // identical | differs | linked | not-deployed | unavailable
	Detail  string       `json:"detail,omitempty"`
	Changes []FileChange `json:"changes,omitempty"`
}

// Diff compares canonical assets with their origin or deployed copies.
func (a *App) Diff(o DiffOptions) ([]*DiffResult, error) {
	if o.Against == "" {
		o.Against = "target"
	}
	if o.Against != "origin" && o.Against != "target" {
		return nil, fmt.Errorf("--against must be origin or target")
	}
	var assets []*Asset
	if o.Asset != "" {
		as, err := a.Store.Find(o.Asset, o.Kind)
		if err != nil {
			return nil, err
		}
		assets = []*Asset{as}
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
	var out []*DiffResult
	for _, as := range assets {
		if o.Against == "origin" {
			if as.Meta == nil || as.Meta.Source == nil {
				if o.Asset != "" {
					out = append(out, &DiffResult{Asset: as.Ref.String(), Against: "origin", State: "unavailable", Detail: "no recorded upstream"})
				}
				continue
			}
			out = append(out, a.diffOrigin(as, !o.Stat))
			continue
		}
		scopes := a.diffScopes(as, o)
		if len(scopes) == 0 && o.Asset != "" {
			return nil, fmt.Errorf("%s is not assigned to a target; name one with --target", as.Ref)
		}
		for _, s := range scopes {
			for _, u := range a.Plan(s.Target, s.Project, as, "") {
				out = append(out, a.diffUnit(as, u, !o.Stat))
			}
		}
	}
	return out, nil
}

func (a *App) diffScopes(as *Asset, o DiffOptions) []Scoped {
	project := a.ProjectKey(o.Project)
	if len(o.Targets) > 0 {
		var out []Scoped
		for _, t := range o.Targets {
			out = append(out, Scoped{t, project})
		}
		return out
	}
	var out []Scoped
	for _, s := range a.Assigned(as.Ref.String()) {
		if (project == "" || s.Project == project) && a.Config.Enabled(s.Target) {
			out = append(out, s)
		}
	}
	return out
}

func (a *App) diffOrigin(as *Asset, withDiff bool) *DiffResult {
	r := &DiffResult{Asset: as.Ref.String(), Against: "origin", Other: describeSource(a, as.Meta.Source)}
	c, cleanup, err := a.fetchSource(as.Meta.Source, as.Kind, "")
	defer cleanup()
	if err != nil {
		r.State, r.Detail = "unavailable", err.Error()
		return r
	}
	dir, _, clean, err := stage(c)
	defer clean()
	if err != nil {
		r.State, r.Detail = "unavailable", err.Error()
		return r
	}
	r.Changes, _ = CompareTrees(dir, as.Path, withDiff)
	r.State = "identical"
	if len(r.Changes) > 0 {
		r.State = "differs"
	}
	return r
}

func (a *App) diffUnit(as *Asset, u *Unit, withDiff bool) *DiffResult {
	r := &DiffResult{Asset: as.Ref.String(), Against: "target", Target: u.Target, Project: u.Project, Other: a.Abbrev(u.Dest)}
	finish := func(ch []FileChange) *DiffResult {
		r.Changes = ch
		r.State = "identical"
		if len(ch) > 0 {
			r.State = "differs"
		}
		return r
	}
	if u.blocked != "" {
		r.State, r.Detail = "unavailable", u.blocked
		return r
	}
	switch u.Method {
	case MethodInPlace:
		r.State, r.Detail = "linked", "read directly from the store"
		return r
	case MethodLink:
		if t, err := fsx.LinkTarget(u.Dest); err == nil && fsx.SamePath(t, u.Source) {
			r.State, r.Detail = "linked", "symlink to the store"
			return r
		}
	}
	switch u.Method {
	case MethodLink, MethodCopy, MethodGenerate:
		if !fsx.Exists(u.Dest) {
			r.State, r.Detail = "not-deployed", ""
			return r
		}
		if u.content != nil {
			cur, _ := os.ReadFile(u.Dest)
			return finish(compareBytes(filepath.Base(u.Dest), cur, u.content, true, true, withDiff))
		}
		ch, _ := CompareTrees(u.Dest, u.Source, withDiff)
		return finish(ch)
	case MethodPackage:
		ack := a.State.LatestAck(u.Target, u.Project, u.Asset)
		if ack != nil {
			r.Detail = "account copy acknowledged at " + shortHash(ack.Hash) + "; comparing with the last package built"
			if hashMatches(u.Want, ack.Hash) {
				r.State, r.Detail = "identical", "acknowledged upload matches the store"
				return r
			}
		}
		if !fsx.Exists(u.Dest) {
			r.State = "not-deployed"
			if ack != nil {
				r.State, r.Detail = "differs", fmt.Sprintf("acknowledged %s, store is %s; the uploaded copy cannot be read back", shortHash(ack.Hash), shortHash(u.Want))
			}
			return r
		}
		if u.format == "text" {
			cur, _ := os.ReadFile(u.Dest)
			return finish(compareBytes(filepath.Base(u.Dest), cur, u.content, true, true, withDiff))
		}
		tmp, err := os.MkdirTemp("", "agentctl-pkg-")
		if err != nil {
			r.State, r.Detail = "unavailable", err.Error()
			return r
		}
		defer os.RemoveAll(tmp)
		if err := fsx.Unzip(u.Dest, tmp); err != nil {
			r.State, r.Detail = "unavailable", err.Error()
			return r
		}
		ch, _ := CompareTrees(filepath.Join(tmp, as.Name), as.Path, withDiff)
		return finish(ch)
	case MethodConfig, MethodManual:
		var have json.RawMessage
		var found bool
		var err error
		switch {
		case u.Method == MethodConfig:
			have, found, err = readJSONEntry(u.Dest, u.Key)
		case u.verify != nil:
			found, _, have, err = u.verify()
		default:
			r.State, r.Detail = "unavailable", "account configuration cannot be read back"
			return r
		}
		if err != nil {
			r.State, r.Detail = "unavailable", err.Error()
			return r
		}
		if !found {
			r.State = "not-deployed"
			return r
		}
		if mcpMatch(u.value, have) {
			r.State = "identical"
			return r
		}
		return finish(compareBytes(u.Key, prettyJSON(have), prettyJSON(u.value), true, true, withDiff))
	}
	r.State = "unavailable"
	return r
}

func prettyJSON(raw json.RawMessage) []byte {
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return raw
	}
	b, _ := json.MarshalIndent(v, "", "  ")
	return append(b, '\n')
}
