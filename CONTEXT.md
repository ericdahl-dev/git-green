# git-green

A terminal dashboard that shows live GitHub CI status across multiple repos, updating automatically via polling.

## Language

**Repo**: A GitHub repository being monitored. Identified by `owner/name`.
_Avoid_: project, service

**Workflow**: The primary display unit. A named CI workflow within a Repo (e.g. `CI`, `Deploy`). Shows the status of its latest Run.
_Avoid_: pipeline, action

**Run**: A single execution of a Workflow, triggered by a push, PR, or manual dispatch. Has a status (queued, in_progress, success, failure, cancelled).
_Avoid_: build, execution

**Job**: A named unit of work within a Run. Drill-down target below Workflow.
_Avoid_: step (Step is a lower-level concept within a Job)

**Step**: The lowest-level unit within a Job. Not a primary display target.

**Org**: A GitHub organisation. Token configuration is scoped to Org, not Repo.
_Avoid_: organisation, account

**PR**: An open GitHub Pull Request within a Repo. The dashboard shows each open PR as an expandable row with its own Stoplight, derived from Workflow Runs on that PR's head SHA.
_Avoid_: pull request (use PR), change, diff

**Stack**: A GitHub stacked-PR group — an ordered chain of PRs, each based on the one below it, rooted on a trunk branch. GitHub numbers Stacks out of the same counter as PRs, so a Stack number sits alongside the PR numbers it holds (PR #271 lives in Stack #272). Created with `gh stack`. The dashboard renders a Stack as one collapsible row between the Repo and its PR rows.
_Avoid_: chain, series, stacked PRs (use Stack)

**Branch section**: The default/configured-branch CI rows rendered above PR rows when a Repo is expanded. Shows the latest Run per Workflow for the tracked branch.

**Config file**: The user-managed file at `~/.config/git-green/config.toml` that lists which Repos to watch and any per-Org token overrides. Can be edited directly or managed via the in-TUI Repo manager.

**Repo manager**: The in-TUI CRUD screen (key: `m`) for adding, editing, deleting, and toggling Repos. Changes are written to the Config file immediately and the Poller reloads automatically.
_Avoid_: settings screen, config editor

## Relationships

- A **Repo** belongs to exactly one **Org**
- A **Repo** has one or more **Workflows**
- A **Workflow** has many **Runs**; the dashboard shows only the latest per branch or PR head SHA
- A **Run** has one or more **Jobs**
- A **Job** has one or more **Steps**
- A **PR** has a head SHA; the dashboard fetches the latest Run per Workflow for that SHA
- A **PR** belongs to at most one **Stack**; a **Stack** holds two or more **PRs** in a fixed bottom-to-top order
- A **Stack** keeps counting **PRs** that have already merged, so its size can exceed the number of open **PRs** the dashboard shows
- A **Repo** may be **disabled** (`enabled = false`), in which case it is excluded from all Poller activity and makes no API calls

## Example dialogue

> **Dev:** "Why is the CI red for git-green?"
> **User:** "The `CI` **Workflow** is failing — the latest **Run** has a **Job** called `test` that errored."

> **Dev:** "Why is PR #42 yellow?"
> **User:** "The `CI` **Workflow** **Run** on that **PR**'s head SHA is still in progress."

## Stoplight

The visual health indicator for a Repo or PR. Aggregates across all Workflows using worst-case: the worst individual Workflow status determines the Stoplight color.

| Color | Meaning | GitHub statuses |
|---|---|---|
| 🟢 Green | Healthy | `success`, `neutral`, `skipped` |
| 🔴 Red | Broken or blocked | `failure`, `timed_out`, `action_required` |
| 🟡 Yellow | In progress | `queued`, `in_progress` |
| ⚪ Grey | No signal | `cancelled`, no runs yet |

_Avoid_: badge, indicator, light

## Active-first sorting

Repos, Stacks, and PRs are sorted by Stoplight priority so the most actionable items appear at the top: 🟡 in-progress → 🔴 failing → 🟢 passing → ⚪ no signal. Order is stable within each tier.

**Stacks are the exception.** Members of a Stack always render bottom-to-top, in the order they have to merge, because reordering them by Stoplight would hide that order. The Stack as a whole still sorts active-first against other Stacks and standalone PRs.
_Avoid_: bubbling, floating

## Dashboard tree

The Dashboard renders a two-level expandable tree:

```
▶ 🔴  owner/repo                     2 PRs open
▼ 🟡  owner/other-repo               CI · in_progress
      branch: main
          ●  CI
             ●  test
      ▼ 🔴  stack #272 · 4 PRs · wse-1937-id-search
            ▶ 🟢  1/4  PR #268 · feat: parse ids
            ▶ 🔴  2/4  PR #270 · feat: export rows
      ▶ 🟡  PR #7 · feat: something
      ▼ 🔴  PR #3 · fix: auth bug
            ✗  CI
               ✗  test
```

- **Repo row**: expand/collapse with `enter`/`space`. When expanded shows Branch section, then Stack rows and standalone PR rows.
- **Branch section**: non-navigable; always rendered above PR rows when a Repo is expanded.
- **Stack row**: navigable; expand/collapse with `enter`/`space` to show its member PR rows. Its Stoplight is the most actionable of its members', and `f` and `o` act on the first failing member so a collapsed Stack still exposes what broke.
- **PR row**: navigable; expand/collapse with `enter`/`space` to show that PR's Workflow runs. A PR inside a Stack renders one level deeper and carries its `position/size`.

_Avoid_: detail view, drill-down screen

## Polling

The mechanism by which the TUI fetches fresh data from the GitHub API. Runs on a configurable interval (default: 15 seconds). On API failure, the last known status is retained and shown with a staleness indicator rather than blanking the row.

Each poll cycle makes the following API calls per enabled Repo:
- 1 × `Repositories.Get` (first poll only, when no branch is configured — result cached)
- 1 × `ListRepositoryWorkflowRuns` (branch runs)
- 1 × `PullRequests.List`
- 1 × GraphQL query for Stack membership, only when 2+ PRs are open (a Stack needs at least two). A failure here costs only the grouping — PRs still render, ungrouped.
- 1 × `ListRepositoryWorkflowRuns` per open PR (head SHA runs)
- 1 × `ListWorkflowJobs` per branch Workflow Run

Disabled Repos are skipped entirely — no API calls are made for them.

A failed fetch retains the last known status and marks the Repo stale. A Repo
skipped by **Pacing** is not stale — nothing failed, it is simply not due yet.

_Avoid_: refresh, sync, watch

## Pacing

The adjustment of poll frequency to a token's remaining GitHub REST budget.
GitHub reports the budget on every response (`x-ratelimit-remaining`,
`-reset`), so the Poller always knows how much is left and when the window
resets.

Rather than polling flat-out until a 403 and then going dark for the rest of
the hour, the Poller spreads what is left across the time remaining:

```
spendable  = remaining - Reserve
affordable = spendable / cost per cycle
interval   = max(configured, time to reset / affordable)
```

- **Reserve**: 100 calls per token that git-green never spends. A token is
  usually shared with `gh` and other tooling, and polling it to zero takes
  those down too. When not even one cycle is affordable, the Poller waits out
  the window rather than dipping into the Reserve.
- **Cost per cycle** is measured, not assumed — every fetch reports how many
  REST calls it made. Expanding a Repo raises it (see the Polling call list),
  so the pace re-prices itself each cycle.
- **Scoped to the token, not the Org.** Orgs sharing a token share one budget,
  so they are paced together; pacing them separately would spend it twice.
- A healthy token is never held back: while there is budget to spare the
  formula lands below the configured interval, and the configured one wins.

When a token is paced below its configured interval, the title bar says so —
which Org, how much budget is left, the pace, and when it recovers. Healthy
tokens show nothing.

_Avoid_: throttling (use Pacing), backoff, rate limiting (that is GitHub's
403, which Pacing exists to avoid)

## Keybindings

### Dashboard

| Key | Action |
|---|---|
| `↑` / `k` | Navigate up |
| `↓` / `j` | Navigate down |
| `enter` / `space` | Expand/collapse Repo, Stack, or PR row |
| `f` | Re-run the failed Workflow Run on the selected row (`enter` confirms, `esc` cancels) |
| `r` | Force refresh |
| `o` | Open run in browser |
| `m` | Open Repo manager |
| `q` | Quit |
| `?` | Toggle help overlay |
| `esc` | Close help overlay |

**Re-run** is the only write action. It calls `RerunFailedJobsByID` for the
selected row's first failed Workflow Run, falling back to `RerunWorkflowByID`
when GitHub reports there are no individually-failed jobs, and needs a token
with `actions: write` for that org. The confirmation and the outcome (including
a 403 from a read-only token) both render in the dashboard hint line.

_Avoid_: fix, retry

### Repo manager

| Key | Action |
|---|---|
| `↑` / `k` | Navigate up |
| `↓` / `j` | Navigate down |
| `t` / `space` | Toggle enable / disable |
| `a` | Add Repo |
| `e` | Edit Repo |
| `d` | Delete Repo |
| `esc` | Back to Dashboard |

## Install

- **Homebrew**: `brew install ericdahl-dev/tap/git-green`
- **Go**: `go install github.com/ericdahl-dev/git-green@latest`

## CLI

- **`git-green`** (no args) — start the dashboard; requires a valid config file.
- **`git-green init`** — interactive starter config (`--force` overwrites an existing file).
- **`GIT_GREEN_DEBUG`** — when non-empty, emit debug logs for each repo poll (stderr).

## Flagged ambiguities

- "monitor" — resolved: means live dashboard display, not notification/alerting
- "detail view" — resolved: removed in favour of inline expand/collapse tree on the Dashboard
- "enabled/disabled" — resolved: `enabled = false` in config suppresses all API calls for that Repo; the Repo manager toggles this at runtime
