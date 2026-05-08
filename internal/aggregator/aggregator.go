package aggregator

// Stoplight represents the health color for a Repo.
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

// RunStatus mirrors the relevant GitHub API status/conclusion values.
type RunStatus string

const (
	StatusSuccess        RunStatus = "success"
	StatusNeutral        RunStatus = "neutral"
	StatusSkipped        RunStatus = "skipped"
	StatusFailure        RunStatus = "failure"
	StatusTimedOut       RunStatus = "timed_out"
	StatusActionRequired RunStatus = "action_required"
	StatusQueued         RunStatus = "queued"
	StatusInProgress     RunStatus = "in_progress"
	StatusCancelled      RunStatus = "cancelled"
)

func statusToStoplight(s RunStatus) Stoplight {
	switch s {
	case StatusSuccess, StatusNeutral, StatusSkipped:
		return StoplightGreen
	case StatusFailure, StatusTimedOut, StatusActionRequired:
		return StoplightRed
	case StatusQueued, StatusInProgress:
		return StoplightYellow
	default:
		return StoplightGrey
	}
}

// Aggregate returns the worst-case Stoplight across all provided run statuses.
// Red > Yellow > Green > Grey.
func Aggregate(statuses []RunStatus) Stoplight {
	result := StoplightGrey
	for _, s := range statuses {
		light := statusToStoplight(s)
		if light > result {
			result = light
		}
	}
	return result
}
