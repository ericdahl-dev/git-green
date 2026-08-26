package ui

import (
	"context"
	"errors"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/ericdahl-dev/git-green/internal/aggregator"
	githubclient "github.com/ericdahl-dev/git-green/internal/github"
	"github.com/ericdahl-dev/git-green/internal/state"
)

// fakeRerunner records the call it was given and returns a canned error.
type fakeRerunner struct {
	err    error
	calls  int
	owner  string
	name   string
	runID  int64
	called bool
}

func (f *fakeRerunner) RerunFailedJobs(_ context.Context, owner, name string, runID int64) error {
	f.calls++
	f.called = true
	f.owner, f.name, f.runID = owner, name, runID
	return f.err
}

func key(s string) tea.KeyMsg {
	if s == " " {
		return tea.KeyMsg{Type: tea.KeySpace}
	}
	if len(s) > 1 {
		switch s {
		case "enter":
			return tea.KeyMsg{Type: tea.KeyEnter}
		case "esc":
			return tea.KeyMsg{Type: tea.KeyEsc}
		}
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

func wfRun(name, conclusion string, id int64) githubclient.WorkflowRun {
	return githubclient.WorkflowRun{
		WorkflowName: name,
		Status:       "completed",
		Conclusion:   conclusion,
		RunID:        id,
	}
}

func snapshotWith(runs ...githubclient.WorkflowRun) state.Snapshot {
	return state.New([]state.RepoState{{
		Owner:     "ericdahl-dev",
		Name:      "git-green",
		Stoplight: aggregator.StoplightRed,
		Runs:      runs,
	}})
}

// dashboardWith builds a dashboard over a single repo with the given runs,
// wired to fake.
func dashboardWith(fake Rerunner, runs ...githubclient.WorkflowRun) Dashboard {
	return NewDashboard(snapshotWith(runs...)).WithRerunner(context.Background(), fake)
}

// send applies msg and, when the returned command produces one, the message it
// produces — enough to drive a re-run to completion without a real event loop.
func send(t *testing.T, d Dashboard, msg tea.Msg) Dashboard {
	t.Helper()
	d, cmd := d.Update(msg)
	if cmd == nil {
		return d
	}
	out := cmd()
	if out == nil {
		return d
	}
	if _, ok := out.(selectionExpiredMsg); ok {
		return d
	}
	d, _ = d.Update(out)
	return d
}

func TestRunFailed(t *testing.T) {
	cases := map[string]bool{
		"failure":         true,
		"timed_out":       true,
		"action_required": true,
		"startup_failure": true,
		"success":         false,
		"skipped":         false,
		"cancelled":       false,
		"":                false,
	}
	for conclusion, want := range cases {
		if got := runFailed(wfRun("CI", conclusion, 1)); got != want {
			t.Errorf("runFailed(%q) = %v, want %v", conclusion, got, want)
		}
	}
}

func TestRerunIgnoredWhenRunIsGreen(t *testing.T) {
	fake := &fakeRerunner{}
	d := dashboardWith(fake, wfRun("CI", "success", 7))

	d, _ = d.Update(key("f"))
	if d.AwaitingConfirm() {
		t.Fatal("expected f on a green row to do nothing")
	}

	if _, _ = d.Update(key("enter")); fake.called {
		t.Error("expected no re-run to be fired")
	}
}

func TestRerunIgnoredWithoutRerunner(t *testing.T) {
	d := NewDashboard(snapshotWith(wfRun("CI", "failure", 7)))

	d, _ = d.Update(key("f"))
	if d.AwaitingConfirm() {
		t.Error("expected f with no rerunner wired to do nothing")
	}
}

func TestRerunConfirmFires(t *testing.T) {
	fake := &fakeRerunner{}
	d := dashboardWith(fake, wfRun("CI", "failure", 7))

	d, _ = d.Update(key("f"))
	if !d.AwaitingConfirm() {
		t.Fatal("expected f on a failed row to ask for confirmation")
	}
	if !strings.Contains(d.hintLine(), "[enter] confirm") {
		t.Errorf("hint line does not prompt for confirmation: %q", d.hintLine())
	}

	d = send(t, d, key("enter"))
	if fake.calls != 1 {
		t.Fatalf("re-run called %d times, want 1", fake.calls)
	}
	if fake.owner != "ericdahl-dev" || fake.name != "git-green" || fake.runID != 7 {
		t.Errorf("re-run targeted %s/%s run %d, want ericdahl-dev/git-green run 7", fake.owner, fake.name, fake.runID)
	}
	if d.rerunStatus != rerunShowResult {
		t.Errorf("status = %v, want rerunShowResult", d.rerunStatus)
	}
	if !strings.Contains(d.hintLine(), "re-run requested") {
		t.Errorf("hint line = %q, want it to report the request", d.hintLine())
	}
}

func TestRerunEscCancels(t *testing.T) {
	fake := &fakeRerunner{}
	d := dashboardWith(fake, wfRun("CI", "failure", 7))

	d, _ = d.Update(key("f"))
	d, _ = d.Update(key("esc"))
	if d.AwaitingConfirm() {
		t.Error("expected esc to leave the confirmation")
	}
	if fake.called {
		t.Error("expected esc not to fire a re-run")
	}
}

func TestRerunErrorSurfacesInHintLine(t *testing.T) {
	fake := &fakeRerunner{err: errors.New("403 Resource not accessible by personal access token")}
	d := dashboardWith(fake, wfRun("CI", "failure", 7))

	d, _ = d.Update(key("f"))
	d = send(t, d, key("enter"))

	if !d.rerunFailure {
		t.Fatal("expected the failure to be recorded")
	}
	hint := d.hintLine()
	if !strings.Contains(hint, "not accessible by personal access token") {
		t.Errorf("hint line = %q, want it to carry the API error", hint)
	}
}

func TestRerunSuccessClearsOnNextSnapshot(t *testing.T) {
	fake := &fakeRerunner{}
	d := dashboardWith(fake, wfRun("CI", "failure", 7))

	d, _ = d.Update(key("f"))
	d = send(t, d, key("enter"))
	d, _ = d.Update(snapshotWith(wfRun("CI", "", 8)))

	if d.rerunStatus != rerunIdle {
		t.Errorf("status = %v, want rerunIdle after the next poll", d.rerunStatus)
	}
	if !strings.Contains(d.hintLine(), "f re-run") {
		t.Errorf("hint line = %q, want the normal keybinding hints back", d.hintLine())
	}
}

func TestRerunErrorSurvivesNextSnapshot(t *testing.T) {
	fake := &fakeRerunner{err: errors.New("boom")}
	d := dashboardWith(fake, wfRun("CI", "failure", 7))

	d, _ = d.Update(key("f"))
	d = send(t, d, key("enter"))
	d, _ = d.Update(snapshotWith(wfRun("CI", "failure", 7)))

	if d.rerunStatus != rerunShowResult {
		t.Errorf("status = %v, want the error to stay until it times out", d.rerunStatus)
	}

	d, _ = d.Update(rerunResultExpiredMsg{})
	if d.rerunStatus != rerunIdle {
		t.Errorf("status = %v, want rerunIdle once the result expires", d.rerunStatus)
	}
}

func TestRerunTargetsFirstFailedRun(t *testing.T) {
	fake := &fakeRerunner{}
	d := dashboardWith(fake,
		wfRun("Lint", "success", 1),
		wfRun("CI", "failure", 2),
		wfRun("Release", "failure", 3),
	)

	target := d.selectedRerunTarget()
	if target == nil {
		t.Fatal("expected a target")
	}
	if target.runID != 2 || target.workflow != "CI" {
		t.Errorf("target = %s run %d, want CI run 2", target.workflow, target.runID)
	}
	if got, want := target.fullName(), "ericdahl-dev/git-green"; got != want {
		t.Errorf("fullName = %q, want %q", got, want)
	}
}

func TestRerunTargetsSelectedPRRun(t *testing.T) {
	snap := state.New([]state.RepoState{{
		Owner:     "ericdahl-dev",
		Name:      "git-green",
		Stoplight: aggregator.StoplightRed,
		Runs:      []githubclient.WorkflowRun{wfRun("CI", "failure", 1)},
		PRs: []state.PRState{{
			Number:    29,
			Title:     "Re-run failed runs",
			Stoplight: aggregator.StoplightRed,
			Runs:      []githubclient.WorkflowRun{wfRun("CI", "failure", 99)},
		}},
	}})
	d := NewDashboard(snap).WithRerunner(context.Background(), &fakeRerunner{})

	// Expand the repo so the PR row is navigable, then move onto it.
	d, _ = d.Update(key("enter"))
	d, _ = d.Update(key("j"))

	target := d.selectedRerunTarget()
	if target == nil {
		t.Fatal("expected a target on the PR row")
	}
	if target.runID != 99 {
		t.Errorf("target run = %d, want the PR's run 99", target.runID)
	}
}

func TestRerunTargetEmptyDashboard(t *testing.T) {
	d := NewDashboard(state.New(nil)).WithRerunner(context.Background(), &fakeRerunner{})
	if target := d.selectedRerunTarget(); target != nil {
		t.Errorf("target = %+v, want nil on an empty dashboard", target)
	}
}

// Expansion is keyed by "owner/name", not by slice index, so re-sorting the
// snapshot (which happens whenever a stoplight changes) cannot transfer an
// open row onto a different repo.
func TestExpansionSurvivesReordering(t *testing.T) {
	green := state.RepoState{Owner: "o", Name: "green", Stoplight: aggregator.StoplightGreen}
	red := state.RepoState{Owner: "o", Name: "red", Stoplight: aggregator.StoplightRed}

	d := NewDashboard(state.New([]state.RepoState{green, red}))
	d.repoExp["o/red"] = true

	if got := d.ExpandedRepos(); len(got) != 1 || got[0] != "o/red" {
		t.Fatalf("ExpandedRepos = %v, want [o/red]", got)
	}

	// Reverse the order, as a stoplight change would.
	d.snapshot = state.New([]state.RepoState{red, green})
	d.rows = d.buildRows()

	if !d.repoExp["o/red"] {
		t.Error("o/red lost its expansion after reordering")
	}
	if d.repoExp["o/green"] {
		t.Error("o/green became expanded after reordering")
	}
}
