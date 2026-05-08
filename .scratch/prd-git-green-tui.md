# PRD: git-green TUI

**Labels:** `ready-for-agent`

---

## Problem Statement

Developers working across multiple GitHub repositories have no fast, at-a-glance way to see CI health without switching to a browser, navigating to each repo, and reading run status one at a time. This context-switching breaks flow, especially when monitoring a set of repos during a release or a refactor.

## Solution

A terminal dashboard (TUI) that displays live GitHub CI status for a user-configured set of repos. Each repo is represented as a single stoplight row — green, red, yellow, or grey — aggregated across all its workflows. Users can drill into a repo to see individual workflow jobs, force a refresh, or open a run in the browser, all without leaving the terminal. Data is fetched by polling the GitHub API every 15 seconds (configurable).

## User Stories

1. As a developer, I want to see a stoplight indicator per repo on launch, so that I can assess CI health across all my repos at a glance.
2. As a developer, I want the dashboard to update automatically, so that I don't have to manually refresh to see new run results.
3. As a developer, I want a red stoplight when any workflow in a repo is failing or timed out, so that I immediately know something needs attention.
4. As a developer, I want a yellow stoplight when a workflow is queued or running, so that I know work is in progress.
5. As a developer, I want a green stoplight when all workflows succeed, so that I can confidently move on.
6. As a developer, I want a grey stoplight when a run is cancelled or no runs exist yet, so that the dashboard doesn't misrepresent ambiguous states as failures.
7. As a developer, I want to navigate repos with arrow keys and select one with enter, so that I can explore details without touching the mouse.
8. As a developer, I want a detail view showing each workflow's jobs and their statuses for the latest run, so that I can identify which job is failing without opening the browser.
9. As a developer, I want to press `o` to open the current run in the browser, so that I can read full logs when needed.
10. As a developer, I want to press `r` to force an immediate refresh, so that I can get up-to-date status after a push without waiting for the poll interval.
11. As a developer, I want to press `?` to see a help overlay of keybindings, so that I can discover functionality without reading docs.
12. As a developer, I want to press `q` to quit cleanly, so that I can exit without killing the terminal.
13. As a developer, I want to configure which repos to watch in a config file, so that my dashboard is consistent across sessions.
14. As a developer, I want to optionally filter which workflows are shown per repo in the config file, so that noisy or irrelevant workflows (cron jobs, dependabot) don't pollute the dashboard.
15. As a developer, I want to optionally pin a specific branch per repo in the config file, so that I watch the branch that matters rather than defaulting blindly.
16. As a developer, I want the default branch's CI shown when no branch is configured, so that setup is minimal for most repos.
17. As a developer, I want to configure a GitHub token per org in the config file, so that I can monitor repos across orgs with different credentials.
18. As a developer, I want token values to be referenceable as environment variable names in the config file, so that I don't store credentials in plaintext on disk.
19. As a developer, I want repos with no per-org token configured to fall back to `gh auth token`, so that I don't need to duplicate my primary credential.
20. As a developer, I want the last known status shown with a staleness indicator when the GitHub API is unreachable, so that the dashboard remains useful during connectivity hiccups.
21. As a developer, I want persistent errors (e.g. bad token) surfaced in the detail view, so that I can diagnose auth problems without guessing.
22. As a developer, I want the poll interval to be configurable in the config file, so that I can trade off freshness against API rate limits.
23. As a developer, I want the TUI to distribute as a single binary, so that installation requires no runtime or dependency management.

## Implementation Decisions

### Modules

- **Config** — Parses and validates `~/.config/git-green/config.toml`. Owns the canonical shape of repo list, org token map, and global settings (poll interval). Exposes a clean, typed struct; all downstream modules consume Config, never raw TOML.

- **GitHub client** — Wraps `google/go-github` with per-org authentication. Given a Repo and optional branch, fetches the latest Workflow runs and their Jobs. Accepts a token directly (resolved by Config); knows nothing about the config file itself.

