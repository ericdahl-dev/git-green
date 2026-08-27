package poller

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/go-github/v72/github"

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
	// paced tracks each token's REST budget and when it may next be polled.
	// Keyed by token rather than by org because orgs sharing a token share a
	// budget, and pacing them independently would spend it twice over.
	paced map[string]*tokenPace
	// rateLimitedUntil records, per org, when GitHub said the quota resets.
	// Polling that org is skipped until then rather than spending every cycle
	// collecting 403s.
	rateLimitedUntil map[string]time.Time
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
	return &Poller{
		cfg:              cfg,
		factory:          factory,
		dispatcher:       webhooks.New(cfg.Webhooks),
		current:          repos,
		stuck:            make(map[string]*stuckEntry),
		paced:            make(map[string]*tokenPace),
		expanded:         make(map[string]bool),
		rateLimitedUntil: make(map[string]time.Time),
		now:              time.Now,
	}
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

// rateLimitPause reports how long an org is still rate-limited for, and clears
// the entry once it has elapsed.
func (p *Poller) rateLimitPause(org string) (time.Duration, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	until, ok := p.rateLimitedUntil[org]
	if !ok {
		return 0, false
	}
	if remaining := until.Sub(p.now()); remaining > 0 {
		return remaining, true
	}
	delete(p.rateLimitedUntil, org)
	return 0, false
}

// noteRateLimit records a reset time reported by GitHub so subsequent polls
// for that org back off instead of burning cycles on 403s.
func (p *Poller) noteRateLimit(org string, err error) {
	var rlErr *github.RateLimitError
	var abuseErr *github.AbuseRateLimitError
	var until time.Time
	switch {
	case errors.As(err, &rlErr):
		until = rlErr.Rate.Reset.Time
	case errors.As(err, &abuseErr):
		if abuseErr.RetryAfter != nil {
			until = p.now().Add(*abuseErr.RetryAfter)
		}
	default:
		return
	}
	if until.IsZero() {
		return
	}
	p.mu.Lock()
	p.rateLimitedUntil[org] = until
	p.mu.Unlock()
	logx.Debug("rate limited", "org", org, "until", until)
}

// tokenPace is one token's budget and the pace it buys.
type tokenPace struct {
	budget   ratelimit.Budget
	cost     int // REST calls one full cycle costs this token
	interval time.Duration
	nextDue  time.Time
	orgs     map[string]bool
}

// fetchCost is what one repo's fetch spent, reported back so the cycle can be
// priced per token.
type fetchCost struct {
	token   string
	calls   int
	budget  ratelimit.Budget
	spent   bool // false when the repo was skipped or never reached the API
	skipped bool
}

// due reports whether a token may be polled this cycle. An unpaced token — one
// that has never reported a budget, or is comfortably inside it — is always due.
func (p *Poller) due(token string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	pace, ok := p.paced[token]
	if !ok || pace.nextDue.IsZero() {
		return true
	}
	return !p.now().Before(pace.nextDue)
}

// repriceTokens folds a cycle's spending into each token's pace. A token that
// was skipped this cycle keeps the pace it already had.
func (p *Poller) repriceTokens(costs []fetchCost) {
	configured := time.Duration(p.cfg.Settings.PollInterval) * time.Second
	now := p.now()

	p.mu.Lock()
	defer p.mu.Unlock()
	for _, c := range costs {
		if !c.spent || c.token == "" {
			continue
		}
		pace, ok := p.paced[c.token]
		if !ok {
			pace = &tokenPace{orgs: make(map[string]bool)}
			p.paced[c.token] = pace
		}
		pace.cost += c.calls
		// Repos race, so take the leanest reading of the cycle: it is the one
		// closest to what is actually left.
		if c.budget.Known() && (!pace.budget.Known() || c.budget.Remaining < pace.budget.Remaining) {
			pace.budget = c.budget
		}
	}

	for token, pace := range p.paced {
		if !anySpent(costs, token) {
			continue
		}
		interval, throttled := ratelimit.Interval(configured, pace.budget, pace.cost, now)
		pace.interval = interval
		if throttled {
			pace.nextDue = now.Add(interval)
			logx.Debug("throttling token", "orgs", orgList(pace.orgs),
				"remaining", pace.budget.Remaining, "cost", pace.cost, "interval", interval)
		} else {
			// Healthy tokens are never held back: the ticker alone decides
			// when they poll, exactly as before pacing existed.
			pace.nextDue = time.Time{}
		}
		// The cost is re-measured every cycle: expanding a repo changes it.
		pace.cost = 0
	}
}

func anySpent(costs []fetchCost, token string) bool {
	for _, c := range costs {
		if c.spent && c.token == token {
			return true
		}
	}
	return false
}

func orgList(orgs map[string]bool) []string {
	out := make([]string, 0, len(orgs))
	for o := range orgs {
		out = append(out, o)
	}
	sort.Strings(out)
	return out
}

