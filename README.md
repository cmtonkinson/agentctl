# agentctl

Manage agent assets (instructions, skills, plugins, and tools) from one
authoritative store at `~/.agents/`, and deploy them to the agents you use:

| Target        | What it is                        |
|---------------|-----------------------------------|
| `chatgpt`     | ChatGPT account and desktop       |
| `claude-chat` | Claude account and desktop chat   |
| `codex`       | Codex CLI                         |
| `claude-code` | Claude Code                       |

agentctl finds the assets already on your machine, copies them into the store
without touching the originals, and deploys them back out as links, copies,
config entries, or upload packages. It tracks what it deployed so it never
overwrites anything it didn't put there.

## Install

Requires Go 1.24+.

```sh
go install github.com/cmtonkinson/agentctl/cmd/agentctl@latest
# or, from a checkout:
make install            # builds ./agentctl and installs to ~/.local/bin
```

It is a single static binary with no runtime dependencies. `git` is needed only
to import from or update against repositories.

## Quick start

```sh
agentctl inventory                                   # what's out there?
agentctl import --from claude-code --all --dry-run   # preview pulling it in
agentctl import --from claude-code --all
agentctl target assign claude-code prune-prose format-markdown
agentctl target assign codex prune-prose
agentctl deploy --all --dry-run
agentctl deploy --all
agentctl status
```

A skill you imported from `~/.claude/skills/foo` is still sitting there, so the
first deploy reports a **conflict**: agentctl won't replace a directory it didn't
create. If the copy is identical to the store, `deploy --adopt` swaps it for a
link and keeps the original under `~/.agents/.agentctl/trash/`. Otherwise,
move the original aside yourself.

## The store

```
~/.agents/
  agentctl.yaml                          configuration (YAML)
  instructions/<name>/AGENTS.md          shared rules
  instructions/<name>/targets/<t>.md     optional adapter appended for target <t>
  skills/<name>/SKILL.md                 plus scripts/, references/, assets/
  plugins/<name>/.claude-plugin/plugin.json
  tools/<name>/tool.json                 MCP server definition and/or scripts
  .agentctl/meta/<kind>/<name>.json      provenance: source, revision, license, hashes
  .agentctl/state.json                   machine-local: deployments, acknowledgements
  .agentctl/packages/                    generated upload packages
  .agentctl/trash/                       removed or adopted files
```

Asset directories hold exactly what you authored. agentctl's bookkeeping lives
under `.agentctl/`. If you keep the store in git, consider ignoring
`.agentctl/state.json`, `.agentctl/packages/`, and `.agentctl/trash/`. agentctl
never commits, pushes, or publishes anything.

A `tool.json` looks like this:

```json
{
  "description": "GitHub MCP server",
  "mcp": {
    "command": "npx",
    "args": ["-y", "@modelcontextprotocol/server-github"],
    "env": { "GITHUB_TOKEN": "${GITHUB_TOKEN}" }
  },
  "bin": ["bin/gh-helper"],
  "requires": ["node"]
}
```

Remote servers use `"url"` (and optionally `"type": "http"` and `"headers"`)
instead of `"command"`. A tool directory without `tool.json` is treated as its
executables.

## How each target is served

| Kind         | claude-code                         | codex                               | claude-chat                               | chatgpt                          |
|--------------|-------------------------------------|-------------------------------------|-------------------------------------------|----------------------------------|
| instructions | `~/.claude/CLAUDE.md` (link / copy / composed) | `~/.codex/AGENTS.md` (link / copy / composed) | text to paste into profile preferences | text to paste into custom instructions (1,500 char check) |
| skills       | `~/.claude/skills/<name>` (link / copy) | `~/.agents/skills/<name>`, in place when the store is `~/.agents` | ZIP to upload in Settings → Capabilities | ZIP to upload (plan-dependent) |
| plugins      | `~/.claude/skills/<name>`, loaded as `<name>@skills-dir` | unsupported | ZIP to upload in the desktop app | unsupported |
| MCP tools    | prints `claude mcp add-json`, then verifies `~/.claude.json`; project scope merges `.mcp.json` | prints `codex mcp add`, then verifies `config.toml` | stdio: merged into `claude_desktop_config.json`; remote: custom connector (manual) | remote only: connector (manual) |
| script tools | linked into `~/.local/bin`          | linked into `~/.local/bin`          | unsupported                               | unsupported                      |

`agentctl target show TARGET` prints the resolved paths. Override any of them with
`agentctl config set targets.<target>.paths.<key> PATH`. `$CLAUDE_CONFIG_DIR`,
`$CODEX_HOME`, `$XDG_CONFIG_HOME`, and `$APPDATA` are respected.

### Instructions

When one instruction asset is assigned to a target and it has no adapter,
agentctl links it directly. When several are assigned, or an asset has a
`targets/<target>.md` adapter, they are joined in assignment order into one
generated file with a header comment. Removing one asset from a target rewrites
the file from the rest.

### Upload-only targets and acknowledgements

claude.ai and ChatGPT don't let you install anything through an API, so
agentctl builds a self-contained package (deterministic ZIP or text file) and
prints the manual steps, ending with:

```
→ Then record it: agentctl target acknowledge claude-chat skills/morning-brief --hash 1a2b3c4d5e6f
```

Until you acknowledge it, the asset reports `manual`, not `ok`. If the store
changes afterwards, it reports `outdated`. The same applies to MCP commands
agentctl asks you to run. Where it can read the client's config back (Claude
Code, Codex, Claude Desktop), it verifies the entry instead of relying on your
acknowledgement.

### Credentials

