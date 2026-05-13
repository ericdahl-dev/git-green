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

// SelectedRunURL returns the HTML URL of the primary workflow run for the
// selected repo row or PR row, if any.
func (d Dashboard) SelectedRunURL() string {
	if len(d.rows) == 0 {
		return ""
	}
	row := d.rows[d.cursor]
	repo := d.snapshot.Repos[row.repoIdx]
	switch row.kind {
	case kindPR:
		if row.prIdx >= len(repo.PRs) {
			return ""
		}
		pr := repo.PRs[row.prIdx]
		if len(pr.Runs) > 0 {
			return pr.Runs[0].HTMLURL
		}
	default:
		if len(repo.Runs) > 0 {
			return repo.Runs[0].HTMLURL
		}
	}
	return ""
}

// BodyView renders the dashboard without the app title (the root model prepends title and spinner).
func (d Dashboard) BodyView() string {
	out := ""

	if len(d.snapshot.Repos) == 0 {
		out += staleStyle.Render("  No repos configured.") + "\n"
	}

	for rowIdx, row := range d.rows {
		selected := rowIdx == d.cursor && !d.selectionFade
		r := d.snapshot.Repos[row.repoIdx]

		switch row.kind {
		case kindRepo:
			expanded := d.repoExp[row.repoIdx]
			triangle := "▶"
			if expanded {
				triangle = "▼"
			}
			line := repoRow(r)
			if selected {
				out += selectedStyle.Render(triangle+" "+line) + "\n"
			} else {
				out += normalStyle.Render("  "+line) + "\n"
			}
			if expanded {
				out += renderBranchSection(r)
			}

		case kindPR:
			pr := r.PRs[row.prIdx]
			prExpanded := d.prExp[[2]int{row.repoIdx, row.prIdx}]
			tri := "▶"
			if prExpanded {
				tri = "▼"
			}
			line := fmt.Sprintf("%s  PR #%d · %s", pr.Stoplight.String(), pr.Number, pr.Title)
			if selected {
				out += selectedStyle.Render(prIndent+tri+" "+line) + "\n"
			} else {
				out += normalStyle.Render(prIndent+tri+" "+line) + "\n"
			}
			if prExpanded {
				out += renderPRRuns(pr)
			}
		}
	}

	out += "\n" + hintStyle.Render("↑/↓ navigate  enter/space expand  o open  r refresh  q quit  ? help")
	return out
}

func (d Dashboard) View() string {
	return d.BodyView()
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
