package githubclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/ericdahl-dev/git-green/internal/logx"
)

// Stack is the GitHub stacked-PR group a PR belongs to. GitHub numbers stacks
// out of the same counter as issues and PRs, so a stack number sits alongside
// the PR numbers it contains (PR #271 lives in stack #272).
type Stack struct {
	Number   int    // GitHub's stack number
	Size     int    // entries in the stack, including already-merged ones
	Position int    // 1-based position of this PR, bottom (trunk-most) first
	HeadRef  string // head branch of this PR, used to label the stack
}

// graphQLEndpoint is where stack membership and review state live. The REST
// API does not expose stacks at all, and would need a call per PR for reviews,
// so this is the only cheap way to read them.
const graphQLEndpoint = "https://api.github.com/graphql"

// prMetaQuery asks for stack membership and review state of every open PR in
// one repo. It is a single point against the GraphQL rate limit regardless of
// PR count. %s is replaced with the stack fields, or with nothing for hosts
// that do not know about stacks.
const prMetaQuery = `query($owner:String!,$name:String!){
  repository(owner:$owner,name:$name){
    pullRequests(states:OPEN,first:100){
      nodes{
        number
        headRefName
        reviewDecision
        latestReviews(first:20){nodes{state}}
        reviewRequests{totalCount}
        %s
      }
    }
  }
}`

const stackFields = `stack{number size}
        stackEntry{position}`

type prMetaResponse struct {
	Data struct {
		Repository struct {
			PullRequests struct {
				Nodes []struct {
					Number         int    `json:"number"`
					HeadRefName    string `json:"headRefName"`
					ReviewDecision string `json:"reviewDecision"`
					LatestReviews  struct {
						Nodes []struct {
							State string `json:"state"`
						} `json:"nodes"`
					} `json:"latestReviews"`
					ReviewRequests struct {
						TotalCount int `json:"totalCount"`
					} `json:"reviewRequests"`
					Stack *struct {
						Number int `json:"number"`
						Size   int `json:"size"`
					} `json:"stack"`
					StackEntry *struct {
						Position int `json:"position"`
					} `json:"stackEntry"`
				} `json:"nodes"`
			} `json:"pullRequests"`
		} `json:"repository"`
	} `json:"data"`
	Errors []struct {
		Message string `json:"message"`
	} `json:"errors"`
}

// prMeta is what the GraphQL query adds to a PR beyond the REST listing.
type prMeta struct {
	Stack  *Stack // nil when the PR is not stacked
	Review Review
}

// errGraphQL marks an error GraphQL reported in its response body, as opposed
// to a transport or HTTP failure.
var errGraphQL = errors.New("graphql error")

// fetchPRMeta returns stack membership and review state keyed by PR number.
// Stacks are a young GitHub feature: a host that does not know the field
// rejects the whole query, so on a GraphQL error it asks again without the
// stack fields, keeping review state and costing only the grouping. Callers
// treat any error here as "no metadata" rather than failing the whole repo.
func (c *Client) fetchPRMeta(ctx context.Context, owner, name string) (map[int]prMeta, error) {
	// A Client assembled without the GraphQL transport — as tests of the REST
	// paths do — simply has no stack or review support.
	if c.http == nil || c.graphQLURL == "" {
		return nil, errors.New("no GraphQL transport configured")
	}

	parsed, err := c.queryPRMeta(ctx, owner, name, stackFields)
	if errors.Is(err, errGraphQL) {
		logx.Debug("stacks unavailable, retrying without them", "repo", owner+"/"+name, "err", err)
		parsed, err = c.queryPRMeta(ctx, owner, name, "")
	}
	if err != nil {
		return nil, err
	}

	meta := make(map[int]prMeta)
	for _, node := range parsed.Data.Repository.PullRequests.Nodes {
		latest := make([]string, 0, len(node.LatestReviews.Nodes))
		for _, r := range node.LatestReviews.Nodes {
			latest = append(latest, r.State)
		}
		m := prMeta{Review: deriveReview(node.ReviewDecision, latest, node.ReviewRequests.TotalCount)}
		if node.Stack != nil && node.StackEntry != nil {
			m.Stack = &Stack{
				Number:   node.Stack.Number,
				Size:     node.Stack.Size,
				Position: node.StackEntry.Position,
				HeadRef:  node.HeadRefName,
			}
		}
		meta[node.Number] = m
	}
	return meta, nil
}

func (c *Client) queryPRMeta(ctx context.Context, owner, name, extraFields string) (prMetaResponse, error) {
	var parsed prMetaResponse
	body, err := json.Marshal(map[string]any{
		"query":     fmt.Sprintf(prMetaQuery, extraFields),
		"variables": map[string]string{"owner": owner, "name": name},
	})
	if err != nil {
		return parsed, fmt.Errorf("encoding PR query for %s/%s: %w", owner, name, err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.graphQLURL, bytes.NewReader(body))
	if err != nil {
		return parsed, fmt.Errorf("building PR query for %s/%s: %w", owner, name, err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return parsed, fmt.Errorf("querying PRs for %s/%s: %w", owner, name, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return parsed, fmt.Errorf("querying PRs for %s/%s: %s", owner, name, resp.Status)
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return parsed, fmt.Errorf("decoding PRs for %s/%s: %w", owner, name, err)
	}
	if len(parsed.Errors) > 0 {
		return parsed, fmt.Errorf("querying PRs for %s/%s: %w: %s", owner, name, errGraphQL, parsed.Errors[0].Message)
	}
	return parsed, nil
}
