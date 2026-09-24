package cli

import (
	"fmt"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/cmtonkinson/agentctl/internal/core"
)

func newTargetCmd(g *globals) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "target COMMAND",
		Short: "Inspect and configure agent targets",
		Args:  cobra.NoArgs,
		RunE:  func(cmd *cobra.Command, args []string) error { return cmd.Help() },
	}
	cmd.AddCommand(
		&cobra.Command{
			Use:   "list",
			Short: "List targets, detection, and assignment counts",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, args []string) error {
				a, err := g.open()
				if err != nil {
					return err
				}
				type row struct {
					Name     string         `json:"name"`
					Title    string         `json:"title"`
					Enabled  bool           `json:"enabled"`
					Detected core.Detection `json:"detected"`
					Assigned int            `json:"assigned"`
				}
				var rows []row
				for _, t := range core.Targets {
					rows = append(rows, row{t.Name, t.Title, a.Config.Enabled(t.Name), a.Detect(t.Name), len(a.Config.Assignments(t.Name, ""))})
				}
				p := g.printer()
				return p.emit(rows, func() {
					var tr [][]string
					for _, r := range rows {
						en := "enabled"
						if !r.Enabled {
							en = "disabled"
						}
						det := "found"
						if !r.Detected.Found {
							det = "not found"
						}
						tr = append(tr, []string{r.Name, r.Title, en, fmt.Sprint(r.Assigned), det + ": " + r.Detected.Detail})
					}
					p.table([]string{"TARGET", "DESCRIPTION", "STATE", "ASSIGNED", "DETECTION"}, tr)
				})
			},
		},
		&cobra.Command{
			Use:   "show TARGET",
			Short: "Show a target's paths, support, and assignments",
			Args:  cobra.ExactArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				a, err := g.open()
				if err != nil {
					return err
				}
				def := core.TargetByName(args[0])
				if def == nil {
					return usageErr("unknown target %q (have: %s)", args[0], strings.Join(core.TargetNames(), ", "))
				}
				paths := a.TargetPaths(def.Name)
				projects := map[string][]string{}
				for _, pr := range a.Config.ProjectPaths() {
					if as := a.Config.Assignments(def.Name, pr); len(as) > 0 {
						projects[pr] = as
					}
				}
				out := map[string]any{
					"name": def.Name, "title": def.Title, "enabled": a.Config.Enabled(def.Name),
					"detected": a.Detect(def.Name), "paths": paths, "support": kindSupport(def.Name),
					"assets": nonNilStrings(a.Config.Assignments(def.Name, "")), "projects": projects,
				}
				p := g.printer()
				return p.emit(out, func() {
					d := a.Detect(def.Name)
					p.printf("%s — %s\n", def.Name, def.Title)
					p.printf("  enabled   %v\n", a.Config.Enabled(def.Name))
					p.printf("  detected  %v (%s)\n", d.Found, d.Detail)
					p.printf("\nPaths (override: agentctl config set targets.%s.paths.KEY PATH):\n", def.Name)
					for _, k := range def.PathKeys {
						p.printf("  %-15s %s\n", k.Key, dash(p.abbrev(paths[k.Key])))
					}
					p.printf("\nSupport:\n")
					for _, k := range core.AllKinds {
						p.printf("  %-13s %s\n", k, kindSupport(def.Name)[string(k)])
					}
					p.printf("\nAssigned:\n")
					assets := a.Config.Assignments(def.Name, "")
					if len(assets) == 0 {
						p.printf("  (none)\n")
					}
					for _, r := range assets {
						p.printf("  %s\n", r)
					}
					for _, pr := range sortedKeysOf(projects) {
						p.printf("  project %s: %s\n", pr, strings.Join(projects[pr], ", "))
					}
				})
			},
		},
		enableCmd(g, true),
		enableCmd(g, false),
		assignCmd(g, true),
		assignCmd(g, false),
		ackCmd(g),
	)
	return cmd
}

