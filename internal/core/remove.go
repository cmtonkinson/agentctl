package core

import (
	"fmt"
	"os"
	"strings"

	"github.com/cmtonkinson/agentctl/internal/fsx"
)

// RemoveOptions configures Remove.
type RemoveOptions struct {
	Targets []string
	Project string
	Store   bool
	DryRun  bool
}

// RemoveAction is one step of a removal.
type RemoveAction struct {
	Target  string   `json:"target,omitempty"`
	Project string   `json:"project,omitempty"`
	Path    string   `json:"path,omitempty"`
	Action  string   `json:"action"` // removed | rewritten | unassigned | trashed | manual | kept | refused (dry run: would ...)
	Detail  string   `json:"detail,omitempty"`
	Steps   []string `json:"steps,omitempty"`
}

// Remove removes an asset's managed deployments from targets and/or the
// asset itself from the store. It refuses to touch anything agentctl did
// not deploy or that changed since deployment.
func (a *App) Remove(ref string, o RemoveOptions) ([]*RemoveAction, error) {
	if len(o.Targets) == 0 && !o.Store {
		return nil, fmt.Errorf("remove needs a destination: --target TARGET or --store")
	}
	as, err := a.Store.Find(ref, "")
	if err != nil {
		return nil, err
	}
	project := a.ProjectKey(o.Project)
	var out []*RemoveAction
	refused := false
	for _, t := range o.Targets {
		if TargetByName(t) == nil {
			return nil, fmt.Errorf("unknown target %q", t)
		}
		acts, ok := a.removeFromTarget(as, t, project, o.DryRun)
		out = append(out, acts...)
		refused = refused || !ok
	}
	if refused {
		if !o.DryRun {
			a.SaveState()
		}
		return out, fmt.Errorf("some destinations were left in place; see above")
	}
	if !o.DryRun && len(o.Targets) > 0 {
		if err := a.SaveConfig(); err != nil {
			return out, err
		}
		if err := a.SaveState(); err != nil {
			return out, err
		}
	}
	if o.Store {
		acts, err := a.removeFromStore(as, o.Targets, o.DryRun)
		out = append(out, acts...)
		if err != nil {
			return out, err
		}
	}
	return out, nil
}

func verb(dry bool, done string) string {
	if !dry {
		return done
	}
	return map[string]string{"removed": "would remove", "unassigned": "would unassign", "trashed": "would trash"}[done]
}

func (a *App) removeFromTarget(as *Asset, target, project string, dry bool) ([]*RemoveAction, bool) {
	ref := as.Ref.String()
	recs := a.State.ForAsset(ref, target, project)
	assigned := contains(a.Config.Assignments(target, project), ref)
	ack := a.State.LatestAck(target, project, ref)
	units := a.Plan(target, project, as, "")
	removeSteps := func(key string) []string {
		for _, u := range units {
			if u.Key == key || key == "" {
				return u.removeSteps
			}
		}
		return nil
	}
	var out []*RemoveAction
	act := func(path, action, detail string, steps []string) {
		out = append(out, &RemoveAction{Target: target, Project: project, Path: a.Abbrev(path), Action: action, Detail: detail, Steps: steps})
	}
	if len(recs) == 0 && !assigned && ack == nil {
		act("", "refused", "not assigned or deployed to "+target, nil)
		return out, false
	}
	ok := true
	var drop []*Deployment
	for _, rec := range recs {
		shared := false
		for _, other := range a.State.ForDest(rec.Dest, rec.Key) {
			if other != rec && !(other.Target == target && other.Project == project && other.Asset == ref) && !(as.Kind == Instructions && other.Target == target) {
				shared = true
			}
		}
		if shared {
			act(rec.Dest, "kept", "shared with another deployment", nil)
			drop = append(drop, rec)
			continue
		}
		switch rec.Method {
		case MethodLink:
			switch {
			case !fsx.Exists(rec.Dest):
				act(rec.Dest, "removed", "already gone", nil)
			case fsx.IsSymlink(rec.Dest) && linksIntoStore(a, rec.Dest):
				if !dry {
					if err := os.Remove(rec.Dest); err != nil {
						act(rec.Dest, "refused", err.Error(), nil)
						ok = false
						continue
					}
				}
				act(rec.Dest, verb(dry, "removed"), "link", nil)
			default:
				act(rec.Dest, "refused", "no longer a link to the store", nil)
				ok = false
				continue
			}
		case MethodCopy, MethodGenerate:
			if as.Kind == Instructions {
				if a.removeInstruction(as, rec, target, project, dry, act) {
					drop = append(drop, rec)
				} else {
					ok = false
				}
				continue
			}
			h, err := fsx.Hash(rec.Dest)
			switch {
			case !fsx.Exists(rec.Dest):
				act(rec.Dest, "removed", "already gone", nil)
			case err == nil && h == rec.DestHash:
				if !dry {
					if err := os.RemoveAll(rec.Dest); err != nil {
						act(rec.Dest, "refused", err.Error(), nil)
						ok = false
						continue
					}
				}
				act(rec.Dest, verb(dry, "removed"), "copy", nil)
			default:
				act(rec.Dest, "refused", "modified after deployment", nil)
				ok = false
				continue
			}
		case MethodPackage:
			if !dry {
				os.Remove(rec.Dest)
			}
			act(rec.Dest, "manual", "package deleted; remove the uploaded copy yourself", removeSteps(rec.Key))
		case MethodConfig:
			have, found, err := readJSONEntry(rec.Dest, rec.Key)
			switch {
			case err != nil:
				act(rec.Dest, "refused", err.Error(), nil)
				ok = false
				continue
			case !found:
				act(rec.Dest, "removed", "entry already gone", nil)
			case ownershipHash(have) == rec.DestHash:
				if !dry {
					if err := editJSONEntry(rec.Dest, rec.Key, nil); err != nil {
						act(rec.Dest, "refused", err.Error(), nil)
						ok = false
						continue
					}
				}
				act(rec.Dest, verb(dry, "removed"), rec.Key, nil)
			default:
				act(rec.Dest, "refused", rec.Key+" was modified after deployment", nil)
				ok = false
				continue
			}
		case MethodManual:
			act(rec.Dest, "manual", "remove it with the client", removeSteps(rec.Key))
		}
		drop = append(drop, rec)
	}
	if len(recs) == 0 && ack != nil {
		act("", "manual", "acknowledged upload; remove it from the account yourself", removeSteps(""))
	}
	if !ok {
		return out, false
	}
	if !dry {
		for _, d := range drop {
			a.State.Drop(d)
		}
		a.State.DropAcks(target, project, ref)
		if len(a.Config.Unassign(target, project, ref)) > 0 {
			act("", "unassigned", "", nil)
		}
	} else if assigned {
		act("", "would unassign", "", nil)
	}
	return out, true
}

