package ui

import (
	"context"
	"fmt"
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
	stackPRIndent = "            "
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

type Dashboard struct {
	tree          tree
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
// through r. Re-runs fire against ctx so they are canceled when the app exits.
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
	return Dashboard{tree: newTree(snap), lastActivity: time.Now()}
}

// ExpandedReposMsg carries the repos whose rows are currently open, so the
// poller can fetch job and PR-run detail for those and skip it elsewhere.
type ExpandedReposMsg struct {
	Repos []string
}

// ExpandedRepos returns the "owner/name" of every currently expanded repo.
func (d Dashboard) ExpandedRepos() []string { return d.tree.expandedRepos() }

func expandedChangedCmd(repos []string) tea.Cmd {
	return func() tea.Msg { return ExpandedReposMsg{Repos: repos} }
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
			d.tree.move(-1)
		case "down", "j":
			d.tree.move(1)
		case "enter", " ":
			// Expanding a Repo asks for detail the poller was not fetching,
			// so tell it and refresh rather than leaving the row empty until
			// the next tick. Stacks and PRs live under an already-expanded
			// Repo, so their detail is on hand.
			if d.tree.toggle() {
				return d, tea.Batch(selectionTimeoutCmd(), expandedChangedCmd(d.ExpandedRepos()))
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
		opened := d.tree.update(msg)
		var cmd tea.Cmd
		if opened {
			// Newly opened repos need the job and PR-run detail the poller
			// skips for collapsed ones.
			cmd = expandedChangedCmd(d.ExpandedRepos())
		}
		// The poll that follows a successful re-run shows the new run, so the
		// transient notice has done its job. Errors stay until they time out.
		if d.rerunStatus == rerunShowResult && !d.rerunFailure {
			d.clearRerunResult()
		}
		return d, cmd
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
	n, ok := d.tree.selected()
	if !ok {
		return nil
	}
	for _, run := range n.runs() {
		// Only a finished, red Run can be re-run; one still going is yellow.
		if aggregator.Of(run.Conclusion) != aggregator.StoplightRed || run.RunID == 0 {
			continue
		}
		return &rerunTarget{
			owner:    n.repo.Owner,
			name:     n.repo.Name,
			runID:    run.RunID,
			workflow: run.WorkflowName,
		}
	}
	return nil
}

// Throttles reports the tokens the poller has slowed down, for the title bar.
func (d Dashboard) Throttles() []state.Throttle { return d.tree.snapshot.Throttles }

func (d Dashboard) SelectedRepo() *state.RepoState {
	n, ok := d.tree.selected()
	if !ok {
		return nil
	}
	return &n.repo
}

// SelectedRunURL returns the HTML URL of the primary workflow run for the
// selected row, if any.
func (d Dashboard) SelectedRunURL() string {
	n, ok := d.tree.selected()
	if !ok {
		return ""
	}
	runs := n.runs()
	if len(runs) == 0 {
		return ""
	}
	return runs[0].HTMLURL
}

// BodyView renders the dashboard without the app title (the root model prepends title and spinner).
func (d Dashboard) BodyView() string {
	out := ""

	if len(d.tree.snapshot.Repos) == 0 {
		out += staleStyle.Render("  No repos configured.") + "\n"
	}

	for rowIdx, n := range d.tree.rows {
		selected := rowIdx == d.tree.cursor && !d.selectionFade
		r := n.repo
		expanded := d.tree.isExpanded(n)

		switch n.key.kind {
		case kindRepo:
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

		case kindStack:
			tri := "▶"
			if expanded {
				tri = "▼"
			}
			line := prIndent + tri + " " + n.group.title()
			if selected {
				out += selectedStyle.Render(line) + "\n"
			} else {
				out += normalStyle.Render(line) + "\n"
			}

		case kindPR:
			pr := n.pr
			tri := "▶"
			if expanded {
				tri = "▼"
			}
			indent := prIndent
			position := ""
			if n.inStack {
				indent = stackPRIndent
				position = fmt.Sprintf("%d/%d  ", pr.Stack.Position, pr.Stack.Size)
			}
			line := fmt.Sprintf("%s  %sPR #%d%s · %s", pr.Stoplight.String(), position, pr.Number, reviewGlyph(pr.Review), pr.Title)
			if selected {
				out += selectedStyle.Render(indent+tri+" "+line) + "\n"
			} else {
				out += normalStyle.Render(indent+tri+" "+line) + "\n"
			}
			if expanded {
				out += renderPRRuns(pr, indent+"    ")
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
		out += wfStyle.Render(fmt.Sprintf("%s%s  %s", wfIndent, runIcon(run.Effective()), run.WorkflowName)) + "\n"
		for _, job := range run.Jobs {
			out += fmt.Sprintf("%s%s  %s\n", jobIndent, runIcon(job.Effective()), job.Name)
		}
	}
	return out
}

func renderPRRuns(pr state.PRState, indent string) string {
	if len(pr.Runs) == 0 {
		return staleStyle.Render(indent+"no runs") + "\n"
	}
	jobIndent := indent + "    "
	out := ""
	for _, run := range pr.Runs {
		out += wfStyle.Render(fmt.Sprintf("%s%s  %s", indent, runIcon(run.Effective()), run.WorkflowName)) + "\n"
		for _, job := range run.Jobs {
			out += fmt.Sprintf("%s%s  %s\n", jobIndent, runIcon(job.Effective()), job.Name)
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
		if aggregator.Of(run.Effective()) == r.Stoplight {
			return runSummary(run)
		}
	}
	return runSummary(r.Runs[0])
}

func runSummary(run githubclient.WorkflowRun) string {
	s := run.Effective()
	if s == "" {
		s = "unknown"
	}
	return fmt.Sprintf("%s · %s", run.WorkflowName, s)
}