// kindSupport summarizes each kind's support on a target.
func kindSupport(target string) map[string]string {
	sum := map[core.Kind]string{}
	switch target {
	case "claude-code":
		sum = map[core.Kind]string{
			core.Instructions: "native: CLAUDE.md (link, copy, or composed)",
			core.Skills:       "native: skills directory (link or copy)",
			core.Plugins:      "native: skills-dir plugin (link or copy)",
			core.Tools:        "MCP: printed `claude mcp add-json` (user) or .mcp.json (project); scripts: bin-dir",
		}
	case "codex":
		sum = map[core.Kind]string{
			core.Instructions: "native: AGENTS.md (link, copy, or composed)",
			core.Skills:       "native: ~/.agents/skills (link or copy; in place when the store is ~/.agents)",
			core.Plugins:      "unsupported",
			core.Tools:        "MCP: printed `codex mcp add`; scripts: bin-dir",
		}
	case "claude-chat":
		sum = map[core.Kind]string{
			core.Instructions: "package: text to paste into profile preferences",
			core.Skills:       "package: ZIP to upload in Settings → Capabilities",
			core.Plugins:      "package: ZIP to upload in the desktop app",
			core.Tools:        "MCP stdio: claude_desktop_config.json; remote: custom connector (manual)",
		}
	case "chatgpt":
		sum = map[core.Kind]string{
			core.Instructions: "package: text to paste into custom instructions",
			core.Skills:       "package: ZIP to upload (plan-dependent)",
			core.Plugins:      "unsupported",
			core.Tools:        "remote MCP: connector in developer mode (manual); stdio unsupported",
		}
	}
	out := map[string]string{}
	for k, v := range sum {
		out[string(k)] = v
	}
	return out
}

func sortedKeysOf(m map[string][]string) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func nonNilStrings(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func enableCmd(g *globals, on bool) *cobra.Command {
	name, short := "enable", "Select a target by default"
	if !on {
		name, short = "disable", "Stop selecting a target by default"
	}
	return &cobra.Command{
		Use:   name + " TARGET",
		Short: short,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := g.open()
			if err != nil {
				return err
			}
			if core.TargetByName(args[0]) == nil {
				return usageErr("unknown target %q", args[0])
			}
			a.Config.SetEnabled(args[0], on)
			if err := a.SaveConfig(); err != nil {
				return err
			}
			p := g.printer()
			return p.emit(map[string]any{"target": args[0], "enabled": on}, func() {
				p.printf("%s %sd\n", args[0], name)
			})
		},
	}
}

