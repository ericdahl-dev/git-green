package poller

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/ericdahl-dev/git-green/internal/aggregator"
	"github.com/ericdahl-dev/git-green/internal/alerts"
	"github.com/ericdahl-dev/git-green/internal/config"
	githubclient "github.com/ericdahl-dev/git-green/internal/github"
	"github.com/ericdahl-dev/git-green/internal/logx"
	"github.com/ericdahl-dev/git-green/internal/ratelimit"
	"github.com/ericdahl-dev/git-green/internal/state"
	"github.com/ericdahl-dev/git-green/internal/webhooks"
)

// Fetcher is the interface the Poller uses to fetch runs — allows test substitution.
type Fetcher interface {
	FetchAll(ctx context.Context, q githubclient.RepoQuery) (githubclient.RepoData, error)
}

// maxInFlight caps how many Repos one cycle fetches at once. GitHub's
// secondary rate limit keys on concurrency, so an unbounded burst of every
// Repo trips it long before the hourly budget runs low.
const maxInFlight = 4

// ClientFactory creates a Fetcher for a given token.
type ClientFactory func(token string) Fetcher

// Poller orchestrates periodic fetches across all configured repos.
type Poller struct {
	cfg        *config.Config
	factory    ClientFactory
	dispatcher *webhooks.Dispatcher
	mu         sync.Mutex
	current    []state.RepoState
	// alerts decides when a branch or PR has been stuck long enough to send
	// a webhook. Only the fetch loop touches it.
	alerts *alerts.Watcher
	// expanded holds the "owner/name" of repos whose rows are open in the UI.
	// Only those fetch per-run jobs and per-PR runs; see RepoQuery.Detail.
	expanded map[string]bool
	// pacer paces each token against its REST budget and holds tokens
	// GitHub has rate-limited until their reset.
	pacer *ratelimit.Pacer
	// now is swappable in tests so threshold crossings can be exercised
	// without waiting on the wall clock.
	now func() time.Time

	// refresh wakes the fetch loop early; pending holds a Config waiting to
	// be applied at the start of its next cycle (guarded by mu). Only the
	// loop goroutine replaces cfg and dispatcher, and only between cycles, so
	// a cycle's fetches read them without a lock.
	refresh chan struct{}
	pending *config.Config
}

// New creates a Poller with the given config and client factory.
func New(cfg *config.Config, factory ClientFactory) *Poller {
	enabled := cfg.EnabledRepos()
	repos := make([]state.RepoState, len(enabled))
	for i, r := range enabled {
		repos[i] = state.RepoState{
			Owner:     r.Owner,
			Name:      r.Name,
			Branch:    r.Branch,
			Stoplight: aggregator.StoplightGray,
		}
	}
	cfg = cfg.Clone()
	p := &Poller{
		cfg:        cfg,
		factory:    factory,
		dispatcher: webhooks.New(cfg.Webhooks),
		current:    repos,
		alerts:     alerts.NewWatcher(stuckThreshold(cfg)),
		expanded:   make(map[string]bool),
		now:        time.Now,
		refresh:    make(chan struct{}, 1),
	}
	// Read the clock through p.now so tests that swap it move pacing too.
	p.pacer = ratelimit.NewPacer(func() time.Time { return p.now() })
	return p
}

// SetExpandedRepos records which repo rows are open in the UI, as
// "owner/name". Expanded repos fetch the job and PR-run detail their rows
// render; collapsed ones skip it.
func (p *Poller) SetExpandedRepos(names []string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.expanded = make(map[string]bool, len(names))
	for _, n := range names {
		p.expanded[n] = true
	}
}

func (p *Poller) isExpanded(owner, name string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.expanded[owner+"/"+name]
}

// Throttles reports every token currently polling slower than configured, for
// the title bar.
func (p *Poller) Throttles() []state.Throttle { return p.pacer.Throttles() }

// Snapshot returns an immutable view of the current (possibly initial) state.
func (p *Poller) Snapshot() state.Snapshot {
	p.mu.Lock()
	defer p.mu.Unlock()
	return state.New(p.current)
}

// Start runs the fetch loop until ctx is canceled or the returned cancel is
// called, sending a Snapshot after every cycle. The loop is the only fetcher
// and the only sender, so cycles never overlap and the channel closes only
// once nothing can send on it.
func (p *Poller) Start(ctx context.Context) (<-chan state.Snapshot, context.CancelFunc) {
	ctx, cancel := context.WithCancel(ctx)
	ch := make(chan state.Snapshot, 1)

	go func() {
		defer close(ch)
		ticker := time.NewTicker(p.interval())
		defer ticker.Stop()
		for {
			if p.applyPending() {
				ticker.Reset(p.interval())
			}
			publish(ch, p.fetch(ctx))
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			case <-p.refresh:
			}
		}
	}()

	return ch, cancel
}

// Refresh asks the loop for a cycle now instead of at the next tick. Requests
// made while one is already waiting collapse into it.
func (p *Poller) Refresh() {
	select {
	case p.refresh <- struct{}{}:
	default:
	}
}

// Reload swaps in a new Config (a copy, so later edits to cfg do not reach
// the Poller) from the next cycle, and asks for that cycle now.
func (p *Poller) Reload(cfg *config.Config) {
	p.mu.Lock()
	p.pending = cfg.Clone()
	p.mu.Unlock()
	p.Refresh()
}

