package ui

import (
	"fmt"
	"sort"

	"github.com/ericdahl-dev/git-green/internal/aggregator"
	"github.com/ericdahl-dev/git-green/internal/state"
)

// prGroup is one unit under an expanded Repo: either a standalone PR or a
// stack of them. Grouping happens at render time so the snapshot stays a flat
// list of PRs.
type prGroup struct {
	stackNum  int    // 0 for a standalone PR
	stackSize int    // entries GitHub counts in the stack, merged ones included
	label     string // head branch of the bottom PR, which names the stack
	prIdxs    []int  // indices into RepoState.PRs, bottom to top for a stack
	stoplight aggregator.Stoplight
}

func (g prGroup) isStack() bool { return g.stackNum != 0 }

// title renders the stack row's text. The open count and the stack size differ
// once part of the stack has merged, and that gap is worth showing: it says how
// much of the stack has already landed.
func (g prGroup) title() string {
	open := len(g.prIdxs)
	count := fmt.Sprintf("%d PRs", open)
	if open == 1 {
		count = "1 PR"
	}
	if g.stackSize > open {
		count = fmt.Sprintf("%d of %d PRs", open, g.stackSize)
	}
	row := fmt.Sprintf("%s  stack #%d · %s", g.stoplight.String(), g.stackNum, count)
	if g.label != "" {
		row += " · " + g.label
	}
	return row
}

// groupPRs splits a repo's PRs into stacks and standalone PRs.
//
// Members of a stack stay in stack order — bottom (trunk-most) first — because
// that is the order they have to merge in, so active-first sorting would only
// obscure it. The groups themselves still sort active-first, a stack taking the
// most actionable Stoplight of its members.
func groupPRs(prs []state.PRState) []prGroup {
	var groups []prGroup
	byStack := make(map[int]int) // stack number -> index into groups

	for i, pr := range prs {
		if pr.Stack == nil {
			groups = append(groups, prGroup{prIdxs: []int{i}, stoplight: pr.Stoplight})
			continue
		}
		gi, ok := byStack[pr.Stack.Number]
		if !ok {
			gi = len(groups)
			byStack[pr.Stack.Number] = gi
			groups = append(groups, prGroup{
				stackNum:  pr.Stack.Number,
				stackSize: pr.Stack.Size,
				stoplight: pr.Stoplight,
			})
		}
		g := &groups[gi]
		g.prIdxs = append(g.prIdxs, i)
		if stoplightPriority(pr.Stoplight) < stoplightPriority(g.stoplight) {
			g.stoplight = pr.Stoplight
		}
	}

	for gi := range groups {
		g := &groups[gi]
		if !g.isStack() {
			continue
		}
		sort.SliceStable(g.prIdxs, func(a, b int) bool {
			pa, pb := prs[g.prIdxs[a]], prs[g.prIdxs[b]]
			if pa.Stack.Position != pb.Stack.Position {
				return pa.Stack.Position < pb.Stack.Position
			}
			return pa.Number < pb.Number
		})
		g.label = prs[g.prIdxs[0]].Stack.HeadRef
	}

	sort.SliceStable(groups, func(a, b int) bool {
		return stoplightPriority(groups[a].stoplight) < stoplightPriority(groups[b].stoplight)
	})
	return groups
}