func linksIntoStore(a *App, p string) bool {
	t, err := fsx.LinkTarget(p)
	return err == nil && fsx.Within(a.Store.Root, t)
}

// removeInstruction drops one asset from a composed instructions file,
// rewriting it from the remaining assets or deleting it when none remain.
func (a *App) removeInstruction(as *Asset, rec *Deployment, target, project string, dry bool, act func(string, string, string, []string)) bool {
	ref := as.Ref.String()
	if !fsx.Exists(rec.Dest) {
		act(rec.Dest, "removed", "already gone", nil)
		return true
	}
	if h, _ := fsx.Hash(rec.Dest); h != rec.DestHash && !fsx.IsSymlink(rec.Dest) {
		act(rec.Dest, "refused", "modified after deployment", nil)
		return false
	}
	var remaining *Asset
	for _, r := range a.Config.Assignments(target, project) {
		if r == ref {
			continue
		}
		if pr, err := ParseRef(r); err == nil && pr.Kind == Instructions {
			if other, err := a.Store.Load(pr); err == nil {
				remaining = other
				break
			}
		}
	}
	if remaining == nil {
		if !dry {
			if err := os.Remove(rec.Dest); err != nil {
				act(rec.Dest, "refused", err.Error(), nil)
				return false
			}
		}
		act(rec.Dest, verb(dry, "removed"), "instructions file", nil)
		return true
	}
	if dry {
		act(rec.Dest, "would rewrite", "without "+ref, nil)
		return true
	}
	a.Config.Unassign(target, project, ref)
	for _, u := range a.Plan(target, project, remaining, "") {
		e := a.Evaluate(u)
		if err := a.Apply(e, false); err != nil {
			a.Config.Assign(target, project, ref)
			act(rec.Dest, "refused", err.Error(), nil)
			return false
		}
	}
	act(rec.Dest, "rewritten", "without "+ref, nil)
	return true
}

func (a *App) removeFromStore(as *Asset, clearedTargets []string, dry bool) ([]*RemoveAction, error) {
	ref := as.Ref.String()
	var blockers []string
	for _, d := range a.State.ForAsset(ref, "", "") {
		blockers = append(blockers, Scoped{d.Target, d.Project}.String())
	}
	if len(blockers) > 0 && !dry {
		return nil, fmt.Errorf("%s is still deployed to %s; remove those first (agentctl remove %s --target TARGET)", ref, strings.Join(dedupe(blockers), ", "), ref)
	}
	all, _ := a.Store.List()
	var dependents []string
	for _, other := range all {
		if other.Meta != nil && other.Meta.Requires != nil && contains(other.Meta.Requires.Assets, ref) {
			dependents = append(dependents, other.Ref.String())
		}
	}
	if len(dependents) > 0 {
		return nil, fmt.Errorf("%s is required by %s", ref, strings.Join(dependents, ", "))
	}
	var out []*RemoveAction
	for _, s := range a.Assigned(ref) {
		out = append(out, &RemoveAction{Target: s.Target, Project: s.Project, Action: verb(dry, "unassigned")})
		if !dry {
			a.Config.Unassign(s.Target, s.Project, ref)
		}
	}
	if dry {
		out = append(out, &RemoveAction{Path: a.Abbrev(as.Path), Action: "would trash", Detail: "moved to " + a.Abbrev(a.Store.Internal("trash"))})
		return out, nil
	}
	if err := a.SaveConfig(); err != nil {
		return out, err
	}
	dst, err := a.trashAsset(as)
	if err != nil {
		return out, err
	}
	out = append(out, &RemoveAction{Path: a.Abbrev(as.Path), Action: "trashed", Detail: "moved to " + a.Abbrev(dst)})
	return out, nil
}

func dedupe(s []string) []string {
	var out []string
	for _, x := range s {
		if !contains(out, x) {
			out = append(out, x)
		}
	}
	return out
}
