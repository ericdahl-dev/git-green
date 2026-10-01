package githubclient

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/google/go-github/v72/github"
)

func run(name, event string, workflowID int64) *github.WorkflowRun {
	return &github.WorkflowRun{
		Name:       github.Ptr(name),
		Event:      github.Ptr(event),
		WorkflowID: github.Ptr(workflowID),
	}
}

func TestKeepRunDropsDependabotUpdates(t *testing.T) {
	r := run("npm_and_yarn in /. for @hotwired/turbo, axios", "dynamic", 0)
	if keepRun(r, nil) {
		t.Error("expected Dependabot update run to be dropped")
	}
}

func dynamicRun(name, path string) *github.WorkflowRun {
	r := run(name, "dynamic", 7)
	r.Path = github.Ptr(path)
	return r
}

// GitHub's built-in workflows all run with event "dynamic". CodeQL is real CI
// signal and is often a repo's only workflow; Dependabot's update runs and
// Copilot's PR reviews are not.
func TestKeepRunKeepsCodeQLButNotOtherBuiltIns(t *testing.T) {
	cases := []struct {
		run  *github.WorkflowRun
		keep bool
	}{
		{dynamicRun("Push on main", "dynamic/github-code-scanning/codeql"), true},
		{dynamicRun("PR #121", "dynamic/github-code-scanning/codeql"), true},
		{dynamicRun("npm_and_yarn in /. for axios", "dynamic/dependabot/dependabot-updates"), false},
		{dynamicRun("Copilot code review", "dynamic/copilot-pull-request-reviewer/copilot-pull-request-reviewer"), false},
	}
	for _, tc := range cases {
		if got := keepRun(tc.run, nil); got != tc.keep {
			t.Errorf("%s (%s): keep = %v, want %v", tc.run.GetName(), tc.run.GetPath(), got, tc.keep)
		}
	}
}

// CodeQL names each run after its trigger, so the Workflow row needs a stable
// name, and the workflows filter matches on it.
func TestCodeQLRunsAreNamedCodeQL(t *testing.T) {
	r := dynamicRun("Scheduled", "dynamic/github-code-scanning/codeql")
	if got := workflowName(r); got != "CodeQL" {
		t.Errorf("workflowName = %q, want CodeQL", got)
	}
	if got := workflowName(run("CI", "push", 1)); got != "CI" {
		t.Errorf("workflowName = %q, want CI", got)
	}
	if !keepRun(r, newFilterSet([]string{"CodeQL"})) {
		t.Error("a workflows filter naming CodeQL must keep its runs")
	}
}

func TestKeepRunHonoursWorkflowFilter(t *testing.T) {
	filter := newFilterSet([]string{"CI"})
	if !keepRun(run("CI", "push", 1), filter) {
		t.Error("expected CI to be kept")
	}
	if keepRun(run("Lint", "push", 2), filter) {
		t.Error("expected Lint to be filtered out")
	}
	if !keepRun(run("Lint", "push", 2), newFilterSet(nil)) {
		t.Error("expected empty filter to keep everything")
	}
}

func TestRunKeyIsStableAcrossRenamedRuns(t *testing.T) {
	a := run("CI", "push", 42)
	b := run("CI · rerun", "push", 42)
	if runKey(a) != runKey(b) {
		t.Errorf("expected same workflow ID to share a key, got %q and %q", runKey(a), runKey(b))
	}
	if runKey(run("CI", "push", 42)) == runKey(run("CI", "push", 43)) {
		t.Error("expected different workflow IDs to have different keys")
	}
}

func TestRunKeyFallsBackToName(t *testing.T) {
	if got, want := runKey(run("CI", "push", 0)), "name:CI"; got != want {
		t.Errorf("runKey = %q, want %q", got, want)
	}
}

func apiError(status int, message string) error {
	return &github.ErrorResponse{
		Response: &http.Response{StatusCode: status},
		Message:  message,
	}
}

func TestIsNoFailedJobs(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{name: "nil", err: nil},
		{name: "plain error", err: errors.New("no failed jobs")},
		{name: "forbidden", err: apiError(http.StatusForbidden, "Resource not accessible by personal access token"), want: false},
		{name: "no failed jobs", err: apiError(http.StatusForbidden, "No failed jobs to rerun."), want: true},
		{name: "wrapped", err: fmt.Errorf("re-running: %w", apiError(http.StatusForbidden, "No failed jobs to rerun.")), want: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isNoFailedJobs(tc.err); got != tc.want {
				t.Errorf("isNoFailedJobs = %v, want %v", got, tc.want)
			}
		})
	}
}

