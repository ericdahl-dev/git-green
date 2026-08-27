package githubclient

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/ericdahl-dev/git-green/internal/logx"
	"github.com/google/go-github/v72/github"
	"golang.org/x/oauth2"
)

// Job represents a single job within a workflow run.
type Job struct {
	Name       string
	Status     string
	Conclusion string
}

// WorkflowRun holds the latest run for a single workflow.
type WorkflowRun struct {
	WorkflowName string
	Status       string
	Conclusion   string
	HTMLURL      string
	RunID        int64
	Jobs         []Job
}

// PR represents an open pull request.
type PR struct {
	Number    int
	Title     string
	HeadSHA   string
	HTMLURL   string
	Mergeable string // "clean", "conflicting", "unknown", or "" if not yet computed
	Stack     *Stack // non-nil when the PR is part of a stack
}

// PRRun groups an open PR with its workflow runs.
type PRRun struct {
	PR   PR
	Runs []WorkflowRun
}

// RepoData holds all CI data fetched in a single pass for a repo.
type RepoData struct {
	BranchRuns     []WorkflowRun
	PRRuns         []PRRun
	ResolvedBranch string // the branch that was actually queried
}

// RepoQuery describes what to fetch for a single repo.
type RepoQuery struct {
	Owner     string
	Name      string
	Branch    string   // empty = use repo default branch
	Workflows []string // nil = all workflows
	// Detail requests the per-run job lists and the per-PR workflow runs.
	// Both are only rendered when a repo row is expanded, and both cost one
	// API call each — per workflow and per open PR respectively — so a
	// collapsed repo skips them and costs two calls per poll instead of
	// two plus the size of the repo.
	Detail bool
}

// dependabotEvent is the event GitHub assigns to the runs Dependabot generates
// for its own update jobs. Those runs are named after the update
// ("npm_and_yarn in /. for axios, ...") rather than after a workflow, so they
// never collapse in a name-keyed dedupe and can swamp a repo's branch section.
const dependabotEvent = "dynamic"

func newFilterSet(workflows []string) map[string]bool {
	set := make(map[string]bool, len(workflows))
	for _, wf := range workflows {
		set[wf] = true
	}
	return set
}

// runKey returns the dedupe key for a run: the workflow ID, which is stable
// across runs of the same workflow file. Falls back to the run name when
// GitHub omits the ID.
func runKey(run *github.WorkflowRun) string {
	if id := run.GetWorkflowID(); id != 0 {
		return strconv.FormatInt(id, 10)
	}
	return "name:" + run.GetName()
}

// keepRun reports whether a run belongs in the dashboard: Dependabot's own
// update runs are dropped, and when the query names workflows only those are
// kept.
func keepRun(run *github.WorkflowRun, filterSet map[string]bool) bool {
	if run.GetEvent() == dependabotEvent {
		return false
	}
	if len(filterSet) > 0 && !filterSet[run.GetName()] {
		return false
	}
	return true
}

// Client fetches CI data from GitHub.
type Client struct {
	gh   *github.Client
	http *http.Client // same auth as gh, used for the GraphQL stack query
	// graphQLURL is a field so tests can point it at a stub server.
	graphQLURL string
}

// New creates a Client authenticated with the given token.
func New(token string) *Client {
	ts := oauth2.StaticTokenSource(&oauth2.Token{AccessToken: token})
	tc := oauth2.NewClient(context.Background(), ts)
	return &Client{gh: github.NewClient(tc), http: tc, graphQLURL: graphQLEndpoint}
}

