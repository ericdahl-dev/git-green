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
	kindStack
	kindPR
)

// stackKey identifies an expanded stack. It carries the repo name rather than
// its index because a repo's index moves when the config reloads or the
// active-first sort shifts, which would otherwise reassign expansion state to
// whatever repo landed on that index.
type stackKey struct {
	repo  string
	stack int
}

type flatRow struct {
	kind     rowKind
	repoIdx  int
	prIdx    int  // only for kindPR
	groupIdx int  // index into the repo's prGroups, for kindStack and stacked kindPR
	inStack  bool // kindPR rendered inside an expanded stack
}

type Dashboard struct {
	snapshot      state.Snapshot
	rows          []flatRow
	cursor        int
	repoExp       map[string]bool
	stackExp      map[stackKey]bool
	prExp         map[[2]int]bool
	groups        map[int][]prGroup // repoIdx -> its PR groups, rebuilt with rows
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
		repoExp:      make(map[string]bool),
		stackExp:     make(map[stackKey]bool),
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

func (d *Dashboard) buildRows() []flatRow {
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

	d.groups = make(map[int][]prGroup, len(d.snapshot.Repos))
	var rows []flatRow
	for _, i := range repoOrder {
		r := d.snapshot.Repos[i]
		groups := groupPRs(r.PRs)
		d.groups[i] = groups
		rows = append(rows, flatRow{kind: kindRepo, repoIdx: i})
		if !d.repoExp[r.FullName()] {
			continue
		}
		for gi, g := range groups {
			if !g.isStack() {
				rows = append(rows, flatRow{kind: kindPR, repoIdx: i, prIdx: g.prIdxs[0], groupIdx: gi})
				continue
			}
			rows = append(rows, flatRow{kind: kindStack, repoIdx: i, groupIdx: gi})
			if !d.stackExp[stackKey{repo: r.FullName(), stack: g.stackNum}] {
				continue
			}
			for _, j := range g.prIdxs {
				rows = append(rows, flatRow{kind: kindPR, repoIdx: i, prIdx: j, groupIdx: gi, inStack: true})
			}
		}
	}
	return rows
}

// ExpandedReposMsg carries the repos whose rows are currently open, so the
// poller can fetch job and PR-run detail for those and skip it elsewhere.
type ExpandedReposMsg struct {
	Repos []string
}

// ExpandedRepos returns the "owner/name" of every currently expanded repo.
func (d Dashboard) ExpandedRepos() []string {
	var out []string
	for name, open := range d.repoExp {
		if open {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

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
				key := d.snapshot.Repos[row.repoIdx].FullName()
				d.repoExp[key] = !d.repoExp[key]
				d.rows = d.buildRows()
				if d.cursor >= len(d.rows) {
					d.cursor = len(d.rows) - 1
				}
				// Expanding a repo asks for detail the poller was not
				// fetching, so tell it and refresh rather than leaving the
				// row empty until the next tick.
				return d, tea.Batch(selectionTimeoutCmd(), expandedChangedCmd(d.ExpandedRepos()))
			case kindStack:
				g := d.group(row)
				if g == nil {
					break
				}
				// A stack's members live under an already-expanded repo, so
				// their detail is on hand — no refetch needed here.
				key := stackKey{repo: d.snapshot.Repos[row.repoIdx].FullName(), stack: g.stackNum}
				d.stackExp[key] = !d.stackExp[key]
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
	for _, run := range d.rowRuns(row) {
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

// group returns the prGroup a stack row or stacked PR row belongs to.
func (d Dashboard) group(row flatRow) *prGroup {
	groups := d.groups[row.repoIdx]
	if row.groupIdx >= len(groups) {
		return nil
	}
	return &groups[row.groupIdx]
}

// rowRuns returns the workflow runs a row stands for. A stack row stands for
// every run across its members, bottom to top, so re-running and opening from a
// collapsed stack still reach the layer that needs attention.
func (d Dashboard) rowRuns(row flatRow) []githubclient.WorkflowRun {
	repo := d.snapshot.Repos[row.repoIdx]
	switch row.kind {
	case kindStack:
		g := d.group(row)
		if g == nil {
			return nil
		}
		var runs []githubclient.WorkflowRun
		for _, j := range g.prIdxs {
			if j < len(repo.PRs) {
				runs = append(runs, repo.PRs[j].Runs...)
			}
		}
		return runs
	case kindPR:
		if row.prIdx >= len(repo.PRs) {
			return nil
		}
		return repo.PRs[row.prIdx].Runs
	default:
		return repo.Runs
	}
}

// Throttles reports the tokens the poller has slowed down, for the title bar.
func (d Dashboard) Throttles() []state.Throttle { return d.snapshot.Throttles }

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
	runs := d.rowRuns(d.rows[d.cursor])
	if len(runs) == 0 {
		return ""
	}
	return runs[0].HTMLURL
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
			expanded := d.repoExp[r.FullName()]
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
			g := d.group(row)
			if g == nil {
				break
			}
			tri := "▶"
			if d.stackExp[stackKey{repo: r.FullName(), stack: g.stackNum}] {
				tri = "▼"
			}
			line := prIndent + tri + " " + g.title()
			if selected {
				out += selectedStyle.Render(line) + "\n"
			} else {
				out += normalStyle.Render(line) + "\n"
			}

		case kindPR:
			pr := r.PRs[row.prIdx]
			prExpanded := d.prExp[[2]int{row.repoIdx, row.prIdx}]
			tri := "▶"
			if prExpanded {
				tri = "▼"
			}
			indent := prIndent
			position := ""
			if row.inStack {
				indent = stackPRIndent
				position = fmt.Sprintf("%d/%d  ", pr.Stack.Position, pr.Stack.Size)
			}
			line := fmt.Sprintf("%s  %sPR #%d · %s", pr.Stoplight.String(), position, pr.Number, pr.Title)
			if selected {
				out += selectedStyle.Render(indent+tri+" "+line) + "\n"
			} else {
				out += normalStyle.Render(indent+tri+" "+line) + "\n"
			}
			if prExpanded {
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

func renderPRRuns(pr state.PRState, indent string) string {
	if len(pr.Runs) == 0 {
		return staleStyle.Render(indent+"no runs") + "\n"
	}
	jobIndent := indent + "    "
	out := ""
	for _, run := range pr.Runs {
		status := run.Conclusion
		if status == "" {
			status = run.Status
		}
		out += wfStyle.Render(fmt.Sprintf("%s%s  %s", indent, workflowStatusIcon(status), run.WorkflowName)) + "\n"
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
