// Package aggregator is the one place that interprets a Run's GitHub status.
// Everything that colours, sorts, re-runs or alerts on a Run asks it, rather
// than reading status strings itself.
package aggregator

import githubclient "github.com/ericdahl-dev/git-green/internal/github"

// Stoplight represents the health color for a Repo, PR or Run.
type Stoplight int

const (
	StoplightGrey   Stoplight = iota // no runs, or all cancelled
	StoplightGreen                   // all passing
	StoplightYellow                  // in progress
	StoplightRed                     // failing or blocked
)

func (s Stoplight) String() string {
	switch s {
	case StoplightGreen:
		return "🟢"
	case StoplightRed:
		return "🔴"
	case StoplightYellow:
		return "🟡"
	default:
		return "⚪"
	}
}

// ActiveFirst ranks a Stoplight for Active-first sorting: in progress, then
// failing, then passing, then no signal. Lower sorts first.
func (s Stoplight) ActiveFirst() int {
	switch s {
	case StoplightYellow:
		return 0
	case StoplightRed:
		return 1
	case StoplightGreen:
		return 2
	default:
		return 3
	}
}

// Of maps one effective GitHub status (a Run or Job's conclusion, or its
// status while it has none) to a Stoplight.
func Of(status string) Stoplight {
	switch status {
	case "success", "neutral", "skipped":
		return StoplightGreen
	case "failure", "timed_out", "action_required", "startup_failure":
		return StoplightRed
	case "queued", "in_progress", "requested", "waiting", "pending":
		return StoplightYellow
	default:
		return StoplightGrey
	}
}

// Runs returns the worst-case Stoplight across runs: Red > Yellow > Green >
// Grey. No runs is Grey.
func Runs(runs []githubclient.WorkflowRun) Stoplight {
	result := StoplightGrey
	for _, r := range runs {
		if light := Of(r.Effective()); light > result {
			result = light
		}
	}
	return result
}