// Throttles reports every token currently polling slower than configured, for
// the title bar. Healthy tokens are omitted — there is nothing to say.
func (p *Poller) Throttles() []state.Throttle {
	configured := time.Duration(p.cfg.Settings.PollInterval) * time.Second
	p.mu.Lock()
	defer p.mu.Unlock()

	var out []state.Throttle
	for _, pace := range p.paced {
		if pace.interval <= configured || !pace.budget.Known() {
			continue
		}
		out = append(out, state.Throttle{
			Orgs:      orgList(pace.orgs),
			Remaining: pace.budget.Remaining,
			Limit:     pace.budget.Limit,
			Reset:     pace.budget.Reset,
			Interval:  pace.interval,
		})
	}
	sort.Slice(out, func(a, b int) bool {
		return strings.Join(out[a].Orgs, ",") < strings.Join(out[b].Orgs, ",")
	})
	return out
}

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

	costs := make([]fetchCost, len(enabled))

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
			if token, err := p.cfg.TokenForOrg(repo.Owner); err == nil && !p.due(token) {
				results[i] = prev
				costs[i] = fetchCost{token: token, skipped: true}
				return
			}
			results[i], costs[i] = p.fetchRepo(ctx, repo, prev)
		}(i, repo)
	}

	wg.Wait()
	p.repriceTokens(costs)

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

func (p *Poller) fetchRepo(ctx context.Context, repo config.Repo, prev state.RepoState) (state.RepoState, fetchCost) {
	logx.Debug("fetch repo", "owner", repo.Owner, "name", repo.Name)
	// Keep whatever branch was already resolved. Falling back to the config
	// value (often empty) would throw the resolution away on every error and
	// force another Repositories.Get next cycle — most expensive exactly when
	// the API is already refusing us.
	resolved := repo.Branch
	if resolved == "" {
		resolved = prev.Branch
	}

	var cost fetchCost
	stale := func(err error) (state.RepoState, fetchCost) {
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
		}, cost
	}

	if pause, limited := p.rateLimitPause(repo.Owner); limited {
		return stale(fmt.Errorf("rate limited for %s; retrying in %s", repo.Owner, pause.Round(time.Second)))
	}

	token, err := p.cfg.TokenForOrg(repo.Owner)
	if err != nil {
		return stale(err)
	}
	cost.token = token
	p.noteTokenOrg(token, repo.Owner)

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
	cost.calls, cost.budget, cost.spent = data.Calls, data.Budget, true
	if err != nil {
		p.noteRateLimit(repo.Owner, err)
		return stale(err)
	}

	runs := data.BranchRuns
	prRuns := data.PRRuns

	// Aggregate stoplight from default-branch runs.
	statuses := make([]aggregator.RunStatus, 0, len(runs))
	for _, r := range runs {
		s := r.Conclusion
		if s == "" {
			s = r.Status
		}
		statuses = append(statuses, aggregator.RunStatus(s))
	}

	// Build PRStates.
	prStates := make([]state.PRState, 0, len(prRuns))
	for _, pr := range prRuns {
		prStatuses := make([]aggregator.RunStatus, 0, len(pr.Runs))
		for _, r := range pr.Runs {
			s := r.Conclusion
			if s == "" {
				s = r.Status
			}
			prStatuses = append(prStatuses, aggregator.RunStatus(s))
		}

		prStates = append(prStates, state.PRState{
			Number:    pr.PR.Number,
			Title:     pr.PR.Title,
			HTMLURL:   pr.PR.HTMLURL,
			Stoplight: aggregator.Aggregate(prStatuses),
			Runs:      pr.Runs,
			Mergeable: pr.PR.Mergeable,
			Stack:     pr.PR.Stack,
		})
	}

	return state.RepoState{
		Owner:     repo.Owner,
		Name:      repo.Name,
		Branch:    data.ResolvedBranch,
		Stoplight: aggregator.Aggregate(statuses),
		Runs:      runs,
		PRs:       prStates,
		StaleAt:   nil,
		Err:       nil,
	}, cost
}

// noteTokenOrg remembers which orgs ride on a token, so a throttle notice can
// name them.
func (p *Poller) noteTokenOrg(token, org string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	pace, ok := p.paced[token]
	if !ok {
		pace = &tokenPace{orgs: make(map[string]bool)}
		p.paced[token] = pace
	}
	pace.orgs[org] = true
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
	for _, run := range rs.Runs {
		if run.Conclusion == "failure" || run.Conclusion == "timed_out" {
			return true, "prolonged_failure"
		}
		if run.Status == "in_progress" {
			return true, "prolonged_in_progress"
		}
	}
	return false, ""
}

// prStuckReason returns whether the PR is stuck and why.
func (p *Poller) prStuckReason(pr *state.PRState) (bool, string) {
	if pr.Mergeable == "dirty" || pr.Mergeable == "conflicting" {
		return true, "conflict"
	}
	for _, run := range pr.Runs {
		if run.Conclusion == "failure" || run.Conclusion == "timed_out" {
			return true, "prolonged_failure"
		}
		if run.Status == "in_progress" {
			return true, "prolonged_in_progress"
		}
	}
	return false, ""
}
