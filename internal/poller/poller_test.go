package poller

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/google/go-github/v72/github"

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

	// The Poller copies its Config, so set the interval before handing it over.
	cfg.Settings.PollInterval = 1
	p := New(cfg, factory)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

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

// recordingFetcher captures the queries it was asked to run, so a test can
// assert what detail the poller requested.
type recordingFetcher struct {
	mu      sync.Mutex
	queries []githubclient.RepoQuery
	err     error
}

func (f *recordingFetcher) FetchAll(_ context.Context, q githubclient.RepoQuery) (githubclient.RepoData, error) {
	f.mu.Lock()
	f.queries = append(f.queries, q)
	f.mu.Unlock()
	if f.err != nil {
		return githubclient.RepoData{}, f.err
	}
	return githubclient.RepoData{ResolvedBranch: "main"}, nil
}

func (f *recordingFetcher) last(t *testing.T) githubclient.RepoQuery {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.queries) == 0 {
		t.Fatal("the fetcher was never called")
	}
	return f.queries[len(f.queries)-1]
}

// The org needs an explicit token: without one the poller falls back to
// `gh auth token`, so the test would pass on a developer's authenticated
// machine and fail in CI, where there is no gh login.
func recordingPoller(t *testing.T, f *recordingFetcher) *Poller {
	t.Helper()
	cfg := writeConfig(t, `
[[orgs]]
name = "o"
token = "test-token"

[[repos]]
owner = "o"
name = "r"
`)
	return New(cfg, func(string) Fetcher { return f })
}

func TestCollapsedRepoDoesNotRequestDetail(t *testing.T) {
	f := &recordingFetcher{}
	p := recordingPoller(t, f)

	p.fetch(context.Background())

	if got := f.last(t); got.Detail {
		t.Error("collapsed repo requested job and PR-run detail")
	}
}

func TestExpandedRepoRequestsDetail(t *testing.T) {
	f := &recordingFetcher{}
	p := recordingPoller(t, f)
	p.SetExpandedRepos([]string{"o/r"})

	p.fetch(context.Background())

	if got := f.last(t); !got.Detail {
		t.Error("expanded repo did not request detail")
	}

	// Collapsing it again drops back to the cheap query.
	p.SetExpandedRepos(nil)
	p.fetch(context.Background())
	if got := f.last(t); got.Detail {
		t.Error("collapsing did not stop detail fetching")
	}
}

func TestResolvedBranchSurvivesAnError(t *testing.T) {
	f := &recordingFetcher{}
	p := recordingPoller(t, f)

	p.fetch(context.Background()) // resolves "main"

	// Now every fetch fails. The resolved branch must be carried forward, so
	// the next query still names it rather than asking GitHub to resolve the
	// default branch again.
	f.err = errors.New("boom")
	p.fetch(context.Background())

	if got := f.last(t).Branch; got != "main" {
		t.Errorf("branch after error = %q, want main to be preserved", got)
	}
	if p.Snapshot().Repos[0].Branch != "main" {
		t.Errorf("state lost the resolved branch: %q", p.Snapshot().Repos[0].Branch)
	}
}

func TestRateLimitBacksOffUntilReset(t *testing.T) {
	f := &recordingFetcher{}
	p := recordingPoller(t, f)

	now := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	p.now = func() time.Time { return now }

	reset := now.Add(30 * time.Minute)
	f.err = &github.RateLimitError{Rate: github.Rate{Reset: github.Timestamp{Time: reset}}}

	p.fetch(context.Background())
	after := len(f.queries)
	if after != 1 {
		t.Fatalf("expected the first poll to reach the API, got %d calls", after)
	}

	// While rate limited, polling must not touch the API at all.
	for i := 0; i < 5; i++ {
		now = now.Add(time.Minute)
		p.fetch(context.Background())
	}
	if len(f.queries) != after {
		t.Errorf("made %d calls while rate limited, want none after the first", len(f.queries)-after)
	}
	if err := p.Snapshot().Repos[0].Err; err == nil {
		t.Error("expected the rate limit to surface as an error on the row")
	}

	// Past the reset it resumes.
	now = reset.Add(time.Second)
	f.err = nil
	p.fetch(context.Background())
	if len(f.queries) != after+1 {
		t.Error("did not resume polling after the reset time")
	}
}

