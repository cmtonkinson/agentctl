// Package cli defines agentctl's commands and renders their output.
package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/cmtonkinson/agentctl/internal/core"
)

// ExitError carries a process exit code. A nil Err exits silently.
type ExitError struct {
	Code int
	Err  error
}

func (e *ExitError) Error() string {
	if e.Err == nil {
		return fmt.Sprintf("exit %d", e.Code)
	}
	return e.Err.Error()
}

// Exit codes.
const (
	ExitOK     = 0
	ExitError1 = 1 // failure
	ExitUsage  = 2 // invalid invocation
	ExitCheck  = 3 // --check found action required
)

func usageErr(format string, args ...any) error {
	return &ExitError{Code: ExitUsage, Err: fmt.Errorf(format, args...)}
}

var errCheck = &ExitError{Code: ExitCheck}

type globals struct {
	store string
	json  bool
	quiet bool
	out   io.Writer
	err   io.Writer
	app   *core.App
}

func (g *globals) open() (*core.App, error) {
	if g.app != nil {
		return g.app, nil
	}
	a, err := core.Open(g.store)
	if err != nil {
		return nil, err
	}
	g.app = a
	return a, nil
}

func (g *globals) printer() *printer {
	home, _ := os.UserHomeDir()
	return &printer{out: g.out, json: g.json, quiet: g.quiet, home: home}
}

// Execute runs agentctl and returns the process exit code.
func Execute(version string, args []string, stdout, stderr io.Writer) int {
	g := &globals{out: stdout, err: stderr}
	root := newRoot(g, version)
	root.SetArgs(args)
	root.SetOut(stdout)
	root.SetErr(stderr)
	err := root.Execute()
	if err == nil {
		return ExitOK
	}
	var ee *ExitError
	code := ExitError1
	if errors.As(err, &ee) {
		code = ee.Code
		if ee.Err == nil {
			return code
		}
		err = ee.Err
	} else if isUsageError(err) {
		code = ExitUsage
	}
	if g.json {
		fmt.Fprintf(stderr, "{\"error\": %q}\n", err.Error())
	} else {
		fmt.Fprintf(stderr, "agentctl: %s\n", err)
		if code == ExitUsage {
			fmt.Fprintln(stderr, "Run 'agentctl help' for usage.")
		}
	}
	return code
}

func isUsageError(err error) bool {
	msg := err.Error()
	for _, p := range []string{"unknown command", "unknown flag", "unknown shorthand", "flag needs an argument", "invalid argument", "accepts ", "requires at least", "requires exactly"} {
		if strings.Contains(msg, p) {
			return true
		}
	}
	return false
}

func newRoot(g *globals, version string) *cobra.Command {
	cobra.EnableCommandSorting = false
	root := &cobra.Command{
		Use:           "agentctl",
		Short:         "Manage agent assets from ~/.agents/",
		Version:       version,
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Help()
		},
	}
	root.PersistentFlags().StringVar(&g.store, "store", core.DefaultStore(), "Authoritative store")
	root.PersistentFlags().BoolVar(&g.json, "json", false, "Emit structured output")
	root.PersistentFlags().BoolVarP(&g.quiet, "quiet", "q", false, "Print errors only")
	root.SetVersionTemplate("agentctl {{.Version}}\n")
	root.Flags().BoolP("version", "v", false, "Show version")

	root.AddCommand(
		newInventoryCmd(g),
		newListCmd(g),
		newShowCmd(g),
		newImportCmd(g),
		newDiffCmd(g),
		newEditCmd(g),
		newDeployCmd(g),
		newStatusCmd(g),
		newUpdateCmd(g),
		newRemoveCmd(g),
		newDoctorCmd(g),
		newTargetCmd(g),
		newConfigCmd(g),
	)
	root.SetHelpCommand(&cobra.Command{
		Use:   "help [COMMAND]",
		Short: "Show command help",
		RunE: func(cmd *cobra.Command, args []string) error {
			target, _, err := root.Find(args)
			if err != nil || target == nil {
				return usageErr("unknown help topic %q", strings.Join(args, " "))
			}
			return target.Help()
		},
	})
	root.SetFlagErrorFunc(func(cmd *cobra.Command, err error) error {
		return &ExitError{Code: ExitUsage, Err: err}
	})
	defaultHelp := root.HelpFunc()
	root.SetHelpFunc(func(cmd *cobra.Command, args []string) {
		if cmd == root {
			fmt.Fprint(cmd.OutOrStdout(), rootHelp(root))
			return
		}
		defaultHelp(cmd, args)
	})
	return root
}

func rootHelp(root *cobra.Command) string {
	var b strings.Builder
	b.WriteString("Manage agent assets from ~/.agents/.\n\nUsage:\n  agentctl [GLOBAL OPTIONS] COMMAND [ARGS] [OPTIONS]\n\nTargets:\n")
	for _, t := range core.Targets {
		fmt.Fprintf(&b, "  %-13s %s\n", t.Name, t.Title)
	}
	b.WriteString("\nAssets:\n")
	for _, k := range core.AllKinds {
		fmt.Fprintf(&b, "  %-13s %s\n", k, core.KindHelp[k])
	}
	b.WriteString("\nCommands:\n")
	for _, c := range root.Commands() {
		if c.Hidden || c.Name() == "completion" {
			continue
		}
		fmt.Fprintf(&b, "  %-13s %s\n", c.Name(), c.Short)
	}
	b.WriteString(`
Global options:
  --store PATH          Authoritative store [~/.agents; $AGENTCTL_STORE]
  --json                Emit structured output
  -q, --quiet           Print errors only
  -v, --version         Show version
  -h, --help            Show help

Selection options (where a command accepts them):
  --kind KIND           Filter by asset type
  --target TARGET       Select a target; repeatable
  --all-targets         Select every target, including disabled ones
  --origin ORIGIN       personal | third-party | system
  --scope SCOPE         user | account | project
  --project PATH        Select a project
  --exclude PATH        Exclude a source path; repeatable

Examples:
  agentctl inventory --target claude-chat --kind skills
  agentctl import --from claude-code --kind skills --all --dry-run
  agentctl import ~/.claude/skills/example
  agentctl import https://github.com/org/repo/tree/main/skills/example
  agentctl show superpowers --dependencies --compatibility
  agentctl target assign claude-code prune-prose format-markdown
  agentctl deploy --target claude-code --all --dry-run
  agentctl deploy morning-brief --target claude-chat --method package
  agentctl diff morning-brief --target claude-chat
  agentctl update --check
  agentctl doctor
  agentctl config exclude add ~/work/client-repo

Boundaries:
  Source lives in ~/.agents/; vendor caches remain application-managed.
  Credentials remain in each client's credential store.
  Unsupported account operations produce explicit manual steps.
  No automatic commits, pushes, or public publication.

Exit status: 0 success, 1 failure, 2 usage error, 3 --check found work to do.
Run 'agentctl help COMMAND' for command options.
`)
	return b.String()
}
