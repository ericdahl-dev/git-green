package ui

import (
	"strings"
	"testing"

	"github.com/ericdahl-dev/git-green/internal/aggregator"
	githubclient "github.com/ericdahl-dev/git-green/internal/github"
	"github.com/ericdahl-dev/git-green/internal/state"
)

// stackedPR builds a PR sitting at position of size in stack number.
func stackedPR(num int, light aggregator.Stoplight, stackNum, position, size int, head string) state.PRState {
	return state.PRState{
		Number:    num,
		Title:     "stacked work",
		Stoplight: light,
		Stack: &githubclient.Stack{
			Number:   stackNum,
			Size:     size,
			Position: position,
			HeadRef:  head,
		},
	}
}

func lonePR(num int, light aggregator.Stoplight) state.PRState {
	return state.PRState{Number: num, Title: "solo work", Stoplight: light}
}

func repoWithPRs(prs ...state.PRState) state.Snapshot {
	return state.New([]state.RepoState{{
		Owner:     "ndlibrary",
		Name:      "annex-ims",
		Stoplight: aggregator.StoplightRed,
		PRs:       prs,
	}})
}

func TestGroupPRsOrdersStackMembersBottomToTop(t *testing.T) {
	// Deliberately out of order, and with the green member last so an
	// active-first sort would move it if stack order were not enforced.
	groups := groupPRs([]state.PRState{
		stackedPR(271, aggregator.StoplightYellow, 272, 4, 4, "top"),
		stackedPR(268, aggregator.StoplightGreen, 272, 1, 4, "bottom"),
		stackedPR(270, aggregator.StoplightRed, 272, 3, 4, "third"),
		stackedPR(269, aggregator.StoplightGreen, 272, 2, 4, "second"),
	})

	if len(groups) != 1 {
		t.Fatalf("got %d groups, want 1 stack", len(groups))
	}
	g := groups[0]
	if !g.isStack() || g.stackNum != 272 {
		t.Fatalf("got stackNum %d, want 272", g.stackNum)
	}
	if want := []int{1, 3, 2, 0}; !equalInts(g.prIdxs, want) {
		t.Errorf("got member order %v, want %v (bottom to top)", g.prIdxs, want)
	}
	if g.label != "bottom" {
		t.Errorf("got label %q, want the bottom member's head branch", g.label)
	}
	// Yellow is the most actionable member, so the stack shows yellow.
	if g.stoplight != aggregator.StoplightYellow {
		t.Errorf("got stoplight %v, want yellow from the worst member", g.stoplight)
	}
}

func TestGroupPRsSortsGroupsActiveFirst(t *testing.T) {
	groups := groupPRs([]state.PRState{
		lonePR(256, aggregator.StoplightGreen),
		stackedPR(260, aggregator.StoplightGreen, 265, 1, 2, "a"),
		stackedPR(261, aggregator.StoplightRed, 265, 2, 2, "b"),
		lonePR(259, aggregator.StoplightYellow),
	})

	var order []int
	for _, g := range groups {
		if g.isStack() {
			order = append(order, g.stackNum)
			continue
		}
		order = append(order, 0)
	}
	// Yellow standalone, then the stack (red via its worst member), then green.
	if want := []int{0, 265, 0}; !equalInts(order, want) {
		t.Fatalf("got group order %v, want %v", order, want)
	}
	if groups[0].prIdxs[0] != 3 {
		t.Errorf("expected the yellow standalone PR first, got index %d", groups[0].prIdxs[0])
	}
}

func TestGroupPRsKeepsStandalonePRsUngrouped(t *testing.T) {
	groups := groupPRs([]state.PRState{
		lonePR(256, aggregator.StoplightGreen),
		lonePR(259, aggregator.StoplightGreen),
	})
	if len(groups) != 2 {
		t.Fatalf("got %d groups, want one per standalone PR", len(groups))
	}
	for _, g := range groups {
		if g.isStack() {
			t.Error("a PR with no stack must not be grouped as one")
		}
	}
}

