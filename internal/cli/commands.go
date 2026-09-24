package cli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/cmtonkinson/agentctl/internal/core"
	"github.com/cmtonkinson/agentctl/internal/fsx"
)

func newInventoryCmd(g *globals) *cobra.Command {
	var sel selection
	var refresh, includeCache, duplicates, unmanaged bool
	cmd := &cobra.Command{
		Use:   "inventory [OPTIONS]",
		Short: "Discover assets across local files and connected accounts",
		Long: `Discover instructions, skills, plugins, and tools in each target's files, and
the uploads recorded for account targets. Each item has an ID that
'agentctl import' accepts.

Status column:
  managed     deployed by agentctl from the store
  in-store    the store holds identical content
  differs     the store has an asset with this name but different content
  unmanaged   absent from the store
  recorded    an acknowledged upload to an account target`,
		Example: `  agentctl inventory --target claude-chat --kind skills
  agentctl inventory --unmanaged
  agentctl inventory --duplicates
  agentctl inventory --scope project --target claude-code`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := g.open()
			if err != nil {
				return err
			}
			s, err := sel.resolve(a)
			if err != nil {
				return err
			}
			res, err := a.Inventory(core.InventoryOptions{Selection: s, Refresh: refresh, IncludeCache: includeCache})
			if err != nil {
				return err
			}
			p := g.printer()
			if duplicates {
				groups := a.Duplicates(res.Items)
				return p.emit(map[string]any{"duplicates": groups, "notes": res.Notes}, func() {
					if len(groups) == 0 {
						p.printf("No duplicate names found.\n")
					}
					for _, gr := range groups {
						state := "identical copies"
						if gr.Differs {
							state = "copies differ"
						}
						p.printf("%s/%s  (%s)\n", gr.Kind, gr.Name, state)
						for _, c := range gr.Copies {
							p.printf("  %-12s  %-40s  %s\n", core.ShortHash(c.Hash), c.Where, p.abbrev(c.Path))
						}
					}
				})
			}
			items := res.Items
			if unmanaged {
				var keep []*core.Item
				for _, it := range items {
					if it.Status == core.ItemUnmanaged || it.Status == core.ItemDiffers {
						keep = append(keep, it)
					}
				}
				items = keep
			}
			return p.emit(map[string]any{"items": items, "notes": res.Notes}, func() {
				if len(items) == 0 {
					p.printf("Nothing found.\n")
				} else {
					var rows [][]string
					for _, it := range items {
						name := it.Name
						if it.Plugin != "" && it.Kind != core.Plugins {
							name += " (" + it.Plugin + ")"
						}
						status := it.Status
						if it.Cached {
							status += ", cached"
						}
						loc := p.abbrev(it.Path)
						if it.Key != "" {
							loc += " [" + lastSegment(it.Key) + "]"
						}
						if it.Scope == core.ScopeAccount {
							loc = it.Note
						}
						rows = append(rows, []string{it.ID, it.Target, it.Scope, string(it.Kind), name, status, loc})
					}
					p.table([]string{"ID", "TARGET", "SCOPE", "KIND", "NAME", "STATUS", "LOCATION"}, rows)
				}
				for _, n := range res.Notes {
					p.printf("note: %s\n", n)
				}
			})
		},
	}
	addSelection(cmd, &sel, selAll&^selAllTargets)
	f := cmd.Flags()
	f.BoolVar(&refresh, "refresh", false, "Refresh account inventories where supported")
	f.BoolVar(&includeCache, "include-cache", false, "Include cached assets not confirmed installed")
	f.BoolVar(&duplicates, "duplicates", false, "Show duplicate names and differing copies")
	f.BoolVar(&unmanaged, "unmanaged", false, "Show assets absent from the authoritative store")
	return cmd
}

func lastSegment(key string) string {
	if i := strings.LastIndex(key, "."); i >= 0 {
		return key[i+1:]
	}
	return key
}