func assignCmd(g *globals, assign bool) *cobra.Command {
	var project string
	name, short := "assign", "Assign assets to a target"
	if !assign {
		name, short = "unassign", "Unassign assets from a target (deployed files stay; see remove)"
	}
	cmd := &cobra.Command{
		Use:   name + " TARGET ASSET...",
		Short: short,
		Args:  cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := g.open()
			if err != nil {
				return err
			}
			target := args[0]
			if core.TargetByName(target) == nil {
				return usageErr("unknown target %q", target)
			}
			key := ""
			if project != "" {
				key = a.ProjectKey(project)
			}
			var refs, warnings []string
			for _, arg := range args[1:] {
				as, err := a.Store.Find(arg, "")
				if err != nil {
					if !assign {
						// Allow unassigning refs whose asset is already gone.
						if r, perr := core.ParseRef(arg); perr == nil && r.Kind != "" {
							refs = append(refs, r.String())
							continue
						}
					}
					return err
				}
				refs = append(refs, as.Ref.String())
				if assign {
					sup := a.Support(target, as)
					if sup.Level == core.SupportUnsupported {
						warnings = append(warnings, fmt.Sprintf("%s: %s", as.Ref, sup.Notes[0]))
					}
					if key != "" && !contains(sup.Scopes, core.ScopeProject) {
						warnings = append(warnings, fmt.Sprintf("%s: %s does not support project scope for %s", as.Ref, target, as.Kind))
					}
				}
			}
			var changed []string
			if assign {
				changed = a.Config.Assign(target, key, refs...)
			} else {
				changed = a.Config.Unassign(target, key, refs...)
			}
			if err := a.SaveConfig(); err != nil {
				return err
			}
			p := g.printer()
			return p.emit(map[string]any{"target": target, "project": key, name + "ed": nonNilStrings(changed), "warnings": warnings}, func() {
				where := target
				if key != "" {
					where += "@" + key
				}
				if len(changed) == 0 {
					p.printf("No change to %s.\n", where)
				}
				for _, r := range changed {
					p.printf("%sed %s → %s\n", name, r, where)
				}
				for _, w := range warnings {
					p.printf("warning: %s\n", w)
				}
				if assign && len(changed) > 0 {
					p.printf("Deploy with: agentctl deploy --target %s --all\n", target)
				}
				if !assign && len(changed) > 0 {
					p.printf("Deployed copies are left in place; remove them with: agentctl remove ASSET --target %s\n", target)
				}
			})
		},
	}
	cmd.Flags().StringVar(&project, "project", "", "Assign within a project instead of user scope")
	return cmd
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func ackCmd(g *globals) *cobra.Command {
	var hash, project string
	cmd := &cobra.Command{
		Use:   "acknowledge TARGET ASSET --hash HASH",
		Short: "Record a completed manual upload",
		Long: `Record that you completed a manual step (an upload, paste, or connector)
for ASSET on TARGET. HASH is the value 'agentctl deploy' printed; it must
match the current package or one agentctl built earlier.`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if hash == "" {
				return usageErr("--hash is required")
			}
			a, err := g.open()
			if err != nil {
				return err
			}
			ack, warnings, err := a.Acknowledge(args[0], project, args[1], hash)
			if err != nil {
				return err
			}
			p := g.printer()
			return p.emit(map[string]any{"acknowledgement": ack, "warnings": warnings}, func() {
				p.printf("recorded %s on %s at %s\n", ack.Asset, ack.Target, core.ShortHash(ack.Hash))
				for _, w := range warnings {
					p.printf("warning: %s\n", w)
				}
			})
		},
	}
	cmd.Flags().StringVar(&hash, "hash", "", "Hash printed by deploy")
	cmd.Flags().StringVar(&project, "project", "", "Project scope")
	return cmd
}

