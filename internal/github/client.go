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

// FetchAll fetches branch runs and open-PR runs in a single pass, calling
// ListWorkflows only once.
func (c *Client) FetchAll(ctx context.Context, q RepoQuery) (RepoData, error) {
	workflows, _, err := c.gh.Actions.ListWorkflows(ctx, q.Owner, q.Name, &github.ListOptions{PerPage: 100})
	if err != nil {
		return RepoData{}, fmt.Errorf("listing workflows for %s/%s: %w", q.Owner, q.Name, err)
	}

	filterSet := make(map[string]bool, len(q.Workflows))
	for _, wf := range q.Workflows {
		filterSet[wf] = true
	}

	var filtered []*github.Workflow
	for _, wf := range workflows.Workflows {
		if len(filterSet) > 0 && !filterSet[wf.GetName()] {
			continue
		}
		filtered = append(filtered, wf)
	}

	// Fetch branch runs.
	branchRuns, err := c.fetchBranchRuns(ctx, q, filtered)
	if err != nil {
		return RepoData{}, err
	}

	// Fetch open PRs.
	prs, _, err := c.gh.PullRequests.List(ctx, q.Owner, q.Name, &github.PullRequestListOptions{
		State:       "open",
		ListOptions: github.ListOptions{PerPage: 50},
	})
	if err != nil {
		return RepoData{}, fmt.Errorf("listing PRs for %s/%s: %w", q.Owner, q.Name, err)
	}

	// Fetch PR runs.
	var prRuns []PRRun
	for _, pr := range prs {
		sha := pr.GetHead().GetSHA()
		p := PR{
			Number:  pr.GetNumber(),
			Title:   pr.GetTitle(),
			HeadSHA: sha,
			HTMLURL: pr.GetHTMLURL(),
		}
		runs, err := c.fetchRunsForSHA(ctx, q, filtered, sha)
		if err != nil {
			return RepoData{}, fmt.Errorf("PR #%d: %w", p.Number, err)
		}
		prRuns = append(prRuns, PRRun{PR: p, Runs: runs})
	}

	return RepoData{BranchRuns: branchRuns, PRRuns: prRuns}, nil
}

func (c *Client) fetchBranchRuns(ctx context.Context, q RepoQuery, workflows []*github.Workflow) ([]WorkflowRun, error) {
	var results []WorkflowRun
	for _, wf := range workflows {
		runs, _, err := c.gh.Actions.ListWorkflowRunsByID(ctx, q.Owner, q.Name, wf.GetID(), &github.ListWorkflowRunsOptions{
			Branch:      q.Branch,
			ListOptions: github.ListOptions{PerPage: 1},
		})
		if err != nil {
			return nil, fmt.Errorf("listing runs for workflow %q in %s/%s: %w", wf.GetName(), q.Owner, q.Name, err)
		}
		if len(runs.WorkflowRuns) == 0 {
			results = append(results, WorkflowRun{WorkflowName: wf.GetName()})
			continue
		}
		wr, err := c.buildWorkflowRun(ctx, q, wf.GetName(), runs.WorkflowRuns[0])
		if err != nil {
			return nil, err
		}
		results = append(results, wr)
	}
	return results, nil
}

func (c *Client) fetchRunsForSHA(ctx context.Context, q RepoQuery, workflows []*github.Workflow, sha string) ([]WorkflowRun, error) {
	var results []WorkflowRun
	for _, wf := range workflows {
		runs, _, err := c.gh.Actions.ListWorkflowRunsByID(ctx, q.Owner, q.Name, wf.GetID(), &github.ListWorkflowRunsOptions{
			HeadSHA:     sha,
			ListOptions: github.ListOptions{PerPage: 1},
		})
		if err != nil {
			return nil, fmt.Errorf("listing runs for workflow %q in %s/%s: %w", wf.GetName(), q.Owner, q.Name, err)
		}
		if len(runs.WorkflowRuns) == 0 {
			results = append(results, WorkflowRun{WorkflowName: wf.GetName()})
			continue
		}
		wr, err := c.buildWorkflowRun(ctx, q, wf.GetName(), runs.WorkflowRuns[0])
		if err != nil {
			return nil, err
		}
		results = append(results, wr)
	}
	return results, nil
}

func (c *Client) buildWorkflowRun(ctx context.Context, q RepoQuery, name string, run *github.WorkflowRun) (WorkflowRun, error) {
	wr := WorkflowRun{
		WorkflowName: name,
		Status:       run.GetStatus(),
		Conclusion:   run.GetConclusion(),
		HTMLURL:      run.GetHTMLURL(),
		RunID:        run.GetID(),
	}
	jobs, _, err := c.gh.Actions.ListWorkflowJobs(ctx, q.Owner, q.Name, run.GetID(), &github.ListWorkflowJobsOptions{})
	if err != nil {
		return WorkflowRun{}, fmt.Errorf("listing jobs for run %d in %s/%s: %w", run.GetID(), q.Owner, q.Name, err)
	}
	for _, j := range jobs.Jobs {
		wr.Jobs = append(wr.Jobs, Job{
			Name:       j.GetName(),
			Status:     j.GetStatus(),
			Conclusion: j.GetConclusion(),
		})
	}
	return wr, nil
}
