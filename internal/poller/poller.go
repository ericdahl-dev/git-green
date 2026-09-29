package poller

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/ericdahl-dev/git-green/internal/aggregator"
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

// ClientFactory creates a Fetcher for a given token.
type ClientFactory func(token string) Fetcher

// stuckEntry records when a single condition was first seen in a bad state and
// whether its webhook has already fired, so a wedged repo alerts once rather
// than on every poll cycle.
type stuckEntry struct {
	since   time.Time
	reason  string
	alerted bool
}

// Poller orchestrates periodic fetches across all configured repos.
type Poller struct {
	cfg        *config.Config
	factory    ClientFactory
	dispatcher *webhooks.Dispatcher
	mu         sync.Mutex
	current    []state.RepoState
	// stuck is keyed by repo + condition (see stuckKey) and guarded by mu.
	stuck map[string]*stuckEntry
	// expanded holds the "owner/name" of repos whose rows are open in the UI.
	// Only those fetch per-run jobs and per-PR runs; see RepoQuery.Detail.
	expanded map[string]bool
	// pacer paces each token against its REST budget and holds tokens
	// GitHub has rate-limited until their reset.
	pacer *ratelimit.Pacer
	// now is swappable in tests so threshold crossings can be exercised
	// without waiting on the wall clock.
	now func() time.Time
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
			Stoplight: aggregator.StoplightGrey,
		}
	}
	p := &Poller{
		cfg:        cfg,
		factory:    factory,
		dispatcher: webhooks.New(cfg.Webhooks),
		current:    repos,
		stuck:      make(map[string]*stuckEntry),
		expanded:   make(map[string]bool),
		now:        time.Now,
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

// ReloadConfig replaces the config (e.g. after CRUD edits) and triggers an
// immediate fetch so the dashboard reflects the new repo list.
func (p *Poller) ReloadConfig(cfg *config.Config, ctx context.Context, ch chan<- state.Snapshot) {
	p.mu.Lock()
	p.cfg = cfg
	p.dispatcher = webhooks.New(cfg.Webhooks)
	p.mu.Unlock()
	go p.fetch(ctx, ch)
}

func (p *Poller) fetch(ctx context.Context, ch chan<- state.Snapshot) {
	var wg sync.WaitGroup
	enabled := p.cfg.EnabledRepos()
	results := make([]state.RepoState, len(enabled))

	p.mu.Lock()
	previous := make([]state.RepoState, len(p.current))
	copy(previous, p.current)
	p.mu.Unlock()

	for i, repo := range enabled {
		wg.Add(1)
		go func(i int, repo config.Repo) {
			defer wg.Done()
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
	p.pacer.EndCycle(time.Duration(p.cfg.Settings.PollInterval) * time.Second)

	p.mu.Lock()
	p.current = results
	// Compute events under the lock (evaluateStuck mutates p.stuck), but POST
	// them after releasing it so a hanging endpoint cannot stall Snapshot().
	events := p.evaluateStuck(results)
	p.mu.Unlock()

	for _, evt := range events {
		p.dispatcher.Dispatch(evt)
	}

	snap := state.New(results)
	snap.Throttles = p.Throttles()
	select {
	case ch <- snap:
	default:
		// drop if consumer is slow; next tick will send a fresher snapshot
	}
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
		now := time.Now()
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

// stuckKey identifies one stuck condition: a repo's default branch, or one of
// its open PRs.
func stuckKey(r state.RepoState, scope string) string {
	return r.FullName() + "#" + scope
}

// evaluateStuck folds the freshly-polled state into the stuck bookkeeping and
// returns the events that should fire this cycle.
//
// A condition alerts exactly once, on the first cycle where it has been bad for
// at least the configured threshold. Recovering prunes the entry, so the next
// incident re-arms. Callers must hold p.mu.
func (p *Poller) evaluateStuck(repos []state.RepoState) []webhooks.Event {
	threshold := time.Duration(p.cfg.Settings.StuckThresholdMinutes) * time.Minute
	now := p.now()
	seen := make(map[string]struct{}, len(p.stuck))
	var events []webhooks.Event

	// track records one stuck condition and appends an event if this is the
	// cycle that crosses the threshold.
	track := func(key, reason string, build func(since time.Time) webhooks.Event) {
		seen[key] = struct{}{}
		entry, ok := p.stuck[key]
		// A changed reason (an in-progress run turning into a failure) is a new
		// condition, so restart the clock and allow a fresh alert.
		if !ok || entry.reason != reason {
			entry = &stuckEntry{since: now, reason: reason}
			p.stuck[key] = entry
		}
		if entry.alerted || now.Sub(entry.since) < threshold {
			return
		}
		entry.alerted = true
		events = append(events, build(entry.since))
	}

	for i := range repos {
		r := repos[i]

		if stuck, reason := p.branchStuckReason(&r); stuck {
			track(stuckKey(r, "branch"), reason, func(since time.Time) webhooks.Event {
				runURL, workflow := "", ""
				if len(r.Runs) > 0 {
					runURL = r.Runs[0].HTMLURL
					workflow = r.Runs[0].WorkflowName
				}
				return webhooks.Event{
					Event:      "branch_stuck",
					Reason:     reason,
					Repo:       r.FullName(),
					Workflow:   workflow,
					RunURL:     runURL,
					StuckSince: since,
					Timestamp:  now,
				}
			})
		}

		for j := range r.PRs {
			pr := r.PRs[j]
			stuck, reason := p.prStuckReason(&pr)
			if !stuck {
				continue
			}
			track(stuckKey(r, fmt.Sprintf("pr-%d", pr.Number)), reason, func(since time.Time) webhooks.Event {
				runURL, workflow := "", ""
				if len(pr.Runs) > 0 {
					runURL = pr.Runs[0].HTMLURL
					workflow = pr.Runs[0].WorkflowName
				}
				return webhooks.Event{
					Event:  "pr_stuck",
					Reason: reason,
					Repo:   r.FullName(),
					PR: &webhooks.PRInfo{
						Number: pr.Number,
						Title:  pr.Title,
						URL:    pr.HTMLURL,
					},
					Workflow:   workflow,
					RunURL:     runURL,
					StuckSince: since,
					Timestamp:  now,
				}
			})
		}
	}

	// Conditions that recovered stop being tracked, which re-arms them.
	for key := range p.stuck {
		if _, ok := seen[key]; !ok {
			delete(p.stuck, key)
		}
	}

	return events
}

// branchStuckReason returns whether the branch is stuck and why.
func (p *Poller) branchStuckReason(rs *state.RepoState) (bool, string) {
	return runsStuckReason(rs.Runs)
}

// prStuckReason returns whether the PR is stuck and why.
func (p *Poller) prStuckReason(pr *state.PRState) (bool, string) {
	if pr.Mergeable == "dirty" || pr.Mergeable == "conflicting" {
		return true, "conflict"
	}
	return runsStuckReason(pr.Runs)
}

// runsStuckReason reports the first Run that is failing or still going, which
// is what "stuck" means once it has lasted past the threshold.
func runsStuckReason(runs []githubclient.WorkflowRun) (bool, string) {
	for _, run := range runs {
		switch aggregator.Of(run.Effective()) {
		case aggregator.StoplightRed:
			return true, "prolonged_failure"
		case aggregator.StoplightYellow:
			return true, "prolonged_in_progress"
		}
	}
	return false, ""
}
