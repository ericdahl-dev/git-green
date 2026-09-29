package aggregator

import (
	"sort"
	"testing"

	githubclient "github.com/ericdahl-dev/git-green/internal/github"
)

func TestOfMapsEveryStatus(t *testing.T) {
	cases := map[string]Stoplight{
		"success":         StoplightGreen,
		"neutral":         StoplightGreen,
		"skipped":         StoplightGreen,
		"failure":         StoplightRed,
		"timed_out":       StoplightRed,
		"action_required": StoplightRed,
		"startup_failure": StoplightRed,
		"queued":          StoplightYellow,
		"in_progress":     StoplightYellow,
		"requested":       StoplightYellow,
		"waiting":         StoplightYellow,
		"pending":         StoplightYellow,
		"cancelled":       StoplightGrey,
		"stale":           StoplightGrey,
		"":                StoplightGrey,
	}
	for status, want := range cases {
		if got := Of(status); got != want {
			t.Errorf("Of(%q) = %v, want %v", status, got, want)
		}
	}
}

func run(status, conclusion string) githubclient.WorkflowRun {
	return githubclient.WorkflowRun{Status: status, Conclusion: conclusion}
}

func TestRunsIsWorstCase(t *testing.T) {
	cases := []struct {
		name string
		runs []githubclient.WorkflowRun
		want Stoplight
	}{
		{"no runs", nil, StoplightGrey},
		{"all cancelled", []githubclient.WorkflowRun{run("completed", "cancelled"), run("completed", "cancelled")}, StoplightGrey},
		{"green and running", []githubclient.WorkflowRun{run("completed", "success"), run("in_progress", "")}, StoplightYellow},
		{"red beats everything", []githubclient.WorkflowRun{run("completed", "success"), run("in_progress", ""), run("completed", "failure")}, StoplightRed},
		{"red beats queued", []githubclient.WorkflowRun{run("queued", ""), run("completed", "timed_out")}, StoplightRed},
	}
	for _, tc := range cases {
		if got := Runs(tc.runs); got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}

// A finished run reports its conclusion; one still going only has a status.
func TestRunsReadsConclusionBeforeStatus(t *testing.T) {
	if got := Runs([]githubclient.WorkflowRun{run("completed", "failure")}); got != StoplightRed {
		t.Errorf("completed/failure: got %v, want red", got)
	}
	if got := Runs([]githubclient.WorkflowRun{run("in_progress", "")}); got != StoplightYellow {
		t.Errorf("in_progress: got %v, want yellow", got)
	}
}

func TestActiveFirstOrder(t *testing.T) {
	lights := []Stoplight{StoplightGrey, StoplightGreen, StoplightRed, StoplightYellow}
	sort.Slice(lights, func(a, b int) bool { return lights[a].ActiveFirst() < lights[b].ActiveFirst() })
	want := []Stoplight{StoplightYellow, StoplightRed, StoplightGreen, StoplightGrey}
	for i := range want {
		if lights[i] != want[i] {
			t.Fatalf("got %v, want %v", lights, want)
		}
	}
}
