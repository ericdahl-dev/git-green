package poller

import (
	"context"
	"sync"
	"time"

	"github.com/ericdahl-dev/git-green/internal/aggregator"
	"github.com/ericdahl-dev/git-green/internal/config"
	githubclient "github.com/ericdahl-dev/git-green/internal/github"
	"github.com/ericdahl-dev/git-green/internal/state"
)

// Fetcher is the interface the Poller uses to fetch runs — allows test substitution.
type Fetcher interface {
	FetchRuns(ctx context.Context, q githubclient.RepoQuery) ([]githubclient.WorkflowRun, error)
}

// ClientFactory creates a Fetcher for a given token.
type ClientFactory func(token string) Fetcher

// Poller orchestrates periodic fetches across all configured repos.
type Poller struct {
	cfg     *config.Config
	factory ClientFactory
	mu      sync.Mutex
	current []state.RepoState
}

// New creates a Poller with the given config and client factory.
func New(cfg *config.Config, factory ClientFactory) *Poller {
	repos := make([]state.RepoState, len(cfg.Repos))
	for i, r := range cfg.Repos {
		repos[i] = state.RepoState{
			Owner:     r.Owner,
			Name:      r.Name,
			Stoplight: aggregator.StoplightGrey,
		}
	}
	return &Poller{cfg: cfg, factory: factory, current: repos}
}

// Start begins polling on the configured interval, sending Snapshots to the returned channel.
// Call the returned cancel func to stop.
func (p *Poller) Start(ctx context.Context) (<-chan state.Snapshot, context.CancelFunc) {
	ctx, cancel := context.WithCancel(ctx)
	ch := make(chan state.Snapshot, 1)

	go func() {
		defer close(ch)
		p.fetch(ctx, ch)
		ticker := time.NewTicker(time.Duration(p.cfg.Settings.PollInterval) * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				p.fetch(ctx, ch)
			}
		}
	}()

	return ch, cancel
}

// ForceRefresh triggers an immediate out-of-cycle fetch.
func (p *Poller) ForceRefresh(ctx context.Context, ch chan<- state.Snapshot) {
	go p.fetch(ctx, ch)
}

func (p *Poller) fetch(ctx context.Context, ch chan<- state.Snapshot) {
	var wg sync.WaitGroup
	results := make([]state.RepoState, len(p.cfg.Repos))

	p.mu.Lock()
	previous := make([]state.RepoState, len(p.current))
	copy(previous, p.current)
	p.mu.Unlock()

	for i, repo := range p.cfg.Repos {
		wg.Add(1)
		go func(i int, repo config.Repo) {
			defer wg.Done()
			rs := p.fetchRepo(ctx, repo, previous[i])
			results[i] = rs
		}(i, repo)
	}

	wg.Wait()

	p.mu.Lock()
	p.current = results
	p.mu.Unlock()

	snap := state.New(results)
	select {
	case ch <- snap:
	default:
		// drop if consumer is slow; next tick will send a fresher snapshot
	}
}

func (p *Poller) fetchRepo(ctx context.Context, repo config.Repo, prev state.RepoState) state.RepoState {
	token, err := p.cfg.TokenForOrg(repo.Owner)
	if err != nil {
		now := time.Now()
		return state.RepoState{
			Owner:     repo.Owner,
			Name:      repo.Name,
			Stoplight: prev.Stoplight,
			Runs:      prev.Runs,
			StaleAt:   &now,
			Err:       err,
		}
	}

	client := p.factory(token)
	runs, err := client.FetchRuns(ctx, githubclient.RepoQuery{
		Owner:     repo.Owner,
		Name:      repo.Name,
		Branch:    repo.Branch,
		Workflows: repo.Workflows,
	})
	if err != nil {
		now := time.Now()
		return state.RepoState{
			Owner:     repo.Owner,
			Name:      repo.Name,
			Stoplight: prev.Stoplight,
			Runs:      prev.Runs,
			StaleAt:   &now,
			Err:       err,
		}
	}

	statuses := make([]aggregator.RunStatus, 0, len(runs))
	for _, r := range runs {
		s := r.Conclusion
		if s == "" {
			s = r.Status
		}
		statuses = append(statuses, aggregator.RunStatus(s))
	}

	return state.RepoState{
		Owner:     repo.Owner,
		Name:      repo.Name,
		Stoplight: aggregator.Aggregate(statuses),
		Runs:      runs,
		StaleAt:   nil,
		Err:       nil,
	}
}
