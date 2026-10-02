package ui

import (
	"testing"

	"github.com/ericdahl-dev/git-green/internal/aggregator"
	githubclient "github.com/ericdahl-dev/git-green/internal/github"
	"github.com/ericdahl-dev/git-green/internal/state"
)

func treeRepo(name string, light aggregator.Stoplight, prs ...state.PRState) state.RepoState {
	return state.RepoState{Owner: "o", Name: name, Stoplight: light, PRs: prs}
}

// selectKind walks the cursor to the first row of kind whose number is num.
func selectKind(t *testing.T, tr *tree, kind rowKind, num int) {
	t.Helper()
	tr.cursor = 0
	for range tr.rows {
		n, _ := tr.selected()
		if n.key.kind == kind && n.key.num == num {
			return
		}
		tr.move(1)
	}
	t.Fatalf("no %d row numbered %d", kind, num)
}

// PR expansion follows the PR, not its position: when a PR above it closes,
// the expanded one stays expanded and its neighbor does not open.
func TestTreePRExpansionFollowsThePR(t *testing.T) {
	tr := newTree(state.New([]state.RepoState{
		treeRepo("a", aggregator.StoplightGreen, lonePR(1, aggregator.StoplightGreen), lonePR(2, aggregator.StoplightGreen)),
	}))
	tr.toggle() // expand the repo
	selectKind(t, &tr, kindPR, 2)
	tr.toggle() // expand PR #2

	// PR #1 closes, so PR #2 moves to index 0 and a new PR #3 takes index 1.
	tr.update(state.New([]state.RepoState{
		treeRepo("a", aggregator.StoplightGreen, lonePR(2, aggregator.StoplightGreen), lonePR(3, aggregator.StoplightGreen)),
	}))

	for _, n := range tr.rows {
		if n.key.kind != kindPR {
			continue
		}
		if want := n.pr.Number == 2; tr.isExpanded(n) != want {
			t.Errorf("PR #%d expanded = %v, want %v", n.pr.Number, tr.isExpanded(n), want)
		}
	}
}

func TestTreeCursorFollowsSelectionAcrossResort(t *testing.T) {
	tr := newTree(state.New([]state.RepoState{
		treeRepo("a", aggregator.StoplightGreen),
		treeRepo("b", aggregator.StoplightGreen),
	}))
	selectKind(t, &tr, kindRepo, 0)
	tr.move(1) // on b

	// b starts failing and sorts above a.
	tr.update(state.New([]state.RepoState{
		treeRepo("a", aggregator.StoplightGreen),
		treeRepo("b", aggregator.StoplightRed),
	}))
	if n, _ := tr.selected(); n.repo.Name != "b" {
		t.Errorf("cursor on %q, want b", n.repo.Name)
	}
}

func TestTreeToggleReportsRepoChanges(t *testing.T) {
	tr := newTree(state.New([]state.RepoState{
		treeRepo("a", aggregator.StoplightGreen, lonePR(1, aggregator.StoplightGreen)),
	}))
	if !tr.toggle() {
		t.Error("toggling a Repo changes what the poller fetches")
	}
	if got := tr.expandedRepos(); len(got) != 1 || got[0] != "o/a" {
		t.Errorf("expandedRepos = %v", got)
	}
	selectKind(t, &tr, kindPR, 1)
	if tr.toggle() {
		t.Error("toggling a PR does not change what the poller fetches")
	}
}

func TestTreeStackRowRunsCoverEveryMember(t *testing.T) {
	bottom := stackedPR(10, aggregator.StoplightGreen, 12, 1, 2, "b")
	bottom.Runs = []githubclient.WorkflowRun{{WorkflowName: "CI", RunID: 1}}
	top := stackedPR(11, aggregator.StoplightRed, 12, 2, 2, "t")
	top.Runs = []githubclient.WorkflowRun{{WorkflowName: "CI", RunID: 2}}

	tr := newTree(state.New([]state.RepoState{treeRepo("a", aggregator.StoplightRed, top, bottom)}))
	tr.toggle()
	selectKind(t, &tr, kindStack, 12)
	n, _ := tr.selected()
	runs := n.runs()
	if len(runs) != 2 || runs[0].RunID != 1 || runs[1].RunID != 2 {
		t.Errorf("stack runs = %+v, want bottom then top", runs)
	}
}

func TestTreeEmpty(t *testing.T) {
	tr := newTree(state.New(nil))
	if _, ok := tr.selected(); ok {
		t.Error("an empty tree has no selection")
	}
	if tr.toggle() {
		t.Error("toggling nothing changes nothing")
	}
	tr.move(1)
	tr.move(-1)
}
