package ui

import (
	"testing"

	"github.com/ericdahl-dev/git-green/internal/aggregator"
	"github.com/ericdahl-dev/git-green/internal/state"
)

func repoState(name string, light aggregator.Stoplight, prs ...state.PRState) state.RepoState {
	return state.RepoState{Owner: "o", Name: name, Stoplight: light, PRs: prs}
}

func pr(n int, light aggregator.Stoplight) state.PRState {
	return state.PRState{Number: n, Title: "pr", Stoplight: light}
}

func isOpen(d Dashboard, name string) bool { return d.repoExp["o/"+name] }

func TestAutoExpandOpensActiveAndFailingRepos(t *testing.T) {
	d := NewDashboard(state.New(nil))
	d, cmd := d.Update(state.New([]state.RepoState{
		repoState("green", aggregator.StoplightGreen),
		repoState("running", aggregator.StoplightYellow),
		repoState("failing", aggregator.StoplightRed),
		repoState("idle", aggregator.StoplightGrey),
	}))
	if isOpen(d, "green") || isOpen(d, "idle") {
		t.Error("green/grey repos should stay collapsed")
	}
	if !isOpen(d, "running") || !isOpen(d, "failing") {
		t.Error("yellow/red repos should open")
	}
	if cmd == nil {
		t.Fatal("opening repos should tell the poller to fetch their detail")
	}
	if _, ok := cmd().(ExpandedReposMsg); !ok {
		t.Error("expected ExpandedReposMsg")
	}
}

func TestAutoExpandCollapsesWhenRepoGoesGreen(t *testing.T) {
	d := NewDashboard(state.New(nil))
	d, _ = d.Update(state.New([]state.RepoState{repoState("r", aggregator.StoplightYellow)}))
	d, _ = d.Update(state.New([]state.RepoState{repoState("r", aggregator.StoplightGreen)}))
	if isOpen(d, "r") {
		t.Error("repo should collapse once it is green again")
	}
}

func TestAutoExpandStaysOpenWhilePRActive(t *testing.T) {
	d := NewDashboard(state.New(nil))
	d, _ = d.Update(state.New([]state.RepoState{repoState("r", aggregator.StoplightYellow)}))
	d, _ = d.Update(state.New([]state.RepoState{
		repoState("r", aggregator.StoplightGreen, pr(1, aggregator.StoplightYellow)),
	}))
	if !isOpen(d, "r") {
		t.Error("repo should stay open while one of its PRs is running")
	}
}

func TestAutoExpandRespectsManualCollapse(t *testing.T) {
	d := NewDashboard(state.New(nil))
	snap := state.New([]state.RepoState{repoState("r", aggregator.StoplightRed, pr(1, aggregator.StoplightGreen))})
	d, _ = d.Update(snap)
	d, _ = d.Update(key("enter")) // cursor on the repo row: collapse it
	if isOpen(d, "r") {
		t.Fatal("enter should collapse the repo")
	}
	d, _ = d.Update(snap)
	if isOpen(d, "r") {
		t.Error("a repo the user collapsed should stay collapsed while its status is unchanged")
	}
}

func TestAutoExpandKeepsCursorOnSelectedRepo(t *testing.T) {
	d := NewDashboard(state.New(nil))
	d, _ = d.Update(state.New([]state.RepoState{
		repoState("a", aggregator.StoplightGreen),
		repoState("b", aggregator.StoplightGreen),
	}))
	d, _ = d.Update(key("j")) // select b
	d, _ = d.Update(state.New([]state.RepoState{
		repoState("a", aggregator.StoplightRed, pr(1, aggregator.StoplightRed), pr(2, aggregator.StoplightRed)),
		repoState("b", aggregator.StoplightGreen),
	}))
	row := d.rows[d.cursor]
	if row.kind != kindRepo || d.snapshot.Repos[row.repoIdx].Name != "b" {
		t.Errorf("cursor moved off repo b to %+v", row)
	}
}
