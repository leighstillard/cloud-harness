# cloud-harness

A generic bootstrap for Claude Code cloud sessions that works whichever repo a session opens.

One Go binary, `harness`, does four things:

- `harness setup` runs from your cloud environment's setup script. It installs your pinned plugins, merges SessionStart/Stop/PreCompact hooks into the VM's `~/.claude/settings.json` (never overwriting anything already there), and writes a short `~/.claude/CLAUDE.md`.
- `harness boot` runs at every session start. It installs any private session plugins now that the GitHub proxy is up, pulls persistent memory for you and for this repo from a memory provider (Mem0 Platform today, behind an interface), and prints a block of at most 40 lines into context.
- `harness remember "<fact>"` writes memory back. `harness remember --auto` runs from the Stop and PreCompact hooks and stores only lines that start with `Remember:`.
- `harness status` tells you what mode you are in, whether a key is present or expected from the proxy, whether the provider is reachable, how old the cached block is, and which pinned plugins are installed.

Nothing here needs an always-on server. Nothing personal goes in this repo; your values live in your cloud environment's setup script.

## Install

Releases publish `harness-linux-amd64`, `harness-linux-arm64` and `harness-darwin-arm64` plus `SHA256SUMS`.

```bash
curl -fsSL "https://github.com/OWNER/cloud-harness/releases/latest/download/harness-linux-$(dpkg --print-architecture)" -o /usr/local/bin/harness
chmod +x /usr/local/bin/harness
```

From source:

```bash
go install github.com/leighstillard/cloud-harness/cmd/harness@latest
```

## Config: `~/.config/cloud-harness/config.yml`

```yaml
user_id: <string>                 # memory scope owner
memory:
  provider: mem0                  # only provider so far
  base_url: https://api.mem0.ai
  key_env: MEM0_API_KEY           # read locally; never read in the cloud, where a network secret supplies it
plugins:
  setup:                          # public marketplaces; installed by `harness setup`, cached in the environment snapshot
    - { name: harness, marketplace: cloud-harness, source: OWNER/cloud-harness, version: 0.1.0 }
  session:                        # may be private; installed by `harness boot` once the GitHub proxy is up; skipped when present
    - { name: spec-butler, marketplace: spec-butler, source: OWNER/spec-butler, version: 0.1.0 }
claude_md: |                      # optional override of cloud/CLAUDE.md
```

`source` is anything `claude plugin marketplace add` accepts: `owner/repo`, a git URL, or a local path. `marketplace` must match the `name` in that marketplace's `.claude-plugin/marketplace.json`. `version` is recorded by `harness status`; the CLI installs what the marketplace currently publishes.

Set `CLOUD_HARNESS_CONFIG_DIR` to use a different config directory. `CLAUDE_CONFIG_DIR` is honoured the same way Claude Code honours it.

## Cloud environment steps

