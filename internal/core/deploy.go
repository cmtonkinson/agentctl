package core

import (
	"fmt"
	"sort"
)

// DeployOptions configures Deploy and Status.
type DeployOptions struct {
	Selection
	Assets []string // explicit refs
	All    bool
	Method string
	DryRun bool
	Check  bool
	Adopt  bool
	Only   string // status filter
}

// DeployResult is the outcome of Deploy or Status.
type DeployResult struct {
	Evals    []*Eval  `json:"results"`
	Assigned []string `json:"assigned,omitempty"`
	Notes    []string `json:"notes,omitempty"`
}

// Counts tallies states.
func (r *DeployResult) Counts() map[string]int {
	c := map[string]int{}
	for _, e := range r.Evals {
		c[e.State]++
	}
	return c
}

// NeedsAction reports whether any result is not ok.
func (r *DeployResult) NeedsAction() bool {
	for _, e := range r.Evals {
		if e.State != StateOK {
			return true
		}
	}
	return false
}

// Failed reports whether any apply failed.
func (r *DeployResult) Failed() bool {
	for _, e := range r.Evals {
		if e.Error != "" {
			return true
		}
	}
	return false
}

type work struct {
	target, project string
	asset           *Asset
}

// selectWork resolves which (target, asset) pairs a deploy or status covers.
func (a *App) selectWork(opts DeployOptions, explicitTargetsOnly bool) ([]work, []string, error) {
	project := ""
	if opts.Project != "" {
		project = a.ProjectKey(opts.Project)
	}
	var out []work
	var notes []string
	if len(opts.Assets) > 0 {
		for _, ref := range opts.Assets {
			as, err := a.Store.Find(ref, opts.Kind)
			if err != nil {
				return nil, nil, err
			}
			targets := opts.Targets
			if len(targets) == 0 && opts.AllTargets {
				targets, _ = a.SelectedTargets(opts.Selection)
			}
			if len(targets) == 0 {
				for _, s := range a.Assigned(as.Ref.String()) {
					if s.Project == project && a.Config.Enabled(s.Target) {
						targets = append(targets, s.Target)
					}
				}
				if len(targets) == 0 {
					return nil, nil, fmt.Errorf("%s is not assigned to any enabled target; name one with --target or assign it with `agentctl target assign`", as.Ref)
				}
			}
			for _, t := range targets {
				if TargetByName(t) == nil {
					return nil, nil, fmt.Errorf("unknown target %q", t)
				}
				out = append(out, work{t, project, as})
			}
		}
		return out, notes, nil
	}
	if !opts.All && explicitTargetsOnly {
		return nil, nil, fmt.Errorf("name assets to deploy, or use --all for every assigned asset")
	}
	targets, err := a.SelectedTargets(opts.Selection)
	if err != nil {
		return nil, nil, err
	}
	scopes := []string{project}
	if project == "" && opts.Scope != ScopeUser && opts.Scope != ScopeAccount && !explicitTargetsOnly {
		scopes = append(scopes, a.Config.ProjectPaths()...)
	}
	if opts.Scope == ScopeProject && project == "" {
		scopes = a.Config.ProjectPaths()
	}
	for _, p := range scopes {
		for _, t := range targets {
			for _, ref := range a.Config.Assignments(t, p) {
				r, err := ParseRef(ref)
				if err != nil {
					notes = append(notes, fmt.Sprintf("%s: ignoring invalid assignment %q", t, ref))
					continue
				}
				as, err := a.Store.Load(r)
				if err != nil {
					out = append(out, work{t, p, &Asset{Ref: r, Problems: []string{"assigned but not in the store"}}})
					continue
				}
				if opts.MatchAsset(as) {
					out = append(out, work{t, p, as})
				}
			}
		}
	}
	return out, notes, nil
}

func (a *App) plan(w work, method string) []*Unit {
	if w.asset.Path == "" {
		return []*Unit{{Target: w.target, Project: w.project, Asset: w.asset.Ref.String(), asset: w.asset, blocked: "assigned but not in the store"}}
	}
	return a.Plan(w.target, w.project, w.asset, method)
}

// Status evaluates assigned assets without changing anything.
func (a *App) Status(opts DeployOptions) (*DeployResult, error) {
	ws, notes, err := a.selectWork(opts, false)
	if err != nil {
		return nil, err
	}
	res := &DeployResult{Notes: notes}
	for _, w := range ws {
		for _, u := range a.plan(w, "") {
			e := a.Evaluate(u)
			if opts.Only != "" && e.State != opts.Only {
				continue
			}
			res.Evals = append(res.Evals, e)
		}
	}
	sortEvals(res.Evals)
	return res, nil
}

