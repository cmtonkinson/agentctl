package core

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/cmtonkinson/agentctl/internal/fsx"
)

// Deployment methods.
const (
	MethodLink     = "link"     // symlink to the store
	MethodCopy     = "copy"     // copy of the store content
	MethodGenerate = "generate" // generated file (composed instructions)
	MethodInPlace  = "in-place" // target reads the store directly
	MethodPackage  = "package"  // file for manual upload, then acknowledged
	MethodConfig   = "config"   // entry merged into a JSON config file
	MethodManual   = "manual"   // command or UI step the user performs
)

// Unit is one deployable piece of an asset on a target.
type Unit struct {
	Target  string   `json:"target"`
	Project string   `json:"project,omitempty"`
	Asset   string   `json:"asset"`
	Method  string   `json:"method,omitempty"`
	Dest    string   `json:"dest,omitempty"`
	Key     string   `json:"key,omitempty"`
	Source  string   `json:"source,omitempty"`
	Want    string   `json:"hash,omitempty"`
	Steps   []string `json:"steps,omitempty"`
	Notes   []string `json:"notes,omitempty"`

	blocked     string
	asset       *Asset
	content     []byte          // generated file content
	value       json.RawMessage // desired config value
	format      string          // package format: zip | text
	verify      func() (found bool, match bool, have json.RawMessage, err error)
	group       []string // instruction assets composed into the same file
	removeSteps []string
}

// Eval is a unit with its current state.
type Eval struct {
	*Unit
	State     string `json:"state"`
	Detail    string `json:"detail,omitempty"`
	Action    string `json:"action,omitempty"`
	Error     string `json:"error,omitempty"`
	adoptable bool
}

func (a *App) scopedPaths(target, project string) map[string]string {
	if project != "" {
		return ProjectPaths(target, a.AbsPath(project))
	}
	return a.TargetPaths(target)
}

// Plan works out how asset reaches target (project "" = user scope).
// method is "", auto, link, copy, or package.
func (a *App) Plan(target, project string, as *Asset, method string) []*Unit {
	base := func() *Unit { return &Unit{Target: target, Project: project, Asset: as.Ref.String(), asset: as} }
	block := func(format string, args ...any) []*Unit {
		u := base()
		u.blocked = fmt.Sprintf(format, args...)
		return []*Unit{u}
	}
	sup := a.Support(target, as)
	if sup.Level == SupportUnsupported {
		return block("%s", sup.Notes[0])
	}
	if project != "" && !contains(sup.Scopes, ScopeProject) {
		return block("%s does not support project scope for %s", target, as.Kind)
	}
	if len(as.Problems) > 0 && (as.Kind == Skills || as.Kind == Plugins) && !fsx.Exists(as.MainFile()) {
		return block("%s", as.Problems[0])
	}
	if why := a.dependencyBlock(target, project, as); why != "" {
		return block("%s", why)
	}
	if method == "" || method == "auto" {
		// The configured default applies only where the target supports it.
		method = ""
		if m := a.Config.Deploy.Method; m != "" && m != "auto" && contains(sup.Methods, m) {
			method = m
		}
		if method == "" {
			method = sup.Methods[0]
			if method == MethodLink && a.GOOS == "windows" {
				method = MethodCopy
			}
		}
	}
	if !contains(sup.Methods, method) {
		return block("method %s is not available for %s on %s (available: %s)", method, as.Kind, target, strings.Join(sup.Methods, ", "))
	}
	switch as.Kind {
	case Instructions:
		return a.planInstructions(base, target, project, as, method)
	case Skills, Plugins:
		return a.planDir(base, target, project, as, method)
	case Tools:
		return a.planTool(base, target, project, as, method)
	}
	return block("unknown kind")
}

// dependencyBlock explains why required assets prevent deployment.
func (a *App) dependencyBlock(target, project string, as *Asset) string {
	if as.Meta == nil || as.Meta.Requires == nil {
		return ""
	}
	for _, req := range as.Meta.Requires.Assets {
		r, err := ParseRef(req)
		if err != nil || r.Kind == "" {
			continue
		}
		dep, err := a.Store.Load(r)
		if err != nil {
			return fmt.Sprintf("requires %s, which is not in the store", req)
		}
		if a.Support(target, dep).Level == SupportUnsupported {
			return fmt.Sprintf("requires %s, which %s does not support", req, target)
		}
		if !contains(a.Config.Assignments(target, project), req) {
			return fmt.Sprintf("requires %s; assign it too: agentctl target assign %s %s", req, target, req)
		}
	}
	return ""
}

