package ui

import (
	"sort"

	"github.com/ericdahl-dev/git-green/internal/aggregator"
	githubclient "github.com/ericdahl-dev/git-green/internal/github"
	"github.com/ericdahl-dev/git-green/internal/state"
)

type rowKind int

const (
	kindRepo rowKind = iota
	kindStack
	kindPR
)

// nodeKey identifies a row by what it shows, never by position. Positions
// move whenever a PR opens or closes, the config reloads, or active-first
// sorting reshuffles, and anything keyed by them drifts onto the wrong row.
type nodeKey struct {
	repo string // "owner/name"
	kind rowKind
	num  int // Stack or PR number; 0 for a Repo
}

func repoKey(name string) nodeKey { return nodeKey{repo: name, kind: kindRepo} }

// node is one navigable row of the Dashboard tree: a Repo, a Stack, or a PR.
type node struct {
	key     nodeKey
	repo    state.RepoState
	group   prGroup       // kindStack only
	pr      state.PRState // kindPR only
	inStack bool          // a PR rendered inside an expanded Stack
}

// runs returns the Runs a row stands for. A Stack stands for every Run across
// its members, bottom to top, so re-running and opening from a collapsed Stack
// still reach the layer that needs attention.
func (n node) runs() []githubclient.WorkflowRun {
	switch n.key.kind {
	case kindStack:
		var runs []githubclient.WorkflowRun
		for _, j := range n.group.prIdxs {
			runs = append(runs, n.repo.PRs[j].Runs...)
		}
		return runs
	case kindPR:
		return n.pr.Runs
	default:
		return n.repo.Runs
	}
}

// tree is the Dashboard tree: the rows under the current snapshot, which of
// them are expanded, and where the cursor is. It keeps expansion and the
// cursor attached to Repos, Stacks and PRs across every snapshot.
type tree struct {
	snapshot  state.Snapshot
	expanded  map[nodeKey]bool
	attention map[string]bool // last seen needsAttention per Repo, for auto-expand
	rows      []node
	cursor    int
}

func newTree(snap state.Snapshot) tree {
	t := tree{
		snapshot:  snap,
		expanded:  make(map[nodeKey]bool),
		attention: make(map[string]bool),
	}
	t.build()
	return t
}

// update moves the tree onto a new snapshot, auto-expanding Repos whose CI
// started or stopped needing attention. It reports whether any Repo opened.
func (t *tree) update(snap state.Snapshot) bool {
	sel, ok := t.selectedKey()
	t.snapshot = snap
	opened := t.autoExpand()
	t.build()
	if ok {
		t.restore(sel)
	} else {
		t.move(0)
	}
	return opened
}

// toggle expands or collapses the selected row. It reports whether that was a
// Repo, since only Repo expansion changes what the poller fetches.
func (t *tree) toggle() bool {
	n, ok := t.selected()
	if !ok {
		return false
	}
	t.expanded[n.key] = !t.expanded[n.key]
	t.build()
	t.restore(n.key)
	return n.key.kind == kindRepo
}

// move shifts the cursor by delta rows, staying on the tree.
func (t *tree) move(delta int) {
	t.cursor = max(0, min(t.cursor+delta, len(t.rows)-1))
}

func (t tree) selected() (node, bool) {
	if t.cursor < 0 || t.cursor >= len(t.rows) {
		return node{}, false
	}
	return t.rows[t.cursor], true
}

func (t tree) isExpanded(n node) bool { return t.expanded[n.key] }

// expandedRepos returns the "owner/name" of every expanded Repo, sorted.
func (t tree) expandedRepos() []string {
	var out []string
	for k, open := range t.expanded {
		if open && k.kind == kindRepo {
			out = append(out, k.repo)
		}
	}
	sort.Strings(out)
	return out
}

func (t tree) selectedKey() (nodeKey, bool) {
	n, ok := t.selected()
	return n.key, ok
}

func (t *tree) build() {
	repos := make([]state.RepoState, len(t.snapshot.Repos))
	copy(repos, t.snapshot.Repos)
	sort.SliceStable(repos, func(a, b int) bool {
		return repos[a].Stoplight.ActiveFirst() < repos[b].Stoplight.ActiveFirst()
	})

	// A fresh slice, not t.rows[:0]: Bubble Tea copies the model by value, and
	// an earlier copy must keep the rows it rendered.
	t.rows = nil
	for _, r := range repos {
		name := r.FullName()
		t.rows = append(t.rows, node{key: repoKey(name), repo: r})
		if !t.expanded[repoKey(name)] {
			continue
		}
		for _, g := range groupPRs(r.PRs) {
			if !g.isStack() {
				pr := r.PRs[g.prIdxs[0]]
				t.rows = append(t.rows, node{key: nodeKey{name, kindPR, pr.Number}, repo: r, pr: pr})
				continue
			}
			stack := node{key: nodeKey{name, kindStack, g.stackNum}, repo: r, group: g}
			t.rows = append(t.rows, stack)
			if !t.expanded[stack.key] {
				continue
			}
			for _, j := range g.prIdxs {
				pr := r.PRs[j]
				t.rows = append(t.rows, node{key: nodeKey{name, kindPR, pr.Number}, repo: r, pr: pr, inStack: true})
			}
		}
	}
}

// restore puts the cursor back on sel after a rebuild, falling back to sel's
// Repo row, then to the nearest row that still exists.
func (t *tree) restore(sel nodeKey) {
	repoRow := -1
	for i, n := range t.rows {
		if n.key == sel {
			t.cursor = i
			return
		}
		if repoRow < 0 && n.key == repoKey(sel.repo) {
			repoRow = i
		}
	}
	if repoRow >= 0 {
		t.cursor = repoRow
		return
	}
	t.move(0)
}

// needsAttention reports whether a Repo has CI running or failing, on its
// branch or on any PR the dashboard knows about.
func needsAttention(r state.RepoState) bool {
	active := func(s aggregator.Stoplight) bool {
		return s == aggregator.StoplightYellow || s == aggregator.StoplightRed
	}
	if active(r.Stoplight) {
		return true
	}
	for _, p := range r.PRs {
		if active(p.Stoplight) {
			return true
		}
	}
	return false
}

// autoExpand opens Repos that start needing attention and closes Repos that
// stop. It acts only when that state changes (or a Repo first appears), so a
// row the user opened or closed by hand stays that way until its CI moves.
// It reports whether any Repo was opened.
func (t *tree) autoExpand() bool {
	opened := false
	seen := make(map[string]bool, len(t.snapshot.Repos))
	for _, r := range t.snapshot.Repos {
		name := r.FullName()
		seen[name] = true
		now := needsAttention(r)
		if was, known := t.attention[name]; known && was == now {
			continue
		}
		t.attention[name] = now
		if now && !t.expanded[repoKey(name)] {
			opened = true
		}
		t.expanded[repoKey(name)] = now
	}
	for name := range t.attention {
		if !seen[name] {
			delete(t.attention, name)
		}
	}
	return opened
}