// Deploy brings selected assets up to date on selected targets.
func (a *App) Deploy(opts DeployOptions) (*DeployResult, error) {
	ws, notes, err := a.selectWork(opts, true)
	if err != nil {
		return nil, err
	}
	res := &DeployResult{Notes: notes}
	mutate := !opts.DryRun && !opts.Check
	if mutate {
		for _, w := range ws {
			if w.asset.Path != "" && !contains(a.Config.Assignments(w.target, w.project), w.asset.Ref.String()) {
				a.Config.Assign(w.target, w.project, w.asset.Ref.String())
				res.Assigned = append(res.Assigned, Scoped{w.target, w.project}.String()+" "+w.asset.Ref.String())
			}
		}
		if len(res.Assigned) > 0 {
			if err := a.SaveConfig(); err != nil {
				return nil, err
			}
		}
	}
	done := map[string]bool{} // shared destinations already handled this run
	for _, w := range ws {
		for _, u := range a.plan(w, opts.Method) {
			e := a.Evaluate(u)
			res.Evals = append(res.Evals, e)
			key := u.Target + "|" + u.Dest + "|" + u.Key
			if u.Dest != "" && done[key] {
				e.Action = "shared"
				continue
			}
			if !mutate {
				e.Action = previewAction(e, opts.Adopt)
				continue
			}
			before := e.State
			if e.State == StateBlocked {
				e.Action = "blocked"
				continue
			}
			if e.State == StateConflict && !(opts.Adopt && e.adoptable) {
				e.Action, e.Error = "refused", e.Detail
				continue
			}
			if err := a.Apply(e, opts.Adopt); err != nil {
				e.Error = err.Error()
				e.Action = "failed"
				continue
			}
			done[key] = true
			after := a.Evaluate(u)
			e.State, e.Detail = after.State, after.Detail
			e.Action = appliedAction(before, e)
		}
	}
	if mutate {
		if err := a.SaveState(); err != nil {
			return res, err
		}
	}
	sortEvals(res.Evals)
	return res, nil
}

func previewAction(e *Eval, adopt bool) string {
	switch e.State {
	case StateOK:
		return "unchanged"
	case StateMissing:
		if e.Method == MethodPackage {
			return "would package"
		}
		if e.Method == MethodManual {
			return "would print steps"
		}
		return "would create"
	case StateOutdated:
		return "would update"
	case StateConflict:
		if adopt && e.adoptable {
			return "would adopt"
		}
		return "refused"
	case StateManual:
		return "awaiting manual step"
	}
	return "blocked"
}

func appliedAction(before string, e *Eval) string {
	switch {
	case before == StateOK:
		return "unchanged"
	case e.Method == MethodPackage && before == StateManual:
		return "awaiting upload"
	case e.Method == MethodPackage:
		return "packaged"
	case e.Method == MethodManual && e.State != StateOK:
		return "steps printed"
	case before == StateConflict:
		return "adopted"
	case before == StateOutdated:
		return "updated"
	}
	return "created"
}

func sortEvals(es []*Eval) {
	sort.SliceStable(es, func(i, j int) bool {
		if es[i].Target != es[j].Target {
			return es[i].Target < es[j].Target
		}
		if es[i].Project != es[j].Project {
			return es[i].Project < es[j].Project
		}
		return es[i].Asset < es[j].Asset
	})
}

// Acknowledge records a completed manual step for asset on target.
func (a *App) Acknowledge(target, project, ref, hash string) (*Ack, []string, error) {
	if TargetByName(target) == nil {
		return nil, nil, fmt.Errorf("unknown target %q", target)
	}
	as, err := a.Store.Find(ref, "")
	if err != nil {
		return nil, nil, err
	}
	project = a.ProjectKey(project)
	var match *Unit
	var wants []string
	for _, u := range a.Plan(target, project, as, "") {
		if u.blocked != "" {
			return nil, nil, fmt.Errorf("%s on %s is blocked: %s", as.Ref, target, u.blocked)
		}
		wants = append(wants, u.Want)
		if hashMatches(u.Want, hash) {
			match = u
		}
	}
	var warnings []string
	full := ""
	if match != nil {
		full = match.Want
	} else {
		for _, d := range a.State.ForAsset(as.Ref.String(), target, project) {
			if hashMatches(d.SourceHash, hash) {
				full = d.SourceHash
				warnings = append(warnings, "that hash is from an older revision; status will report it as outdated")
				break
			}
		}
	}
	if full == "" {
		cur := ""
		if len(wants) > 0 {
			cur = shortHash(wants[0])
		}
		return nil, nil, fmt.Errorf("hash %s does not match %s on %s (current: %s); run `agentctl deploy` and upload the current package", hash, as.Ref, target, cur)
	}
	ack := &Ack{Target: target, Project: project, Asset: as.Ref.String(), Hash: full, At: a.Now().UTC()}
	refs := []string{as.Ref.String()}
	if match != nil {
		refs = append(refs, match.group...)
	}
	seen := map[string]bool{}
	for _, r := range refs {
		if seen[r] {
			continue
		}
		seen[r] = true
		cp := *ack
		cp.Asset = r
		a.State.AddAck(&cp)
	}
	return ack, warnings, a.SaveState()
}
