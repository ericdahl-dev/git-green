package poller

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ericdahl-dev/git-green/internal/aggregator"
	"github.com/ericdahl-dev/git-green/internal/config"
	githubclient "github.com/ericdahl-dev/git-green/internal/github"
	"github.com/ericdahl-dev/git-green/internal/state"
	"github.com/ericdahl-dev/git-green/internal/webhooks"
)

// stubFetcher is a test double for the GitHub client.
type stubFetcher struct {
	runs   []githubclient.WorkflowRun
	prRuns []githubclient.PRRun
	err    error
}

func (s *stubFetcher) FetchAll(_ context.Context, _ githubclient.RepoQuery) (githubclient.RepoData, error) {
	return githubclient.RepoData{BranchRuns: s.runs, PRRuns: s.prRuns}, s.err
}

func stubFactory(runs []githubclient.WorkflowRun, err error) ClientFactory {
	return func(_ string) Fetcher {
		return &stubFetcher{runs: runs, err: err}
	}
}

func writeConfig(t *testing.T, content string) *config.Config {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	_ = os.WriteFile(path, []byte(content), 0600)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	return cfg
}

func TestFetchUpdatesStoplight(t *testing.T) {
	cfg := writeConfig(t, `
[[orgs]]
name = "ericdahl-dev"
token = "test-token"

[[repos]]
owner = "ericdahl-dev"
name = "git-green"
`)
	runs := []githubclient.WorkflowRun{
		{WorkflowName: "CI", Status: "completed", Conclusion: "success"},
	}
	p := New(cfg, stubFactory(runs, nil))
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	ch, stop := p.Start(ctx)
	defer stop()

	snap := <-ch
	if snap.Repos[0].Stoplight != aggregator.StoplightGreen {
		t.Errorf("expected green, got %v", snap.Repos[0].Stoplight)
	}
}

func TestFetchErrorRetainsLastKnownStatus(t *testing.T) {
	cfg := writeConfig(t, `
[[orgs]]
name = "ericdahl-dev"
token = "test-token"

[[repos]]
owner = "ericdahl-dev"
name = "git-green"
`)
	// First fetch succeeds with green.
	successRuns := []githubclient.WorkflowRun{
		{WorkflowName: "CI", Status: "completed", Conclusion: "success"},
	}

	calls := 0
	factory := func(_ string) Fetcher {
		calls++
		if calls == 1 {
			return &stubFetcher{runs: successRuns}
		}
		return &stubFetcher{err: errors.New("api down")}
	}

	p := New(cfg, factory)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Test via Start with very short interval.
	cfg.Settings.PollInterval = 1
	pollCh, stop := p.Start(ctx)
	defer stop()

	first := <-pollCh
	if first.Repos[0].Stoplight != aggregator.StoplightGreen {
		t.Fatalf("expected green on first fetch, got %v", first.Repos[0].Stoplight)
	}

	second := <-pollCh
	if second.Repos[0].Stoplight != aggregator.StoplightGreen {
		t.Errorf("expected retained green on error, got %v", second.Repos[0].Stoplight)
	}
	if !second.Repos[0].IsStale() {
		t.Error("expected staleness set on error")
	}
}

func TestFetchClearsStaleOnSuccess(t *testing.T) {
	cfg := writeConfig(t, `
[[orgs]]
name = "ericdahl-dev"
token = "test-token"

[[repos]]
owner = "ericdahl-dev"
name = "git-green"
`)
	runs := []githubclient.WorkflowRun{
		{WorkflowName: "CI", Status: "completed", Conclusion: "success"},
	}

	calls := 0
	factory := func(_ string) Fetcher {
		calls++
		if calls == 2 {
			return &stubFetcher{err: errors.New("transient")}
		}
		return &stubFetcher{runs: runs}
	}

	cfg.Settings.PollInterval = 1
	p := New(cfg, factory)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	pollCh, stop := p.Start(ctx)
	defer stop()

	<-pollCh          // first: success
	stale := <-pollCh // second: error → stale
	if !stale.Repos[0].IsStale() {
		t.Fatal("expected stale after error")
	}

	fresh := <-pollCh // third: success → cleared
	if fresh.Repos[0].IsStale() {
		t.Error("expected staleness cleared after successful fetch")
	}
}

func TestBranchStuckReasonFailure(t *testing.T) {
	cfg := &config.Config{}
	cfg.Settings.StuckThresholdMinutes = 30
	p := &Poller{cfg: cfg, dispatcher: webhooks.New(nil)}

	rs := &state.RepoState{
		Runs: []githubclient.WorkflowRun{
			{WorkflowName: "CI", Status: "completed", Conclusion: "failure"},
		},
	}
	stuck, reason := p.branchStuckReason(rs)
	if !stuck {
		t.Error("expected stuck=true for failure conclusion")
	}
	if reason != "prolonged_failure" {
		t.Errorf("expected prolonged_failure, got %q", reason)
	}
}

