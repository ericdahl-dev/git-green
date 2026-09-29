# Project Instructions for AI Agents

git-green is a terminal dashboard showing live GitHub CI status across repos. Domain language lives in `CONTEXT.md`; decisions in `docs/adr/`.

## Build & Test

```bash
go build ./...
go test -race ./...
golangci-lint run ./...
```

CI (`.github/workflows/ci.yml`) runs the same three. Releases are cut by pushing a `v*` tag; GoReleaser publishes binaries and the Homebrew cask (`ericdahl-dev/tap/git-green`).

## Architecture Overview

Go + Bubble Tea (ADR 0001).

- `internal/github` — REST and GraphQL client (runs, PRs, stacks, reviews)
- `internal/poller` — polls each Repo on an interval, paced against the token's REST budget
- `internal/aggregator` — rolls Run statuses up into a Stoplight
- `internal/state` — immutable snapshots handed to the UI
- `internal/ui` — dashboard tree, Repo manager, help overlay
- `internal/config` — `~/.config/git-green/config.toml`

## Workflow

- Branch off `main`, open a PR, squash-merge. Commit messages follow Conventional Commits and PRs close their issue (`Closes #N`).
- Keep `CONTEXT.md` in sync when behaviour or domain language changes.

## Agent skills

### Issue tracker

GitHub Issues in the `ericdahl-dev` org at https://github.com/ericdahl-dev/git-green. See `docs/agents/issue-tracker.md`. Beads (`bd`) is retired — do not use it.

### Triage labels

Default canonical labels (`needs-triage`, `needs-info`, `ready-for-agent`, `ready-for-human`, `wontfix`). See `docs/agents/triage-labels.md`.

### Domain docs

Single-context — `CONTEXT.md` + `docs/adr/` at repo root. See `docs/agents/domain.md`.