func newListCmd(g *globals) *cobra.Command {
	var sel selection
	cmd := &cobra.Command{
		Use:   "list [OPTIONS]",
		Short: "List assets in the authoritative store",
		Long:  "List assets in the store with their origin, version, and target assignments.\nWith --target, list only assets assigned to those targets.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := g.open()
			if err != nil {
				return err
			}
			s, err := sel.resolve(a)
			if err != nil {
				return err
			}
			all, err := a.Store.List()
			if err != nil {
				return err
			}
			type row struct {
				Asset       string        `json:"asset"`
				Kind        core.Kind     `json:"kind"`
				Name        string        `json:"name"`
				Origin      string        `json:"origin"`
				Version     string        `json:"version,omitempty"`
				Description string        `json:"description,omitempty"`
				Path        string        `json:"path"`
				Targets     []core.Scoped `json:"targets"`
				Problems    []string      `json:"problems,omitempty"`
			}
			var rows []row
			for _, as := range all {
				if !s.MatchAsset(as) {
					continue
				}
				assigned := a.Assigned(as.Ref.String())
				if len(s.Targets) > 0 {
					hit := false
					for _, sc := range assigned {
						for _, t := range s.Targets {
							hit = hit || sc.Target == t
						}
					}
					if !hit {
						continue
					}
				}
				rows = append(rows, row{as.Ref.String(), as.Kind, as.Name, as.Origin(), as.Version, as.Description, as.Path, nonNilScoped(assigned), as.Problems})
			}
			p := g.printer()
			return p.emit(rows, func() {
				if len(rows) == 0 {
					if !a.Store.Exists() {
						p.printf("The store %s does not exist yet. Start with: agentctl inventory\n", p.abbrev(a.Store.Root))
					} else {
						p.printf("No assets match.\n")
					}
					return
				}
				var t [][]string
				for _, r := range rows {
					var ts []string
					for _, sc := range r.Targets {
						ts = append(ts, sc.String())
					}
					desc := r.Description
					if len(r.Problems) > 0 {
						desc = "(!) " + r.Problems[0]
					}
					t = append(t, []string{r.Asset, r.Origin, dash(r.Version), dash(strings.Join(ts, ",")), trunc(desc, 60)})
				}
				p.table([]string{"ASSET", "ORIGIN", "VERSION", "TARGETS", "DESCRIPTION"}, t)
			})
		},
	}
	addSelection(cmd, &sel, selKind|selTarget|selOrigin)
	return cmd
}

func nonNilScoped(s []core.Scoped) []core.Scoped {
	if s == nil {
		return []core.Scoped{}
	}
	return s
}