func TestBranchStuckReasonInProgress(t *testing.T) {
	cfg := &config.Config{}
	cfg.Settings.StuckThresholdMinutes = 30
	p := &Poller{cfg: cfg, dispatcher: webhooks.New(nil)}

	rs := &state.RepoState{
		Runs: []githubclient.WorkflowRun{
			{WorkflowName: "CI", Status: "in_progress"},
		},
	}
	stuck, reason := p.branchStuckReason(rs)
	if !stuck {
		t.Error("expected stuck=true for in_progress")
	}
	if reason != "prolonged_in_progress" {
		t.Errorf("expected prolonged_in_progress, got %q", reason)
	}
}

func TestBranchNotStuckWhenSuccess(t *testing.T) {
	cfg := &config.Config{}
	cfg.Settings.StuckThresholdMinutes = 30
	p := &Poller{cfg: cfg, dispatcher: webhooks.New(nil)}

	rs := &state.RepoState{
		Runs: []githubclient.WorkflowRun{
			{WorkflowName: "CI", Status: "completed", Conclusion: "success"},
		},
	}
	stuck, _ := p.branchStuckReason(rs)
	if stuck {
		t.Error("expected stuck=false for success")
	}
}

func TestPRStuckReasonConflict(t *testing.T) {
	cfg := &config.Config{}
	cfg.Settings.StuckThresholdMinutes = 30
	p := &Poller{cfg: cfg, dispatcher: webhooks.New(nil)}

	pr := &state.PRState{Mergeable: "dirty"}
	stuck, reason := p.prStuckReason(pr)
	if !stuck {
		t.Error("expected stuck=true for dirty mergeable")
	}
	if reason != "conflict" {
		t.Errorf("expected conflict, got %q", reason)
	}
}

func TestPRStuckReasonConflictingState(t *testing.T) {
	cfg := &config.Config{}
	cfg.Settings.StuckThresholdMinutes = 30
	p := &Poller{cfg: cfg, dispatcher: webhooks.New(nil)}

	pr := &state.PRState{Mergeable: "conflicting"}
	stuck, reason := p.prStuckReason(pr)
	if !stuck || reason != "conflict" {
		t.Errorf("expected stuck=true/conflict, got stuck=%v reason=%q", stuck, reason)
	}
}

func TestPRNotStuckWhenClean(t *testing.T) {
	cfg := &config.Config{}
	cfg.Settings.StuckThresholdMinutes = 30
	p := &Poller{cfg: cfg, dispatcher: webhooks.New(nil)}

	pr := &state.PRState{
		Mergeable: "clean",
		Runs:      []githubclient.WorkflowRun{{WorkflowName: "CI", Status: "completed", Conclusion: "success"}},
	}
	stuck, _ := p.prStuckReason(pr)
	if stuck {
		t.Error("expected stuck=false for clean/success")
	}
}

