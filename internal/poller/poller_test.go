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

	<-pollCh // first: success
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

func TestDispatchStuckEventsFiredOnce(t *testing.T) {
	var received []webhooks.Event
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var evt webhooks.Event
		if err := json.NewDecoder(r.Body).Decode(&evt); err == nil {
			received = append(received, evt)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	cfg := &config.Config{}
	cfg.Settings.StuckThresholdMinutes = 0 // fire immediately
	cfg.Webhooks = []config.Webhook{{URL: srv.URL}}
	p := &Poller{cfg: cfg, dispatcher: webhooks.New(cfg.Webhooks)}

	// Simulate two consecutive polls both showing a failing branch.
	// First poll: no previous state → fire event.
	previous := []state.RepoState{
		{Owner: "o", Name: "r"},
	}
	current := []state.RepoState{
		{
			Owner: "o",
			Name:  "r",
			Runs:  []githubclient.WorkflowRun{{WorkflowName: "CI", Status: "completed", Conclusion: "failure"}},
		},
	}
	p.dispatchStuckEvents(previous, current)

	// Second poll: previous has StuckSince set → should NOT fire again.
	previous2 := current // StuckSince now set
	current2 := []state.RepoState{
		{
			Owner: "o",
			Name:  "r",
			Runs:  []githubclient.WorkflowRun{{WorkflowName: "CI", Status: "completed", Conclusion: "failure"}},
			StuckSince: current[0].StuckSince,
		},
	}
	p.dispatchStuckEvents(previous2, current2)

	if len(received) != 1 {
		t.Errorf("expected exactly 1 webhook event, got %d", len(received))
	}
	if len(received) > 0 && received[0].Event != "branch_stuck" {
		t.Errorf("expected branch_stuck, got %q", received[0].Event)
	}
}