func newShowCmd(g *globals) *cobra.Command {
	var sel selection
	var files, deps, compat, prov bool
	cmd := &cobra.Command{
		Use:   "show ASSET [OPTIONS]",
		Short: "Show an asset's source, dependencies, and target support",
		Long:  "Show an asset from the store. ASSET is a name or KIND/NAME.\nWith no section flags, every section is shown.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := g.open()
			if err != nil {
				return err
			}
			s, err := sel.resolve(a)
			if err != nil {
				return err
			}
			as, err := a.Store.Find(args[0], s.Kind)
			if err != nil {
				return err
			}
			if !files && !deps && !compat && !prov {
				deps, compat, prov = true, true, true
			}
			hash, _ := as.Hash()
			out := map[string]any{
				"asset": as.Ref.String(), "kind": as.Kind, "name": as.Name, "path": as.Path,
				"main": as.MainFile(), "description": as.Description, "version": as.Version,
				"license": as.License, "origin": as.Origin(), "hash": hash,
				"targets": nonNilScoped(a.Assigned(as.Ref.String())), "problems": as.Problems,
			}
			if as.Kind == core.Instructions {
				out["adapters"] = as.Adapters
			}
			if as.Tool != nil {
				out["tool"] = as.Tool
			}
			var fl []core.FileInfo
			var dp *core.Deps
			var cp []core.Compat
			var pv *core.Provenance
			if files {
				fl = a.Files(as)
				out["files"] = fl
			}
			if deps {
				dp = a.Dependencies(as)
				out["dependencies"] = dp
			}
			if compat {
				cp = a.Compatibility(as)
				out["compatibility"] = cp
			}
			if prov {
				pv = a.ProvenanceOf(as)
				out["provenance"] = pv
			}
			p := g.printer()
			return p.emit(out, func() {
				p.printf("%s\n", as.Ref)
				kv := [][]string{{"path", p.abbrev(as.Path)}}
				if as.Description != "" {
					kv = append(kv, []string{"description", trunc(as.Description, 100)})
				}
				kv = append(kv, []string{"origin", as.Origin()}, []string{"version", dash(as.Version)}, []string{"license", dash(as.License)}, []string{"hash", core.ShortHash(hash)})
				var ts []string
				for _, sc := range a.Assigned(as.Ref.String()) {
					ts = append(ts, sc.String())
				}
				kv = append(kv, []string{"assigned", dash(strings.Join(ts, ", "))})
				if len(as.Adapters) > 0 {
					kv = append(kv, []string{"adapters", strings.Join(as.Adapters, ", ")})
				}
				if as.Tool != nil && as.Tool.MCP != nil {
					m := as.Tool.MCP
					desc := m.Transport() + ": "
					if m.Remote() {
						desc += m.URL
					} else {
						desc += strings.Join(append([]string{m.Command}, m.Args...), " ")
					}
					kv = append(kv, []string{"mcp", desc})
				}
				if as.Tool != nil && len(as.Tool.Bin) > 0 {
					kv = append(kv, []string{"bin", strings.Join(as.Tool.Bin, ", ")})
				}
				for _, r := range kv {
					p.printf("  %-12s %s\n", r[0], r[1])
				}
				for _, pr := range as.Problems {
					p.printf("  %-12s %s\n", "problem", pr)
				}
				if pv != nil {
					p.printf("\nProvenance:\n")
					if pv.Source == nil {
						p.printf("  no recorded upstream (created in the store)\n")
					} else {
						p.printf("  %-12s %s (%s)\n", "source", pv.SourceLabel, pv.Source.Type)
						if pv.Source.Revision != "" {
							p.printf("  %-12s %s\n", "revision", pv.Source.Revision)
						}
						if pv.ImportedAt != nil {
							p.printf("  %-12s %s\n", "imported", pv.ImportedAt.Local().Format("2006-01-02 15:04"))
						}
						if pv.UpdatedAt != nil {
							p.printf("  %-12s %s\n", "updated", pv.UpdatedAt.Local().Format("2006-01-02 15:04"))
						}
						p.printf("  %-12s %s\n", "upstream", core.ShortHash(pv.SourceHash))
					}
					if pv.LocalEdits {
						p.printf("  %-12s yes (current %s)\n", "local edits", core.ShortHash(pv.CurrentHash))
					}
				}
				if dp != nil {
					p.printf("\nDependencies:\n")
					if len(dp.Assets) == 0 && len(dp.Commands) == 0 && len(dp.Notes) == 0 {
						p.printf("  none detected\n")
					}
					for _, d := range dp.Assets {
						st := "in store"
						if !d.InStore {
							st = "MISSING from store"
						}
						p.printf("  asset    %-24s %s\n", d.Ref, st)
					}
					for _, c := range dp.Commands {
						st := c.Path
						if !c.Found {
							st = "NOT FOUND on PATH"
						}
						p.printf("  command  %-24s %s  (%s)\n", c.Name, st, trunc(strings.Join(c.From, ", "), 50))
					}
					for _, n := range dp.Notes {
						p.printf("  note     %s\n", n)
					}
				}
				if cp != nil {
					p.printf("\nCompatibility:\n")
					var rows [][]string
					for _, c := range cp {
						state := c.State
						if state == "" {
							state = "not assigned"
						}
						if !c.Enabled {
							state += " (disabled)"
						}
						rows = append(rows, []string{"  " + c.Target, c.Level, dash(strings.Join(c.Methods, ",")), state, trunc(strings.Join(c.Notes, " "), 90)})
					}
					p.table(nil, rows)
				}
				if fl != nil {
					p.printf("\nFiles:\n")
					for _, f := range fl {
						mark := ""
						if f.Executable {
							mark = " *"
						}
						if f.Link != "" {
							mark = " → " + f.Link
						}
						p.printf("  %8d  %s%s\n", f.Size, f.Path, mark)
					}
				}
			})
		},
	}
	addSelection(cmd, &sel, selKind)
	f := cmd.Flags()
	f.BoolVar(&files, "files", false, "List packaged files")
	f.BoolVar(&deps, "dependencies", false, "Show required tools, runtimes, and other assets")
	f.BoolVar(&compat, "compatibility", false, "Show support and limitations for each target")
	f.BoolVar(&prov, "provenance", false, "Show origin and imported revision")
	return cmd
}

