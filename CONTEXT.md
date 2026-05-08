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

**Branch section**: The default/configured-branch CI rows rendered above PR rows when a Repo is expanded. Shows the latest Run per Workflow for the tracked branch.

**Config file**: The user-managed file at `~/.config/git-green/config.toml` that lists which Repos to watch and any per-Org token overrides.

## Relationships

- A **Repo** belongs to exactly one **Org**
- A **Repo** has one or more **Workflows**
- A **Workflow** has many **Runs**; the dashboard shows only the latest per branch or PR head SHA
- A **Run** has one or more **Jobs**
- A **Job** has one or more **Steps**
- A **PR** has a head SHA; the dashboard fetches the latest Run per Workflow for that SHA

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

Repos and PRs are sorted by Stoplight priority so the most actionable items appear at the top: 🟡 in-progress → 🔴 failing → 🟢 passing → ⚪ no signal. Order is stable within each tier.
_Avoid_: bubbling, floating

## Dashboard tree

The Dashboard renders a two-level expandable tree:

```
▶ 🔴  owner/repo                     2 PRs open
▼ 🟡  owner/other-repo               CI · in_progress
      branch: main
          ●  CI
             ●  test
      ▶ 🟡  PR #7 · feat: something
      ▼ 🔴  PR #3 · fix: auth bug
            ✗  CI
               ✗  test
```

- **Repo row**: expand/collapse with `enter`/`space`. When expanded shows Branch section then PR rows.
- **Branch section**: non-navigable; always rendered above PR rows when a Repo is expanded.
- **PR row**: navigable; expand/collapse with `enter`/`space` to show that PR's Workflow runs.

_Avoid_: detail view, drill-down screen

## Polling

The mechanism by which the TUI fetches fresh data from the GitHub API. Runs on a configurable interval (default: 15 seconds). On API failure, the last known status is retained and shown with a staleness indicator rather than blanking the row.

Each poll cycle makes the following API calls per Repo:
- 1 × `ListRepositoryWorkflowRuns` (branch runs)
- 1 × `PullRequests.List`
- 1 × `ListRepositoryWorkflowRuns` per open PR (head SHA runs)
- 1 × `ListWorkflowJobs` per branch Workflow Run

_Avoid_: refresh, sync, watch

## Keybindings

| Key | Action |
|---|---|
| `↑` / `k` | Navigate up |
| `↓` / `j` | Navigate down |
| `enter` / `space` | Expand/collapse Repo or PR row |
| `r` | Force refresh |
| `o` | Open run in browser |
| `q` | Quit |
| `?` | Toggle help overlay |

## Flagged ambiguities

- "monitor" — resolved: means live dashboard display, not notification/alerting
- "detail view" — resolved: removed in favour of inline expand/collapse tree on the Dashboard
