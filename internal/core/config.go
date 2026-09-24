package core

import (
	"bytes"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/cmtonkinson/agentctl/internal/fsx"
)

// ConfigFile is the config file name inside the store.
const ConfigFile = "agentctl.yaml"

// Config is <store>/agentctl.yaml.
type Config struct {
	Editor   string                    `yaml:"editor,omitempty" json:"editor,omitempty"`
	BinDir   string                    `yaml:"bin-dir,omitempty" json:"bin-dir,omitempty"`
	Exclude  []string                  `yaml:"exclude,omitempty" json:"exclude,omitempty"`
	Deploy   DeployConfig              `yaml:"deploy,omitempty" json:"deploy"`
	Targets  map[string]*TargetConfig  `yaml:"targets,omitempty" json:"targets,omitempty"`
	Projects map[string]*ProjectConfig `yaml:"projects,omitempty" json:"projects,omitempty"`
}

// DeployConfig holds deploy defaults.
type DeployConfig struct {
	Method string `yaml:"method,omitempty" json:"method,omitempty"`
	Output string `yaml:"output,omitempty" json:"output,omitempty"`
}

// TargetConfig is per-target configuration.
type TargetConfig struct {
	Enabled *bool             `yaml:"enabled,omitempty" json:"enabled,omitempty"`
	Assets  []string          `yaml:"assets,omitempty" json:"assets,omitempty"`
	Paths   map[string]string `yaml:"paths,omitempty" json:"paths,omitempty"`
}

// ProjectConfig holds project-scope assignments, keyed by target.
type ProjectConfig struct {
	Targets map[string]*TargetConfig `yaml:"targets,omitempty" json:"targets,omitempty"`
}

// LoadConfig reads the config file; a missing file yields defaults.
func LoadConfig(p string) (*Config, error) {
	c := &Config{}
	b, err := os.ReadFile(p)
	if fsx.IsNotExist(err) {
		return c, nil
	}
	if err != nil {
		return nil, err
	}
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	if err := dec.Decode(c); err != nil && err.Error() != "EOF" {
		return nil, fmt.Errorf("%s: %w", p, err)
	}
	return c, nil
}

// Save writes the config file.
func (c *Config) Save(p string) error {
	var buf bytes.Buffer
	buf.WriteString("# agentctl configuration. Edit with `agentctl config` and `agentctl target`.\n")
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(c); err != nil {
		return err
	}
	enc.Close()
	return fsx.WriteFileAtomic(p, buf.Bytes(), 0o644)
}

func (c *Config) target(name string, create bool) *TargetConfig {
	if c.Targets == nil {
		if !create {
			return nil
		}
		c.Targets = map[string]*TargetConfig{}
	}
	t := c.Targets[name]
	if t == nil && create {
		t = &TargetConfig{}
		c.Targets[name] = t
	}
	return t
}

// Assignments returns the asset refs assigned to target (project "" = user scope).
func (c *Config) Assignments(target, project string) []string {
	t := c.scopeTarget(target, project, false)
	if t == nil {
		return nil
	}
	return t.Assets
}

func (c *Config) scopeTarget(target, project string, create bool) *TargetConfig {
	if project == "" {
		return c.target(target, create)
	}
	if c.Projects == nil {
		if !create {
			return nil
		}
		c.Projects = map[string]*ProjectConfig{}
	}
	p := c.Projects[project]
	if p == nil {
		if !create {
			return nil
		}
		p = &ProjectConfig{}
		c.Projects[project] = p
	}
	if p.Targets == nil {
		if !create {
			return nil
		}
		p.Targets = map[string]*TargetConfig{}
	}
	t := p.Targets[target]
	if t == nil && create {
		t = &TargetConfig{}
		p.Targets[target] = t
	}
	return t
}

// Assign adds refs to a target's assignments, keeping order; returns the
// refs that were newly added.
func (c *Config) Assign(target, project string, refs ...string) []string {
	t := c.scopeTarget(target, project, true)
	var added []string
	for _, r := range refs {
		if !contains(t.Assets, r) {
			t.Assets = append(t.Assets, r)
			added = append(added, r)
		}
	}
	return added
}

// Unassign removes refs; returns the refs that were removed.
func (c *Config) Unassign(target, project string, refs ...string) []string {
	t := c.scopeTarget(target, project, false)
	if t == nil {
		return nil
	}
	var removed []string
	keep := t.Assets[:0]
	for _, a := range t.Assets {
		if contains(refs, a) {
			removed = append(removed, a)
			continue
		}
		keep = append(keep, a)
	}
	t.Assets = keep
	c.prune()
	return removed
}

// ProjectPaths lists configured projects.
func (c *Config) ProjectPaths() []string { return sortedKeys(c.Projects) }

