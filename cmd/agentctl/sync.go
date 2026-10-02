package main

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// app holds the authoritative store and the known local agent locations.
type app struct {
	store, home, codexHome, claudeHome string
	out, errout                        io.Writer
}

// execute parses the small set of store and synchronization commands.
func execute(version string, args []string, out, errout io.Writer) int {
	a := app{store: os.Getenv("AGENTCTL_STORE"), home: os.Getenv("HOME"), out: out, errout: errout}
	if a.home == "" {
		fmt.Fprintln(errout, "HOME is not set")
		return 1
	}
	if a.store == "" {
		a.store = filepath.Join(a.home, ".agents")
	}
	if a.codexHome = os.Getenv("CODEX_HOME"); a.codexHome == "" {
		a.codexHome = filepath.Join(a.home, ".codex")
	}
	if a.claudeHome = os.Getenv("CLAUDE_CONFIG_DIR"); a.claudeHome == "" {
		a.claudeHome = filepath.Join(a.home, ".claude")
	}
	if len(args) == 0 {
		a.help()
		return 0
	}
	var err error
	switch args[0] {
	case "help", "-h", "--help":
		a.help()
	case "version", "--version", "-v":
		fmt.Fprintf(out, "agentctl %s\n", version)
	case "list":
		err = a.list(args[1:])
	case "status":
		err = a.status(args[1:])
	case "diff":
		err = a.diff(args[1:])
	case "pull":
		err = a.pull(args[1:])
	case "adopt":
		err = a.adopt(args[1:])
	case "deploy":
		err = a.deploy(args[1:])
	case "export":
		err = a.export(args[1:])
	default:
		err = fmt.Errorf("unknown command %q (run agentctl help)", args[0])
	}
	if err != nil {
		fmt.Fprintln(errout, "agentctl:", err)
		return 1
	}
	return 0
}

// help prints the supported commands and their safety flags.
func (a app) help() {
	fmt.Fprint(a.out, `agentctl — keep instructions and skills in ~/.agents/

Usage:
  agentctl list [-v|--verbose]
  agentctl status [NAME [TARGET]] [-v|--verbose]
  agentctl diff [NAME [TARGET]] [-v|--verbose]
  agentctl pull NAME [SOURCE] [--replace] [--dry-run]
  agentctl adopt NAME [SOURCE] [--replace] [--dry-run]
  agentctl deploy [NAME [TARGET]] [--target=TARGET] [--copy] [--replace] [--dry-run]
  agentctl export [NAME [TARGET]] [--target=TARGET]
  agentctl version

NAME is "agents.md" (alias "instructions") or a skill directory name.
TARGET is codex or claude-code for local commands; claude-chat or chatgpt
for export. Deploy defaults to both local targets; export defaults to both
account targets. Pull and adopt infer SOURCE when only one local agent has the
asset. Omit NAME to deploy the entrypoint and all skills, or to export the store.

The store is ~/.agents/{instructions,skills}/.
Codex reads skills there directly. Instructions use a pointer; other local
skills use links by default. --copy writes copies instead. --replace backs up
divergent destinations before changing them. Pull copies a live asset into the
store; adopt then deploys it to the other local agent. Pull requires --replace
if the canonical version differs. No command moves a source. --dry-run previews
actions without writing.

list prints aligned types and names; status groups kind, item, and target.
-v/--verbose shows paths for list and status, and changed lines for diff.
Bare diff shows changed files across all local drift.

Environment: AGENTCTL_STORE, CODEX_HOME, CLAUDE_CONFIG_DIR, HOME.
`)
}

