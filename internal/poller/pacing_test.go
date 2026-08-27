package poller

import (
	"context"
	"sync"
	"testing"
	"time"

	githubclient "github.com/ericdahl-dev/git-green/internal/github"
	"github.com/ericdahl-dev/git-green/internal/ratelimit"
	"github.com/ericdahl-dev/git-green/internal/state"
)

// budgetFetcher reports a fixed cost and budget, and counts how many times it
// was actually called.
type budgetFetcher struct {
	mu     sync.Mutex
	calls  int
	cost   int
	budget ratelimit.Budget
}

func (b *budgetFetcher) FetchAll(_ context.Context, _ githubclient.RepoQuery) (githubclient.RepoData, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.calls++
	return githubclient.RepoData{
		ResolvedBranch: "main",
		Calls:          b.cost,
		Budget:         b.budget,
	}, nil
}

func (b *budgetFetcher) count() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.calls
}

const twoRepoConfig = `
[settings]
poll_interval_seconds = 60

[[orgs]]
name = "acme"
token = "shared-token"

[[repos]]
owner = "acme"
name = "one"
enabled = true

[[repos]]
owner = "acme"
name = "two"
enabled = true
`

func pacingPoller(t *testing.T, f Fetcher, now time.Time) *Poller {
	t.Helper()
	p := New(writeConfig(t, twoRepoConfig), func(string) Fetcher { return f })
	p.now = func() time.Time { return now }
	return p
}

// A token with plenty of budget is never held back — the ticker alone decides
// when it polls.
func TestHealthyTokenIsNeverPaced(t *testing.T) {
	now := time.Now()
	f := &budgetFetcher{cost: 20, budget: ratelimit.Budget{
		Remaining: 4800, Limit: 5000, Reset: now.Add(50 * time.Minute),
	}}
	p := pacingPoller(t, f, now)
	ch := make(chan state.Snapshot, 4)

	p.fetch(context.Background(), ch)
	p.fetch(context.Background(), ch)

	if got := f.count(); got != 4 {
		t.Errorf("fetched %d times across 2 cycles of 2 repos, want 4", got)
	}
	if snap := p.Snapshot(); len(p.Throttles()) != 0 {
		t.Errorf("healthy token reported as throttled: %+v (%d repos)", p.Throttles(), len(snap.Repos))
	}
}

// A thin budget slows the token down, and every repo riding that token is
// skipped together — they share the allowance.
func TestThinBudgetSkipsTheWholeToken(t *testing.T) {
	now := time.Now()
	f := &budgetFetcher{cost: 150, budget: ratelimit.Budget{
		Remaining: 400, Limit: 5000, Reset: now.Add(30 * time.Minute),
	}}
	p := pacingPoller(t, f, now)
	ch := make(chan state.Snapshot, 4)

	p.fetch(context.Background(), ch)
	if got := f.count(); got != 2 {
		t.Fatalf("first cycle fetched %d repos, want 2", got)
	}

	throttles := p.Throttles()
	if len(throttles) != 1 {
		t.Fatalf("got %d throttle notices, want 1", len(throttles))
	}
	if got := throttles[0].Orgs; len(got) != 1 || got[0] != "acme" {
		t.Errorf("notice names %v, want [acme]", got)
	}
	if throttles[0].Interval <= time.Minute {
		t.Errorf("interval %s is not slower than the configured 60s", throttles[0].Interval)
	}

	// Still inside the stretched interval: both repos sit this one out and
	// keep their previous state rather than going stale.
	p.fetch(context.Background(), ch)
	if got := f.count(); got != 2 {
		t.Errorf("throttled token polled again after %d fetches, want it skipped", got)
	}
	for _, r := range p.Snapshot().Repos {
		if r.IsStale() {
			t.Errorf("%s went stale while merely throttled", r.FullName())
		}
	}
}

// Once the stretched interval elapses the token polls again.
func TestThrottledTokenResumesWhenDue(t *testing.T) {
	start := time.Now()
	f := &budgetFetcher{cost: 150, budget: ratelimit.Budget{
		Remaining: 400, Limit: 5000, Reset: start.Add(30 * time.Minute),
	}}
	p := pacingPoller(t, f, start)
	ch := make(chan state.Snapshot, 4)

	p.fetch(context.Background(), ch)
	interval := p.Throttles()[0].Interval

	p.now = func() time.Time { return start.Add(interval + time.Second) }
	p.fetch(context.Background(), ch)

	if got := f.count(); got != 4 {
		t.Errorf("fetched %d times, want 4 once the interval elapsed", got)
	}
}

// The notice rides on the snapshot so the title bar can render it.
func TestSnapshotCarriesThrottles(t *testing.T) {
	now := time.Now()
	f := &budgetFetcher{cost: 150, budget: ratelimit.Budget{
		Remaining: 400, Limit: 5000, Reset: now.Add(30 * time.Minute),
	}}
	p := pacingPoller(t, f, now)
	ch := make(chan state.Snapshot, 4)

	p.fetch(context.Background(), ch)

	select {
	case snap := <-ch:
		if len(snap.Throttles) != 1 {
			t.Errorf("snapshot carried %d throttles, want 1", len(snap.Throttles))
		}
	default:
		t.Fatal("no snapshot was sent")
	}
}