func newConfigCmd(g *globals) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config COMMAND",
		Short: "Inspect and configure agentctl",
		Long:  "Configuration lives in <store>/agentctl.yaml.",
		Args:  cobra.NoArgs,
		RunE:  func(cmd *cobra.Command, args []string) error { return cmd.Help() },
	}
	show := &cobra.Command{
		Use:   "show",
		Short: "Show configuration and effective values",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := g.open()
			if err != nil {
				return err
			}
			paths := map[string]map[string]string{}
			for _, t := range core.Targets {
				paths[t.Name] = a.TargetPaths(t.Name)
			}
			out := map[string]any{
				"store": a.Store.Root, "config_file": a.ConfigPath(), "config": a.Config,
				"effective": map[string]any{
					"editor": a.Editor(), "bin-dir": a.BinDir(), "deploy.output": a.OutputDir(),
					"deploy.method": firstNonEmpty(a.Config.Deploy.Method, "auto"), "target_paths": paths,
				},
			}
			p := g.printer()
			return p.emit(out, func() {
				p.printf("store          %s\n", p.abbrev(a.Store.Root))
				p.printf("config file    %s\n\n", p.abbrev(a.ConfigPath()))
				p.printf("editor         %s\n", a.Editor())
				p.printf("bin-dir        %s\n", p.abbrev(a.BinDir()))
				p.printf("deploy.method  %s\n", firstNonEmpty(a.Config.Deploy.Method, "auto"))
				p.printf("deploy.output  %s\n", p.abbrev(a.OutputDir()))
				p.printf("exclude        %s\n", dash(strings.Join(a.Config.Exclude, ", ")))
				p.printf("\nTargets:\n")
				for _, t := range core.Targets {
					p.printf("  %-12s enabled=%v assets=%d\n", t.Name, a.Config.Enabled(t.Name), len(a.Config.Assignments(t.Name, "")))
				}
				if len(a.Config.Projects) > 0 {
					p.printf("\nProjects:\n")
					for _, pr := range a.Config.ProjectPaths() {
						p.printf("  %s\n", pr)
					}
				}
				p.printf("\nKeys:\n")
				for _, k := range core.ConfigKeys {
					p.printf("  %-30s %s\n", k[0], k[1])
				}
			})
		},
	}
	get := &cobra.Command{
		Use:   "get KEY",
		Short: "Print a configuration value",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := g.open()
			if err != nil {
				return err
			}
			v, err := a.Config.Get(args[0])
			if err != nil {
				return usageErr("%v", err)
			}
			p := g.printer()
			return p.emit(map[string]any{"key": args[0], "value": v}, func() {
				switch x := v.(type) {
				case []string:
					for _, s := range x {
						p.printf("%s\n", s)
					}
				default:
					p.printf("%v\n", x)
				}
			})
		},
	}
	set := &cobra.Command{
		Use:   "set KEY VALUE",
		Short: "Set a configuration value (an empty VALUE clears it)",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := g.open()
			if err != nil {
				return err
			}
			value := args[1]
			if value != "" && (args[0] == "bin-dir" || args[0] == "deploy.output" || strings.Contains(args[0], ".paths.")) {
				value = a.ProjectKey(value) // store paths as ~/... when under home
			}
			if err := a.Config.Set(args[0], value); err != nil {
				return usageErr("%v", err)
			}
			if err := a.SaveConfig(); err != nil {
				return err
			}
			p := g.printer()
			return p.emit(map[string]any{"key": args[0], "value": value}, func() {
				p.printf("%s = %s\n", args[0], value)
			})
		},
	}
	exclude := &cobra.Command{
		Use:   "exclude COMMAND",
		Short: "Manage source paths excluded from discovery and import",
		Args:  cobra.NoArgs,
		RunE:  func(cmd *cobra.Command, args []string) error { return cmd.Help() },
	}
	exclude.AddCommand(
		excludeEdit(g, true),
		excludeEdit(g, false),
		&cobra.Command{
			Use:   "list",
			Short: "List excluded paths",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, args []string) error {
				a, err := g.open()
				if err != nil {
					return err
				}
				p := g.printer()
				return p.emit(nonNilStrings(a.Config.Exclude), func() {
					if len(a.Config.Exclude) == 0 {
						p.printf("No excluded paths.\n")
					}
					for _, e := range a.Config.Exclude {
						p.printf("%s\n", e)
					}
				})
			},
		},
	)
	cmd.AddCommand(show, get, set, exclude)
	return cmd
}

func excludeEdit(g *globals, add bool) *cobra.Command {
	name := "add"
	if !add {
		name = "remove"
	}
	return &cobra.Command{
		Use:   name + " PATH",
		Short: map[bool]string{true: "Exclude a path", false: "Stop excluding a path"}[add],
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := g.open()
			if err != nil {
				return err
			}
			path := a.ProjectKey(args[0])
			var changed bool
			if add {
				changed = a.Config.AddExclude(path)
			} else {
				changed = a.Config.RemoveExclude(path) || a.Config.RemoveExclude(args[0])
			}
			if !changed {
				if add {
					return fmt.Errorf("%s is already excluded", path)
				}
				return fmt.Errorf("%s is not excluded", path)
			}
			if err := a.SaveConfig(); err != nil {
				return err
			}
			p := g.printer()
			return p.emit(map[string]any{"path": path, "excluded": add}, func() {
				if add {
					p.printf("excluded %s\n", path)
				} else {
					p.printf("no longer excluding %s\n", path)
				}
			})
		},
	}
}