// assetNames returns instructions and the immediate skill directories in the store.
func (a app) assetNames() ([]string, error) {
	names := []string{}
	if exists(a.source("instructions")) {
		names = append(names, "instructions")
	}
	entries, err := os.ReadDir(filepath.Join(a.store, "skills"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	for _, entry := range entries {
		if entry.Name() == ".DS_Store" || !entry.IsDir() {
			continue
		}
		if exists(filepath.Join(a.store, "skills", entry.Name(), "SKILL.md")) {
			names = append(names, entry.Name())
		}
	}
	return names, nil
}

// parseFlags permits boolean flags before or after positional arguments.
func parseFlags(flags *flag.FlagSet, args []string) ([]string, error) {
	options, positional := []string{}, []string{}
	for _, arg := range args {
		if strings.HasPrefix(arg, "-") {
			options = append(options, arg)
		} else {
			positional = append(positional, arg)
		}
	}
	if err := flags.Parse(options); err != nil {
		return nil, err
	}
	return positional, nil
}

// instructionFiles inventories files without analyzing their references.
func (a app) instructionFiles() ([]string, error) {
	entries, err := os.ReadDir(filepath.Join(a.store, "instructions"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	names := []string{}
	for _, entry := range entries {
		if !entry.IsDir() && entry.Name() != ".DS_Store" {
			names = append(names, entry.Name())
		}
	}
	return names, nil
}

// displayPath abbreviates home paths without changing paths outside the home.
func (a app) displayPath(path string) string {
	rel, err := filepath.Rel(a.home, path)
	if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		if rel == "." {
			return "~"
		}
		return "~/" + filepath.ToSlash(rel)
	}
	return path
}

// list prints aligned canonical instruction and skill names or paths.
func (a app) list(args []string) error {
	flags := flag.NewFlagSet("list", flag.ContinueOnError)
	flags.SetOutput(a.errout)
	verbose := flags.Bool("verbose", false, "show paths")
	flags.BoolVar(verbose, "v", false, "show paths")
	positional, err := parseFlags(flags, args)
	if err != nil {
		return err
	}
	if len(positional) != 0 {
		return errors.New("usage: agentctl list [-v|--verbose]")
	}
	instructions, err := a.instructionFiles()
	if err != nil {
		return err
	}
	for _, name := range instructions {
		label := name
		if *verbose {
			label = a.displayPath(filepath.Join(a.store, "instructions", name))
		}
		fmt.Fprintf(a.out, "%-11s %s\n", "instruction", label)
	}
	names, err := a.assetNames()
	if err != nil {
		return err
	}
	for _, name := range names {
		if name == "instructions" {
			continue
		}
		label := name
		if *verbose {
			label = a.displayPath(a.source(name))
		}
		fmt.Fprintf(a.out, "%-11s %s\n", "skill", label)
	}
	return nil
}

// normalizeName preserves the instructions alias for the entrypoint.
func normalizeName(name string) string {
	if name == "agents.md" {
		return "instructions"
	}
	return name
}

// source returns an asset's path in the authoritative store.
func (a app) source(name string) string {
	if name == "instructions" {
		return filepath.Join(a.store, "instructions", "agents.md")
	}
	return filepath.Join(a.store, "skills", name)
}

// targetPath returns the single known user-global location for an asset.
func (a app) targetPath(target, name string) (string, error) {
	if !validName(name) {
		return "", fmt.Errorf("invalid asset name %q", name)
	}
	switch target {
	case "codex":
		if name == "instructions" {
			return filepath.Join(a.codexHome, "AGENTS.md"), nil
		}
		return filepath.Join(a.codexHome, "skills", name), nil
	case "claude-code":
		if name == "instructions" {
			return filepath.Join(a.claudeHome, "CLAUDE.md"), nil
		}
		return filepath.Join(a.claudeHome, "skills", name), nil
	default:
		return "", fmt.Errorf("unknown local target %q", target)
	}
}

// validName keeps asset selection inside the known skill directories.
func validName(name string) bool {
	return name != "" && name != "." && name != ".." && !strings.ContainsAny(name, `/\`) && !strings.HasPrefix(name, ".")
}

// liveNames includes known local skills, including newly installed ones.
func (a app) liveNames(target string) ([]string, error) {
	base, err := a.targetPath(target, "placeholder")
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(filepath.Dir(base))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	names := []string{"instructions"}
	for _, entry := range entries {
		name := entry.Name()
		if !validName(name) {
			continue
		}
		full := filepath.Join(filepath.Dir(base), name)
		if exists(filepath.Join(full, "SKILL.md")) {
			names = append(names, name)
		}
	}
	return names, nil
}

// knownNames unions canonical assets and installs in the selected agents.
func (a app) knownNames(targets []string) ([]string, error) {
	names, err := a.assetNames()
	if err != nil {
		return nil, err
	}
	set := map[string]bool{}
	for _, name := range names {
		set[name] = true
	}
	for _, target := range targets {
		live, err := a.liveNames(target)
		if err != nil {
			return nil, err
		}
		for _, name := range live {
			set[name] = true
		}
	}
	names = []string{}
	if set["instructions"] {
		names = append(names, "instructions")
		delete(set, "instructions")
	}
	skills := []string{}
	for name := range set {
		skills = append(skills, name)
	}
	sort.Strings(skills)
	return append(names, skills...), nil
}

// status groups instruction files and skills by item, then agent.
func (a app) status(args []string) error {
	flags := flag.NewFlagSet("status", flag.ContinueOnError)
	flags.SetOutput(a.errout)
	verbose := flags.Bool("verbose", false, "show paths")
	flags.BoolVar(verbose, "v", false, "show paths")
	positional, err := parseFlags(flags, args)
	if err != nil {
		return err
	}
	if len(positional) > 2 {
		return errors.New("usage: agentctl status [NAME [TARGET]] [-v|--verbose]")
	}
	targets := []string{"codex", "claude-code"}
	selected := ""
	if len(positional) == 1 {
		if positional[0] == "codex" || positional[0] == "claude-code" {
			targets = positional
		} else {
			selected = normalizeName(positional[0])
		}
	} else if len(positional) == 2 {
		selected = normalizeName(positional[0])
		targets = positional[1:]
	}
	if selected != "" {
		if _, err := a.targetPath(targets[0], selected); err != nil {
			return err
		}
		label := selected
		if *verbose {
			label = a.displayPath(a.source(selected))
		}
		if selected == "instructions" {
			if !*verbose {
				label = "agents.md"
			}
			fmt.Fprintf(a.out, "Instructions:\n%s\n", label)
		} else {
			fmt.Fprintf(a.out, "Skills:\n%s\n", label)
		}
		return a.statusTargets(targets, selected, *verbose)
	}
	names, err := a.knownNames(targets)
	if err != nil {
		return err
	}
	instructions, err := a.instructionFiles()
	if err != nil {
		return err
	}
	// Show the entrypoint even when it is only present in an agent.
	if !exists(a.source("instructions")) {
		for _, target := range targets {
			path, _ := a.targetPath(target, "instructions")
			if exists(path) {
				instructions = append(instructions, "agents.md")
				sort.Strings(instructions)
				break
			}
		}
	}
	fmt.Fprintln(a.out, "Instructions:")
	for _, name := range instructions {
		label := name
		if *verbose {
			label = a.displayPath(filepath.Join(a.store, "instructions", name))
		}
		fmt.Fprintln(a.out, label)
		if name == "agents.md" {
			if err := a.statusTargets(targets, "instructions", *verbose); err != nil {
				return err
			}
		}
	}
	fmt.Fprintln(a.out, "\nSkills:")
	for _, name := range names {
		if name == "instructions" {
			continue
		}
		label := name
		if *verbose {
			label = a.displayPath(a.source(name))
		}
		fmt.Fprintln(a.out, label)
		if err := a.statusTargets(targets, name, *verbose); err != nil {
			return err
		}
	}
	return nil
}

// statusTargets reports use methods and optional live paths for one item.
func (a app) statusTargets(targets []string, name string, verbose bool) error {
	for _, target := range targets {
		state, err := a.state(target, name)
		if err != nil {
			return err
		}
		fmt.Fprintf(a.out, "  %-11s %s", target, state)
		if verbose && state != "direct" {
			path, err := a.targetPath(target, name)
			if err != nil {
				return err
			}
			fmt.Fprintf(a.out, " (%s)", a.displayPath(path))
		}
		fmt.Fprintln(a.out)
	}
	return nil
}

// state compares canonical and live assets, recognizing direct and linked use.
func (a app) state(target, name string) (string, error) {
	path, err := a.targetPath(target, name)
	if err != nil {
		return "", err
	}
	src := a.source(name)
	srcExists, dstExists := exists(src), exists(path)
	if name != "instructions" && target == "codex" && filepath.Clean(a.store) == filepath.Join(a.home, ".agents") && !dstExists {
		if srcExists {
			return "direct", nil
		}
		return "missing", nil
	}
	if !srcExists && dstExists {
		return "new", nil
	}
	if srcExists && !dstExists {
		return "missing", nil
	}
	if !srcExists {
		return "missing", nil
	}
	if linked(path, src) {
		return "linked", nil
	}
	if name == "instructions" && pointerTo(path, src) {
		return "pointer", nil
	}
	equal, err := same(src, path)
	if err != nil {
		return "", err
	}
	if equal {
		return "same copy", nil
	}
	return "different", nil
}

// diff shows all local drift or a selected canonical/live comparison.
func (a app) diff(args []string) error {
	flags := flag.NewFlagSet("diff", flag.ContinueOnError)
	flags.SetOutput(a.errout)
	verbose := flags.Bool("verbose", false, "show changed lines")
	flags.BoolVar(verbose, "v", false, "show changed lines")
	positional, err := parseFlags(flags, args)
	if err != nil {
		return err
	}
	if len(positional) == 2 {
		return a.diffName(normalizeName(positional[0]), positional[1:], *verbose)
	}
	if len(positional) > 2 {
		return errors.New("usage: agentctl diff [NAME [TARGET]] [-v|--verbose]")
	}
	targets := []string{"codex", "claude-code"}
	if len(positional) == 1 {
		return a.diffName(normalizeName(positional[0]), targets, *verbose)
	}
	names, err := a.knownNames(targets)
	if err != nil {
		return err
	}
	found, kind := false, ""
	for _, name := range names {
		item, section := name, "Skills:"
		if name == "instructions" {
			item, section = "agents.md", "Instructions:"
		}
		printed := false
		for _, target := range targets {
			state, err := a.state(target, name)
			if err != nil {
				return err
			}
			path, _ := a.targetPath(target, name)
			if state != "different" && state != "new" && state != "missing" {
				continue
			}
			if !exists(path) && !exists(a.source(name)) {
				continue
			}
			if kind != section {
				if found {
					fmt.Fprintln(a.out)
				}
				fmt.Fprintln(a.out, section)
				kind = section
			}
			if !printed {
				fmt.Fprintln(a.out, item)
				printed = true
			}
			fmt.Fprintf(a.out, "  %-11s %s\n", target, diffStateLabel(state, target))
			if err := a.diffAsset(target, name, *verbose); err != nil {
				return err
			}
			found = true
		}
	}
	if !found {
		fmt.Fprintln(a.out, "No differences.")
	}
	return nil
}

// diffName shows one asset's state and changed files for selected local targets.
func (a app) diffName(name string, targets []string, verbose bool) error {
	if name == "instructions" {
		fmt.Fprintln(a.out, "Instructions:\nagents.md")
	} else {
		fmt.Fprintf(a.out, "Skills:\n%s\n", name)
	}
	for _, target := range targets {
		state, err := a.state(target, name)
		if err != nil {
			return err
		}
		fmt.Fprintf(a.out, "  %-11s %s\n", target, diffStateLabel(state, target))
		if state == "different" || state == "new" || state == "missing" {
			if err := a.diffAsset(target, name, verbose); err != nil {
				return err
			}
		}
	}
	return nil
}

// diffStateLabel identifies which side supplies one-sided file names.
func diffStateLabel(state, target string) string {
	switch state {
	case "missing":
		return state + " (files in store)"
	case "new":
		return state + " (files in " + target + ")"
	default:
		return state
	}
}

// diffAsset compares portable snapshots, treating an absent side as empty.
func (a app) diffAsset(target, name string, verbose bool) error {
	path, err := a.targetPath(target, name)
	if err != nil {
		return err
	}
	src := a.source(name)
	state, err := a.state(target, name)
	if err != nil {
		return err
	}
	if state == "linked" || state == "pointer" || state == "direct" {
		fmt.Fprintf(a.out, "%s: no difference\n", state)
		return nil
	}
	if !exists(src) && !exists(path) {
		return errors.New("both canonical and live assets are missing")
	}
	tmp, err := os.MkdirTemp("", "agentctl-diff-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	directory := name != "instructions"
	for _, side := range []struct{ source, label string }{{src, "canonical"}, {path, "live"}} {
		dst := filepath.Join(tmp, side.label)
		if exists(side.source) {
			err = copyAsset(side.source, dst)
		} else if directory {
			err = os.Mkdir(dst, 0755)
		} else {
			err = os.WriteFile(dst, nil, 0644)
		}
		if err != nil {
			return err
		}
	}
	if !verbose {
		if !directory {
			equal, err := same(filepath.Join(tmp, "canonical"), filepath.Join(tmp, "live"))
			if err != nil {
				return err
			}
			if !equal {
				fmt.Fprintln(a.out, "    agents.md")
			}
			return nil
		}
		files := map[string][2]string{}
		for side, label := range []string{"canonical", "live"} {
			root := filepath.Join(tmp, label)
			err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
				if walkErr != nil {
					return walkErr
				}
				if entry.IsDir() {
					return nil
				}
				rel, err := filepath.Rel(root, path)
				if err != nil {
					return err
				}
				pair := files[rel]
				pair[side] = path
				files[rel] = pair
				return nil
			})
			if err != nil {
				return err
			}
		}
		names := make([]string, 0, len(files))
		for file := range files {
			names = append(names, file)
		}
		sort.Strings(names)
		for _, file := range names {
			pair := files[file]
			if pair[0] != "" && pair[1] != "" {
				equal, err := same(pair[0], pair[1])
				if err != nil {
					return err
				}
				if equal {
					continue
				}
			}
			fmt.Fprintln(a.out, "    "+filepath.ToSlash(file))
		}
		return nil
	}
	cmd := exec.Command("git", "diff", "--no-index", "--", "canonical", "live")
	cmd.Dir = tmp
	cmd.Stdout, cmd.Stderr = a.out, a.errout
	err = cmd.Run()
	if e, ok := err.(*exec.ExitError); ok && e.ExitCode() == 1 {
		return nil
	}
	return err
}

// pull copies a live asset into the store, preserving a divergent canonical copy.
func (a app) pull(args []string) error {
	flags := flag.NewFlagSet("pull", flag.ContinueOnError)
	flags.SetOutput(a.errout)
	replace := flags.Bool("replace", false, "back up and replace a different canonical asset")
	dryRun := flags.Bool("dry-run", false, "preview without writing")
	positional, err := parseFlags(flags, args)
	if err != nil {
		return err
	}
	if len(positional) < 1 || len(positional) > 2 {
		return errors.New("usage: agentctl pull NAME [SOURCE] [--replace] [--dry-run]")
	}
	name := normalizeName(positional[0])
	source := ""
	if len(positional) == 2 {
		source = positional[1]
	} else {
		source, err = a.inferSource(name)
		if err != nil {
			return err
		}
	}
	path, err := a.targetPath(source, name)
	if err != nil {
		return err
	}
	if !exists(path) {
		return fmt.Errorf("live asset missing: %s", path)
	}
	src := a.source(name)
	if linked(path, src) || name == "instructions" && pointerTo(path, src) {
		return errors.New("live asset already uses the store")
	}
	if exists(src) {
		equal, err := same(path, src)
		if err != nil {
			return err
		}
		if equal {
			fmt.Fprintln(a.out, "already identical")
			return nil
		}
		if !*replace {
			return errors.New("canonical asset differs; inspect with diff, then use --replace")
		}
		if *dryRun {
			fmt.Fprintf(a.out, "would backup %s under %s\n", a.displayPath(src), a.displayPath(filepath.Join(a.home, ".local/state/agentctl/backups")))
		} else if err := a.backup(src); err != nil {
			return err
		}
	}
	if *dryRun {
		fmt.Fprintf(a.out, "would copy %s -> %s\n", a.displayPath(path), a.displayPath(src))
		return nil
	}
	if err := replaceWithCopy(path, src); err != nil {
		return err
	}
	fmt.Fprintf(a.out, "pulled %s from %s\n", name, source)
	return nil
}

// inferSource accepts an omitted source only when one local asset exists.
func (a app) inferSource(name string) (string, error) {
	sources := []string{}
	for _, target := range []string{"codex", "claude-code"} {
		path, err := a.targetPath(target, name)
		if err != nil {
			return "", err
		}
		if exists(path) {
			sources = append(sources, target)
		}
	}
	if len(sources) == 1 {
		return sources[0], nil
	}
	if len(sources) == 0 {
		return "", fmt.Errorf("no local source for %s", name)
	}
	return "", fmt.Errorf("multiple local sources for %s; specify codex or claude-code", name)
}

// adopt adds a new local asset to the store and makes it available to the other agent.
func (a app) adopt(args []string) error {
	flags := flag.NewFlagSet("adopt", flag.ContinueOnError)
	flags.SetOutput(a.errout)
	replace := flags.Bool("replace", false, "back up and replace a different destination")
	dryRun := flags.Bool("dry-run", false, "preview without writing")
	positional, err := parseFlags(flags, args)
	if err != nil {
		return err
	}
	if len(positional) < 1 || len(positional) > 2 {
		return errors.New("usage: agentctl adopt NAME [SOURCE] [--replace] [--dry-run]")
	}
	name := normalizeName(positional[0])
	src := a.source(name)
	if exists(src) {
		return fmt.Errorf("%s is already in the store; use deploy %s", name, name)
	}
	source := ""
	if len(positional) == 2 {
		source = positional[1]
	} else {
		source, err = a.inferSource(name)
		if err != nil {
			return err
		}
	}
	from, err := a.targetPath(source, name)
	if err != nil {
		return err
	}
	if !exists(from) {
		return fmt.Errorf("live asset missing: %s", from)
	}
	if linked(from, src) || name == "instructions" && pointerTo(from, src) {
		return errors.New("live asset already uses the store")
	}
	other := "codex"
	if source == "codex" {
		other = "claude-code"
	}
	to, err := a.targetPath(other, name)
	if err != nil {
		return err
	}
	if exists(to) && !(name == "instructions" && pointerTo(to, src)) {
		equal, err := same(from, to)
		if err != nil {
			return err
		}
		if !equal && !*replace {
			return fmt.Errorf("%s differs at %s; inspect with diff or use --replace", name, a.displayPath(to))
		}
	}
	if *dryRun {
		fmt.Fprintf(a.out, "would copy %s -> %s\n", a.displayPath(from), a.displayPath(src))
		if other == "codex" && name != "instructions" && filepath.Clean(a.store) == filepath.Join(a.home, ".agents") && !exists(to) {
			fmt.Fprintln(a.out, "codex: would use the store directly")
		} else if name == "instructions" && pointerTo(to, src) {
			fmt.Fprintln(a.out, "instructions: already points to the store")
		} else {
			if exists(to) {
				fmt.Fprintf(a.out, "would backup %s under %s\n", a.displayPath(to), a.displayPath(filepath.Join(a.home, ".local/state/agentctl/backups")))
			}
			action := "link"
			if name == "instructions" {
				action = "pointer"
			}
			fmt.Fprintf(a.out, "would %s %s -> %s\n", action, a.displayPath(src), a.displayPath(to))
		}
		return nil
	}
	if err := a.pull([]string{name, source}); err != nil {
		return err
	}
	deployArgs := []string{name, other}
	if *replace {
		deployArgs = append(deployArgs, "--replace")
	}
	return a.deploy(deployArgs)
}

// deployment records a preflighted local write or no-op.
type deployment struct{ target, name, src, path, action, state, conflict string }

// deploy preflights every selected asset before previewing or applying writes.
func (a app) deploy(args []string) error {
	flags := flag.NewFlagSet("deploy", flag.ContinueOnError)
	flags.SetOutput(a.errout)
	copyMode := flags.Bool("copy", false, "copy instead of link")
	replace := flags.Bool("replace", false, "back up and replace a different live asset")
	dryRun := flags.Bool("dry-run", false, "preview without writing")
	targetFlag := flags.String("target", "", "limit deployment to one local target")
	positional, err := parseFlags(flags, args)
	if err != nil {
		return err
	}
	if len(positional) > 2 || len(positional) == 2 && *targetFlag != "" {
		return errors.New("usage: agentctl deploy [NAME [TARGET]] [--target=TARGET] [--copy] [--replace] [--dry-run]")
	}
	names := []string{}
	if len(positional) > 0 {
		names = append(names, normalizeName(positional[0]))
	} else {
		names, err = a.assetNames()
		if err != nil {
			return err
		}
	}
	targets := []string{"codex", "claude-code"}
	if len(positional) == 2 {
		targets = positional[1:]
	} else if *targetFlag != "" {
		targets = []string{*targetFlag}
	}
	if len(targets) == 1 {
		if _, err := a.targetPath(targets[0], "instructions"); err != nil {
			return err
		}
	}
	plans, conflicts := []deployment{}, []error{}
	for _, name := range names {
		src := a.source(name)
		if !exists(src) {
			return fmt.Errorf("canonical asset missing: %s", src)
		}
		for _, target := range targets {
			path, err := a.targetPath(target, name)
			if err != nil {
				return err
			}
			state, err := a.state(target, name)
			if err != nil {
				return err
			}
			plan := deployment{target: target, name: name, src: src, path: path, action: "link", state: state}
			switch {
			case state == "direct" || state == "pointer" || state == "linked" && !*copyMode && name != "instructions" || state == "same copy" && *copyMode:
				plan.action = "none"
			case *copyMode:
				plan.action = "copy"
			case name == "instructions":
				plan.action = "pointer"
			}
			if (state == "different" || state == "new") && !*replace {
				plan.conflict = fmt.Sprintf("%s differs at %s; inspect with diff or use --replace", name, a.displayPath(path))
				conflicts = append(conflicts, errors.New(plan.conflict))
			}
			plans = append(plans, plan)
		}
	}
	if *dryRun {
		for _, plan := range plans {
			if plan.conflict != "" {
				fmt.Fprintf(a.out, "conflict: %s\n", plan.conflict)
				continue
			}
			if plan.action != "copy" && plan.action != "link" && plan.action != "pointer" {
				fmt.Fprintf(a.out, "%s/%s: %s\n", plan.target, plan.name, plan.state)
				continue
			}
			if exists(plan.path) {
				fmt.Fprintf(a.out, "would backup %s under %s\n", a.displayPath(plan.path), a.displayPath(filepath.Join(a.home, ".local/state/agentctl/backups")))
			}
			fmt.Fprintf(a.out, "would %s %s -> %s\n", plan.action, a.displayPath(plan.src), a.displayPath(plan.path))
		}
		return errors.Join(conflicts...)
	}
	if len(conflicts) != 0 {
		return errors.Join(conflicts...)
	}
	for _, plan := range plans {
		if plan.action != "copy" && plan.action != "link" && plan.action != "pointer" {
			fmt.Fprintf(a.out, "%s/%s: %s\n", plan.target, plan.name, plan.state)
			continue
		}
		if exists(plan.path) {
			if err := a.backup(plan.path); err != nil {
				return err
			}
		}
		switch plan.action {
		case "copy":
			err = replaceWithCopy(plan.src, plan.path)
		case "pointer":
			err = writePointer(plan.src, plan.path)
		case "link":
			err = replaceWithLink(plan.src, plan.path)
		}
		if err != nil {
			return err
		}
		if plan.action == "pointer" {
			fmt.Fprintf(a.out, "%s: points to %s\n", plan.name, plan.src)
		} else {
			verb := "linked"
			if plan.action == "copy" {
				verb = "copied"
			}
			fmt.Fprintf(a.out, "%s: %s to %s\n", plan.name, verb, plan.path)
		}
	}
	return nil
}

// export packages canonical text for manual account installation.
func (a app) export(args []string) error {
	flags := flag.NewFlagSet("export", flag.ContinueOnError)
	flags.SetOutput(a.errout)
	targetFlag := flags.String("target", "", "limit export to one account target")
	positional, err := parseFlags(flags, args)
	if err != nil {
		return err
	}
	if len(positional) > 2 || len(positional) == 2 && *targetFlag != "" {
		return errors.New("usage: agentctl export [NAME [TARGET]] [--target=TARGET]")
	}
	names := []string{}
	if len(positional) > 0 {
		name := normalizeName(positional[0])
		if !validName(name) {
			return fmt.Errorf("invalid asset name %q", name)
		}
		names = append(names, name)
	} else {
		var err error
		names, err = a.assetNames()
		if err != nil {
			return err
		}
	}
	targets := []string{"claude-chat", "chatgpt"}
	if len(positional) == 2 {
		targets = positional[1:]
	} else if *targetFlag != "" {
		targets = []string{*targetFlag}
	}
	if len(targets) == 1 && targets[0] != "claude-chat" && targets[0] != "chatgpt" {
		return fmt.Errorf("unknown account target %q", targets[0])
	}
	for _, target := range targets {
		outDir := filepath.Join(a.home, ".local", "state", "agentctl", "exports", target)
		if err := os.MkdirAll(outDir, 0755); err != nil {
			return err
		}
		for _, name := range names {
			src := a.source(name)
			if !exists(src) {
				return fmt.Errorf("canonical asset missing: %s", src)
			}
			dst := filepath.Join(outDir, name+".zip")
			if name == "instructions" {
				dst = filepath.Join(outDir, "instructions")
				if err := replaceWithCopy(filepath.Join(a.store, "instructions"), dst); err != nil {
					return err
				}
			} else if err := zipDir(src, dst); err != nil {
				return err
			}
			fmt.Fprintln(a.out, dst)
		}
	}
	fmt.Fprintln(a.out, "Install these files manually in the account. Instruction references may need adaptation. agentctl cannot inspect account versions or confirm drift.")
	return nil
}

// exists reports whether a path resolves to an existing file or directory.
func exists(path string) bool { _, err := os.Stat(path); return err == nil }

// skipEntry omits generated local files that are not portable skill source.
func skipEntry(entry fs.DirEntry) bool {
	name := entry.Name()
	if entry.IsDir() {
		return name == ".git" || name == ".venv" || name == "__pycache__" || name == "node_modules" || name == ".pytest_cache"
	}
	return name == ".DS_Store" || strings.HasSuffix(name, ".pyc")
}

// linked reports whether a top-level symlink points to canonical content.
func linked(dst, src string) bool {
	info, err := os.Lstat(dst)
	if err != nil || info.Mode()&os.ModeSymlink == 0 {
		return false
	}
	resolved, err := filepath.EvalSymlinks(dst)
	if err != nil {
		return false
	}
	canonical, err := filepath.EvalSymlinks(src)
	return err == nil && resolved == canonical
}

// pointerTo recognizes the user's existing direct-use instruction wrapper.
func pointerTo(dst, src string) bool {
	data, err := os.ReadFile(dst)
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(data)) == pointerText(src)
}

// pointerText gives local agents an unambiguous path into the instruction store.
func pointerText(src string) string {
	if filepath.Clean(src) == filepath.Join(os.Getenv("HOME"), ".agents", "instructions", "agents.md") {
		return "Follow instructions at ~/.agents/instructions/agents.md now."
	}
	return "Follow instructions at " + src + " now."
}

// writePointer installs a small instruction file referring to canonical text.
func writePointer(src, dst string) error {
	tmp, err := os.CreateTemp("", "agentctl-pointer-")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := fmt.Fprintln(tmp, pointerText(src)); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return replaceWithCopy(tmp.Name(), dst)
}

// same compares regular files or full directory trees by names and content.
func same(left, right string) (bool, error) {
	l, err := fingerprint(left)
	if err != nil {
		return false, err
	}
	r, err := fingerprint(right)
	if err != nil {
		return false, err
	}
	return bytes.Equal(l, r), nil
}

// fingerprint hashes visible asset contents and relative names in stable order.
func fingerprint(root string) ([]byte, error) {
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil, err
	}
	root = resolved
	h := sha256.New()
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if skipEntry(entry) {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 && path != root {
			return fmt.Errorf("symlink inside asset: %s", path)
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		info, err := os.Stat(path)
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("unsupported asset entry: %s", path)
		}
		fmt.Fprintf(h, "%s\x00%t\x00", rel, info.Mode()&0111 != 0)
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		_, err = io.Copy(h, file)
		closeErr := file.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
		_, err = h.Write([]byte{0})
		return err
	})
	if err != nil {
		return nil, err
	}
	return h.Sum(nil), nil
}

// replaceWithCopy stages a copy before replacing a single asset path.
func replaceWithCopy(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return err
	}
	tmp, err := os.MkdirTemp(filepath.Dir(dst), ".agentctl-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	staged := filepath.Join(tmp, "asset")
	if err := copyAsset(src, staged); err != nil {
		return err
	}
	if err := os.RemoveAll(dst); err != nil {
		return err
	}
	return os.Rename(staged, dst)
}

// copyAsset copies files and skill trees, resolving contained symlink files.
func copyAsset(src, dst string) error {
	resolved, err := filepath.EvalSymlinks(src)
	if err != nil {
		return err
	}
	src = resolved
	info, err := os.Stat(src)
	if err != nil {
		return err
	}
	if info.Mode().IsRegular() {
		return copyFile(src, dst, info.Mode())
	}
	if !info.IsDir() {
		return fmt.Errorf("unsupported asset: %s", src)
	}
	return filepath.WalkDir(src, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if skipEntry(entry) {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 && path != src {
			return fmt.Errorf("symlink inside asset: %s", path)
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		out := filepath.Join(dst, rel)
		info, err := os.Stat(path)
		if err != nil {
			return err
		}
		if info.IsDir() {
			return os.MkdirAll(out, info.Mode().Perm())
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("unsupported asset entry: %s", path)
		}
		return copyFile(path, out, info.Mode())
	})
}

// copyFile copies one regular file while preserving its mode.
func copyFile(src, dst string, mode fs.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode.Perm())
	if err != nil {
		return err
	}
	_, err = io.Copy(out, in)
	closeErr := out.Close()
	if err != nil {
		return err
	}
	return closeErr
}

// replaceWithLink installs a top-level symlink to the canonical asset.
func replaceWithLink(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return err
	}
	if err := os.RemoveAll(dst); err != nil {
		return err
	}
	return os.Symlink(src, dst)
}

// backup saves a divergent asset outside the dotfiles store before replacement.
func (a app) backup(src string) error {
	base := filepath.Join(a.home, ".local", "state", "agentctl", "backups", time.Now().UTC().Format("20060102T150405.000000000"))
	if err := os.MkdirAll(base, 0755); err != nil {
		return err
	}
	dst := filepath.Join(base, filepath.Base(src))
	if err := copyRaw(src, dst); err != nil {
		return err
	}
	fmt.Fprintf(a.out, "backup: %s\n", dst)
	return nil
}

// copyRaw preserves every file and symlink in a backup, including generated data.
func copyRaw(src, dst string) error {
	return filepath.WalkDir(src, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		out := filepath.Join(dst, rel)
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if info.IsDir() {
			return os.MkdirAll(out, info.Mode().Perm())
		}
		if info.Mode()&os.ModeSymlink != 0 {
			if err := os.MkdirAll(filepath.Dir(out), 0755); err != nil {
				return err
			}
			link, err := os.Readlink(path)
			if err != nil {
				return err
			}
			return os.Symlink(link, out)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("unsupported backup entry: %s", path)
		}
		return copyFile(path, out, info.Mode())
	})
}

// zipDir creates a skill upload archive with the skill directory at its root.
func zipDir(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return err
	}
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	zw := zip.NewWriter(out)
	err = filepath.WalkDir(src, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if skipEntry(entry) {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 && path != src {
			return fmt.Errorf("symlink inside asset: %s", path)
		}
		if entry.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		info, err := os.Stat(path)
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("unsupported asset entry: %s", path)
		}
		header, err := zip.FileInfoHeader(info)
		if err != nil {
			return err
		}
		header.Name = filepath.ToSlash(filepath.Join(filepath.Base(src), rel))
		header.Method = zip.Deflate
		writer, err := zw.CreateHeader(header)
		if err != nil {
			return err
		}
		in, err := os.Open(path)
		if err != nil {
			return err
		}
		_, err = io.Copy(writer, in)
		closeErr := in.Close()
		if err != nil {
			return err
		}
		return closeErr
	})
	if closeErr := zw.Close(); err == nil {
		err = closeErr
	}
	if closeErr := out.Close(); err == nil {
		err = closeErr
	}
	return err
}
