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

**Config file**: The user-managed file at `~/.config/git-green/config.toml` that lists which Repos to watch and any per-Org token overrides.

## Relationships

- A **Repo** belongs to exactly one **Org**
- A **Repo** has one or more **Workflows**
- A **Workflow** has many **Runs**; the dashboard shows only the latest
- A **Run** has one or more **Jobs**
- A **Job** has one or more **Steps**

## Example dialogue

> **Dev:** "Why is the CI red for git-green?"
> **User:** "The `CI` **Workflow** is failing — the latest **Run** has a **Job** called `test` that errored."

## Stoplight

The visual health indicator for a Repo. Aggregates across all Workflows using worst-case: the worst individual Workflow status determines the Repo's Stoplight color.

| Color | Meaning | GitHub statuses |
|---|---|---|
| 🟢 Green | Healthy | `success`, `neutral`, `skipped` |
| 🔴 Red | Broken or blocked | `failure`, `timed_out`, `action_required` |
| 🟡 Yellow | In progress | `queued`, `in_progress` |
| ⚪ Grey | No signal | `cancelled`, no runs yet |

_Avoid_: badge, indicator, light

## Views

**Dashboard**: The main view. One row per Repo showing its Stoplight, name, and latest Workflow summary. Entry point on launch.

**Detail view**: Shown when a Repo is selected from the Dashboard. Lists all Workflows for that Repo with their latest Run's Jobs and statuses.

_Avoid_: home screen, list view, drill-down screen

## Polling

The mechanism by which the TUI fetches fresh data from the GitHub API. Runs on a configurable interval (default: 15 seconds). On API failure, the last known status is retained and shown with a staleness indicator rather than blanking the row.
_Avoid_: refresh, sync, watch

## Keybindings

| Key | Action |
|---|---|
| `↑` / `↓` | Navigate |
| `enter` | Open Detail view |
| `esc` / `backspace` | Return to Dashboard |
| `r` | Force refresh |
| `o` | Open run in browser |
| `q` | Quit |
| `?` | Toggle help overlay |

## Flagged ambiguities

- "monitor" — resolved: means live dashboard display, not notification/alerting