func newImportCmd(g *globals) *cobra.Command {
	var sel selection
	var o core.ImportOptions
	cmd := &cobra.Command{
		Use:   "import SOURCE [OPTIONS]",
		Short: "Copy assets into the store; preserve originals",
		Long: `Copy assets into the store.

SOURCE is an inventory ID, a local path (asset directory, SKILL.md,
AGENTS.md/CLAUDE.md, MCP JSON file, or executable), a ZIP, or a repository
URL. Repository URLs accept URL#subpath and GitHub /tree/REF/PATH links.
With --from TARGET, SOURCE is a name or ID from that target's inventory, or
omitted with --all.

Records source, version, license, and content hashes.
Never moves originals or silently overwrites canonical files.
Plugin-backed skills retain their parent-plugin dependency.
MCP credentials are replaced by ${VAR} placeholders and stay in each client.`,
		Example: `  agentctl import ~/.claude/skills/example
  agentctl import --from claude-code --kind skills --all --dry-run
  agentctl import 3fa2c1d0
  agentctl import ~/Downloads/morning-brief.zip
  agentctl import https://github.com/org/skills --all --kind skills
  agentctl import https://github.com/org/repo#skills/pdf --version v1.2.0`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := g.open()
			if err != nil {
				return err
			}
			s, err := sel.resolve(a)
			if err != nil {
				return err
			}
			o.Selection = s
			source := ""
			if len(args) == 1 {
				source = args[0]
			}
			if o.Name != "" {
				if err := core.ValidName(o.Name); err != nil {
					return usageErr("%v", err)
				}
			}
			results, err := a.Import(source, o)
			p := g.printer()
			if len(results) > 0 {
				if perr := p.emit(results, func() { printImports(p, results) }); perr != nil {
					return perr
				}
			}
			return err
		},
	}
	addSelection(cmd, &sel, selKind|selOrigin|selScope|selProject|selExclude)
	f := cmd.Flags()
	f.StringVar(&o.Name, "name", "", "Set the canonical name")
	f.StringVar(&o.From, "from", "", "Import from a target's discovered installation")
	f.BoolVar(&o.All, "all", false, "Import all assets matching selection options")
	f.StringVar(&o.Version, "version", "", "Select an upstream release or revision")
	f.StringVar(&o.OnConflict, "on-conflict", "fail", "fail | skip | rename")
	f.BoolVar(&o.DryRun, "dry-run", false, "Preview copies and required dependencies")
	return cmd
}

func printImports(p *printer, results []*core.ImportResult) {
	for _, r := range results {
		p.printf("%-13s %-32s ← %s\n", r.Action, r.Asset, r.Source)
		var meta []string
		if r.Origin != "" {
			meta = append(meta, r.Origin)
		}
		if r.Version != "" {
			meta = append(meta, "version "+r.Version)
		}
		if r.License != "" {
			meta = append(meta, "license "+r.License)
		}
		if r.Hash != "" {
			meta = append(meta, core.ShortHash(r.Hash))
		}
		if len(meta) > 0 && r.Action != "identical" && r.Action != "skipped" {
			p.printf("              %s\n", strings.Join(meta, ", "))
		}
		if r.Reason != "" {
			p.printf("              %s\n", r.Reason)
		}
		for _, d := range r.Requires {
			st := "in store"
			if !d.InStore {
				st = "not in store; " + d.Hint
			}
			p.printf("              requires %s (%s)\n", d.Ref, st)
		}
		for _, w := range r.Warnings {
			p.printf("              warning: %s\n", w)
		}
	}
}