1. Create a cloud environment. Its **Network access** must reach `api.mem0.ai` (or your provider's host) and GitHub releases: full access does, and so does Custom with the default package managers plus `api.mem0.ai`.
2. Add a **network secret** for `api.mem0.ai` (see below).
3. Copy [`cloud/setup.sh.example`](cloud/setup.sh.example) into the environment's **Setup script**, fill in `OWNER`, `user_id` and your plugins.
4. Start a session in any repo. The `## harness` block appears at the top of context.

What the setup script can and cannot do, from the [cloud environments docs](https://code.claude.com/docs/en/cloud-environments):

- It runs as root, before Claude Code launches, before the environment's variables are set, and before the GitHub proxy is up. It can only fetch public things, so `plugins.setup` must be public. Private marketplaces go in `plugins.session`.
- It is snapshotted. Later sessions skip it until the script or the allowlist changes, or about seven days pass. `harness boot` re-checks pinned plugins every session, so a failed install self-heals.
- It must exit zero and finish well under five minutes. The example script never fails the session.

SessionStart hooks run every session with the proxy up. Hooks merged into `~/.claude/settings.json` by a setup script are reported to fire ([anthropics/claude-code#89215](https://github.com/anthropics/claude-code/issues/89215)); `harness setup` merges them and the `harness` plugin declares the same hooks, so one of the two paths always runs. `harness boot` de-duplicates a double fire within the same session.

## The network-secret pattern

In the cloud the binary never reads an API key. The agent proxy attaches it to requests for the hosts you list on the secret, after the request leaves the VM. For Mem0 the header is `Authorization` with the prefix `Token`, not the form's default `Bearer`:

| Field | Value |
| --- | --- |
| Allowed websites | `api.mem0.ai` |
| Header name | `Authorization` |
| Prefix | `Token` |
| Value | your Mem0 API key |

If the prefix is left at `Bearer`, Mem0 answers `401 token_not_valid` and `harness status` reports `reach: unauthorized`.

`harness status` in the cloud prints `key: proxy-expected`. If the key were readable in the VM, anyone using the environment could read it; that is why it is not an environment variable.

## Local use

Locally the binary reads `~/.config/cloud-harness/.env` (or the environment) for the variable named by `memory.key_env`:

```bash
mkdir -p ~/.config/cloud-harness
echo 'MEM0_API_KEY=m0-...' > ~/.config/cloud-harness/.env
chmod 600 ~/.config/cloud-harness/.env
harness status
```

`.env` is gitignored here and should never be committed anywhere. Install the plugin locally with:

```bash
claude plugin marketplace add OWNER/cloud-harness
claude plugin install harness@cloud-harness
```

## The boot block

```
## harness
repo: acme/widgets
session: cse_...
### Global
- Prefer short commit subjects
### This repo
- Use make test before pushing (+3 more)
### Ready beads
- b-12: wire the exporter
Run `harness remember "<fact>"` for anything worth keeping; scoped to this repo unless --global.
```

- Global memories have no `agent_id`; repo memories use `agent_id=<owner/name>` from the origin remote (or the cwd basename when there is none).
- The block is cached at `~/.claude/harness-last.md`. If the provider takes more than 2 s, the cache is printed with `(cached ...)` in the header. Boot always exits 0.
- If `bd` is on PATH and `.beads/` exists, the top five of `bd ready --json` are listed. If `bdh` is on PATH, one line of `bdh status` is shown.

## Memory writes

```bash
harness remember "The integration tests need docker compose up db first"
harness remember --global "Prefer tabs in Go"
```

Each write carries `run_id=<session id>` and metadata `{source: harness, repo, ts}`. Facts are stored verbatim (`infer=false`).

`--auto` reads the hook JSON on stdin, takes the last assistant message, and stores lines starting `Remember:` (repo scope) or `Remember global:`. Anything else exits 0 silently. A small ledger prevents the Stop and PreCompact hooks from storing the same line twice. Every global store, automatic or via `--global`, is announced to you after the turn through the Stop hook's `systemMessage` (PreCompact cannot show one, so those wait for the next Stop), and `harness status` lists the last ten.

## Layout

```
.claude-plugin/marketplace.json     marketplace "cloud-harness" with one plugin: harness
plugins/harness/                    plugin manifest, hooks.json, skills/harness/SKILL.md
cmd/harness/                        setup | boot | remember | status | version
internal/config                     config.yml schema + loader
internal/memory                     Provider interface and the Mem0 client
internal/repo                       owner/name from the origin remote
internal/context                    block renderer, cache, bd/bdh helpers
internal/plugins                    idempotent installs via the claude CLI
internal/settings                   hook merge into settings.json
cloud/                              setup.sh.example and the default CLAUDE.md
.github/workflows                   ci.yml and release.yml (tag v* builds and uploads binaries)
```

## Development

```bash
go test ./...
go vet ./...
shellcheck cloud/setup.sh.example
claude plugin validate .
```

`go test ./...` makes no network calls; the Mem0 client is tested against an in-process HTTP server.
