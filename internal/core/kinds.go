package core

import (
	"fmt"
	"strings"
)

// Kind is an asset type.
type Kind string

const (
	Instructions Kind = "instructions"
	Skills       Kind = "skills"
	Plugins      Kind = "plugins"
	Tools        Kind = "tools"
)

// AllKinds lists asset kinds in display order.
var AllKinds = []Kind{Instructions, Skills, Plugins, Tools}

// KindHelp describes each kind for help output.
var KindHelp = map[Kind]string{
	Instructions: "Shared rules and target-specific instruction adapters",
	Skills:       "SKILL.md packages, scripts, references, and assets",
	Plugins:      "Plugin packages and their dependencies",
	Tools:        "Scripts and MCP server definitions",
}

// ParseKind accepts plural or singular kind names.
func ParseKind(s string) (Kind, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "instructions", "instruction":
		return Instructions, nil
	case "skills", "skill":
		return Skills, nil
	case "plugins", "plugin":
		return Plugins, nil
	case "tools", "tool", "mcp":
		return Tools, nil
	}
	return "", fmt.Errorf("unknown kind %q (want instructions, skills, plugins, or tools)", s)
}

// MainFile is the file that defines an asset of this kind.
func (k Kind) MainFile() string {
	switch k {
	case Instructions:
		return "AGENTS.md"
	case Skills:
		return "SKILL.md"
	case Plugins:
		return ".claude-plugin/plugin.json"
	case Tools:
		return "tool.json"
	}
	return ""
}

// Origins.
const (
	OriginPersonal   = "personal"
	OriginThirdParty = "third-party"
	OriginSystem     = "system"
)

// Scopes.
const (
	ScopeUser    = "user"
	ScopeAccount = "account"
	ScopeProject = "project"
)

// ParseOrigin validates an origin name.
func ParseOrigin(s string) (string, error) {
	switch s {
	case OriginPersonal, OriginThirdParty, OriginSystem:
		return s, nil
	case "thirdparty", "third_party":
		return OriginThirdParty, nil
	}
	return "", fmt.Errorf("unknown origin %q (want personal, third-party, or system)", s)
}

// ParseScope validates a scope name.
func ParseScope(s string) (string, error) {
	switch s {
	case ScopeUser, ScopeAccount, ScopeProject:
		return s, nil
	}
	return "", fmt.Errorf("unknown scope %q (want user, account, or project)", s)
}

// Deployment states.
const (
	StateOK       = "ok"
	StateMissing  = "missing"
	StateOutdated = "outdated"
	StateConflict = "conflict"
	StateBlocked  = "blocked"
	StateManual   = "manual"
)

// ActionStates are the states that need attention, in severity order.
var ActionStates = []string{StateMissing, StateOutdated, StateConflict, StateBlocked, StateManual}

// ParseState validates a --only value.
func ParseState(s string) (string, error) {
	for _, st := range append([]string{StateOK}, ActionStates...) {
		if s == st {
			return s, nil
		}
	}
	return "", fmt.Errorf("unknown state %q (want missing, outdated, conflict, blocked, manual, or ok)", s)
}

// Ref names an asset in the store.
type Ref struct {
	Kind Kind
	Name string
}

func (r Ref) String() string { return string(r.Kind) + "/" + r.Name }

// ParseRef parses "kind/name" or a bare "name" (Kind left empty).
func ParseRef(s string) (Ref, error) {
	s = strings.TrimSpace(strings.TrimSuffix(s, "/"))
	if s == "" {
		return Ref{}, fmt.Errorf("empty asset name")
	}
	if i := strings.Index(s, "/"); i >= 0 {
		k, err := ParseKind(s[:i])
		if err != nil {
			return Ref{}, err
		}
		name := s[i+1:]
		if err := ValidName(name); err != nil {
			return Ref{}, err
		}
		return Ref{k, name}, nil
	}
	if err := ValidName(s); err != nil {
		return Ref{}, err
	}
	return Ref{Name: s}, nil
}

// ValidName checks that an asset name is safe to use as a directory name.
func ValidName(n string) error {
	if n == "" || n == "." || n == ".." {
		return fmt.Errorf("invalid asset name %q", n)
	}
	for i, r := range n {
		ok := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || (i > 0 && (r == '-' || r == '_' || r == '.'))
		if !ok {
			return fmt.Errorf("invalid asset name %q (use letters, digits, '-', '_', '.'; start with a letter or digit)", n)
		}
	}
	return nil
}

// SanitizeName turns an arbitrary string into a valid asset name.
func SanitizeName(s string) string {
	var b strings.Builder
	for _, r := range strings.TrimSpace(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '.', r == '-':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	out := strings.TrimLeft(b.String(), "-_.")
	out = strings.TrimRight(out, "-")
	if out == "" {
		return "asset"
	}
	return out
}
