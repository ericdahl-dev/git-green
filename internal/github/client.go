package githubclient

import (
	"context"
	"fmt"

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
	Number  int
	Title   string
	HeadSHA string
	HTMLURL string
}

// PRRun groups an open PR with its workflow runs.
type PRRun struct {
	PR   PR
	Runs []WorkflowRun
}

// RepoData holds all CI data fetched in a single pass for a repo.
type RepoData struct {
	BranchRuns []WorkflowRun
	PRRuns     []PRRun
}

// RepoQuery describes what to fetch for a single repo.
type RepoQuery struct {
	Owner     string
	Name      string
	Branch    string   // empty = use repo default branch
	Workflows []string // nil = all workflows
}

// Client fetches CI data from GitHub.
type Client struct {
	gh *github.Client
}

// New creates a Client authenticated with the given token.
func New(token string) *Client {
	ts := oauth2.StaticTokenSource(&oauth2.Token{AccessToken: token})
	tc := oauth2.NewClient(context.Background(), ts)
	return &Client{gh: github.NewClient(tc)}
}

// FetchAll fetches branch runs and open-PR runs with minimal API calls:
//   - 1 call for branch runs (ListRepositoryWorkflowRuns filtered by branch)
//   - 1 call to list open PRs
//   - 1 call per PR for its runs (filtered by head SHA)
//   - 1 call per branch workflow run to fetch jobs
//
// No ListWorkflows call at all.
func (c *Client) FetchAll(ctx context.Context, q RepoQuery) (RepoData, error) {
	// Fetch the latest run per workflow on the default/configured branch.
	branchRuns, err := c.fetchBranchRuns(ctx, q)
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

	// Fetch latest run per workflow for each PR head SHA — 1 call per PR.
	var prRuns []PRRun
	for _, pr := range prs {
		sha := pr.GetHead().GetSHA()
		p := PR{
			Number:  pr.GetNumber(),
			Title:   pr.GetTitle(),
			HeadSHA: sha,
			HTMLURL: pr.GetHTMLURL(),
		}
		runs, err := c.fetchRunsForRef(ctx, q, sha)
		if err != nil {
			return RepoData{}, fmt.Errorf("PR #%d: %w", p.Number, err)
		}
		prRuns = append(prRuns, PRRun{PR: p, Runs: runs})
	}

	return RepoData{BranchRuns: branchRuns, PRRuns: prRuns}, nil
}

// fetchBranchRuns returns the latest run per workflow on the branch, then
// fetches jobs for each. Uses ListRepositoryWorkflowRuns (1 call) instead of
// ListWorkflows + per-workflow queries.
func (c *Client) fetchBranchRuns(ctx context.Context, q RepoQuery) ([]WorkflowRun, error) {
	runs, _, err := c.gh.Actions.ListRepositoryWorkflowRuns(ctx, q.Owner, q.Name, &github.ListWorkflowRunsOptions{
		Branch:      q.Branch,
		ListOptions: github.ListOptions{PerPage: 100},
	})
	if err != nil {
		return nil, fmt.Errorf("listing workflow runs for %s/%s: %w", q.Owner, q.Name, err)
	}

	filterSet := make(map[string]bool, len(q.Workflows))
	for _, wf := range q.Workflows {
		filterSet[wf] = true
	}

	// Keep only the most recent run per workflow name.
	seen := make(map[string]bool)
	var results []WorkflowRun
	for _, run := range runs.WorkflowRuns {
		name := run.GetName()
		if len(filterSet) > 0 && !filterSet[name] {
			continue
		}
		if seen[name] {
			continue
		}
		seen[name] = true

		wr := WorkflowRun{
			WorkflowName: name,
			Status:       run.GetStatus(),
			Conclusion:   run.GetConclusion(),
			HTMLURL:      run.GetHTMLURL(),
			RunID:        run.GetID(),
		}

		// Fetch jobs for branch runs so we can show them expanded.
		jobs, _, err := c.gh.Actions.ListWorkflowJobs(ctx, q.Owner, q.Name, run.GetID(), &github.ListWorkflowJobsOptions{})
		if err != nil {
			return nil, fmt.Errorf("listing jobs for run %d in %s/%s: %w", run.GetID(), q.Owner, q.Name, err)
		}
		for _, j := range jobs.Jobs {
			wr.Jobs = append(wr.Jobs, Job{
				Name:       j.GetName(),
				Status:     j.GetStatus(),
				Conclusion: j.GetConclusion(),
			})
		}
		results = append(results, wr)
	}
	return results, nil
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

	filterSet := make(map[string]bool, len(q.Workflows))
	for _, wf := range q.Workflows {
		filterSet[wf] = true
	}

	seen := make(map[string]bool)
	var results []WorkflowRun
	for _, run := range runs.WorkflowRuns {
		name := run.GetName()
		if len(filterSet) > 0 && !filterSet[name] {
			continue
		}
		if seen[name] {
			continue
		}
		seen[name] = true
		results = append(results, WorkflowRun{
			WorkflowName: name,
			Status:       run.GetStatus(),
			Conclusion:   run.GetConclusion(),
			HTMLURL:      run.GetHTMLURL(),
			RunID:        run.GetID(),
		})
	}
	return results, nil
}
