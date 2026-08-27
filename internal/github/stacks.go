package githubclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
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

// graphQLEndpoint is where stack membership lives. The REST API does not
// expose stacks at all, so this is the only way to read them.
const graphQLEndpoint = "https://api.github.com/graphql"

// stackQuery asks for stack membership of every open PR in one repo. It is a
// single point against the GraphQL rate limit regardless of PR count.
const stackQuery = `query($owner:String!,$name:String!){
  repository(owner:$owner,name:$name){
    pullRequests(states:OPEN,first:100){
      nodes{
        number
        headRefName
        stack{number size}
        stackEntry{position}
      }
    }
  }
}`

type stackResponse struct {
	Data struct {
		Repository struct {
			PullRequests struct {
				Nodes []struct {
					Number      int    `json:"number"`
					HeadRefName string `json:"headRefName"`
					Stack       *struct {
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

// fetchStacks returns stack membership keyed by PR number. Stacks are a young
// GitHub feature: a host that does not know the field answers with a GraphQL
// error, and an error here only costs the stack grouping, so callers treat it
// as "no stacks" rather than failing the whole repo.
func (c *Client) fetchStacks(ctx context.Context, owner, name string) (map[int]Stack, error) {
	// A Client assembled without the GraphQL transport — as tests of the REST
	// paths do — simply has no stack support.
	if c.http == nil || c.graphQLURL == "" {
		return nil, errors.New("no GraphQL transport configured")
	}

	body, err := json.Marshal(map[string]any{
		"query":     stackQuery,
		"variables": map[string]string{"owner": owner, "name": name},
	})
	if err != nil {
		return nil, fmt.Errorf("encoding stack query for %s/%s: %w", owner, name, err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.graphQLURL, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("building stack query for %s/%s: %w", owner, name, err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("querying stacks for %s/%s: %w", owner, name, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("querying stacks for %s/%s: %s", owner, name, resp.Status)
	}

	var parsed stackResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, fmt.Errorf("decoding stacks for %s/%s: %w", owner, name, err)
	}
	if len(parsed.Errors) > 0 {
		return nil, fmt.Errorf("querying stacks for %s/%s: %s", owner, name, parsed.Errors[0].Message)
	}

	stacks := make(map[int]Stack)
	for _, node := range parsed.Data.Repository.PullRequests.Nodes {
		if node.Stack == nil || node.StackEntry == nil {
			continue
		}
		stacks[node.Number] = Stack{
			Number:   node.Stack.Number,
			Size:     node.Stack.Size,
			Position: node.StackEntry.Position,
			HeadRef:  node.HeadRefName,
		}
	}
	return stacks, nil
}
