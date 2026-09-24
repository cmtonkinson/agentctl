package core

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"regexp"
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

var placeholderRe = regexp.MustCompile(`\$\{[^}]*\}`)

// wildMatch matches have against want, where each ${VAR} in want matches
// any text: credentials live in the client, not the store.
func wildMatch(want, have string) bool {
	if !isPlaceholder(want) {
		return want == have
	}
	parts := placeholderRe.Split(want, -1)
	for i, p := range parts {
		parts[i] = regexp.QuoteMeta(p)
	}
	re, err := regexp.Compile("^" + strings.Join(parts, ".*") + "$")
	return err == nil && re.MatchString(have)
}

// mcpMatch compares a desired definition with a client's entry. Parts that
// are ${VAR} placeholders match any value the client holds. Fields the
// client adds (such as "type": "stdio") are ignored.
func mcpMatch(want, have json.RawMessage) bool {
	var w, h map[string]any
	if json.Unmarshal(want, &w) != nil || json.Unmarshal(have, &h) != nil {
		return false
	}
	for _, k := range []string{"command", "url"} {
		if !wildMatch(str(w[k]), str(h[k])) {
			return false
		}
	}
	wa, ha := asStrings(w["args"]), asStrings(h["args"])
	if len(wa) != len(ha) {
		return false
	}
	for i := range wa {
		if !wildMatch(wa[i], ha[i]) {
			return false
		}
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
			if !ok || !wildMatch(str(wv), str(hv)) {
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

// preserveCredentials keeps a client's concrete values wherever the store
// holds a placeholder and the client's value fits it.
func preserveCredentials(want, existing json.RawMessage) json.RawMessage {
	var w, e map[string]any
	if json.Unmarshal(want, &w) != nil || json.Unmarshal(existing, &e) != nil {
		return want
	}
	keep := func(wv, ev any) bool {
		ws, es := str(wv), str(ev)
		return isPlaceholder(ws) && !isPlaceholder(es) && es != "" && wildMatch(ws, es)
	}
	for _, field := range []string{"env", "headers"} {
		wm, _ := w[field].(map[string]any)
		em, _ := e[field].(map[string]any)
		for k, wv := range wm {
			if ev, ok := em[k]; ok && keep(wv, ev) {
				wm[k] = ev
			}
		}
	}
	if wa, ok := w["args"].([]any); ok {
		if ea, ok := e["args"].([]any); ok && len(ea) == len(wa) {
			for i := range wa {
				if keep(wa[i], ea[i]) {
					wa[i] = ea[i]
				}
			}
		}
	}
	if keep(w["url"], e["url"]) {
		w["url"] = e["url"]
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
	case rec != nil && rec.Value != nil && mcpMatch(rec.Value, have):
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

var (
	secretNameRe  = regexp.MustCompile(`(?i)(^|[-_.])(api[-_]?key|apikey|key|token|access[-_]?token|secret|client[-_]?secret|password|passwd|pwd|auth|authorization|credentials?|bearer|sig|signature)$`)
	secretValueRe = regexp.MustCompile(`^(sk-|sk_|pk_live_|rk_live_|ghp_|gho_|ghu_|ghs_|github_pat_|glpat-|xox[abposr]-|AKIA|AIza|ya29\.|eyJ|Bearer )`)
)

func envName(s string) string {
	s = strings.ToUpper(strings.Trim(s, "-_. "))
	s = strings.NewReplacer("-", "_", ".", "_", " ", "_").Replace(s)
	if s == "" {
		return "SECRET"
	}
	return s
}

// looksSecret flags values that are credentials by shape.
func looksSecret(v string) bool {
	return secretValueRe.MatchString(v)
}

// scrubArgs replaces credential-looking command arguments with placeholders.
func scrubArgs(args []string) ([]string, []string) {
	out := append([]string(nil), args...)
	var withheld []string
	for i := 0; i < len(out); i++ {
		a := out[i]
		if isPlaceholder(a) {
			continue
		}
		if strings.HasPrefix(a, "-") {
			flag, val, hasVal := strings.Cut(a, "=")
			name := strings.TrimLeft(flag, "-")
			if hasVal {
				if val != "" && !isPlaceholder(val) && (secretNameRe.MatchString(name) || looksSecret(val)) {
					out[i] = flag + "=${" + envName(name) + "}"
					withheld = append(withheld, "arg "+flag)
				}
				continue
			}
			if secretNameRe.MatchString(name) && i+1 < len(out) && !strings.HasPrefix(out[i+1], "-") && !isPlaceholder(out[i+1]) {
				out[i+1] = "${" + envName(name) + "}"
				withheld = append(withheld, "arg "+flag)
				i++
			}
			continue
		}
		if looksSecret(a) {
			out[i] = "${MCP_SECRET}"
			withheld = append(withheld, fmt.Sprintf("arg %d", i+1))
		}
	}
	return out, withheld
}

// scrubURL replaces credentials in a URL (userinfo, secret query values,
// and token-like path segments) with placeholders.
func scrubURL(raw string) (string, []string) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return raw, nil
	}
	var withheld []string
	userinfo := ""
	if u.User != nil {
		userinfo = "${MCP_URL_USERINFO}@"
		withheld = append(withheld, "url credentials")
	}
	segs := strings.Split(u.EscapedPath(), "/")
	for i, seg := range segs {
		if tokenLike(seg) {
			segs[i] = "${MCP_URL_TOKEN}"
			withheld = append(withheld, "url path token")
		}
	}
	var q []string
	if u.RawQuery != "" {
		for _, pair := range strings.Split(u.RawQuery, "&") {
			k, v, _ := strings.Cut(pair, "=")
			dk, _ := url.QueryUnescape(k)
			dv, _ := url.QueryUnescape(v)
			if v != "" && !isPlaceholder(dv) && (secretNameRe.MatchString(dk) || looksSecret(dv) || tokenLike(dv)) {
				pair = k + "=${" + envName(dk) + "}"
				withheld = append(withheld, "url query "+dk)
			}
			q = append(q, pair)
		}
	}
	out := u.Scheme + "://" + userinfo + u.Host + strings.Join(segs, "/")
	if len(q) > 0 {
		out += "?" + strings.Join(q, "&")
	}
	if u.Fragment != "" {
		out += "#" + u.EscapedFragment()
	}
	return out, withheld
}

// tokenLike reports whether s looks like a random credential: long, with
// both letters and digits.
func tokenLike(s string) bool {
	if len(s) < 24 || isPlaceholder(s) {
		return false
	}
	letters, digits := false, false
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9':
			digits = true
		case r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z':
			letters = true
		case r == '-' || r == '_' || r == '.' || r == '~' || r == '%':
		default:
			return false
		}
	}
	return letters && digits
}

// mcpFromClient converts a client's mcpServers entry into a portable
// definition, replacing credentials (env and header values, secret-looking
// arguments, and URL tokens) with ${VAR} placeholders.
func mcpFromClient(raw json.RawMessage) (*MCPServer, []string) {
	var v map[string]any
	if json.Unmarshal(raw, &v) != nil {
		return nil, nil
	}
	m := &MCPServer{Command: str(v["command"]), Args: asStrings(v["args"])}
	var withheld []string
	var w []string
	m.Args, w = scrubArgs(m.Args)
	withheld = append(withheld, w...)
	if len(m.Args) == 0 {
		m.Args = nil
	}
	m.URL, w = scrubURL(str(v["url"]))
	withheld = append(withheld, w...)
	if t := str(v["type"]); t != "" && t != "stdio" {
		m.Type = t
	}
	scrub := func(field string) map[string]string {
		src, _ := v[field].(map[string]any)
		if len(src) == 0 && field == "env" {
			src, _ = v["env_vars"].(map[string]any)
		}
		if len(src) == 0 {
			return nil
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
				ph = envName(k)
			}
			out[k] = "${" + ph + "}"
			withheld = append(withheld, field+" "+k)
		}
		return out
	}
	m.Env = scrub("env")
	m.Headers = scrub("headers")
	sort.Strings(withheld)
	return m, dedupe(withheld)
}

// describeServer summarizes a (scrubbed) MCP definition.
func describeServer(m *MCPServer) string {
	if m.Remote() {
		return "MCP server at " + m.URL
	}
	return "MCP server: " + truncate(strings.Join(append([]string{m.Command}, m.Args...), " "), 60)
}
