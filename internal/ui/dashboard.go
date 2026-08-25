package ui

import (
	"context"
	"fmt"
	"sort"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/ericdahl-dev/git-green/internal/aggregator"
	githubclient "github.com/ericdahl-dev/git-green/internal/github"
	"github.com/ericdahl-dev/git-green/internal/state"
)

var (
	selectedStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("212"))
	normalStyle   = lipgloss.NewStyle()
	staleStyle    = lipgloss.NewStyle().Faint(true)
	hintStyle     = lipgloss.NewStyle().Faint(true)
	confirmStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("226"))
	successStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("82"))
	errorStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("196"))
	wfStyle       = lipgloss.NewStyle().Faint(false)
	branchIndent  = "      "
	prIndent      = "      "
	wfIndent      = "          "
	jobIndent     = "              "
)

const selectionTimeout = 10 * time.Second

// rerunResultTimeout bounds how long the re-run outcome sits in the hint line.
// A success normally clears sooner, on the first snapshot that follows it.
const rerunResultTimeout = 8 * time.Second

type selectionExpiredMsg struct{}
type rerunDoneMsg struct{ err error }
type rerunResultExpiredMsg struct{}

// Rerunner re-runs a workflow run. The dashboard holds one so tests can
// substitute a fake for the GitHub client.
type Rerunner interface {
	RerunFailedJobs(ctx context.Context, owner, name string, runID int64) error
}

type rerunState int

const (
	rerunIdle rerunState = iota
	rerunConfirming
	rerunExecuting
	rerunShowResult
)

// rerunTarget identifies the workflow run the f key would re-run.
type rerunTarget struct {
	owner    string
	name     string
	runID    int64
	workflow string
}

func (t rerunTarget) fullName() string { return t.owner + "/" + t.name }

// runFailed reports whether a run finished in a state worth re-running. It
// covers the conclusions the dashboard paints red plus startup_failure, which
// is a failure GitHub reports without ever starting a job.
func runFailed(run githubclient.WorkflowRun) bool {
	switch run.Conclusion {
	case "failure", "timed_out", "action_required", "startup_failure":
		return true
	}
	return false
}

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

	rerunner     Rerunner
	rerunCtx     context.Context
	rerunStatus  rerunState
	rerunTarget  *rerunTarget
	rerunMsg     string
	rerunFailure bool
}

// WithRerunner returns a copy of the dashboard wired to re-run failed runs
// through r. Re-runs fire against ctx so they are cancelled when the app exits.
func (d Dashboard) WithRerunner(ctx context.Context, r Rerunner) Dashboard {
	d.rerunCtx = ctx
	d.rerunner = r
	return d
}

// AwaitingConfirm reports whether the dashboard is holding a re-run
// confirmation, in which case the root model must hand it every key.
func (d Dashboard) AwaitingConfirm() bool {
	return d.rerunStatus == rerunConfirming
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

func rerunResultExpiredCmd() tea.Cmd {
	return tea.Tick(rerunResultTimeout, func(time.Time) tea.Msg {
		return rerunResultExpiredMsg{}
	})
}

func (d Dashboard) Init() tea.Cmd { return selectionTimeoutCmd() }

func (d Dashboard) Update(msg tea.Msg) (Dashboard, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		// While confirming a re-run, only enter/esc are meaningful.
		if d.rerunStatus == rerunConfirming {
			switch msg.String() {
			case "enter":
				return d.startRerun()
			case "esc":
				d.rerunStatus = rerunIdle
				d.rerunTarget = nil
			}
			return d, nil
		}

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
		case "f":
			// Only a row whose run actually failed can be re-run.
			if target := d.selectedRerunTarget(); target != nil && d.rerunner != nil {
				d.rerunStatus = rerunConfirming
				d.rerunTarget = target
			}
		}
		return d, selectionTimeoutCmd()
	case selectionExpiredMsg:
		if time.Since(d.lastActivity) >= selectionTimeout {
			d.selectionFade = true
		}
	case rerunDoneMsg:
		d.rerunStatus = rerunShowResult
		if msg.err != nil {
			d.rerunFailure = true
			d.rerunMsg = fmt.Sprintf("re-run failed: %v", msg.err)
		} else {
			d.rerunFailure = false
			d.rerunMsg = fmt.Sprintf("↻ re-run requested · %s", d.rerunTarget.workflow)
		}
		return d, rerunResultExpiredCmd()
	case rerunResultExpiredMsg:
		d.clearRerunResult()
	case state.Snapshot:
		d.snapshot = msg
		d.rows = d.buildRows()
		if d.cursor >= len(d.rows) && len(d.rows) > 0 {
			d.cursor = len(d.rows) - 1
		}
		// The poll that follows a successful re-run shows the new run, so the
		// transient notice has done its job. Errors stay until they time out.
		if d.rerunStatus == rerunShowResult && !d.rerunFailure {
			d.clearRerunResult()
		}
	}
	return d, nil
}

// startRerun moves out of the confirmation and fires the re-run in a command.
func (d Dashboard) startRerun() (Dashboard, tea.Cmd) {
	d.rerunStatus = rerunExecuting
	target := *d.rerunTarget
	rerunner := d.rerunner
	ctx := d.rerunCtx
	if ctx == nil {
		ctx = context.Background()
	}
	return d, func() tea.Msg {
		return rerunDoneMsg{err: rerunner.RerunFailedJobs(ctx, target.owner, target.name, target.runID)}
	}
}

func (d *Dashboard) clearRerunResult() {
	d.rerunStatus = rerunIdle
	d.rerunTarget = nil
	d.rerunMsg = ""
	d.rerunFailure = false
}

// selectedRerunTarget returns the first failed run on the selected row, or nil
// when the row is green, still running, or has no runs at all.
func (d Dashboard) selectedRerunTarget() *rerunTarget {
	if len(d.rows) == 0 {
		return nil
	}
	row := d.rows[d.cursor]
	repo := d.snapshot.Repos[row.repoIdx]
	runs := repo.Runs
	if row.kind == kindPR {
		if row.prIdx >= len(repo.PRs) {
			return nil
		}
		runs = repo.PRs[row.prIdx].Runs
	}
	for _, run := range runs {
		if !runFailed(run) || run.RunID == 0 {
			continue
		}
		return &rerunTarget{
			owner:    repo.Owner,
			name:     repo.Name,
			runID:    run.RunID,
			workflow: run.WorkflowName,
		}
	}
	return nil
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

	out += "\n" + d.hintLine()
	return out
}

// hintLine renders the footer, which doubles as the re-run confirmation prompt
// and result banner.
func (d Dashboard) hintLine() string {
	switch d.rerunStatus {
	case rerunConfirming:
		return confirmStyle.Render(fmt.Sprintf("re-run %s on %s?  [enter] confirm  [esc] cancel",
			d.rerunTarget.workflow, d.rerunTarget.fullName()))
	case rerunExecuting:
		return hintStyle.Render(fmt.Sprintf("re-running %s…", d.rerunTarget.workflow))
	case rerunShowResult:
		if d.rerunFailure {
			return errorStyle.Render(d.rerunMsg)
		}
		return successStyle.Render(d.rerunMsg)
	default:
		return hintStyle.Render("↑/↓ navigate  enter/space expand  f re-run  o open  r refresh  m manage  q quit  ? help")
	}
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
