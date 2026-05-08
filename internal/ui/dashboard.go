package ui

import (
	"fmt"
	"sort"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/ericdahl-dev/git-green/internal/aggregator"
	"github.com/ericdahl-dev/git-green/internal/state"
)

var (
	titleStyle    = lipgloss.NewStyle().Bold(true).MarginBottom(1)
	selectedStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("212"))
	normalStyle   = lipgloss.NewStyle()
	staleStyle    = lipgloss.NewStyle().Faint(true)
	hintStyle     = lipgloss.NewStyle().Faint(true)
	wfStyle       = lipgloss.NewStyle().Faint(false)
	branchIndent  = "      "
	prIndent      = "      "
	wfIndent      = "          "
	jobIndent     = "              "
)

const selectionTimeout = 10 * time.Second

type selectionExpiredMsg struct{}

type rowKind int

const (
	kindRepo rowKind = iota
	kindPR
)

type flatRow struct {
	kind    rowKind
	repoIdx int
	prIdx   int // only for kindPR
}

type Dashboard struct {
	snapshot      state.Snapshot
	rows          []flatRow
	cursor        int
	repoExp       map[int]bool
	prExp         map[[2]int]bool
	lastActivity  time.Time
	selectionFade bool
}

func NewDashboard(snap state.Snapshot) Dashboard {
	d := Dashboard{
		snapshot:     snap,
		repoExp:      make(map[int]bool),
		prExp:        make(map[[2]int]bool),
		lastActivity: time.Now(),
	}
	d.rows = d.buildRows()
	return d
}

// stoplightPriority returns sort order: yellow (active) first, then red, green, grey.
func stoplightPriority(s aggregator.Stoplight) int {
	switch s {
	case aggregator.StoplightYellow:
		return 0
	case aggregator.StoplightRed:
		return 1
	case aggregator.StoplightGreen:
		return 2
	default:
		return 3
	}
}

func (d Dashboard) buildRows() []flatRow {
	// Build sorted repo index order: yellow first, then red, green, grey.
	repoOrder := make([]int, len(d.snapshot.Repos))
	for i := range repoOrder {
		repoOrder[i] = i
	}
	sort.SliceStable(repoOrder, func(a, b int) bool {
		pa := stoplightPriority(d.snapshot.Repos[repoOrder[a]].Stoplight)
		pb := stoplightPriority(d.snapshot.Repos[repoOrder[b]].Stoplight)
		return pa < pb
	})

	var rows []flatRow
	for _, i := range repoOrder {
		r := d.snapshot.Repos[i]
		rows = append(rows, flatRow{kind: kindRepo, repoIdx: i})
		if d.repoExp[i] {
			// Sort PRs: yellow first, then red, green, grey.
			prOrder := make([]int, len(r.PRs))
			for j := range prOrder {
				prOrder[j] = j
			}
			sort.SliceStable(prOrder, func(a, b int) bool {
				pa := stoplightPriority(r.PRs[prOrder[a]].Stoplight)
				pb := stoplightPriority(r.PRs[prOrder[b]].Stoplight)
				return pa < pb
			})
			for _, j := range prOrder {
				rows = append(rows, flatRow{kind: kindPR, repoIdx: i, prIdx: j})
			}
		}
	}
	return rows
}

func selectionTimeoutCmd() tea.Cmd {
	return tea.Tick(selectionTimeout, func(time.Time) tea.Msg {
		return selectionExpiredMsg{}
	})
}

func (d Dashboard) Init() tea.Cmd { return selectionTimeoutCmd() }

func (d Dashboard) Update(msg tea.Msg) (Dashboard, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		d.lastActivity = time.Now()
		d.selectionFade = false
		switch msg.String() {
		case "up", "k":
			if d.cursor > 0 {
				d.cursor--
			}
		case "down", "j":
			if d.cursor < len(d.rows)-1 {
				d.cursor++
			}
		case "enter", " ":
			if len(d.rows) == 0 {
				break
			}
			row := d.rows[d.cursor]
			switch row.kind {
			case kindRepo:
				d.repoExp[row.repoIdx] = !d.repoExp[row.repoIdx]
				d.rows = d.buildRows()
				if d.cursor >= len(d.rows) {
					d.cursor = len(d.rows) - 1
				}
			case kindPR:
				key := [2]int{row.repoIdx, row.prIdx}
				d.prExp[key] = !d.prExp[key]
			}
		}
		return d, selectionTimeoutCmd()
	case selectionExpiredMsg:
		if time.Since(d.lastActivity) >= selectionTimeout {
			d.selectionFade = true
		}
	case state.Snapshot:
		d.snapshot = msg
		d.rows = d.buildRows()
		if d.cursor >= len(d.rows) && len(d.rows) > 0 {
			d.cursor = len(d.rows) - 1
		}
	}
	return d, nil
}

func (d Dashboard) SelectedRepo() *state.RepoState {
	if len(d.rows) == 0 {
		return nil
	}
	r := d.snapshot.Repos[d.rows[d.cursor].repoIdx]
	return &r
}

