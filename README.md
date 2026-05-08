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
- **Multi-org** — per-org token config with `gh auth token` fallback
- **Single binary** — no runtime, no dependencies

## Install

```bash
go install github.com/ericdahl-dev/git-green@latest
```

## Config

Create `~/.config/git-green/config.toml`:

```toml
[settings]
poll_interval = 15  # seconds

[[orgs]]
name = "your-org"
token = "ghp_xxx"          # or token_env = "MY_TOKEN_ENV"

[[repos]]
owner = "your-org"
name = "your-repo"
# branch = "main"          # optional; defaults to repo default branch
# workflows = ["CI"]       # optional; defaults to all workflows
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

| Key | Action |
|---|---|
| `↑` / `k` | Navigate up |
| `↓` / `j` | Navigate down |
| `enter` / `space` | Expand / collapse repo or PR row |
| `r` | Force refresh |
| `o` | Open run in browser |
| `q` | Quit |
| `?` | Help overlay |