func (a *App) instructionGroup(target, project string, as *Asset) []*Asset {
	var group []*Asset
	seen := false
	for _, ref := range a.Config.Assignments(target, project) {
		r, err := ParseRef(ref)
		if err != nil || r.Kind != Instructions {
			continue
		}
		if r.Name == as.Name {
			group = append(group, as)
			seen = true
			continue
		}
		if other, err := a.Store.Load(r); err == nil {
			group = append(group, other)
		}
	}
	if !seen {
		group = append(group, as)
	}
	return group
}

func composeInstructions(target string, group []*Asset, header bool) []byte {
	var b bytes.Buffer
	if header {
		var refs []string
		for _, g := range group {
			refs = append(refs, g.Ref.String())
		}
		fmt.Fprintf(&b, "<!-- Generated by agentctl from %s. Edit the source with `agentctl edit`, then run `agentctl deploy`. -->\n\n", strings.Join(refs, ", "))
	}
	for i, g := range group {
		if i > 0 {
			b.WriteString("\n\n")
		}
		body, _ := os.ReadFile(g.MainFile())
		b.Write(bytes.TrimRight(body, "\n"))
		if g.HasAdapter(target) {
			adapter, _ := os.ReadFile(filepath.Join(g.Path, "targets", target+".md"))
			b.WriteString("\n\n")
			b.Write(bytes.TrimRight(adapter, "\n"))
		}
	}
	b.WriteString("\n")
	return b.Bytes()
}

func refsOf(group []*Asset) []string {
	var out []string
	for _, g := range group {
		out = append(out, g.Ref.String())
	}
	return out
}

func (a *App) planInstructions(base func() *Unit, target, project string, as *Asset, method string) []*Unit {
	group := a.instructionGroup(target, project, as)
	u := base()
	u.group = refsOf(group)
	if TargetByName(target).Local {
		u.Dest = a.scopedPaths(target, project)["instructions"]
		single := len(group) == 1 && !as.HasAdapter(target)
		switch {
		case single && method == MethodLink:
			u.Method = MethodLink
			u.Source = as.MainFile()
			u.Want, _ = fsx.Hash(u.Source)
		case single && method == MethodCopy:
			u.Method = MethodCopy
			u.Source = as.MainFile()
			u.content, _ = os.ReadFile(u.Source)
			u.Want = fsx.HashBytes(u.content)
		default:
			u.Method = MethodGenerate
			u.content = composeInstructions(target, group, true)
			u.Want = fsx.HashBytes(u.content)
			if len(group) > 1 {
				u.Notes = append(u.Notes, fmt.Sprintf("composed from %s", strings.Join(u.group, ", ")))
			}
		}
		return []*Unit{u}
	}
	u.Method = MethodPackage
	u.format = "text"
	u.content = composeInstructions(target, group, false)
	u.Want = fsx.HashBytes(u.content)
	u.Dest = filepath.Join(a.OutputDir(), target, "instructions.md")
	switch target {
	case "claude-chat":
		u.Steps = []string{"Paste " + a.Abbrev(u.Dest) + " into claude.ai → Settings → Profile → personal preferences."}
		u.removeSteps = []string{"Clear the personal preferences in claude.ai → Settings → Profile."}
	case "chatgpt":
		u.Steps = []string{"Paste " + a.Abbrev(u.Dest) + " into ChatGPT → Settings → Personalization → Custom instructions."}
		u.removeSteps = []string{"Clear ChatGPT → Settings → Personalization → Custom instructions."}
		if n := len([]rune(string(u.content))); n > 1500 {
			u.Notes = append(u.Notes, fmt.Sprintf("%d characters; ChatGPT allows 1,500 per custom-instructions field", n))
		}
	}
	u.Steps = append(u.Steps, a.ackStep(u))
	return []*Unit{u}
}

