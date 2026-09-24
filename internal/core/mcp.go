package core

import (
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/cmtonkinson/agentctl/internal/fsx"
	"github.com/cmtonkinson/agentctl/internal/ojson"
)

// clientValue renders an MCP definition in the mcpServers shape used by
// Claude Code, Claude Desktop, and .mcp.json.
func clientValue(m *MCPServer, withType bool) json.RawMessage {
	v := map[string]any{}
	if m.Remote() {
		v["type"] = m.Transport()
		v["url"] = m.URL
		if len(m.Headers) > 0 {
			v["headers"] = m.Headers
		}
	} else {
		if withType {
			v["type"] = "stdio"
		}
		v["command"] = m.Command
		v["args"] = nonNil(m.Args)
		if len(m.Env) > 0 {
			v["env"] = m.Env
		}
	}
	b, _ := json.Marshal(v)
	return b
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// canonicalHash hashes a JSON value independent of key order and spacing.
func canonicalHash(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return fsx.HashBytes(raw)
	}
	b, _ := json.Marshal(v)
	return fsx.HashBytes(b)
}

// ownershipHash identifies an entry agentctl wrote while ignoring env and
// header values, which users fill in with credentials after deployment.
func ownershipHash(raw json.RawMessage) string {
	var v map[string]any
	if len(raw) == 0 || json.Unmarshal(raw, &v) != nil {
		return canonicalHash(raw)
	}
	for _, field := range []string{"env", "headers"} {
		if m, ok := v[field].(map[string]any); ok {
			for k := range m {
				m[k] = ""
			}
		}
	}
	b, _ := json.Marshal(v)
	return fsx.HashBytes(b)
}

func isPlaceholder(s string) bool { return strings.Contains(s, "${") }

func placeholders(m *MCPServer) []string {
	var out []string
	for _, kv := range []map[string]string{m.Env, m.Headers} {
		for k, v := range kv {
			if isPlaceholder(v) {
				out = append(out, k)
			}
		}
	}
	sort.Strings(out)
	return out
}

// mcpMatch compares a desired definition with a client's entry. Values that
// are ${VAR} placeholders match any value the client holds, since
// credentials live in the client.
func mcpMatch(want, have json.RawMessage) bool {
	var w, h map[string]any
	if json.Unmarshal(want, &w) != nil || json.Unmarshal(have, &h) != nil {
		return false
	}
	for _, k := range []string{"command", "url"} {
		if str(w[k]) != str(h[k]) {
			return false
		}
	}
	if !reflect.DeepEqual(asStrings(w["args"]), asStrings(h["args"])) {
		return false
	}
	wt, ht := str(w["type"]), str(h["type"])
	if wt != "" && ht != "" && wt != ht && !(wt == "http" && ht == "streamable-http") {
		return false
	}
	for _, field := range []string{"env", "headers"} {
		wm, _ := w[field].(map[string]any)
		hm, _ := h[field].(map[string]any)
		for k, wv := range wm {
			hv, ok := hm[k]
			if !ok {
				return false
			}
			if ws := str(wv); !isPlaceholder(ws) && ws != str(hv) {
				return false
			}
		}
	}
	return true
}

func asStrings(v any) []string {
	out := []string{}
	if arr, ok := v.([]any); ok {
		for _, x := range arr {
			out = append(out, str(x))
		}
	}
	return out
}

// preserveCredentials keeps a client's concrete values for keys the store
// holds only as placeholders.
func preserveCredentials(want, existing json.RawMessage) json.RawMessage {
	var w, e map[string]any
	if json.Unmarshal(want, &w) != nil || json.Unmarshal(existing, &e) != nil {
		return want
	}
	for _, field := range []string{"env", "headers"} {
		wm, _ := w[field].(map[string]any)
		em, _ := e[field].(map[string]any)
		for k, wv := range wm {
			if ev, ok := em[k]; ok && isPlaceholder(str(wv)) && !isPlaceholder(str(ev)) {
				wm[k] = ev
			}
		}
	}
	b, _ := json.Marshal(w)
	return b
}

func splitKey(key string) (string, string) {
	i := strings.Index(key, ".")
	if i < 0 {
		return key, ""
	}
	return key[:i], key[i+1:]
}