// countingServer stands in for api.github.com and records every path hit, so a
// test can assert how many calls a single poll actually costs.
func countingServer(t *testing.T, paths *[]string) *Client {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		*paths = append(*paths, r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/actions/runs"):
			// Two runs of two different workflows on the branch.
			_, _ = fmt.Fprint(w, `{"total_count":2,"workflow_runs":[
				{"id":1,"name":"CI","workflow_id":11,"status":"completed","conclusion":"success","event":"push"},
				{"id":2,"name":"Lint","workflow_id":22,"status":"completed","conclusion":"success","event":"push"}]}`)
		case strings.HasSuffix(r.URL.Path, "/pulls"):
			// Three open PRs.
			_, _ = fmt.Fprint(w, `[{"number":1,"title":"a","head":{"sha":"s1"}},
				{"number":2,"title":"b","head":{"sha":"s2"}},
				{"number":3,"title":"c","head":{"sha":"s3"}}]`)
		case strings.Contains(r.URL.Path, "/jobs"):
			_, _ = fmt.Fprint(w, `{"total_count":1,"jobs":[{"name":"build","status":"completed","conclusion":"success"}]}`)
		default:
			_, _ = fmt.Fprint(w, `{}`)
		}
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	gh := github.NewClient(srv.Client())
	u, err := url.Parse(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	gh.BaseURL = u
	return &Client{gh: gh}
}

// A collapsed repo must cost a flat two calls no matter how many workflows or
// open PRs it has: the branch runs and the PR list. Job and per-PR run detail
// is not on screen, so it is not fetched.
func TestFetchAllCollapsedCostsTwoCalls(t *testing.T) {
	var paths []string
	c := countingServer(t, &paths)

	data, err := c.FetchAll(context.Background(), RepoQuery{Owner: "o", Name: "r", Branch: "main"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(paths) != 2 {
		t.Fatalf("collapsed poll made %d calls (%v), want 2", len(paths), paths)
	}
	for _, p := range paths {
		if strings.Contains(p, "/jobs") {
			t.Errorf("collapsed poll fetched jobs: %s", p)
		}
	}
	// The rows a collapsed repo renders are still populated.
	if len(data.BranchRuns) != 2 {
		t.Errorf("branch runs = %d, want 2", len(data.BranchRuns))
	}
	if len(data.PRRuns) != 3 {
		t.Errorf("PRs = %d, want 3", len(data.PRRuns))
	}
	for _, pr := range data.PRRuns {
		if len(pr.Runs) != 0 {
			t.Errorf("PR #%d carried runs while collapsed", pr.PR.Number)
		}
	}
}

// Expanding the same repo pays for the detail it now renders: one runs call per
// open PR, and one job call per deduped run, on the branch and on every PR.
func TestFetchAllExpandedFetchesDetail(t *testing.T) {
	var paths []string
	c := countingServer(t, &paths)

	data, err := c.FetchAll(context.Background(), RepoQuery{Owner: "o", Name: "r", Branch: "main", Detail: true})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// 1 branch runs + 2 job calls + 1 PR list + 3 PRs × (1 runs + 2 job calls).
	if len(paths) != 13 {
		t.Fatalf("expanded poll made %d calls (%v), want 13", len(paths), paths)
	}
	jobCalls := 0
	for _, p := range paths {
		if strings.Contains(p, "/jobs") {
			jobCalls++
		}
	}
	if jobCalls != 8 {
		t.Errorf("job calls = %d, want one per run: 2 branch + 3 PRs × 2 (8)", jobCalls)
	}
	for _, r := range data.BranchRuns {
		if len(r.Jobs) == 0 {
			t.Errorf("expanded run %q has no jobs", r.WorkflowName)
		}
	}
	for _, pr := range data.PRRuns {
		for _, r := range pr.Runs {
			if len(r.Jobs) == 0 {
				t.Errorf("PR #%d run %q has no jobs", pr.PR.Number, r.WorkflowName)
			}
		}
	}
}