// stuckPoller builds a Poller with a controllable clock and a webhook endpoint,
// using a realistic 30-minute threshold rather than the zero threshold that a
// hand-built config can have but config.Load can never produce.
func stuckPoller(t *testing.T, received *[]webhooks.Event, now *time.Time) *Poller {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var evt webhooks.Event
		if err := json.NewDecoder(r.Body).Decode(&evt); err == nil {
			*received = append(*received, evt)
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	cfg := &config.Config{}
	cfg.Settings.StuckThresholdMinutes = 30
	cfg.Webhooks = []config.Webhook{{URL: srv.URL}}
	return &Poller{
		cfg:        cfg,
		dispatcher: webhooks.New(cfg.Webhooks),
		stuck:      make(map[string]*stuckEntry),
		now:        func() time.Time { return *now },
	}
}

// dispatch mirrors what fetch does: evaluate, then POST whatever came back.
func (p *Poller) dispatchNow(repos []state.RepoState) {
	for _, evt := range p.evaluateStuck(repos) {
		p.dispatcher.Dispatch(evt)
	}
}

func failingRepo() []state.RepoState {
	return []state.RepoState{{
		Owner: "o",
		Name:  "r",
		Runs:  []githubclient.WorkflowRun{{WorkflowName: "CI", Status: "completed", Conclusion: "failure"}},
	}}
}

func TestStuckDoesNotFireBeforeThreshold(t *testing.T) {
	var received []webhooks.Event
	now := time.Date(2026, 8, 26, 9, 0, 0, 0, time.UTC)
	p := stuckPoller(t, &received, &now)

	// Poll every 15s for 20 minutes. Nothing should fire before 30 minutes.
	for i := 0; i < 80; i++ {
		p.dispatchNow(failingRepo())
		now = now.Add(15 * time.Second)
	}

	if len(received) != 0 {
		t.Fatalf("expected no events before the threshold, got %d", len(received))
	}
}

func TestStuckFiresOnceAfterThreshold(t *testing.T) {
	var received []webhooks.Event
	now := time.Date(2026, 8, 26, 9, 0, 0, 0, time.UTC)
	p := stuckPoller(t, &received, &now)

	first := now
	// Two hours of 30s polls across the 30-minute threshold.
	for i := 0; i < 240; i++ {
		p.dispatchNow(failingRepo())
		now = now.Add(30 * time.Second)
	}

	if len(received) != 1 {
		t.Fatalf("expected exactly 1 event, got %d", len(received))
	}
	evt := received[0]
	if evt.Event != "branch_stuck" {
		t.Errorf("event = %q, want branch_stuck", evt.Event)
	}
	if evt.Reason != "prolonged_failure" {
		t.Errorf("reason = %q, want prolonged_failure", evt.Reason)
	}
	if evt.Repo != "o/r" {
		t.Errorf("repo = %q, want o/r", evt.Repo)
	}
	// StuckSince must be when the condition started, not when it alerted.
	if !evt.StuckSince.Equal(first) {
		t.Errorf("stuck_since = %v, want %v", evt.StuckSince, first)
	}
	if evt.Timestamp.Sub(evt.StuckSince) < 30*time.Minute {
		t.Errorf("fired after %v, want at least the 30m threshold", evt.Timestamp.Sub(evt.StuckSince))
	}
}

func TestStuckReArmsAfterRecovery(t *testing.T) {
	var received []webhooks.Event
	now := time.Date(2026, 8, 26, 9, 0, 0, 0, time.UTC)
	p := stuckPoller(t, &received, &now)

	green := []state.RepoState{{
		Owner: "o",
		Name:  "r",
		Runs:  []githubclient.WorkflowRun{{WorkflowName: "CI", Status: "completed", Conclusion: "success"}},
	}}

	// First incident: stuck long enough to alert.
	for i := 0; i < 5; i++ {
		p.dispatchNow(failingRepo())
		now = now.Add(10 * time.Minute)
	}
	if len(received) != 1 {
		t.Fatalf("first incident: expected 1 event, got %d", len(received))
	}

	// Recovers — the entry should be pruned.
	p.dispatchNow(green)
	if len(p.stuck) != 0 {
		t.Fatalf("expected recovery to prune tracking, got %d entries", len(p.stuck))
	}
	now = now.Add(10 * time.Minute)

	// Second incident: must alert again rather than staying silent.
	for i := 0; i < 5; i++ {
		p.dispatchNow(failingRepo())
		now = now.Add(10 * time.Minute)
	}
	if len(received) != 2 {
		t.Fatalf("second incident: expected 2 events total, got %d", len(received))
	}
}

func TestStuckReasonChangeRestartsTheClock(t *testing.T) {
	var received []webhooks.Event
	now := time.Date(2026, 8, 26, 9, 0, 0, 0, time.UTC)
	p := stuckPoller(t, &received, &now)

	inProgress := []state.RepoState{{
		Owner: "o",
		Name:  "r",
		Runs:  []githubclient.WorkflowRun{{WorkflowName: "CI", Status: "in_progress"}},
	}}

	// 20 minutes in progress — under the threshold, nothing fires.
	for i := 0; i < 2; i++ {
		p.dispatchNow(inProgress)
		now = now.Add(10 * time.Minute)
	}
	if len(received) != 0 {
		t.Fatalf("expected nothing yet, got %d", len(received))
	}

	// It turns into a failure: a new condition, so the clock restarts and the
	// 20 minutes already elapsed must not count toward the threshold.
	p.dispatchNow(failingRepo())
	if len(received) != 0 {
		t.Fatalf("reason change should restart the clock, got %d events", len(received))
	}
	now = now.Add(31 * time.Minute)
	p.dispatchNow(failingRepo())
	if len(received) != 1 {
		t.Fatalf("expected 1 event after the new condition aged out, got %d", len(received))
	}
	if received[0].Reason != "prolonged_failure" {
		t.Errorf("reason = %q, want prolonged_failure", received[0].Reason)
	}
}

func TestStuckPRFiresWithPRInfo(t *testing.T) {
	var received []webhooks.Event
	now := time.Date(2026, 8, 26, 9, 0, 0, 0, time.UTC)
	p := stuckPoller(t, &received, &now)

	repos := []state.RepoState{{
		Owner: "o",
		Name:  "r",
		PRs: []state.PRState{{
			Number:    42,
			Title:     "Add a thing",
			HTMLURL:   "https://github.com/o/r/pull/42",
			Mergeable: "dirty",
		}},
	}}

	for i := 0; i < 5; i++ {
		p.dispatchNow(repos)
		now = now.Add(10 * time.Minute)
	}

	if len(received) != 1 {
		t.Fatalf("expected 1 event, got %d", len(received))
	}
	evt := received[0]
	if evt.Event != "pr_stuck" || evt.Reason != "conflict" {
		t.Errorf("got %q/%q, want pr_stuck/conflict", evt.Event, evt.Reason)
	}
	if evt.PR == nil || evt.PR.Number != 42 {
		t.Fatalf("expected PR 42 in the event, got %+v", evt.PR)
	}
}
