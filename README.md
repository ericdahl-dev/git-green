# git-green

### git your branches green

A terminal dashboard for live GitHub CI health across multiple repos — no browser required.

![git-green dashboard](docs/screenshot.svg)

## Features

- **Stoplight-per-repo** — 🟢 🔴 🟡 ⚪ aggregated worst-case across all workflows
- **PR-level CI tree** — expand any repo to see branch CI and each open PR with its own stoplight
- **Active-first sorting** — in-progress and failing repos/PRs bubble to the top automatically
- **Inline expand/collapse** — navigate with `↑`/`↓`, toggle any row with `enter`/`space`
- **Auto-polling** — refreshes every 15 seconds (configurable); retains last-known status on API errors
- **In-TUI repo management** — add, edit, delete, and enable/disable repos without leaving the terminal
- **Multi-org** — per-org token config with `gh auth token` fallback
- **Single binary** — no runtime, no dependencies
- **Interactive init** — `git-green init` writes a starter config via a terminal form

## Install

### Homebrew

```bash
brew install ericdahl-dev/tap/git-green
```

### Go

```bash
go install github.com/ericdahl-dev/git-green@latest
```

## First-time config

Run an interactive wizard (writes `~/.config/git-green/config.toml`):

```bash
git-green init
```

Use `git-green init --force` to overwrite an existing file.

## Config

Create `~/.config/git-green/config.toml` by hand, or start from `git-green init` and edit:

```toml
[settings]
poll_interval_seconds = 15
# stuck_threshold_minutes = 30   # optional; default 30

[[orgs]]
name = "your-org"
token = "ghp_xxx"          # or token_env = "MY_TOKEN_ENV"

[[repos]]
owner = "your-org"
name = "your-repo"
# branch = "main"          # optional; defaults to repo default branch
# workflows = ["CI"]       # optional; defaults to all workflows
# enabled = false          # optional; disable without deleting (no API calls made)
```

### Personal accounts and orgs without a token

Any `owner` that doesn't match a `[[orgs]]` entry automatically falls back to `gh auth token` — the account you're logged in as via `gh auth login`. No extra config needed:

```toml
# Logged in as Skeyelab via `gh auth login`? Just add the repo:
[[repos]]
owner = "Skeyelab"
name = "your-repo"
```

To use an explicit token for a specific account, add an `[[orgs]]` entry:

```toml
[[orgs]]
name = "some-other-org"
token_env = "SOME_ORG_TOKEN"   # or token = "ghp_xxx"

[[repos]]
owner = "some-other-org"
name = "your-repo"
```

## Keybindings

### Dashboard

| Key | Action |
|---|---|
| `↑` / `k` | Navigate up |
| `↓` / `j` | Navigate down |
| `enter` / `space` | Expand / collapse repo or PR row |
| `r` | Force refresh |
| `o` | Open run in browser |
| `m` | Open repo manager |
| `q` | Quit |
| `?` | Toggle help overlay |
| `esc` | Close help overlay |

### Repo manager (`m`)

| Key | Action |
|---|---|
| `↑` / `k` | Navigate up |
| `↓` / `j` | Navigate down |
| `t` / `space` | Toggle enable / disable |
| `a` | Add repo |
| `e` | Edit repo |
| `d` | Delete repo |
| `esc` | Back to dashboard |

Changes are written to `config.toml` immediately and the poller reloads automatically. Disabled repos make no API calls.

## Troubleshooting

Set `GIT_GREEN_DEBUG=1` to print per-repo fetch debug lines to stderr.
