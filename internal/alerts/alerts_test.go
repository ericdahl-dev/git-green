package alerts

import (
	"testing"
	"time"

	githubclient "github.com/ericdahl-dev/git-green/internal/github"
	"github.com/ericdahl-dev/git-green/internal/state"
	"github.com/ericdahl-dev/git-green/internal/webhooks"
)

var start = time.Date(2026, 8, 26, 9, 0, 0, 0, time.UTC)

// run feeds the same repos to w every step for n steps and collects alerts.
func run(w *Watcher, repos []state.RepoState, now *time.Time, n int, step time.Duration) []webhooks.Event {
	var out []webhooks.Event
	for i := 0; i < n; i++ {
		out = append(out, w.Observe(repos, *now)...)
		*now = now.Add(step)
	}
	return out
}

func repoWith(runs ...githubclient.WorkflowRun) []state.RepoState {
	return []state.RepoState{{Owner: "o", Name: "r", Runs: runs}}
}

var (
	failing    = repoWith(githubclient.WorkflowRun{WorkflowName: "CI", Status: "completed", Conclusion: "failure"})
	inProgress = repoWith(githubclient.WorkflowRun{WorkflowName: "CI", Status: "in_progress"})
	green      = repoWith(githubclient.WorkflowRun{WorkflowName: "CI", Status: "completed", Conclusion: "success"})
)

func TestDoesNotFireBeforeThreshold(t *testing.T) {
	now := start
	if got := run(NewWatcher(30*time.Minute), failing, &now, 80, 15*time.Second); len(got) != 0 {
		t.Fatalf("expected no alerts in 20 minutes, got %d", len(got))
	}
}

func TestFiresOnceAfterThreshold(t *testing.T) {
	now := start
	got := run(NewWatcher(30*time.Minute), failing, &now, 240, 30*time.Second)
	if len(got) != 1 {
		t.Fatalf("expected exactly 1 alert over two hours, got %d", len(got))
	}
	evt := got[0]
	if evt.Event != "branch_stuck" || evt.Reason != "prolonged_failure" || evt.Repo != "o/r" {
		t.Errorf("got %q/%q/%q", evt.Event, evt.Reason, evt.Repo)
	}
	// StuckSince is when the condition started, not when it alerted.
	if !evt.StuckSince.Equal(start) {
		t.Errorf("stuck_since = %v, want %v", evt.StuckSince, start)
	}
	if evt.Timestamp.Sub(evt.StuckSince) < 30*time.Minute {
		t.Errorf("fired after %v, want at least 30m", evt.Timestamp.Sub(evt.StuckSince))
	}
}

func TestReArmsAfterRecovery(t *testing.T) {
	w := NewWatcher(30 * time.Minute)
	now := start
	if got := run(w, failing, &now, 5, 10*time.Minute); len(got) != 1 {
		t.Fatalf("first incident: got %d alerts, want 1", len(got))
	}
	run(w, green, &now, 1, 10*time.Minute)
	if len(w.stuck) != 0 {
		t.Fatalf("recovery should forget the condition, still tracking %d", len(w.stuck))
	}
	if got := run(w, failing, &now, 5, 10*time.Minute); len(got) != 1 {
		t.Fatalf("second incident: got %d alerts, want 1", len(got))
	}
}

func TestReasonChangeRestartsTheClock(t *testing.T) {
	w := NewWatcher(30 * time.Minute)
	now := start
	if got := run(w, inProgress, &now, 2, 10*time.Minute); len(got) != 0 {
		t.Fatalf("expected nothing yet, got %d", len(got))
	}
	// Turning into a failure is a new condition: the 20 minutes in progress
	// must not count toward it.
	if got := w.Observe(failing, now); len(got) != 0 {
		t.Fatalf("reason change should restart the clock, got %d", len(got))
	}
	got := w.Observe(failing, now.Add(31*time.Minute))
	if len(got) != 1 || got[0].Reason != "prolonged_failure" {
		t.Fatalf("got %+v, want one prolonged_failure", got)
	}
}