// FetchAll fetches branch runs and open-PR runs with minimal API calls.
//
// Collapsed (q.Detail false) — 2 calls, regardless of repo size:
//   - 1 call for branch runs (ListRepositoryWorkflowRuns filtered by branch)
//   - 1 call to list open PRs
//
// Expanded (q.Detail true) adds the detail the dashboard actually renders:
//   - 1 call per branch workflow run to fetch jobs
//   - 1 call per PR for its runs (filtered by head SHA)
//   - 1 GraphQL call for stack membership, when 2+ PRs are open
//
// A repo with 4 workflows and 11 open PRs therefore costs 2 calls collapsed
// and 17 REST plus 1 GraphQL expanded. No ListWorkflows call in either case.
func (c *Client) FetchAll(ctx context.Context, q RepoQuery) (RepoData, error) {
	// Fetch the latest run per workflow on the default/configured branch.
	branchRuns, resolvedBranch, err := c.fetchBranchRuns(ctx, q)
	if err != nil {
		return RepoData{}, err
	}

	// Fetch open PRs — 1 call.
	prs, _, err := c.gh.PullRequests.List(ctx, q.Owner, q.Name, &github.PullRequestListOptions{
		State:       "open",
		ListOptions: github.ListOptions{PerPage: 50},
	})
	if err != nil {
		return RepoData{}, fmt.Errorf("listing PRs for %s/%s: %w", q.Owner, q.Name, err)
	}

	// Stack rows only render under an expanded repo, and a stack needs at
	// least two PRs, so anything else is not worth the extra call. A failure
	// here costs only the grouping, never the CI data.
	var stacks map[int]Stack
	if q.Detail && len(prs) > 1 {
		fetched, err := c.fetchStacks(ctx, q.Owner, q.Name)
		if err != nil {
			logx.Debug("stacks unavailable", "repo", q.Owner+"/"+q.Name, "err", err)
		} else {
			stacks = fetched
		}
	}

	// Fetch latest run per workflow for each PR head SHA — 1 call per PR,
	// and only when the repo row is expanded.
	var prRuns []PRRun
	for _, pr := range prs {
		sha := pr.GetHead().GetSHA()
		p := PR{
			Number:    pr.GetNumber(),
			Title:     pr.GetTitle(),
			HeadSHA:   sha,
			HTMLURL:   pr.GetHTMLURL(),
			Mergeable: pr.GetMergeableState(),
		}
		if st, ok := stacks[p.Number]; ok {
			p.Stack = &st
		}
		var runs []WorkflowRun
		if q.Detail {
			runs, err = c.fetchRunsForRef(ctx, q, sha)
			if err != nil {
				return RepoData{}, fmt.Errorf("PR #%d: %w", p.Number, err)
			}
		}
		prRuns = append(prRuns, PRRun{PR: p, Runs: runs})
	}

	return RepoData{BranchRuns: branchRuns, PRRuns: prRuns, ResolvedBranch: resolvedBranch}, nil
}

// fetchBranchRuns returns the latest run per workflow on the branch, then
// fetches jobs for each. Uses ListRepositoryWorkflowRuns (1 call) instead of
// ListWorkflows + per-workflow queries.
// Returns the runs, the resolved branch name, and any error.
func (c *Client) fetchBranchRuns(ctx context.Context, q RepoQuery) ([]WorkflowRun, string, error) {
	branch := q.Branch
	if branch == "" {
		repo, _, err := c.gh.Repositories.Get(ctx, q.Owner, q.Name)
		if err != nil {
			return nil, "", fmt.Errorf("getting default branch for %s/%s: %w", q.Owner, q.Name, err)
		}
		branch = repo.GetDefaultBranch()
	}

	runs, _, err := c.gh.Actions.ListRepositoryWorkflowRuns(ctx, q.Owner, q.Name, &github.ListWorkflowRunsOptions{
		Branch:      branch,
		ListOptions: github.ListOptions{PerPage: 100},
	})
	if err != nil {
		return nil, "", fmt.Errorf("listing workflow runs for %s/%s: %w", q.Owner, q.Name, err)
	}

	filterSet := newFilterSet(q.Workflows)

	// Keep only the most recent run per workflow.
	seen := make(map[string]bool)
	var results []WorkflowRun
	for _, run := range runs.WorkflowRuns {
		if !keepRun(run, filterSet) {
			continue
		}
		key := runKey(run)
		if seen[key] {
			continue
		}
		seen[key] = true

		wr := WorkflowRun{
			WorkflowName: run.GetName(),
			Status:       run.GetStatus(),
			Conclusion:   run.GetConclusion(),
			HTMLURL:      run.GetHTMLURL(),
			RunID:        run.GetID(),
		}

		// Jobs are only rendered under an expanded repo row, and cost one call
		// per run, so they are fetched only when that detail is on screen.
		if q.Detail {
			jobs, _, err := c.gh.Actions.ListWorkflowJobs(ctx, q.Owner, q.Name, run.GetID(), &github.ListWorkflowJobsOptions{})
			if err != nil {
				return nil, "", fmt.Errorf("listing jobs for run %d in %s/%s: %w", run.GetID(), q.Owner, q.Name, err)
			}
			for _, j := range jobs.Jobs {
				wr.Jobs = append(wr.Jobs, Job{
					Name:       j.GetName(),
					Status:     j.GetStatus(),
					Conclusion: j.GetConclusion(),
				})
			}
		}
		results = append(results, wr)
	}
	return results, branch, nil
}