const loopConfig = `
[settings]
poll_interval_seconds = 3600

[[orgs]]
name = "acme"
token = "t"

[[repos]]
owner = "acme"
name = "one"
`

func recv(t *testing.T, ch <-chan state.Snapshot) state.Snapshot {
	t.Helper()
	select {
	case snap := <-ch:
		return snap
	case <-time.After(2 * time.Second):
		t.Fatal("no snapshot arrived")
		return state.Snapshot{}
	}
}

// Refresh runs a cycle now rather than waiting out the hour-long tick.
func TestRefreshRunsACycle(t *testing.T) {
	p := New(writeConfig(t, loopConfig), stubFactory(nil, nil))
	ch, stop := p.Start(context.Background())
	defer stop()
	recv(t, ch)

	p.Refresh()
	recv(t, ch)
}

// Reload takes effect on the next cycle and is isolated from later edits to
// the Config it was handed, as the Repo manager edits in place.
func TestReloadAppliesACopyOfTheConfig(t *testing.T) {
	cfg := writeConfig(t, loopConfig)
	p := New(cfg, stubFactory(nil, nil))
	ch, stop := p.Start(context.Background())
	defer stop()
	recv(t, ch)

	if err := cfg.AddRepo(config.Repo{Owner: "acme", Name: "two"}); err != nil {
		t.Fatal(err)
	}
	if got := len(p.Snapshot().Repos); got != 1 {
		t.Fatalf("an edit before Reload reached the Poller: %d repos", got)
	}

	p.Reload(cfg)
	if got := len(recv(t, ch).Repos); got != 2 {
		t.Fatalf("after Reload got %d repos, want 2", got)
	}

	_ = cfg.RemoveRepo(1)
	p.Refresh()
	if got := len(recv(t, ch).Repos); got != 2 {
		t.Errorf("an edit after Reload reached the Poller: %d repos", got)
	}
}

// Stopping closes the channel once, with no cycle left to send on it.
func TestStopClosesTheChannel(t *testing.T) {
	p := New(writeConfig(t, loopConfig), stubFactory(nil, nil))
	ch, stop := p.Start(context.Background())
	recv(t, ch)
	p.Refresh()
	stop()
	for range ch {
	}
}

// A stuck branch reaches the webhook, and a slow endpoint does not hold the
// cycle up: delivery happens in the background.
func TestStuckAlertIsDeliveredWithoutBlockingTheCycle(t *testing.T) {
	received := make(chan webhooks.Event, 1)
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var evt webhooks.Event
		_ = json.NewDecoder(r.Body).Decode(&evt)
		received <- evt
		<-release // hang until the test is done
	}))
	defer srv.Close()
	defer close(release)

	cfg := writeConfig(t, loopConfig+`
[[webhooks]]
url = "`+srv.URL+`"
`)
	failing := []githubclient.WorkflowRun{{WorkflowName: "CI", Status: "completed", Conclusion: "failure"}}
	p := New(cfg, stubFactory(failing, nil))
	start := time.Date(2026, 9, 28, 9, 0, 0, 0, time.UTC)
	now := start
	p.now = func() time.Time { return now }

	p.fetch(context.Background())
	now = start.Add(time.Duration(cfg.Settings.StuckThresholdMinutes)*time.Minute + time.Second)

	returned := make(chan struct{})
	go func() { p.fetch(context.Background()); close(returned) }()
	select {
	case <-returned:
	case <-time.After(2 * time.Second):
		t.Fatal("the cycle waited on the webhook endpoint")
	}

	select {
	case evt := <-received:
		if evt.Event != "branch_stuck" || evt.Repo != "acme/one" {
			t.Errorf("got %+v", evt)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no webhook arrived")
	}
}