- **Poller** — Owns the polling ticker. On each tick, calls the GitHub client for every configured Repo concurrently, collects results, and publishes a new State snapshot. On client error, retains the last known result for that Repo and sets a staleness timestamp.

- **Aggregator** — Pure function: maps a slice of GitHub run statuses → a Stoplight color using worst-case aggregation across Workflows. Status mapping: `success`/`neutral`/`skipped` → green; `failure`/`timed_out`/`action_required` → red; `queued`/`in_progress` → yellow; `cancelled`/no runs → grey.

- **State / Model** — Immutable snapshot of all Repo statuses (Stoplight, latest Run, Jobs, staleness). The Poller produces a new State on every poll tick; the UI renders from the current State.

- **Dashboard view** — Bubble Tea component. Renders one row per Repo: stoplight color, owner/name, workflow summary, elapsed time. Handles `↑`/`↓` navigation and `enter` to select.

- **Detail view** — Bubble Tea component. Renders all Workflows for the selected Repo, with each Workflow's latest Run's Jobs and statuses. Handles `esc`/`backspace` to return to Dashboard.

- **Root app** — Bubble Tea root model. Owns the poll ticker, routes between Dashboard and Detail views, handles global keybindings (`r`, `o`, `q`, `?`).

### Key architectural decisions

- **Polling over webhooks** — The TUI runs locally with no public endpoint; polling the GitHub API at 15s default is simple and stays well within the 5,000 req/hour rate limit even for dozens of repos.
- **Go + Bubble Tea** — See ADR-0001.
- **Per-org token with `gh auth token` fallback** — Supports multi-org monitoring without requiring explicit token config for the primary org.
- **Default branch only** — Showing all branches would create noise from WIP feature branches. Per-repo branch override available in config.
- **Worst-case Stoplight aggregation** — A repo is only green when all its watched workflows are green. This matches the intuitive meaning of "is this repo healthy?"

### Config file shape

```toml
[settings]
poll_interval = 15  # seconds

[[orgs]]
name = "ericdahl-dev"
# no token = use gh auth token

[[orgs]]
name = "some-other-org"
token_env = "SOME_ORG_TOKEN"  # or token = "ghp_xxx"

[[repos]]
owner = "ericdahl-dev"
name = "git-green"
# branch = "main"       # optional; defaults to repo's default branch
# workflows = ["CI"]    # optional; defaults to all workflows
```

## Testing Decisions

- **What makes a good test:** Test external behaviour through the module's public interface, not implementation details. A good test describes a scenario ("given a run with status `failure`, the Aggregator returns red") rather than asserting on internal state or private functions.

- **Config** — Test parsing of valid TOML, validation errors (missing required fields, invalid poll interval), token resolution (explicit token, env var reference, missing env var), and defaulting behaviour (no branch → nil, no workflows → nil).

- **Aggregator** — Test every status → Stoplight mapping, worst-case aggregation across multiple workflows, and edge cases (no runs, all cancelled, mixed green/yellow).

- **State / Model** — Test that a new State snapshot correctly reflects updated Repo data from the Poller, staleness is set on error and cleared on success, and immutability (new snapshot doesn't mutate previous).

- No unit tests for Dashboard view, Detail view, or GitHub client — Bubble Tea components are integration-tested through use, and the GitHub client is a thin wrapper best verified by end-to-end testing against the real API.

## Out of Scope

- Notification/alerting (sound, desktop notifications, webhook emission)
- Triggering or re-running CI jobs from within the TUI
- Viewing raw log output (step-level streaming) — browser is the escape hatch via `o`
- PR-level CI status (only default/configured branch)
- GitHub Actions secrets or environment management
- Non-GitHub CI systems (CircleCI, Jenkins, etc.)
- Multi-user / shared config

## Further Notes

- The name "git-green" and the stoplight metaphor should be reflected in the visual design — Charm's Lip Gloss library (part of the Bubble Tea ecosystem) handles terminal color styling.
- Once a GitHub remote is created under `ericdahl-dev`, this PRD should be published as a GitHub Issue with the `ready-for-agent` label and this file can be removed.
