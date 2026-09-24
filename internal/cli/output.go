package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/cmtonkinson/agentctl/internal/core"
	"github.com/cmtonkinson/agentctl/internal/fsx"
)

type printer struct {
	out   io.Writer
	json  bool
	quiet bool
	home  string
}

// emit writes v as JSON, or calls text unless quiet.
func (p *printer) emit(v any, text func()) error {
	if p.json {
		b, err := json.MarshalIndent(v, "", "  ")
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(p.out, string(b))
		return err
	}
	if !p.quiet {
		text()
	}
	return nil
}

func (p *printer) printf(format string, args ...any) {
	fmt.Fprintf(p.out, format, args...)
}

func (p *printer) table(headers []string, rows [][]string) {
	tw := tabwriter.NewWriter(p.out, 0, 0, 2, ' ', 0)
	if len(headers) > 0 {
		fmt.Fprintln(tw, strings.Join(headers, "\t"))
	}
	for _, r := range rows {
		fmt.Fprintln(tw, strings.Join(r, "\t"))
	}
	tw.Flush()
}

func (p *printer) abbrev(path string) string { return fsx.Abbrev(path, p.home) }

func trunc(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// selection holds the shared selection flags.
type selection struct {
	kind       string
	targets    []string
	allTargets bool
	origin     string
	scope      string
	project    string
	excludes   []string
}

type selFlag int

const (
	selKind selFlag = 1 << iota
	selTarget
	selAllTargets
	selOrigin
	selScope
	selProject
	selExclude
	selAll = selKind | selTarget | selAllTargets | selOrigin | selScope | selProject | selExclude
)

func addSelection(cmd *cobra.Command, s *selection, which selFlag) {
	f := cmd.Flags()
	if which&selKind != 0 {
		f.StringVar(&s.kind, "kind", "", "Filter by asset type: instructions | skills | plugins | tools")
	}
	if which&selTarget != 0 {
		f.StringArrayVar(&s.targets, "target", nil, "Select a target; repeatable")
	}
	if which&selAllTargets != 0 {
		f.BoolVar(&s.allTargets, "all-targets", false, "Select every target, including disabled ones")
	}
	if which&selOrigin != 0 {
		f.StringVar(&s.origin, "origin", "", "personal | third-party | system")
	}
	if which&selScope != 0 {
		f.StringVar(&s.scope, "scope", "", "user | account | project")
	}
	if which&selProject != 0 {
		f.StringVar(&s.project, "project", "", "Select a project")
	}
	if which&selExclude != 0 {
		f.StringArrayVar(&s.excludes, "exclude", nil, "Exclude a source path; repeatable")
	}
}

func (s *selection) resolve(a *core.App) (core.Selection, error) {
	out := core.Selection{Targets: s.targets, AllTargets: s.allTargets, Excludes: s.excludes}
	if s.kind != "" {
		k, err := core.ParseKind(s.kind)
		if err != nil {
			return out, usageErr("%v", err)
		}
		out.Kind = k
	}
	if s.origin != "" {
		o, err := core.ParseOrigin(s.origin)
		if err != nil {
			return out, usageErr("%v", err)
		}
		out.Origin = o
	}
	if s.scope != "" {
		sc, err := core.ParseScope(s.scope)
		if err != nil {
			return out, usageErr("%v", err)
		}
		out.Scope = sc
	}
	for _, t := range s.targets {
		if core.TargetByName(t) == nil {
			return out, usageErr("unknown target %q (have: %s)", t, strings.Join(core.TargetNames(), ", "))
		}
	}
	if s.project != "" {
		p := a.AbsPath(s.project)
		if !fsx.IsDir(p) {
			return out, fmt.Errorf("project %s is not a directory", s.project)
		}
		out.Project = p
	}
	return out, nil
}

func printSteps(p *printer, steps []string, indent string) {
	for _, s := range steps {
		p.printf("%s→ %s\n", indent, s)
	}
}