func newDiffCmd(g *globals) *cobra.Command {
	var sel selection
	var against string
	var stat bool
	cmd := &cobra.Command{
		Use:   "diff [ASSET] [OPTIONS]",
		Short: "Compare canonical, source, and deployed copies",
		Long: `Compare the store with a deployed copy (--against target, the default) or
with the recorded upstream (--against origin). Diffs read from the other
copy (a/) to the store (b/). Without ASSET, every matching asset is compared.`,
		Example: `  agentctl diff morning-brief --target claude-chat
  agentctl diff prune-prose --against origin
  agentctl diff --stat`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := g.open()
			if err != nil {
				return err
			}
			s, err := sel.resolve(a)
			if err != nil {
				return err
			}
			o := core.DiffOptions{Selection: s, Against: against, Stat: stat}
			if len(args) == 1 {
				o.Asset = args[0]
			}
			results, err := a.Diff(o)
			if err != nil {
				return err
			}
			p := g.printer()
			return p.emit(results, func() {
				if len(results) == 0 {
					p.printf("Nothing to compare.\n")
				}
				for _, r := range results {
					where := r.Against
					if r.Target != "" {
						where = r.Target
						if r.Project != "" {
							where += "@" + r.Project
						}
					}
					head := fmt.Sprintf("%s vs %s: %s", r.Asset, where, r.State)
					if r.Other != "" {
						head += " (" + r.Other + ")"
					}
					p.printf("%s\n", head)
					if r.Detail != "" {
						p.printf("  %s\n", r.Detail)
					}
					for _, c := range r.Changes {
						if stat || c.Diff == "" {
							p.printf("  %-8s %s  +%d -%d\n", c.Status, c.Path, c.Added, c.Removed)
						} else {
							p.printf("%s", c.Diff)
						}
					}
				}
			})
		},
	}
	addSelection(cmd, &sel, selKind|selTarget|selOrigin|selProject)
	cmd.Flags().StringVar(&against, "against", "target", "origin | target")
	cmd.Flags().BoolVar(&stat, "stat", false, "Show a summary instead of full diffs")
	return cmd
}

