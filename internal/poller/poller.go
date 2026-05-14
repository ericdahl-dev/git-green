package poller

import (
	"context"
	"sync"
	"time"

	"github.com/ericdahl-dev/git-green/internal/aggregator"
	"github.com/ericdahl-dev/git-green/internal/config"
	githubclient "github.com/ericdahl-dev/git-green/internal/github"
	"github.com/ericdahl-dev/git-green/internal/logx"
	"github.com/ericdahl-dev/git-green/internal/state"
	"github.com/ericdahl-dev/git-green/internal/webhooks"
)

// Fetcher is the interface the Poller uses to fetch runs — allows test substitution.
type Fetcher interface {
	FetchAll(ctx context.Context, q githubclient.RepoQuery) (githubclient.RepoData, error)
}

// ClientFactory creates a Fetcher for a given token.
type ClientFactory func(token string) Fetcher

// Poller orchestrates periodic fetches across all configured repos.
type Poller struct {
	cfg        *config.Config
	factory    ClientFactory
	dispatcher *webhooks.Dispatcher
	mu         sync.Mutex
	current    []state.RepoState
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
		cfg:        cfg,
		factory:    factory,
		dispatcher: webhooks.New(cfg.Webhooks),
		current:    repos,
	}
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
			results[i] = p.fetchRepo(ctx, repo, prev)
		}(i, repo)
	}

	wg.Wait()

	p.mu.Lock()
	p.current = results
	p.mu.Unlock()

	// Dispatch webhook events for any newly-stuck conditions.
	p.dispatchStuckEvents(previous, results)

	snap := state.New(results)
	select {
	case ch <- snap:
	default:
		// drop if consumer is slow; next tick will send a fresher snapshot
	}
}

func (p *Poller) fetchRepo(ctx context.Context, repo config.Repo, prev state.RepoState) state.RepoState {
	logx.Debug("fetch repo", "owner", repo.Owner, "name", repo.Name)
	token, err := p.cfg.TokenForOrg(repo.Owner)
	if err != nil {
		now := time.Now()
		return state.RepoState{
			Owner:     repo.Owner,
			Name:      repo.Name,
			Branch:    repo.Branch,
			Stoplight: prev.Stoplight,
			Runs:      prev.Runs,
			PRs:       prev.PRs,
			StaleAt:   &now,
			Err:       err,
		}
	}

	client := p.factory(token)
	// Use the previously-resolved branch so we avoid a Repositories.Get call every poll.
	branch := repo.Branch
	if branch == "" {
		branch = prev.Branch
	}
	q := githubclient.RepoQuery{
		Owner:     repo.Owner,
		Name:      repo.Name,
		Branch:    branch,
		Workflows: repo.Workflows,
	}

	data, err := client.FetchAll(ctx, q)
	if err != nil {
		now := time.Now()
		return state.RepoState{
			Owner:     repo.Owner,
			Name:      repo.Name,
			Branch:    repo.Branch,
			Stoplight: prev.Stoplight,
			Runs:      prev.Runs,
			PRs:       prev.PRs,
			StaleAt:   &now,
			Err:       err,
		}
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

		// Carry forward StuckSince from the previous state for this PR.
		var prevStuckSince *time.Time
		for _, prevPR := range prev.PRs {
			if prevPR.Number == pr.PR.Number {
				prevStuckSince = prevPR.StuckSince
				break
			}
		}

		prStates = append(prStates, state.PRState{
			Number:     pr.PR.Number,
			Title:      pr.PR.Title,
			HTMLURL:    pr.PR.HTMLURL,
			Stoplight:  aggregator.Aggregate(prStatuses),
			Runs:       pr.Runs,
			Mergeable:  pr.PR.Mergeable,
			StuckSince: prevStuckSince,
		})
	}

	return state.RepoState{
		Owner:      repo.Owner,
		Name:       repo.Name,
		Branch:     data.ResolvedBranch,
		Stoplight:  aggregator.Aggregate(statuses),
		Runs:       runs,
		PRs:        prStates,
		StaleAt:    nil,
		Err:        nil,
		StuckSince: prev.StuckSince,
	}
}

// dispatchStuckEvents compares previous and current repo states and fires
// webhook events for newly-stuck conditions. Updates StuckSince in-place on
// the current slice to track when each condition was first detected.
func (p *Poller) dispatchStuckEvents(previous, current []state.RepoState) {
	threshold := time.Duration(p.cfg.Settings.StuckThresholdMinutes) * time.Minute
	now := time.Now()

	for i := range current {
		cur := &current[i]
		var prev *state.RepoState
		for j := range previous {
			if previous[j].Owner == cur.Owner && previous[j].Name == cur.Name {
				prev = &previous[j]
				break
			}
		}

		// --- Branch stuck detection ---
		branchStuck, branchReason := p.branchStuckReason(cur)
		if branchStuck {
			if cur.StuckSince == nil {
				t := now
				cur.StuckSince = &t
			}
			// Only fire if this is newly stuck (previous had no StuckSince or was not stuck).
			prevStuck := prev != nil && prev.StuckSince != nil
			if !prevStuck && time.Since(*cur.StuckSince) >= threshold {
				runURL := ""
				workflow := ""
				if len(cur.Runs) > 0 {
					runURL = cur.Runs[0].HTMLURL
					workflow = cur.Runs[0].WorkflowName
				}
				p.dispatcher.Dispatch(webhooks.Event{
					Event:      "branch_stuck",
					Reason:     branchReason,
					Repo:       cur.FullName(),
					Workflow:   workflow,
					RunURL:     runURL,
					StuckSince: *cur.StuckSince,
					Timestamp:  now,
				})
			}
		} else {
			cur.StuckSince = nil
		}

		// --- PR stuck detection ---
		for j := range cur.PRs {
			pr := &cur.PRs[j]
			var prevPR *state.PRState
			if prev != nil {
				for k := range prev.PRs {
					if prev.PRs[k].Number == pr.Number {
						prevPR = &prev.PRs[k]
						break
					}
				}
			}

			prStuck, prReason := p.prStuckReason(pr)
			if prStuck {
				if pr.StuckSince == nil {
					t := now
					pr.StuckSince = &t
				}
				prevPRStuck := prevPR != nil && prevPR.StuckSince != nil
				if !prevPRStuck && time.Since(*pr.StuckSince) >= threshold {
					runURL := ""
					workflow := ""
					if len(pr.Runs) > 0 {
						runURL = pr.Runs[0].HTMLURL
						workflow = pr.Runs[0].WorkflowName
					}
					p.dispatcher.Dispatch(webhooks.Event{
						Event:  "pr_stuck",
						Reason: prReason,
						Repo:   cur.FullName(),
						PR: &webhooks.PRInfo{
							Number: pr.Number,
							Title:  pr.Title,
							URL:    pr.HTMLURL,
						},
						Workflow:   workflow,
						RunURL:     runURL,
						StuckSince: *pr.StuckSince,
						Timestamp:  now,
					})
				}
			} else {
				pr.StuckSince = nil
			}
		}
	}
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