func (a *App) ackStep(u *Unit) string {
	cmd := fmt.Sprintf("agentctl target acknowledge %s %s --hash %s", u.Target, u.Asset, fsx.Short(u.Want))
	if u.Project != "" {
		cmd += " --project " + u.Project
	}
	return "Then record it: " + cmd
}

func (a *App) planDir(base func() *Unit, target, project string, as *Asset, method string) []*Unit {
	u := base()
	u.Want, _ = as.Hash()
	u.Source = as.Path
	if TargetByName(target).Local {
		key := "skills"
		if as.Kind == Plugins {
			key = "plugins"
		}
		u.Dest = filepath.Join(a.scopedPaths(target, project)[key], as.Name)
		u.Method = method
		if fsx.SameLocation(u.Dest, as.Path) {
			u.Method = MethodInPlace
		}
		if as.Kind == Plugins && target == "claude-code" {
			u.Notes = append(u.Notes, "loads as "+as.Name+"@skills-dir; run /reload-plugins or start a new session")
			for _, ip := range readInstalledPlugins(filepath.Join(a.TargetPaths(target)["plugin-data"], "installed_plugins.json")) {
				if strings.HasPrefix(ip.ID, as.Name+"@") {
					u.Notes = append(u.Notes, "also installed as "+ip.ID+"; uninstall one copy to avoid loading it twice: claude plugin uninstall "+ip.ID)
				}
			}
		}
		return []*Unit{u}
	}
	u.Method = MethodPackage
	u.format = "zip"
	u.Dest = filepath.Join(a.OutputDir(), target, string(as.Kind), as.Name+".zip")
	switch {
	case target == "claude-chat" && as.Kind == Skills:
		u.Steps = []string{"Upload " + a.Abbrev(u.Dest) + " in claude.ai → Settings → Capabilities → Skills."}
		u.removeSteps = []string{"Delete the " + as.Name + " skill in claude.ai → Settings → Capabilities → Skills."}
	case target == "claude-chat" && as.Kind == Plugins:
		u.Steps = []string{"Upload " + a.Abbrev(u.Dest) + " as a plugin in the Claude desktop app."}
		u.removeSteps = []string{"Remove the " + as.Name + " plugin in the Claude desktop app."}
	case target == "chatgpt":
		u.Steps = []string{"Upload " + a.Abbrev(u.Dest) + " as a skill in ChatGPT (where your plan offers skills)."}
		u.removeSteps = []string{"Delete the " + as.Name + " skill in ChatGPT."}
	}
	u.Steps = append(u.Steps, a.ackStep(u))
	return []*Unit{u}
}

func (a *App) planTool(base func() *Unit, target, project string, as *Asset, method string) []*Unit {
	var units []*Unit
	spec := as.Tool
	if spec.MCP != nil {
		units = append(units, a.planMCP(base(), target, project, as))
	}
	if TargetByName(target).Local {
		binMethod := method
		if binMethod != MethodLink && binMethod != MethodCopy {
			binMethod = MethodLink
			if a.GOOS == "windows" {
				binMethod = MethodCopy
			}
		}
		for _, bin := range spec.Bin {
			u := base()
			u.Method = binMethod
			u.Source = filepath.Join(as.Path, filepath.FromSlash(bin))
			u.Dest = filepath.Join(a.BinDir(), filepath.Base(bin))
			u.Want, _ = fsx.Hash(u.Source)
			units = append(units, u)
		}
	}
	return units
}

// Evaluate reports a unit's current state.
func (a *App) Evaluate(u *Unit) *Eval {
	e := &Eval{Unit: u}
	if u.blocked != "" {
		e.State, e.Detail = StateBlocked, u.blocked
		return e
	}
	switch u.Method {
	case MethodInPlace:
		e.State, e.Detail = StateOK, "read directly from the store"
	case MethodLink:
		a.evalLink(e)
	case MethodCopy, MethodGenerate:
		a.evalCopy(e)
	case MethodPackage:
		a.evalAcknowledged(e)
	case MethodConfig:
		a.evalConfig(e)
	case MethodManual:
		if u.verify == nil {
			a.evalAcknowledged(e)
		} else {
			a.evalVerified(e)
		}
	default:
		e.State, e.Detail = StateBlocked, "no deployment method"
	}
	return e
}