func newEditCmd(g *globals) *cobra.Command {
	var sel selection
	var file string
	cmd := &cobra.Command{
		Use:   "edit ASSET [OPTIONS]",
		Short: "Open canonical source in $EDITOR",
		Long: `Open an asset's main file (AGENTS.md, SKILL.md, plugin.json, or tool.json)
in the editor from config 'editor', $VISUAL, or $EDITOR. --file opens
another file inside the asset, for example targets/claude-code.md to add
an instruction adapter.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := g.open()
			if err != nil {
				return err
			}
			s, err := sel.resolve(a)
			if err != nil {
				return err
			}
			as, err := a.Store.Find(args[0], s.Kind)
			if err != nil {
				return err
			}
			path := as.MainFile()
			if file != "" {
				path = filepath.Join(as.Path, filepath.FromSlash(file))
				if !fsx.Within(as.Path, path) {
					return usageErr("--file must stay inside the asset")
				}
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					return err
				}
			}
			if g.json {
				return g.printer().emit(map[string]string{"path": path, "editor": a.Editor()}, nil)
			}
			parts := strings.Fields(a.Editor())
			c := exec.Command(parts[0], append(parts[1:], path)...)
			c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
			if err := c.Run(); err != nil {
				return fmt.Errorf("editor: %w", err)
			}
			return nil
		},
	}
	addSelection(cmd, &sel, selKind)
	cmd.Flags().StringVar(&file, "file", "", "Open a particular file within the asset")
	return cmd
}

func newDeployCmd(g *globals) *cobra.Command {
	var sel selection
	var o core.DeployOptions
	var output string
	cmd := &cobra.Command{
		Use:   "deploy [ASSET...] [OPTIONS]",
		Short: "Link, install, or package assets for selected targets",
		Long: `Deploy assets to targets.

Uses supported delivery methods for each target: links (or copies) for
Claude Code and Codex, claude_desktop_config.json for Claude Desktop MCP
servers, and self-contained packages for upload-only targets.
Reports manual upload/authentication steps; does not claim completion
until verified or explicitly recorded with 'target acknowledge'.

Naming an asset and a target it is not assigned to also assigns it.
Refuses to overwrite files agentctl did not deploy. --adopt takes over an
unmanaged copy only when it is identical to the store; the original is
moved to <store>/.agentctl/trash.`,
		Example: `  agentctl deploy --target claude-code --all --dry-run
  agentctl deploy morning-brief --target claude-chat --method package
  agentctl deploy --all --check`,
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := g.open()
			if err != nil {
				return err
			}
			s, err := sel.resolve(a)
			if err != nil {
				return err
			}
			switch o.Method {
			case "", "auto", "link", "copy", "package":
			default:
				return usageErr("--method must be auto, link, copy, or package")
			}
			if len(args) == 0 && !o.All {
				return usageErr("name assets to deploy, or pass --all")
			}
			o.Selection, o.Assets = s, args
			a.OutputOverride = output
			res, err := a.Deploy(o)
			if err != nil {
				return err
			}
			p := g.printer()
			if perr := p.emit(res, func() { printDeploy(p, res, o) }); perr != nil {
				return perr
			}
			if res.Failed() {
				return &ExitError{Code: ExitError1, Err: fmt.Errorf("some deployments were refused or failed")}
			}
			if o.Check && res.NeedsAction() {
				return errCheck
			}
			return nil
		},
	}
	addSelection(cmd, &sel, selKind|selTarget|selAllTargets|selOrigin|selScope|selProject)
	f := cmd.Flags()
	f.BoolVar(&o.All, "all", false, "Deploy all assets assigned to selected targets")
	f.StringVar(&o.Method, "method", "auto", "auto | link | copy | package")
	f.StringVar(&output, "output", "", "Destination for generated packages")
	f.BoolVar(&o.DryRun, "dry-run", false, "Preview changes and manual steps")
	f.BoolVar(&o.Check, "check", false, "Exit unsuccessfully if deployment is out of date")
	f.BoolVar(&o.Adopt, "adopt", false, "Take over unmanaged destinations identical to the store")
	return cmd
}

func evalWhere(e *core.Eval) string {
	if e.Project != "" {
		return e.Target + "@" + e.Project
	}
	return e.Target
}

func printDeploy(p *printer, res *core.DeployResult, o core.DeployOptions) {
	for _, a := range res.Assigned {
		p.printf("assigned %s\n", a)
	}
	if len(res.Evals) == 0 {
		p.printf("Nothing to deploy. Assign assets with: agentctl target assign TARGET ASSET...\n")
		return
	}
	var rows [][]string
	for _, e := range res.Evals {
		action := e.Action
		if o.Check {
			action = e.State
		}
		detail := p.abbrev(e.Dest)
		if e.Error != "" {
			detail = e.Error
		} else if e.State == core.StateBlocked || e.State == core.StateConflict || detail == "" {
			detail = e.Detail
		}
		rows = append(rows, []string{evalWhere(e), e.Asset, dash(e.Method), action, detail})
	}
	p.table([]string{"TARGET", "ASSET", "METHOD", "RESULT", "DETAIL"}, rows)
	printFollowUps(p, res.Evals, o.DryRun || o.Check)
	counts := res.Counts()
	var parts []string
	for _, st := range append([]string{core.StateOK}, core.ActionStates...) {
		if counts[st] > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", counts[st], st))
		}
	}
	prefix := ""
	if o.DryRun {
		prefix = "dry run; "
	}
	p.printf("\n%s%s\n", prefix, strings.Join(parts, ", "))
}

// printFollowUps lists manual steps and notes under the table.
func printFollowUps(p *printer, evals []*core.Eval, preview bool) {
	first := true
	for _, e := range evals {
		showSteps := len(e.Steps) > 0 && e.State != core.StateOK && e.State != core.StateBlocked && e.Action != "shared"
		if !showSteps && len(e.Notes) == 0 {
			continue
		}
		if first {
			p.printf("\n")
			first = false
		}
		p.printf("%s %s:\n", evalWhere(e), e.Asset)
		if showSteps {
			printSteps(p, e.Steps, "  ")
		}
		for _, n := range e.Notes {
			p.printf("  note: %s\n", n)
		}
	}
}

func newStatusCmd(g *globals) *cobra.Command {
	var sel selection
	var only string
	var check bool
	cmd := &cobra.Command{
		Use:   "status [OPTIONS]",
		Short: "Show missing, outdated, conflicting, or unsupported assets",
		Long: `Show the deployment state of every assigned asset.

States:
  ok        deployed and current (or acknowledged)
  missing   assigned but not deployed
  outdated  deployed from an older revision of the store
  conflict  the destination holds something agentctl did not deploy, or
            its deployed copy was modified
  blocked   the target cannot take the asset, or a dependency is missing
  manual    waiting for a manual upload or command, then acknowledgement`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := g.open()
			if err != nil {
				return err
			}
			s, err := sel.resolve(a)
			if err != nil {
				return err
			}
			if only != "" {
				if only, err = core.ParseState(only); err != nil {
					return usageErr("%v", err)
				}
			}
			res, err := a.Status(core.DeployOptions{Selection: s, Only: only})
			if err != nil {
				return err
			}
			p := g.printer()
			if perr := p.emit(res, func() {
				if len(res.Evals) == 0 {
					if only != "" {
						p.printf("Nothing is %s.\n", only)
					} else {
						p.printf("No assets are assigned. Assign with: agentctl target assign TARGET ASSET...\n")
					}
					return
				}
				var rows [][]string
				for _, e := range res.Evals {
					rows = append(rows, []string{evalWhere(e), e.Asset, e.State, dash(e.Method), e.Detail})
				}
				p.table([]string{"TARGET", "ASSET", "STATE", "METHOD", "DETAIL"}, rows)
				for _, n := range res.Notes {
					p.printf("note: %s\n", n)
				}
				counts := res.Counts()
				if counts[core.StateMissing]+counts[core.StateOutdated] > 0 {
					p.printf("\nPreview with `agentctl deploy --all --dry-run`; apply with `agentctl deploy --all`.\n")
				}
				if counts[core.StateConflict] > 0 {
					p.printf("Conflicts: inspect with `agentctl diff ASSET --target TARGET`; agentctl never overwrites them.\n")
				}
				if counts[core.StateManual] > 0 {
					p.printf("Manual steps: `agentctl deploy ASSET --target TARGET` reprints them; record completion with `agentctl target acknowledge`.\n")
				}
			}); perr != nil {
				return perr
			}
			if check && res.NeedsAction() {
				return errCheck
			}
			return nil
		},
	}
	addSelection(cmd, &sel, selKind|selTarget|selAllTargets|selOrigin|selScope|selProject)
	cmd.Flags().StringVar(&only, "only", "", "missing | outdated | conflict | blocked | manual")
	cmd.Flags().BoolVar(&check, "check", false, "Exit unsuccessfully if action is required")
	return cmd
}

func newUpdateCmd(g *globals) *cobra.Command {
	var sel selection
	var o core.UpdateOptions
	cmd := &cobra.Command{
		Use:   "update [ASSET...] [OPTIONS]",
		Short: "Preview or apply upstream updates to imported assets",
		Long: `Compare imported assets with their recorded upstream (path, ZIP, repository,
or target installation). Without --apply, nothing changes.

--check compares repository revisions without cloning.
--apply replaces changed assets; if any selected asset has both an upstream
change and local edits, it stops before changing anything.`,
		Example: `  agentctl update --check
  agentctl update superpowers --dry-run
  agentctl update --apply
  agentctl update pdf --version v2.0.0 --apply`,
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := g.open()
			if err != nil {
				return err
			}
			s, err := sel.resolve(a)
			if err != nil {
				return err
			}
			if o.Apply && (o.DryRun || o.Check) {
				return usageErr("--apply cannot be combined with --check or --dry-run")
			}
			o.Selection, o.Assets = s, args
			results, uerr := a.Update(o)
			p := g.printer()
			if err := p.emit(results, func() {
				if len(results) == 0 {
					p.printf("No assets match.\n")
					return
				}
				var rows [][]string
				for _, r := range results {
					ver := r.Current
					if r.Available != "" && r.Available != r.Current {
						ver = dash(r.Current) + " → " + r.Available
					}
					rows = append(rows, []string{r.Asset, r.State, dash(ver), trunc(dash(firstNonEmpty(r.Detail, r.Source)), 80)})
				}
				p.table([]string{"ASSET", "STATE", "VERSION", "DETAIL"}, rows)
				for _, r := range results {
					if len(r.Changes) == 0 || r.State == core.UpdateCurrent {
						continue
					}
					p.printf("\n%s (store → upstream):\n", r.Asset)
					for _, c := range r.Changes {
						p.printf("  %-8s %s  +%d -%d\n", c.Status, c.Path, c.Added, c.Removed)
					}
				}
				if !o.Apply {
					for _, r := range results {
						if r.State == core.UpdateAvailable {
							p.printf("\nApply with: agentctl update --apply\n")
							break
						}
					}
				} else {
					for _, r := range results {
						if r.State == core.UpdateApplied {
							p.printf("\nCopies deployed to targets may now be outdated; see: agentctl status\n")
							break
						}
					}
				}
			}); err != nil {
				return err
			}
			if uerr != nil {
				return uerr
			}
			if o.Check {
				for _, r := range results {
					if r.State == core.UpdateAvailable || r.State == core.UpdateLocal {
						return errCheck
					}
				}
			}
			return nil
		},
	}
	addSelection(cmd, &sel, selKind|selOrigin)
	f := cmd.Flags()
	f.BoolVar(&o.Check, "check", false, "Report available upstream changes without applying")
	f.StringVar(&o.Version, "version", "", "Select a specific upstream revision")
	f.BoolVar(&o.DryRun, "dry-run", false, "Preview changes")
	f.BoolVar(&o.Apply, "apply", false, "Apply updates; stop on local-edit conflicts")
	return cmd
}

func firstNonEmpty(s ...string) string {
	for _, x := range s {
		if x != "" {
			return x
		}
	}
	return ""
}

func newRemoveCmd(g *globals) *cobra.Command {
	var o core.RemoveOptions
	var project string
	cmd := &cobra.Command{
		Use:   "remove ASSET [OPTIONS]",
		Short: "Remove an asset from the store or a target",
		Long: `Remove an asset's managed deployment from a target (and unassign it), or
remove the asset from the store (moved to <store>/.agentctl/trash).

Requires an explicit destination.
Refuses to remove unmanaged or independently modified files.
Account uploads cannot be deleted remotely; agentctl prints the steps.

Note: here --store means "the store is the destination". To choose a
non-default store for this command, set AGENTCTL_STORE.`,
		Example: `  agentctl remove prune-prose --target claude-code
  agentctl remove old-skill --store --dry-run`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := g.open()
			if err != nil {
				return err
			}
			if len(o.Targets) == 0 && !o.Store {
				return usageErr("remove needs a destination: --target TARGET or --store")
			}
			for _, t := range o.Targets {
				if core.TargetByName(t) == nil {
					return usageErr("unknown target %q", t)
				}
			}
			o.Project = project
			actions, rerr := a.Remove(args[0], o)
			p := g.printer()
			if err := p.emit(actions, func() {
				for _, act := range actions {
					where := act.Target
					if act.Project != "" {
						where += "@" + act.Project
					}
					if where == "" {
						where = "store"
					}
					line := fmt.Sprintf("%-14s %-16s %s", act.Action, where, act.Path)
					if act.Detail != "" {
						line += "  (" + act.Detail + ")"
					}
					p.printf("%s\n", strings.TrimRight(line, " "))
					printSteps(p, act.Steps, "  ")
				}
			}); err != nil {
				return err
			}
			return rerr
		},
	}
	f := cmd.Flags()
	f.StringArrayVar(&o.Targets, "target", nil, "Remove a managed deployment from this target; repeatable")
	f.BoolVar(&o.Store, "store", false, "Remove canonical source")
	f.StringVar(&project, "project", "", "Remove from a project's deployment")
	f.BoolVar(&o.DryRun, "dry-run", false, "Preview removal")
	return cmd
}

func newDoctorCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Check discovery, paths, packages, and dependencies",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := g.open()
			if err != nil {
				return err
			}
			checks := a.Doctor()
			p := g.printer()
			errs, warns := 0, 0
			for _, c := range checks {
				switch c.Status {
				case "error":
					errs++
				case "warn":
					warns++
				}
			}
			if err := p.emit(checks, func() {
				marks := map[string]string{"ok": "ok  ", "warn": "warn", "error": "FAIL"}
				for _, c := range checks {
					p.printf("%s  %-12s %s\n", marks[c.Status], c.Area, c.Message)
					if c.Hint != "" && c.Status != "ok" {
						p.printf("      %-12s → %s\n", "", c.Hint)
					}
				}
				p.printf("\n%d error(s), %d warning(s)\n", errs, warns)
			}); err != nil {
				return err
			}
			if errs > 0 {
				return &ExitError{Code: ExitError1}
			}
			return nil
		},
	}
}

// sortedTargets returns target names in display order.
func sortedTargets(names []string) []string {
	out := append([]string(nil), names...)
	order := map[string]int{}
	for i, t := range core.Targets {
		order[t.Name] = i
	}
	sort.SliceStable(out, func(i, j int) bool { return order[out[i]] < order[out[j]] })
	return out
}