func (c *Config) prune() {
	for pn, p := range c.Projects {
		for tn, t := range p.Targets {
			if len(t.Assets) == 0 && t.Enabled == nil && len(t.Paths) == 0 {
				delete(p.Targets, tn)
			}
		}
		if len(p.Targets) == 0 {
			delete(c.Projects, pn)
		}
	}
}

// Enabled reports whether a target is enabled (default true).
func (c *Config) Enabled(target string) bool {
	t := c.target(target, false)
	return t == nil || t.Enabled == nil || *t.Enabled
}

// SetEnabled enables or disables a target.
func (c *Config) SetEnabled(target string, on bool) {
	c.target(target, true).Enabled = &on
}

// ConfigKeys documents the settable keys.
var ConfigKeys = [][2]string{
	{"editor", "Editor command for `agentctl edit` [$VISUAL, $EDITOR, vi]"},
	{"bin-dir", "Where script tools are linked for local targets [~/.local/bin]"},
	{"deploy.method", "Default deploy method: auto | link | copy | package [auto]"},
	{"deploy.output", "Default output directory for generated packages [<store>/.agentctl/packages]"},
	{"exclude", "Excluded source paths (read-only here; use `config exclude`)"},
	{"targets.<target>.enabled", "Whether a target is selected by default [true]"},
	{"targets.<target>.assets", "Assets assigned to a target (read-only here; use `target assign`)"},
	{"targets.<target>.paths.<key>", "Override a target path (see `target show TARGET`)"},
}

// Get returns a config value by dotted key.
func (c *Config) Get(key string) (any, error) {
	switch key {
	case "editor":
		return c.Editor, nil
	case "bin-dir":
		return c.BinDir, nil
	case "deploy.method":
		return c.Deploy.Method, nil
	case "deploy.output":
		return c.Deploy.Output, nil
	case "exclude":
		return c.Exclude, nil
	}
	parts := strings.Split(key, ".")
	if len(parts) >= 3 && parts[0] == "targets" {
		if TargetByName(parts[1]) == nil {
			return nil, fmt.Errorf("unknown target %q", parts[1])
		}
		t := c.target(parts[1], false)
		switch {
		case len(parts) == 3 && parts[2] == "enabled":
			return c.Enabled(parts[1]), nil
		case len(parts) == 3 && parts[2] == "assets":
			if t == nil {
				return []string{}, nil
			}
			return t.Assets, nil
		case len(parts) == 4 && parts[2] == "paths":
			if t == nil {
				return "", nil
			}
			return t.Paths[parts[3]], nil
		}
	}
	return nil, fmt.Errorf("unknown config key %q (see: agentctl config show)", key)
}

// Set assigns a config value by dotted key. An empty value clears it.
func (c *Config) Set(key, value string) error {
	switch key {
	case "editor":
		c.Editor = value
		return nil
	case "bin-dir":
		c.BinDir = value
		return nil
	case "deploy.method":
		switch value {
		case "", "auto", "link", "copy", "package":
			c.Deploy.Method = value
			return nil
		}
		return fmt.Errorf("deploy.method must be auto, link, copy, or package")
	case "deploy.output":
		c.Deploy.Output = value
		return nil
	case "exclude":
		return fmt.Errorf("use `agentctl config exclude add|remove PATH`")
	}
	parts := strings.Split(key, ".")
	if len(parts) >= 3 && parts[0] == "targets" {
		def := TargetByName(parts[1])
		if def == nil {
			return fmt.Errorf("unknown target %q", parts[1])
		}
		switch {
		case len(parts) == 3 && parts[2] == "enabled":
			on, err := strconv.ParseBool(value)
			if err != nil {
				return fmt.Errorf("enabled must be true or false")
			}
			c.SetEnabled(parts[1], on)
			return nil
		case len(parts) == 3 && parts[2] == "assets":
			return fmt.Errorf("use `agentctl target assign|unassign`")
		case len(parts) == 4 && parts[2] == "paths":
			if !def.HasPathKey(parts[3]) {
				return fmt.Errorf("%s has no path %q (have: %s)", parts[1], parts[3], strings.Join(def.PathKeyNames(), ", "))
			}
			t := c.target(parts[1], true)
			if value == "" {
				delete(t.Paths, parts[3])
				return nil
			}
			if t.Paths == nil {
				t.Paths = map[string]string{}
			}
			t.Paths[parts[3]] = value
			return nil
		}
	}
	return fmt.Errorf("unknown config key %q (see: agentctl config show)", key)
}

// AddExclude adds a path to the exclude list; reports whether it was new.
func (c *Config) AddExclude(p string) bool {
	if contains(c.Exclude, p) {
		return false
	}
	c.Exclude = append(c.Exclude, p)
	sort.Strings(c.Exclude)
	return true
}

// RemoveExclude removes a path; reports whether it was present.
func (c *Config) RemoveExclude(p string) bool {
	for i, e := range c.Exclude {
		if e == p {
			c.Exclude = append(c.Exclude[:i], c.Exclude[i+1:]...)
			return true
		}
	}
	return false
}
