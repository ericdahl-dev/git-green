// Package alerts decides when a Repo's branch or one of its PRs has been stuck
// long enough to alert on. It sees only Snapshots and the time, so it knows
// nothing about how they were fetched or how an alert is delivered.
package alerts

import (
	"fmt"
	"time"

	"github.com/ericdahl-dev/git-green/internal/aggregator"
	githubclient "github.com/ericdahl-dev/git-green/internal/github"
	"github.com/ericdahl-dev/git-green/internal/state"
	"github.com/ericdahl-dev/git-green/internal/webhooks"
)

// Watcher tracks stuck conditions across poll cycles. A condition alerts
// exactly once, on the first cycle where it has been bad for at least the
// threshold; recovering forgets it, so the next incident re-arms.
//
// A Watcher is not safe for concurrent use; the Poller calls it once per
// cycle from its fetch loop.
type Watcher struct {
	threshold time.Duration
	stuck     map[string]*entry // keyed by Repo + scope (branch, or PR number)
}

// entry records when a condition was first seen and whether it has alerted.
type entry struct {
	since   time.Time
	reason  string
	alerted bool
}

// NewWatcher returns a Watcher that alerts once a condition has lasted
// threshold.
func NewWatcher(threshold time.Duration) *Watcher {
	return &Watcher{threshold: threshold, stuck: make(map[string]*entry)}
}

// SetThreshold changes the threshold for conditions already being tracked as
// well as new ones, as a config reload would.
func (w *Watcher) SetThreshold(threshold time.Duration) { w.threshold = threshold }

// Observe folds one poll cycle's Repos into the stuck bookkeeping and returns
// the alerts that fire this cycle.
func (w *Watcher) Observe(repos []state.RepoState, now time.Time) []webhooks.Event {
	seen := make(map[string]bool, len(w.stuck))
	var events []webhooks.Event

	// track records one stuck condition and appends an alert if this is the
	// cycle that crosses the threshold.
	track := func(key, reason string, build func(since time.Time) webhooks.Event) {
		seen[key] = true
		e, ok := w.stuck[key]
		// A changed reason (an in-progress run turning into a failure) is a new
		// condition, so restart the clock and allow a fresh alert.
		if !ok || e.reason != reason {
			e = &entry{since: now, reason: reason}
			w.stuck[key] = e
		}
		if e.alerted || now.Sub(e.since) < w.threshold {
			return
		}
		e.alerted = true
		events = append(events, build(e.since))
	}

	for _, r := range repos {
		if stuck, reason := runsStuckReason(r.Runs); stuck {
			track(key(r, "branch"), reason, func(since time.Time) webhooks.Event {
				return event("branch_stuck", reason, r, nil, r.Runs, since, now)
			})
		}

		for _, pr := range r.PRs {
			k := key(r, fmt.Sprintf("pr-%d", pr.Number))
			stuck, reason := prStuckReason(pr)
			if !stuck && len(pr.Runs) == 0 {
				// A collapsed Repo fetches no PR Runs. That says nothing about
				// the PR, so keep any clock already running rather than reading
				// it as a recovery.
				seen[k] = true
				continue
			}
			if !stuck {
				continue
			}
			info := &webhooks.PRInfo{Number: pr.Number, Title: pr.Title, URL: pr.HTMLURL}
			track(k, reason, func(since time.Time) webhooks.Event {
				return event("pr_stuck", reason, r, info, pr.Runs, since, now)
			})
		}
	}

	// Conditions that recovered stop being tracked, which re-arms them.
	for k := range w.stuck {
		if !seen[k] {
			delete(w.stuck, k)
		}
	}
	return events
}

func key(r state.RepoState, scope string) string { return r.FullName() + "#" + scope }

func event(kind, reason string, r state.RepoState, pr *webhooks.PRInfo, runs []githubclient.WorkflowRun, since, now time.Time) webhooks.Event {
	evt := webhooks.Event{
		Event:      kind,
		Reason:     reason,
		Repo:       r.FullName(),
		PR:         pr,
		StuckSince: since,
		Timestamp:  now,
	}
	if len(runs) > 0 {
		evt.RunURL = runs[0].HTMLURL
		evt.Workflow = runs[0].WorkflowName
	}
	return evt
}

// prStuckReason reports whether a PR is stuck and why: a merge conflict, or a
// Run that is failing or still going.
func prStuckReason(pr state.PRState) (bool, string) {
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