Importing an MCP server from a client config replaces credentials with
`${VAR}` placeholders, so secrets never enter the store. That covers env and
header values, arguments after flags like `--token` or `--api-key`, values
that look like keys (`sk-…`, `ghp_…`, and so on), secret query parameters, and
token-like URL path segments. When deploying, a placeholder matches whatever
value the client already holds, and a merge into `claude_desktop_config.json`
keeps the credentials already there. A change you make to a literal value
(say, `LOG_LEVEL`) makes the entry a conflict, and agentctl won't revert it.

## Commands

```
inventory     Discover assets across local files and connected accounts
list          List assets in the authoritative store
show          Show an asset's source, dependencies, and target support
import        Copy assets into the store; preserve originals
diff          Compare canonical, source, and deployed copies
edit          Open canonical source in $EDITOR
deploy        Link, install, or package assets for selected targets
status        Show missing, outdated, conflicting, or unsupported assets
update        Preview or apply upstream updates to imported assets
remove        Remove an asset from the store or a target
doctor        Check discovery, paths, packages, and dependencies
target        Inspect and configure agent targets
config        Inspect and configure agentctl
help          Show command help
```

Every command accepts `--json` (structured output) and `--quiet`. `agentctl help
COMMAND` lists each command's options. Selection options (`--kind`, `--target`,
`--all-targets`, `--origin`, `--scope`, `--project`, `--exclude`) apply where they
make sense.

### Import sources

```sh
agentctl import ~/.claude/skills/example               # a directory
agentctl import ~/Downloads/morning-brief.zip          # a ZIP (e.g. downloaded from claude.ai)
agentctl import 3fa2c1d0                               # an inventory ID
agentctl import prune-prose --from claude-code         # by name from a target
agentctl import https://github.com/org/repo --all --kind skills
agentctl import https://github.com/org/repo/tree/main/skills/pdf
agentctl import https://github.com/org/repo#skills/pdf --version v1.2.0
agentctl import ./.mcp.json --all                      # every MCP server in a file
```

Import skips symlinks that point outside the asset and warns about them, and
packages never include content from outside the asset. A directory of
executables without a `tool.json` imports as a script tool.

Each import records the source, the version or git revision, the license
(from frontmatter, `plugin.json`, or a LICENSE file), and content hashes.
`--on-conflict fail|skip|rename` controls name collisions. The default, `fail`,
imports nothing when any name collides. A skill that came from inside a plugin
records the plugin as a dependency, and it stays `blocked` on a target until
that plugin is in the store and assigned there too.

### Deployment states

| State      | Meaning |
|------------|---------|
| `ok`       | deployed and current, verified or acknowledged |
| `missing`  | assigned but not deployed |
| `outdated` | agentctl deployed it, and the store has changed since |
| `conflict` | the destination holds something agentctl didn't deploy, or its deployed copy was edited |
| `blocked`  | the target can't take it, or a dependency is missing |
| `manual`   | waiting for an upload or command, then `target acknowledge` |

### Updates

`agentctl update` compares each imported asset with its upstream: a path, ZIP,
repository, or the target it was imported from (plugin caches included).
`--check` compares git revisions without cloning. `--apply` does a file-level
three-way merge. Upstream changes to files you haven't touched are applied, your
local additions and edits are kept, and if you and upstream changed the same
file, it stops before changing anything.

### Ownership

agentctl only replaces or removes what its records show it deployed, and only
while that is unchanged. These count as conflicts:

- a destination another asset occupies
- a symlink you pointed somewhere else
- a deployed copy you edited
- an unreadable directory
- a directory holding `.git`

An explicit `--method copy` is remembered on later deploys.
`update --apply` edits the store in place, so a `.git` inside an asset survives.

## Configuration

```yaml
# ~/.agents/agentctl.yaml
editor: nvim
bin-dir: ~/.local/bin
exclude:
  - ~/work/client-repo
deploy:
  method: auto          # auto | link | copy | package
targets:
  chatgpt:
    enabled: false
  claude-code:
    assets:
      - instructions/core
      - skills/prune-prose
  codex:
    assets:
      - instructions/core
    paths:
      skills: ~/.codex/skills
projects:
  ~/src/app:
    targets:
      claude-code:
        assets:
          - skills/app-conventions
```

Change it with `agentctl config set|get`, `agentctl config exclude
add|remove|list`, and `agentctl target enable|disable|assign|unassign`, or edit it
by hand. Project assignments (`target assign --project PATH`) deploy to the
project's `.claude/skills`, `CLAUDE.md`, `AGENTS.md`, `.agents/skills`, and
`.mcp.json`.

## Exit status

`0` success · `1` failure or refusal · `2` usage error · `3` a `--check` found work to do.

## Boundaries

- Source lives in `~/.agents/`. Vendor caches remain application-managed.
- Credentials remain in each client's credential store.
- Unsupported account operations produce explicit manual steps.
- No automatic commits, pushes, or public publication.

## Notes and limitations

- Account inventories can't be read. Neither claude.ai nor ChatGPT exposes an API
  that lists account skills, instructions, or connectors, so `inventory` shows
  what you've acknowledged. To pull an account skill in, download its ZIP and
  import that.
- Codex plugins aren't supported. Assign a plugin's skills and tools
  individually.
- Codex reads skills from `~/.agents/skills`. With the default store, every
  stored skill is visible to Codex whether assigned or not (`doctor` notes this).
  To make Codex respect assignments, point `targets.codex.paths.skills` somewhere
  else.
- On Windows, `auto` deploys copies instead of symlinks.
- Inside `remove`, `--store` means "remove from the store". To use a
  non-default store with `remove`, set `$AGENTCTL_STORE`.
- agentctl reads Codex's `config.toml` to verify MCP servers but never writes it.

## Development

```sh
make test    # go test ./...
make vet     # go vet + gofmt check
make build
```

## License

MIT. See [LICENSE](LICENSE).