func readJSONEntry(file, key string) (json.RawMessage, bool, error) {
	data, err := os.ReadFile(file)
	if fsx.IsNotExist(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	obj, err := ojson.Parse(data)
	if err != nil {
		return nil, false, fmt.Errorf("%s: %w", file, err)
	}
	top, name := splitKey(key)
	raw, ok := obj.Get(top)
	if !ok {
		return nil, false, nil
	}
	servers, err := ojson.Parse(raw)
	if err != nil {
		return nil, false, fmt.Errorf("%s: %s: %w", file, top, err)
	}
	v, ok := servers.Get(name)
	return v, ok, nil
}

// editJSONEntry sets (value != nil) or deletes an entry in a JSON config
// file, preserving key order and indentation and keeping a backup.
func editJSONEntry(file, key string, value json.RawMessage) error {
	data, err := os.ReadFile(file)
	if err != nil && !fsx.IsNotExist(err) {
		return err
	}
	obj, err := ojson.Parse(data)
	if err != nil {
		return fmt.Errorf("%s: %w", file, err)
	}
	top, name := splitKey(key)
	servers := ojson.New()
	if raw, ok := obj.Get(top); ok {
		if servers, err = ojson.Parse(raw); err != nil {
			return fmt.Errorf("%s: %s: %w", file, top, err)
		}
	}
	if value == nil {
		servers.Delete(name)
	} else {
		servers.Set(name, value)
	}
	sb, err := servers.MarshalJSON()
	if err != nil {
		return err
	}
	obj.Set(top, sb)
	out, err := obj.Format(ojson.DetectIndent(data))
	if err != nil {
		return err
	}
	perm := os.FileMode(0o644)
	if fi, err := os.Stat(file); err == nil {
		perm = fi.Mode().Perm()
		if err := os.WriteFile(file+".agentctl.bak", data, perm); err != nil {
			return err
		}
	}
	return fsx.WriteFileAtomic(file, out, perm)
}

func (a *App) mergeConfig(u *Unit) (json.RawMessage, error) {
	value := u.value
	if existing, ok, err := readJSONEntry(u.Dest, u.Key); err != nil {
		return nil, err
	} else if ok {
		value = preserveCredentials(u.value, existing)
	}
	return value, editJSONEntry(u.Dest, u.Key, value)
}

func (a *App) evalConfig(e *Eval) {
	u := e.Unit
	have, ok, err := readJSONEntry(u.Dest, u.Key)
	rec := a.State.Find(u.Target, u.Project, u.Asset, u.Dest, u.Key)
	switch {
	case err != nil:
		e.State, e.Detail = StateBlocked, err.Error()
	case !ok:
		e.State, e.Detail = StateMissing, "not configured"
	case mcpMatch(u.value, have) && rec != nil:
		e.State, e.Detail = StateOK, "configured in "+a.Abbrev(u.Dest)
	case mcpMatch(u.value, have):
		e.State, e.Detail, e.adoptable = StateConflict, "unmanaged entry identical to the store (deploy --adopt takes it over)", true
	case rec != nil && rec.DestHash == ownershipHash(have):
		e.State, e.Detail = StateOutdated, "store changed since deployment"
	default:
		e.State, e.Detail = StateConflict, "configured differently in "+a.Abbrev(u.Dest)
	}
}

func shellQuote(s string) string {
	if s != "" && strings.IndexFunc(s, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("-_./:=@%+,", r))
	}) < 0 {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func (a *App) planMCP(u *Unit, target, project string, as *Asset) *Unit {
	m := as.Tool.MCP
	name := as.Name
	holders := placeholders(m)
	u.Key = "mcpServers." + name
	switch target {
	case "claude-code":
		u.value = clientValue(m, false)
		u.Want = canonicalHash(u.value)
		if project != "" {
			u.Method = MethodConfig
			u.Dest = ProjectPaths(target, a.AbsPath(project))["mcp"]
			if len(holders) > 0 {
				u.Notes = append(u.Notes, "Claude Code expands ${VAR} placeholders from the environment")
			}
			return u
		}
		u.Method = MethodManual
		u.Dest = a.TargetPaths(target)["settings"]
		u.Steps = []string{"Run: claude mcp add-json --scope user " + shellQuote(name) + " " + shellQuote(string(u.value))}
		if len(holders) > 0 {
			u.Steps = append([]string{"Replace the ${VAR} placeholders (" + strings.Join(holders, ", ") + ") with real values in the command below; credentials stay in Claude Code's config."}, u.Steps...)
		}
		u.removeSteps = []string{"Run: claude mcp remove --scope user " + shellQuote(name)}
		dest := u.Dest
		want := u.value
		u.verify = func() (bool, bool, json.RawMessage, error) {
			have, ok, err := readJSONEntry(dest, "mcpServers."+name)
			return ok, ok && mcpMatch(want, have), have, err
		}
	case "codex":
		u.Method = MethodManual
		u.Dest = a.TargetPaths(target)["config"]
		u.Key = "mcp_servers." + name
		u.value = clientValue(m, false)
		u.Want = canonicalHash(u.value)
		args := []string{"codex", "mcp", "add", shellQuote(name)}
		if m.Remote() {
			args = append(args, "--url", shellQuote(m.URL))
		} else {
			for _, k := range sortedKeys(m.Env) {
				v := m.Env[k]
				if isPlaceholder(v) {
					args = append(args, "--env", `"`+k+"="+v+`"`)
				} else {
					args = append(args, "--env", shellQuote(k+"="+v))
				}
			}
			args = append(args, "--", shellQuote(m.Command))
			for _, x := range m.Args {
				args = append(args, shellQuote(x))
			}
		}
		u.Steps = []string{"Run: " + strings.Join(args, " ")}
		if len(holders) > 0 {
			u.Steps = append([]string{"Export " + strings.Join(holders, ", ") + " first; the shell substitutes the ${VAR} values."}, u.Steps...)
		}
		u.removeSteps = []string{"Run: codex mcp remove " + shellQuote(name)}
		dest, want := u.Dest, u.value
		u.verify = func() (bool, bool, json.RawMessage, error) {
			have, ok, err := readCodexServer(dest, name)
			return ok, ok && mcpMatch(want, have), have, err
		}
	case "claude-chat":
		if m.Remote() {
			u.Method = MethodManual
			u.Want = canonicalHash(clientValue(m, false))
			u.Key = "connector." + name
			u.Steps = []string{fmt.Sprintf("In claude.ai → Settings → Connectors, add a custom connector named %s with URL %s.", name, m.URL), a.ackStep(u)}
			u.removeSteps = []string{"Remove the " + name + " connector in claude.ai → Settings → Connectors."}
			return u
		}
		paths := a.TargetPaths(target)
		if !fsx.IsDir(paths["app"]) && !fsx.Exists(paths["desktop-config"]) {
			u.blocked = "Claude Desktop not found at " + a.Abbrev(paths["app"]) + " (set targets.claude-chat.paths.app)"
			return u
		}
		u.Method = MethodConfig
		u.Dest = paths["desktop-config"]
		u.value = clientValue(m, false)
		u.Want = canonicalHash(u.value)
		u.Notes = append(u.Notes, "restart Claude Desktop to load MCP changes")
		if len(holders) > 0 {
			u.Notes = append(u.Notes, "Claude Desktop does not expand ${VAR}; put real values for "+strings.Join(holders, ", ")+" in "+a.Abbrev(u.Dest)+" (agentctl keeps them on redeploy)")
		}
	case "chatgpt":
		u.Method = MethodManual
		u.Want = canonicalHash(clientValue(m, false))
		u.Key = "connector." + name
		u.Steps = []string{fmt.Sprintf("In ChatGPT → Settings → Apps & Connectors → Advanced settings, enable developer mode, then create a connector named %s with URL %s.", name, m.URL), a.ackStep(u)}
		u.removeSteps = []string{"Delete the " + name + " connector in ChatGPT settings."}
	}
	return u
}

// readCodexServer reads one [mcp_servers.<name>] table from Codex's
// config.toml. agentctl only reads this file; Codex owns it.
func readCodexServer(file, name string) (json.RawMessage, bool, error) {
	servers, err := readCodexServers(file)
	if err != nil {
		return nil, false, err
	}
	v, ok := servers[name]
	return v, ok, nil
}

func readCodexServers(file string) (map[string]json.RawMessage, error) {
	var cfg struct {
		MCPServers map[string]map[string]any `toml:"mcp_servers"`
	}
	if _, err := toml.DecodeFile(file, &cfg); err != nil {
		if fsx.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("%s: %w", file, err)
	}
	out := map[string]json.RawMessage{}
	for name, v := range cfg.MCPServers {
		b, err := json.Marshal(v)
		if err != nil {
			continue
		}
		out[name] = b
	}
	return out, nil
}

// mcpFromClient converts a client's mcpServers entry into a portable
// definition, replacing credential values with ${VAR} placeholders.
func mcpFromClient(raw json.RawMessage) (*MCPServer, []string) {
	var v map[string]any
	if json.Unmarshal(raw, &v) != nil {
		return nil, nil
	}
	m := &MCPServer{Command: str(v["command"]), URL: str(v["url"]), Args: asStrings(v["args"])}
	if len(m.Args) == 0 {
		m.Args = nil
	}
	if t := str(v["type"]); t != "" && t != "stdio" {
		m.Type = t
	}
	var withheld []string
	scrub := func(field string) map[string]string {
		src, _ := v[field].(map[string]any)
		if len(src) == 0 {
			if field == "env" {
				src, _ = v["env_vars"].(map[string]any)
			}
			if len(src) == 0 {
				return nil
			}
		}
		out := map[string]string{}
		for k, val := range src {
			s := str(val)
			if isPlaceholder(s) {
				out[k] = s
				continue
			}
			ph := k
			if field == "headers" {
				ph = strings.ToUpper(SanitizeName(k))
				ph = strings.ReplaceAll(strings.ReplaceAll(ph, "-", "_"), ".", "_")
			}
			out[k] = "${" + ph + "}"
			withheld = append(withheld, k)
		}
		return out
	}
	m.Env = scrub("env")
	m.Headers = scrub("headers")
	sort.Strings(withheld)
	return m, withheld
}
