package ui

import (
	"fmt"
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
	prIndent      = "      "
	wfIndent      = "          "
	jobIndent     = "              "
)

const selectionTimeout = 10 * time.Second

type selectionExpiredMsg struct{}

type Dashboard struct {
	snapshot      state.Snapshot
	cursor        int
	expanded      map[int]bool
	lastActivity  time.Time
	selectionFade bool
}

func NewDashboard(snap state.Snapshot) Dashboard {
	return Dashboard{snapshot: snap, expanded: make(map[int]bool), lastActivity: time.Now()}
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
			if d.cursor < len(d.snapshot.Repos)-1 {
				d.cursor++
			}
		case "enter", " ":
			if d.expanded == nil {
				d.expanded = make(map[int]bool)
			}
			d.expanded[d.cursor] = !d.expanded[d.cursor]
		}
		return d, selectionTimeoutCmd()
	case selectionExpiredMsg:
		if time.Since(d.lastActivity) >= selectionTimeout {
			d.selectionFade = true
		}
	case state.Snapshot:
		d.snapshot = msg
		if d.cursor >= len(d.snapshot.Repos) && len(d.snapshot.Repos) > 0 {
			d.cursor = len(d.snapshot.Repos) - 1
		}
	}
	return d, nil
}

func (d Dashboard) SelectedRepo() *state.RepoState {
	if len(d.snapshot.Repos) == 0 {
		return nil
	}
	r := d.snapshot.Repos[d.cursor]
	return &r
}

func (d Dashboard) View() string {
	out := titleStyle.Render("git-green") + "\n"

	if len(d.snapshot.Repos) == 0 {
		out += staleStyle.Render("  No repos configured.") + "\n"
	}

	for i, r := range d.snapshot.Repos {
		expanded := d.expanded[i]
		triangle := "▶"
		if expanded {
			triangle = "▼"
		}

		row := repoRow(r)
		if i == d.cursor && !d.selectionFade {
			out += selectedStyle.Render(triangle+" "+row) + "\n"
		} else {
			out += normalStyle.Render("  "+row) + "\n"
		}

		if expanded {
			out += renderTree(r)
		}
	}

	out += "\n" + hintStyle.Render("↑/↓ navigate  enter/space expand  o open  r refresh  q quit  ? help")
	return out
}

func renderTree(r state.RepoState) string {
	out := ""
	if r.Err != nil && len(r.Runs) == 0 && len(r.PRs) == 0 {
		out += jobRed.Render(prIndent+"⚠ "+r.Err.Error()) + "\n"
		return out
	}

	if len(r.PRs) > 0 {
		for _, pr := range r.PRs {
			out += wfStyle.Render(fmt.Sprintf("%s%s  PR #%d · %s", prIndent, pr.Stoplight.String(), pr.Number, pr.Title)) + "\n"
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
		}
		return out
	}

	if len(r.Runs) == 0 {
		out += staleStyle.Render(prIndent+"no runs") + "\n"
		return out
	}
	for _, run := range r.Runs {
		status := run.Conclusion
		if status == "" {
			status = run.Status
		}
		out += wfStyle.Render(fmt.Sprintf("%s%s  %s", prIndent, workflowStatusIcon(status), run.WorkflowName)) + "\n"
		for _, job := range run.Jobs {
			jobStatus := job.Conclusion
			if jobStatus == "" {
				jobStatus = job.Status
			}
			out += fmt.Sprintf("%s%s  %s\n", wfIndent, jobStatusIcon(jobStatus), job.Name)
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
