// Package core implements agentctl: the store, targets, discovery, import,
// deployment, and maintenance commands. The cli package renders results.
package core

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/cmtonkinson/agentctl/internal/fsx"
)

// App carries the store, configuration, state, and environment for one run.
type App struct {
	Store  *Store
	Config *Config
	State  *State
	Home   string
	GOOS   string
	Getenv func(string) string
	Now    func() time.Time

	// LookPath and Run are swappable for tests.
	LookPath func(string) (string, error)
	Run      func(ctx context.Context, dir, name string, args ...string) ([]byte, error)

	// OutputOverride replaces the configured package output directory.
	OutputOverride string
}

// DefaultStore returns $AGENTCTL_STORE or ~/.agents.
func DefaultStore() string {
	if s := os.Getenv("AGENTCTL_STORE"); s != "" {
		return s
	}
	return "~/.agents"
}

// Open loads config and state for the store at storePath.
func Open(storePath string) (*App, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("cannot determine home directory: %w", err)
	}
	a := &App{
		Home:     home,
		GOOS:     runtime.GOOS,
		Getenv:   os.Getenv,
		Now:      time.Now,
		LookPath: exec.LookPath,
		Run:      runCommand,
	}
	root, err := filepath.Abs(a.Expand(storePath))
	if err != nil {
		return nil, err
	}
	a.Store = &Store{Root: root}
	if a.Config, err = LoadConfig(a.ConfigPath()); err != nil {
		return nil, err
	}
	if a.State, err = LoadState(a.statePath()); err != nil {
		return nil, fmt.Errorf("%s: %w", a.statePath(), err)
	}
	return a, nil
}

func runCommand(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg != "" {
			return out, fmt.Errorf("%s %s: %s", name, strings.Join(args, " "), lastLine(msg))
		}
		return out, fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), err)
	}
	return out, nil
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return lines[len(lines)-1]
}

// ConfigPath is <store>/agentctl.yaml.
func (a *App) ConfigPath() string { return filepath.Join(a.Store.Root, ConfigFile) }

func (a *App) statePath() string { return a.Store.Internal("state.json") }

// SaveConfig writes the config file.
func (a *App) SaveConfig() error { return a.Config.Save(a.ConfigPath()) }

// SaveState writes the state file.
func (a *App) SaveState() error { return a.State.Save(a.statePath()) }

// Expand resolves ~ in a user-supplied path.
func (a *App) Expand(p string) string { return fsx.Expand(p, a.Home) }

// AbsPath expands and absolutizes a user-supplied path.
func (a *App) AbsPath(p string) string {
	abs, err := filepath.Abs(a.Expand(p))
	if err != nil {
		return a.Expand(p)
	}
	return abs
}

// Abbrev shortens a path for display.
func (a *App) Abbrev(p string) string { return fsx.Abbrev(p, a.Home) }

// ProjectKey returns the config key for a project path (home-relative with ~).
func (a *App) ProjectKey(p string) string {
	if p == "" {
		return ""
	}
	return a.Abbrev(a.AbsPath(p))
}

// BinDir is where script tools are linked.
func (a *App) BinDir() string {
	if a.Config.BinDir != "" {
		return a.AbsPath(a.Config.BinDir)
	}
	return filepath.Join(a.Home, ".local", "bin")
}

// OutputDir is where packages for upload-only targets are written.
func (a *App) OutputDir() string {
	if a.OutputOverride != "" {
		return a.AbsPath(a.OutputOverride)
	}
	if a.Config.Deploy.Output != "" {
		return a.AbsPath(a.Config.Deploy.Output)
	}
	return a.Store.Internal("packages")
}

// Excludes returns configured plus extra exclude paths, absolutized.
func (a *App) Excludes(extra []string) []string {
	var out []string
	for _, e := range append(append([]string{}, a.Config.Exclude...), extra...) {
		out = append(out, a.AbsPath(e))
	}
	return out
}

// Excluded reports whether p lies under any exclude path.
func Excluded(p string, excludes []string) bool {
	if p == "" {
		return false
	}
	for _, e := range excludes {
		if fsx.Within(e, p) {
			return true
		}
	}
	return false
}

// Editor returns the editor command.
func (a *App) Editor() string {
	for _, e := range []string{a.Config.Editor, a.Getenv("VISUAL"), a.Getenv("EDITOR")} {
		if e != "" {
			return e
		}
	}
	return "vi"
}

// Selection narrows commands to kinds, targets, origins, scopes, and a project.
type Selection struct {
	Kind       Kind
	Targets    []string
	AllTargets bool
	Origin     string
	Scope      string
	Project    string // absolute path
	Excludes   []string
}

// SelectedTargets resolves explicit targets, or all enabled targets when
// none are named (every target with AllTargets).
func (a *App) SelectedTargets(sel Selection) ([]string, error) {
	if len(sel.Targets) > 0 {
		var out []string
		for _, t := range sel.Targets {
			if TargetByName(t) == nil {
				return nil, fmt.Errorf("unknown target %q (have: %s)", t, strings.Join(TargetNames(), ", "))
			}
			if !contains(out, t) {
				out = append(out, t)
			}
		}
		return out, nil
	}
	var out []string
	for _, t := range Targets {
		if sel.AllTargets || a.Config.Enabled(t.Name) {
			out = append(out, t.Name)
		}
	}
	return out, nil
}

// MatchAsset reports whether an asset passes kind and origin filters.
func (sel Selection) MatchAsset(as *Asset) bool {
	if sel.Kind != "" && as.Kind != sel.Kind {
		return false
	}
	if sel.Origin != "" && as.Origin() != sel.Origin {
		return false
	}
	return true
}