func TestPRFiresWithPRInfo(t *testing.T) {
	repos := []state.RepoState{{Owner: "o", Name: "r", PRs: []state.PRState{{
		Number: 42, Title: "Add a thing", HTMLURL: "https://github.com/o/r/pull/42", Mergeable: "dirty",
	}}}}
	now := start
	got := run(NewWatcher(30*time.Minute), repos, &now, 5, 10*time.Minute)
	if len(got) != 1 {
		t.Fatalf("got %d alerts, want 1", len(got))
	}
	if got[0].Event != "pr_stuck" || got[0].Reason != "conflict" || got[0].PR == nil || got[0].PR.Number != 42 {
		t.Errorf("got %+v", got[0])
	}
}

// Collapsing a Repo drops its PR Runs from the Snapshot. That must not read as
// a recovery: the clock keeps running and the alert still fires on time.
func TestCollapsedRepoKeepsThePRClock(t *testing.T) {
	failingPR := []state.RepoState{{Owner: "o", Name: "r", PRs: []state.PRState{{
		Number: 7, Runs: []githubclient.WorkflowRun{{WorkflowName: "CI", Status: "completed", Conclusion: "failure"}},
	}}}}
	collapsed := []state.RepoState{{Owner: "o", Name: "r", PRs: []state.PRState{{Number: 7}}}}

	w := NewWatcher(30 * time.Minute)
	now := start
	run(w, failingPR, &now, 2, 10*time.Minute) // 20 minutes failing
	run(w, collapsed, &now, 1, 5*time.Minute)  // collapsed for 5
	got := run(w, failingPR, &now, 1, 0)       // 25 minutes in: still under
	if len(got) != 0 {
		t.Fatalf("fired early: %+v", got)
	}
	now = now.Add(6 * time.Minute)
	if got := w.Observe(failingPR, now); len(got) != 1 {
		t.Fatalf("got %d alerts at 31 minutes, want 1: collapsing reset the clock", len(got))
	}
}

// A PR that closes is forgotten even though it has no Runs either.
func TestClosedPRIsForgotten(t *testing.T) {
	failingPR := []state.RepoState{{Owner: "o", Name: "r", PRs: []state.PRState{{
		Number: 7, Runs: []githubclient.WorkflowRun{{Status: "completed", Conclusion: "failure"}},
	}}}}
	w := NewWatcher(30 * time.Minute)
	w.Observe(failingPR, start)
	w.Observe([]state.RepoState{{Owner: "o", Name: "r"}}, start.Add(time.Minute))
	if len(w.stuck) != 0 {
		t.Errorf("a closed PR is still tracked: %d entries", len(w.stuck))
	}
}

// Stuck follows the Stoplight: every red conclusion is a failure and every
// yellow status is still going.
func TestRunsStuckReasonFollowsStoplight(t *testing.T) {
	cases := []struct {
		run    githubclient.WorkflowRun
		stuck  bool
		reason string
	}{
		{githubclient.WorkflowRun{Status: "completed", Conclusion: "failure"}, true, "prolonged_failure"},
		{githubclient.WorkflowRun{Status: "completed", Conclusion: "action_required"}, true, "prolonged_failure"},
		{githubclient.WorkflowRun{Status: "completed", Conclusion: "startup_failure"}, true, "prolonged_failure"},
		{githubclient.WorkflowRun{Status: "in_progress"}, true, "prolonged_in_progress"},
		{githubclient.WorkflowRun{Status: "queued"}, true, "prolonged_in_progress"},
		{githubclient.WorkflowRun{Status: "completed", Conclusion: "success"}, false, ""},
		{githubclient.WorkflowRun{Status: "completed", Conclusion: "cancelled"}, false, ""},
	}
	for _, tc := range cases {
		stuck, reason := runsStuckReason([]githubclient.WorkflowRun{tc.run})
		if stuck != tc.stuck || reason != tc.reason {
			t.Errorf("%s/%s: got (%v, %q), want (%v, %q)", tc.run.Status, tc.run.Conclusion, stuck, reason, tc.stuck, tc.reason)
		}
	}
}

func TestPRStuckReasonConflict(t *testing.T) {
	for _, m := range []string{"dirty", "conflicting"} {
		if stuck, reason := prStuckReason(state.PRState{Mergeable: m}); !stuck || reason != "conflict" {
			t.Errorf("%s: got (%v, %q), want conflict", m, stuck, reason)
		}
	}
	if stuck, _ := prStuckReason(state.PRState{Mergeable: "clean"}); stuck {
		t.Error("a clean PR with no Runs is not stuck")
	}
}