func (a *App) owned(u *Unit) *Deployment {
	if ds := a.State.ForDest(u.Dest, u.Key); len(ds) > 0 {
		return ds[0]
	}
	return nil
}

func kindOfPath(p string) string {
	if fsx.IsDir(p) {
		return "directory"
	}
	return "file"
}

func (a *App) evalLink(e *Eval) {
	u := e.Unit
	fi, err := os.Lstat(u.Dest)
	if err != nil {
		e.State, e.Detail = StateMissing, "not deployed"
		return
	}
	rec := a.owned(u)
	if fi.Mode()&os.ModeSymlink != 0 {
		target, _ := fsx.LinkTarget(u.Dest)
		if fsx.SamePath(target, u.Source) {
			e.State, e.Detail = StateOK, "linked"
			return
		}
		if rec != nil || fsx.Within(a.Store.Root, target) {
			e.State, e.Detail = StateOutdated, "links to "+a.Abbrev(target)
			return
		}
		e.State, e.Detail = StateConflict, "unmanaged symlink to "+a.Abbrev(target)
		return
	}
	h, _ := fsx.Hash(u.Dest)
	switch {
	case rec != nil && rec.DestHash == h:
		e.State, e.Detail = StateOutdated, "managed copy; will become a link"
	case rec != nil:
		e.State, e.Detail = StateConflict, "managed copy was modified after deployment"
	case h == u.Want:
		e.State, e.Detail, e.adoptable = StateConflict, "unmanaged "+kindOfPath(u.Dest)+" identical to the store (deploy --adopt replaces it)", true
	default:
		e.State, e.Detail = StateConflict, "unmanaged "+kindOfPath(u.Dest)+" exists"
	}
}

func (a *App) evalCopy(e *Eval) {
	u := e.Unit
	fi, err := os.Lstat(u.Dest)
	if err != nil {
		e.State, e.Detail = StateMissing, "not deployed"
		return
	}
	rec := a.owned(u)
	if fi.Mode()&os.ModeSymlink != 0 {
		target, _ := fsx.LinkTarget(u.Dest)
		if rec != nil || fsx.Within(a.Store.Root, target) {
			e.State, e.Detail = StateOutdated, "linked; will become a copy"
			return
		}
		e.State, e.Detail = StateConflict, "unmanaged symlink to "+a.Abbrev(target)
		return
	}
	h, _ := fsx.Hash(u.Dest)
	switch {
	case h == u.Want && rec != nil:
		e.State, e.Detail = StateOK, "up to date"
	case h == u.Want:
		e.State, e.Detail, e.adoptable = StateConflict, "unmanaged "+kindOfPath(u.Dest)+" identical to the store (deploy --adopt takes it over)", true
	case rec != nil && rec.DestHash == h:
		e.State, e.Detail = StateOutdated, "store changed since deployment"
	case rec != nil:
		e.State, e.Detail = StateConflict, "modified after deployment"
	default:
		e.State, e.Detail = StateConflict, "unmanaged "+kindOfPath(u.Dest)+" exists"
	}
}

func (a *App) evalAcknowledged(e *Eval) {
	u := e.Unit
	ack := a.State.LatestAck(u.Target, u.Project, u.Asset)
	if ack != nil && fsx.MatchHash(u.Want, ack.Hash) {
		e.State, e.Detail = StateOK, "acknowledged "+ack.At.Format("2006-01-02")
		return
	}
	rec := a.State.Find(u.Target, u.Project, u.Asset, u.Dest, u.Key)
	current := rec != nil && rec.SourceHash == u.Want
	if u.Method == MethodPackage {
		current = current && fsx.Exists(u.Dest)
	}
	switch {
	case current && u.Method == MethodPackage:
		e.State, e.Detail = StateManual, "package ready; upload it, then acknowledge"
	case current:
		e.State, e.Detail = StateManual, "waiting for the manual step, then acknowledge"
	case ack != nil:
		e.State, e.Detail = StateOutdated, fmt.Sprintf("acknowledged %s; store is now %s", fsx.Short(ack.Hash), fsx.Short(u.Want))
	case rec != nil:
		e.State, e.Detail = StateOutdated, "prepared from an older revision"
	default:
		e.State, e.Detail = StateMissing, "not deployed"
	}
}