// Once part of a stack merges, GitHub still counts it in the stack size. The
// row says so rather than pretending the stack shrank.
func TestStackTitleShowsPartlyMergedStacks(t *testing.T) {
	groups := groupPRs([]state.PRState{
		stackedPR(270, aggregator.StoplightGreen, 272, 3, 4, "third"),
		stackedPR(271, aggregator.StoplightGreen, 272, 4, 4, "top"),
	})
	if got := groups[0].title(); !strings.Contains(got, "2 of 4 PRs") {
		t.Errorf("got %q, want it to report 2 of 4 PRs", got)
	}
}

func TestStackTitleNamesTheStackAndItsBranch(t *testing.T) {
	groups := groupPRs([]state.PRState{
		stackedPR(268, aggregator.StoplightGreen, 272, 1, 2, "wse-1937-id-search"),
		stackedPR(269, aggregator.StoplightGreen, 272, 2, 2, "wse-1935-sort-leak"),
	})
	got := groups[0].title()
	for _, want := range []string{"stack #272", "2 PRs", "wse-1937-id-search"} {
		if !strings.Contains(got, want) {
			t.Errorf("got %q, want it to contain %q", got, want)
		}
	}
}

func TestDashboardCollapsesStackMembersUntilExpanded(t *testing.T) {
	d := NewDashboard(repoWithPRs(
		stackedPR(268, aggregator.StoplightGreen, 272, 1, 2, "bottom"),
		stackedPR(269, aggregator.StoplightGreen, 272, 2, 2, "top"),
		lonePR(259, aggregator.StoplightRed),
	))
	d, _ = d.Update(key("enter")) // expand the repo

	body := d.BodyView()
	if !strings.Contains(body, "stack #272") {
		t.Fatalf("expected the stack row to render:\n%s", body)
	}
	if strings.Contains(body, "PR #268") {
		t.Errorf("stack members must stay hidden until the stack is expanded:\n%s", body)
	}
	if !strings.Contains(body, "PR #259") {
		t.Errorf("a standalone PR must still render at the top level:\n%s", body)
	}

	// The red standalone PR outranks the green stack, so walk down to the
	// stack row rather than assuming where it landed.
	d = moveTo(t, d, kindStack)
	d, _ = d.Update(key("enter"))

	body = d.BodyView()
	for _, want := range []string{"1/2  PR #268", "2/2  PR #269"} {
		if !strings.Contains(body, want) {
			t.Errorf("expected %q in the expanded stack:\n%s", want, body)
		}
	}
}

// A collapsed stack still has to offer the failing run underneath it, or a red
// stack would give the f key nothing to act on.
func TestSelectedRerunTargetReachesIntoACollapsedStack(t *testing.T) {
	prs := []state.PRState{
		stackedPR(268, aggregator.StoplightGreen, 272, 1, 2, "bottom"),
		stackedPR(269, aggregator.StoplightRed, 272, 2, 2, "top"),
	}
	prs[1].Runs = []githubclient.WorkflowRun{wfRun("CI", "failure", 99)}

	d := NewDashboard(repoWithPRs(prs...))
	d, _ = d.Update(key("enter")) // expand the repo
	d = moveTo(t, d, kindStack)

	target := d.selectedRerunTarget()
	if target == nil {
		t.Fatal("expected the stack row to offer its failing member's run")
	}
	if target.runID != 99 {
		t.Errorf("got run %d, want 99", target.runID)
	}
}

// moveTo walks the cursor down to the first row of the given kind.
func moveTo(t *testing.T, d Dashboard, want rowKind) Dashboard {
	t.Helper()
	for range d.rows {
		if d.rows[d.cursor].kind == want {
			return d
		}
		d, _ = d.Update(key("down"))
	}
	t.Fatalf("no row of kind %d in %d rows", want, len(d.rows))
	return d
}

func equalInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
