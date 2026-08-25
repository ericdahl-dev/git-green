package githubclient

import (
	"errors"
	"fmt"
	"net/http"
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