func (d Dashboard) View() string {
	out := titleStyle.Render("git-green") + "\n"

	if len(d.snapshot.Repos) == 0 {
		out += staleStyle.Render("  No repos configured.") + "\n"
	}

	cursorRow := -1
	if len(d.rows) > 0 {
		cursorRow = d.cursor
	}

	rowIdx := 0
	for i, r := range d.snapshot.Repos {
		expanded := d.repoExp[i]

		// Repo row
		triangle := "▶"
		if expanded {
			triangle = "▼"
		}
		repoLine := repoRow(r)
		if rowIdx == cursorRow && !d.selectionFade {
			out += selectedStyle.Render(triangle+" "+repoLine) + "\n"
		} else {
			out += normalStyle.Render("  "+repoLine) + "\n"
		}
		rowIdx++

		if !expanded {
			continue
		}

		// Default branch section
		out += renderBranchSection(r)

		// PR rows
		for j, pr := range r.PRs {
			prExpanded := d.prExp[[2]int{i, j}]
			tri := "▶"
			if prExpanded {
				tri = "▼"
			}
			prLine := fmt.Sprintf("%s  PR #%d · %s", pr.Stoplight.String(), pr.Number, pr.Title)
			if rowIdx == cursorRow && !d.selectionFade {
				out += selectedStyle.Render(prIndent+tri+" "+prLine) + "\n"
			} else {
				out += normalStyle.Render(prIndent+tri+" "+prLine) + "\n"
			}
			rowIdx++

			if prExpanded {
				out += renderPRRuns(pr)
			}
		}
	}

	out += "\n" + hintStyle.Render("↑/↓ navigate  enter/space expand  o open  r refresh  q quit  ? help")
	return out
}

func renderBranchSection(r state.RepoState) string {
	if r.Err != nil && len(r.Runs) == 0 {
		return jobRed.Render(branchIndent+"⚠ "+r.Err.Error()) + "\n"
	}
	if len(r.Runs) == 0 {
		return staleStyle.Render(branchIndent+"no branch runs") + "\n"
	}
	branch := r.BranchName()
	out := staleStyle.Render(branchIndent+"branch: "+branch) + "\n"
	for _, run := range r.Runs {
		status := run.Conclusion
		if status == "" {
			status = run.Status
		}
		out += wfStyle.Render(fmt.Sprintf("%s%s  %s", wfIndent, workflowStatusIcon(status), run.WorkflowName)) + "\n"
		for _, job := range run.Jobs {
			jobStatus := job.Conclusion
			if jobStatus == "" {
				jobStatus = job.Status
			}
			out += fmt.Sprintf("%s%s  %s\n", jobIndent, jobStatusIcon(jobStatus), job.Name)
		}
	}
	return out
}

func renderPRRuns(pr state.PRState) string {
	if len(pr.Runs) == 0 {
		return staleStyle.Render(wfIndent+"no runs") + "\n"
	}
	out := ""
	for _, run := range pr.Runs {
		status := run.Conclusion
		if status == "" {
			status = run.Status
		}
		out += wfStyle.Render(fmt.Sprintf("%s%s  %s", wfIndent, workflowStatusIcon(status), run.WorkflowName)) + "\n"
		for _, job := range run.Jobs {
			jobStatus := job.Conclusion
			if jobStatus == "" {
				jobStatus = job.Status
			}
			out += fmt.Sprintf("%s%s  %s\n", jobIndent, jobStatusIcon(jobStatus), job.Name)
		}
	}
	return out
}

func repoRow(r state.RepoState) string {
	icon := r.Stoplight.String()
	name := r.FullName()
	summary := workflowSummary(r)
	row := fmt.Sprintf("%s  %-40s %s", icon, name, summary)
	if r.IsStale() {
		age := time.Since(*r.StaleAt).Round(time.Second)
		row = staleStyle.Render(row + fmt.Sprintf("  ⚠ last seen %s ago", age))
	}
	return row
}

func workflowSummary(r state.RepoState) string {
	if r.Err != nil && len(r.Runs) == 0 && len(r.PRs) == 0 {
		return "error"
	}
	if len(r.PRs) > 0 {
		open := len(r.PRs)
		if open == 1 {
			return "1 PR open"
		}
		return fmt.Sprintf("%d PRs open", open)
	}
	if len(r.Runs) == 0 {
		return "no runs"
	}
	for _, run := range r.Runs {
		s := run.Conclusion
		if s == "" {
			s = run.Status
		}
		if aggregator.Aggregate([]aggregator.RunStatus{aggregator.RunStatus(s)}) == r.Stoplight {
			if s == "" {
				s = "unknown"
			}
			return fmt.Sprintf("%s · %s", run.WorkflowName, s)
		}
	}
	run := r.Runs[0]
	s := run.Conclusion
	if s == "" {
		s = run.Status
	}
	return fmt.Sprintf("%s · %s", run.WorkflowName, s)
}
