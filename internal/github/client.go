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

// FetchRuns returns the latest WorkflowRun for each workflow in the repo.
func (c *Client) FetchRuns(ctx context.Context, q RepoQuery) ([]WorkflowRun, error) {
	opts := &github.ListWorkflowRunsOptions{
		Branch: q.Branch,
		ListOptions: github.ListOptions{
			PerPage: 1,
		},
	}

	workflows, _, err := c.gh.Actions.ListWorkflows(ctx, q.Owner, q.Name, &github.ListOptions{PerPage: 100})
	if err != nil {
		return nil, fmt.Errorf("listing workflows for %s/%s: %w", q.Owner, q.Name, err)
	}

	filterSet := make(map[string]bool, len(q.Workflows))
	for _, wf := range q.Workflows {
		filterSet[wf] = true
	}

	var results []WorkflowRun
	for _, wf := range workflows.Workflows {
		if len(filterSet) > 0 && !filterSet[wf.GetName()] {
			continue
		}

		runs, _, err := c.gh.Actions.ListWorkflowRunsByID(ctx, q.Owner, q.Name, wf.GetID(), opts)
		if err != nil {
			return nil, fmt.Errorf("listing runs for workflow %q in %s/%s: %w", wf.GetName(), q.Owner, q.Name, err)
		}
		if len(runs.WorkflowRuns) == 0 {
			results = append(results, WorkflowRun{WorkflowName: wf.GetName()})
			continue
		}

		run := runs.WorkflowRuns[0]
		wr := WorkflowRun{
			WorkflowName: wf.GetName(),
			Status:       run.GetStatus(),
			Conclusion:   run.GetConclusion(),
			HTMLURL:      run.GetHTMLURL(),
			RunID:        run.GetID(),
		}

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
