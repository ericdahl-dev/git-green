package poller

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ericdahl-dev/git-green/internal/aggregator"
	"github.com/ericdahl-dev/git-green/internal/config"
	githubclient "github.com/ericdahl-dev/git-green/internal/github"
)

// stubFetcher is a test double for the GitHub client.
type stubFetcher struct {
	runs []githubclient.WorkflowRun
	err  error
}

func (s *stubFetcher) FetchRuns(_ context.Context, _ githubclient.RepoQuery) ([]githubclient.WorkflowRun, error) {
	return s.runs, s.err
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
	os.WriteFile(path, []byte(content), 0600)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	return cfg
}

func TestFetchUpdatesStoplight(t *testing.T) {
	cfg := writeConfig(t, `
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

	// Manually invoke two fetches.
	ch := make(chan interface{ GetRepos() interface{} }, 2)
	_ = ch

	// Use forceRefresh path via direct fetch calls.
	snapCh := make(chan interface{}, 2)
	_ = snapCh

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