func (a *App) evalVerified(e *Eval) {
	u := e.Unit
	found, match, have, err := u.verify()
	rec := a.State.Find(u.Target, u.Project, u.Asset, u.Dest, u.Key)
	switch {
	case err != nil:
		e.State, e.Detail = StateBlocked, err.Error()
	case found && match:
		e.State, e.Detail = StateOK, "verified in "+a.Abbrev(u.Dest)
	case found && rec != nil && rec.DestHash != "" && rec.DestHash == ownershipHash(have):
		e.State, e.Detail = StateOutdated, "configured from an older revision"
	case found:
		e.State, e.Detail = StateConflict, "configured differently in "+a.Abbrev(u.Dest)
	default:
		ack := a.State.LatestAck(u.Target, u.Project, u.Asset)
		switch {
		case ack != nil && fsx.MatchHash(u.Want, ack.Hash):
			e.State, e.Detail = StateOK, "acknowledged "+ack.At.Format("2006-01-02")
		case rec != nil:
			e.State, e.Detail = StateManual, "run the printed command"
		default:
			e.State, e.Detail = StateMissing, "not configured"
		}
	}
}

// Apply performs a unit's deployment and records it. adopt permits taking
// over an unmanaged destination whose content is identical to the store.
func (a *App) Apply(e *Eval, adopt bool) error {
	u := e.Unit
	switch e.State {
	case StateOK:
		return nil
	case StateBlocked:
		return fmt.Errorf("blocked: %s", e.Detail)
	case StateConflict:
		if !(adopt && e.adoptable) {
			return fmt.Errorf("conflict: %s", e.Detail)
		}
	}
	rec := &Deployment{Target: u.Target, Project: u.Project, Asset: u.Asset, Method: u.Method, Dest: u.Dest, Key: u.Key, SourceHash: u.Want, At: a.Now().UTC()}
	switch u.Method {
	case MethodLink, MethodCopy, MethodGenerate:
		if e.State != StateMissing {
			if err := a.clearDest(u.Dest, e.State == StateConflict); err != nil {
				return err
			}
		}
		if err := os.MkdirAll(filepath.Dir(u.Dest), 0o755); err != nil {
			return err
		}
		switch {
		case u.Method == MethodLink:
			if err := os.Symlink(u.Source, u.Dest); err != nil {
				return err
			}
		case u.content != nil:
			mode := os.FileMode(0o644)
			if err := fsx.WriteFileAtomic(u.Dest, u.content, mode); err != nil {
				return err
			}
		default:
			if err := fsx.CopyTree(u.Source, u.Dest); err != nil {
				return err
			}
		}
		if u.Method != MethodLink {
			rec.DestHash, _ = fsx.Hash(u.Dest)
		}
	case MethodPackage:
		if e.State == StateManual {
			return nil
		}
		var err error
		if u.format == "zip" {
			err = fsx.ZipTree(u.Source, u.Dest, u.asset.Name)
		} else {
			err = fsx.WriteFileAtomic(u.Dest, u.content, 0o644)
		}
		if err != nil {
			return err
		}
		rec.DestHash, _ = fsx.Hash(u.Dest)
		rec.Manual = true
	case MethodConfig:
		written, err := a.mergeConfig(u)
		if err != nil {
			return err
		}
		rec.DestHash = ownershipHash(written)
	case MethodManual:
		rec.Manual = true
		if u.verify != nil {
			rec.DestHash = ownershipHash(u.value)
		}
	}
	for _, ref := range append([]string{u.Asset}, u.group...) {
		r := *rec
		r.Asset = ref
		a.State.Upsert(&r)
	}
	return nil
}

// clearDest removes a managed destination before replacing it. When
// adopting, the unmanaged original is moved to the store's trash instead.
func (a *App) clearDest(dest string, adopt bool) error {
	if adopt {
		trash := a.Store.Internal("trash", a.Now().UTC().Format("20060102T150405Z"), "adopted", strings.TrimPrefix(filepath.ToSlash(dest), "/"))
		if err := os.MkdirAll(filepath.Dir(trash), 0o755); err != nil {
			return err
		}
		return os.Rename(dest, trash)
	}
	if fsx.IsSymlink(dest) {
		return os.Remove(dest)
	}
	return os.RemoveAll(dest)
}
