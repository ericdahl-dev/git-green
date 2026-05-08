package state

import (
	"time"

	"github.com/ericdahl-dev/git-green/internal/aggregator"
	githubclient "github.com/ericdahl-dev/git-green/internal/github"
)

// RepoState holds the current display state for a single Repo.
type RepoState struct {
	Owner     string
	Name      string
	Stoplight aggregator.Stoplight
	Runs      []githubclient.WorkflowRun
	StaleAt   *time.Time // non-nil when last fetch failed
	Err       error      // last error, if any
}

func (r RepoState) FullName() string {
	return r.Owner + "/" + r.Name
}

func (r RepoState) IsStale() bool {
	return r.StaleAt != nil
}

// Snapshot is an immutable view of all repo states at a point in time.
type Snapshot struct {
	Repos     []RepoState
	UpdatedAt time.Time
}

// New creates a fresh Snapshot from a slice of RepoStates.
func New(repos []RepoState) Snapshot {
	copied := make([]RepoState, len(repos))
	copy(copied, repos)
	return Snapshot{
		Repos:     copied,
		UpdatedAt: time.Now(),
	}
}