// applyPending installs a Config waiting from Reload. Called only by the loop,
// between cycles. It reports whether there was one.
func (p *Poller) applyPending() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.pending == nil {
		return false
	}
	p.cfg, p.pending = p.pending, nil
	p.dispatcher = webhooks.New(p.cfg.Webhooks)
	p.alerts.SetThreshold(stuckThreshold(p.cfg))
	return true
}

func stuckThreshold(cfg *config.Config) time.Duration {
	return time.Duration(cfg.Settings.StuckThresholdMinutes) * time.Minute
}

func (p *Poller) interval() time.Duration {
	return time.Duration(p.cfg.Settings.PollInterval) * time.Second
}

// publish hands snap to the reader, replacing one it has not taken yet: the
// newest Snapshot is the only one worth showing. It never blocks, which is
// safe because the loop is the channel's only sender.
func publish(ch chan state.Snapshot, snap state.Snapshot) {
	select {
	case <-ch:
	default:
	}
	ch <- snap
}

// fetch runs one cycle across every enabled Repo and returns the result.
func (p *Poller) fetch(ctx context.Context) state.Snapshot {
	var wg sync.WaitGroup
	enabled := p.cfg.EnabledRepos()
	results := make([]state.RepoState, len(enabled))

	p.mu.Lock()
	previous := make([]state.RepoState, len(p.current))
	copy(previous, p.current)
	p.mu.Unlock()

	sem := make(chan struct{}, maxInFlight)
	for i, repo := range enabled {
		wg.Add(1)
		go func(i int, repo config.Repo) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			// find previous state by owner/name since indices may shift
			var prev state.RepoState
			for _, p := range previous {
				if p.Owner == repo.Owner && p.Name == repo.Name {
					prev = p
					break
				}
			}
			// A token polling below the configured rate skips its repos this
			// cycle. Their last-known state stands rather than going stale:
			// nothing failed, the dashboard is just refreshing less often.
			if p.pacedOut(repo.Owner) {
				results[i] = prev
				return
			}
			results[i] = p.fetchRepo(ctx, repo, prev)
		}(i, repo)
	}

	wg.Wait()
	p.pacer.EndCycle(p.interval())

	p.mu.Lock()
	p.current = results
	p.mu.Unlock()

	p.dispatcher.Send(p.alerts.Observe(results, p.now()))

	snap := state.New(results)
	snap.Throttles = p.Throttles()
	return snap
}

// pacedOut reports whether an Org's token is being paced this cycle. A token
// GitHub rate-limited is not paced out: its Repos still go through fetchRepo,
// which marks them stale with the reason.
func (p *Poller) pacedOut(org string) bool {
	token, err := p.cfg.TokenForOrg(org)
	if err != nil || p.pacer.Due(token) {
		return false
	}
	_, limited := p.pacer.LimitedUntil(token)
	return !limited
}

func (p *Poller) fetchRepo(ctx context.Context, repo config.Repo, prev state.RepoState) state.RepoState {
	logx.Debug("fetch repo", "owner", repo.Owner, "name", repo.Name)
	// Keep whatever branch was already resolved. Falling back to the config
	// value (often empty) would throw the resolution away on every error and
	// force another Repositories.Get next cycle — most expensive exactly when
	// the API is already refusing us.
	resolved := repo.Branch
	if resolved == "" {
		resolved = prev.Branch
	}

	stale := func(err error) state.RepoState {
		now := p.now()
		return state.RepoState{
			Owner:     repo.Owner,
			Name:      repo.Name,
			Branch:    resolved,
			Stoplight: prev.Stoplight,
			Runs:      prev.Runs,
			PRs:       prev.PRs,
			StaleAt:   &now,
			Err:       err,
		}
	}

	token, err := p.cfg.TokenForOrg(repo.Owner)
	if err != nil {
		return stale(err)
	}
	if until, limited := p.pacer.LimitedUntil(token); limited {
		return stale(fmt.Errorf("rate limited for %s; retrying in %s", repo.Owner, until.Sub(p.now()).Round(time.Second)))
	}

	client := p.factory(token)
	// Use the previously-resolved branch so we avoid a Repositories.Get call every poll.
	q := githubclient.RepoQuery{
		Owner:     repo.Owner,
		Name:      repo.Name,
		Branch:    resolved,
		Workflows: repo.Workflows,
		Detail:    p.isExpanded(repo.Owner, repo.Name),
	}

	data, err := client.FetchAll(ctx, q)
	p.pacer.Observe(token, repo.Owner, data.Calls, data.Budget)
	if err != nil {
		p.pacer.Failed(token, err)
		return stale(err)
	}

	runs := data.BranchRuns
	prRuns := data.PRRuns

	// Build PRStates.
	prStates := make([]state.PRState, 0, len(prRuns))
	for _, pr := range prRuns {
		prStates = append(prStates, state.PRState{
			Number:    pr.PR.Number,
			Title:     pr.PR.Title,
			HTMLURL:   pr.PR.HTMLURL,
			Stoplight: aggregator.Runs(pr.Runs),
			Runs:      pr.Runs,
			Mergeable: pr.PR.Mergeable,
			Stack:     pr.PR.Stack,
			Review:    pr.PR.Review,
		})
	}

	return state.RepoState{
		Owner:     repo.Owner,
		Name:      repo.Name,
		Branch:    data.ResolvedBranch,
		Stoplight: aggregator.Runs(runs),
		Runs:      runs,
		PRs:       prStates,
		StaleAt:   nil,
		Err:       nil,
	}
}