// fetchRunsForRef returns the latest run per workflow for a given head SHA.
// Uses ListRepositoryWorkflowRuns filtered by HeadSHA (1 call) — no jobs
// fetched for PRs to keep API usage low.
func (c *Client) fetchRunsForRef(ctx context.Context, q RepoQuery, sha string) ([]WorkflowRun, error) {
	runs, _, err := c.gh.Actions.ListRepositoryWorkflowRuns(ctx, q.Owner, q.Name, &github.ListWorkflowRunsOptions{
		HeadSHA:     sha,
		ListOptions: github.ListOptions{PerPage: 100},
	})
	if err != nil {
		return nil, fmt.Errorf("listing workflow runs for sha %s in %s/%s: %w", sha, q.Owner, q.Name, err)
	}

	filterSet := newFilterSet(q.Workflows)

	seen := make(map[string]bool)
	var results []WorkflowRun
	for _, run := range runs.WorkflowRuns {
		if !keepRun(run, filterSet) {
			continue
		}
		key := runKey(run)
		if seen[key] {
			continue
		}
		seen[key] = true
		results = append(results, WorkflowRun{
			WorkflowName: run.GetName(),
			Status:       run.GetStatus(),
			Conclusion:   run.GetConclusion(),
			HTMLURL:      run.GetHTMLURL(),
			RunID:        run.GetID(),
		})
	}
	return results, nil
}

// noFailedJobsFragment is what GitHub says when rerun-failed-jobs has nothing
// to retry individually — every job either passed or never started. Re-running
// the whole run is the right call in that case.
const noFailedJobsFragment = "no failed jobs"

// isNoFailedJobs reports whether err is GitHub declining a rerun-failed-jobs
// request because the run has no individually-failed jobs.
func isNoFailedJobs(err error) bool {
	var resp *github.ErrorResponse
	if !errors.As(err, &resp) {
		return false
	}
	return strings.Contains(strings.ToLower(resp.Message), noFailedJobsFragment)
}

// RerunFailedJobs re-runs the failed jobs of a workflow run, falling back to
// re-running the whole run when GitHub reports there were none. A read-only
// token fails here with 403; the error is returned so the TUI can show it.
func (c *Client) RerunFailedJobs(ctx context.Context, owner, name string, runID int64) error {
	_, err := c.gh.Actions.RerunFailedJobsByID(ctx, owner, name, runID)
	if err == nil {
		return nil
	}
	if !isNoFailedJobs(err) {
		return fmt.Errorf("re-running failed jobs for run %d in %s/%s: %w", runID, owner, name, err)
	}
	if _, err := c.gh.Actions.RerunWorkflowByID(ctx, owner, name, runID); err != nil {
		return fmt.Errorf("re-running run %d in %s/%s: %w", runID, owner, name, err)
	}
	return nil
}
