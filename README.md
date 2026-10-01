# agentctl
Keep one dotfiles-managed source of instructions and skills, and see when the
known live copies diverge.

The store is `~/.agents/{instructions,skills}/`. `agents.md` is the
instruction entrypoint; skill directories contain `SKILL.md` and supporting
files. `agentctl` only checks user-global Codex and Claude Code locations. It
does not scan plugin caches, project directories, or the rest of the disk.

------------------------------------------------------------------------
## Commands
```text
agentctl list [-v|--verbose]
agentctl status [codex|claude-code] [-v|--verbose]
agentctl diff [TARGET NAME] [-v|--verbose]
agentctl pull TARGET NAME [--replace] [--dry-run]
agentctl deploy TARGET [NAME] [--copy] [--replace] [--dry-run]
agentctl export TARGET [NAME]
agentctl help
agentctl version
```

`NAME` is `agents.md` (alias `instructions`) or a skill name. `TARGET` is
`codex` or `claude-code` for local commands. Export targets are `claude-chat`
and `chatgpt`.

`list` prints aligned instruction and skill types and names. `status` groups
by kind, item, and agent. Every instruction file is shown; only the
`agents.md` entrypoint has agent details. Other instruction files are
informational, and their references are not analyzed. `-v` or `--verbose`
shows canonical paths and, for `status`, the applicable live paths.

`status` reports `direct`, `pointer`, `linked`, `same copy`, `different`,
`new`, or `missing`. Bare `diff` shows all local drift, including new and
missing assets, as file names. `diff TARGET NAME` selects one comparison.
Use `-v` or `--verbose` with `diff` to show changed lines. Generated
directories such as `.venv/`, `__pycache__/`, and `node_modules/` are ignored
for comparison and portable copies.

`pull` copies a selected live asset into the store. `deploy` uses the store
directly where supported, writes an instruction pointer, or links a skill.
`--copy` requests a copy. Neither command replaces divergent content without
`--replace`, which saves a complete backup under
`~/.local/state/agentctl/backups/` first. Neither command moves its source.
`--dry-run` previews actions and conflicts without writing or creating
backups. Conflicts return a nonzero exit code. Bulk deployment checks every
selected asset for conflicts before making changes.

Codex uses `~/.agents/skills/` directly when that is the store. Existing
`~/.codex/AGENTS.md` and `~/.claude/CLAUDE.md` files that contain only
`Follow instructions at ~/.agents/instructions/agents.md now.` are recognized
as pointers. Claude Code skills use links by default. A separately
installed Codex skill under `~/.codex/skills/` is still inspected for drift.
Set `AGENTCTL_STORE`, `CODEX_HOME`, or `CLAUDE_CONFIG_DIR` to override paths.

`export` writes the complete instruction directory and skill ZIPs to
`~/.local/state/agentctl/exports/TARGET/` for manual account installation.
The CLI cannot read ChatGPT or Claude chat account versions, so it cannot
report their drift or confirm an upload. Referenced instruction files must
be adapted to each account's instruction format. Re-export and upload after
a store change. Account skill support and upload steps depend on the account.

------------------------------------------------------------------------
## Build and test
Requires Go 1.24.7 or later. The CLI itself uses only the Go standard
library. `git` is required for `diff` and release tasks.
```sh
make build
make test
make vet
make help
```
To put the binary on your path, run
`go build -trimpath -o ~/bin/agentctl ./cmd/agentctl`.

------------------------------------------------------------------------
## Releases
After a commit reaches `main`, run `make deploy patch`, `make deploy minor`,
or `make deploy major`. The target waits for that commit's CI run, then
creates and pushes an annotated SemVer tag. CI builds release archives and
publishes a GitHub Release. `make dist` builds archives locally from the
version tag checked out at `HEAD`. Release binaries are unsigned.

------------------------------------------------------------------------
## License
MIT. See [LICENSE](LICENSE).
