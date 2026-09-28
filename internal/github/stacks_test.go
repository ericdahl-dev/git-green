package githubclient

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// stubGraphQL serves body for the stack query and reports what it received.
func stubGraphQL(t *testing.T, status int, body string) (*Client, *string) {
	t.Helper()
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(buf)
		got = string(buf)
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return &Client{http: srv.Client(), graphQLURL: srv.URL}, &got
}

const stackBody = `{"data":{"repository":{"pullRequests":{"nodes":[
  {"number":268,"headRefName":"wse-1937-id-search","stack":{"number":272,"size":4},"stackEntry":{"position":1}},
  {"number":271,"headRefName":"wse-1955-issue-type-locales","stack":{"number":272,"size":4},"stackEntry":{"position":4}},
  {"number":259,"headRefName":"wse-1862-honest-cache-store","stack":null,"stackEntry":null}
]}}}}`

func TestFetchPRMetaKeysStacksByPRNumber(t *testing.T) {
	c, sent := stubGraphQL(t, http.StatusOK, stackBody)

	meta, err := c.fetchPRMeta(context.Background(), "ndlibrary", "annex-ims")
	if err != nil {
		t.Fatalf("fetchStacks: %v", err)
	}
	stacked := 0
	for _, m := range meta {
		if m.Stack != nil {
			stacked++
		}
	}
	if stacked != 2 {
		t.Fatalf("got %d stacked PRs, want 2 (the unstacked one must have no stack)", stacked)
	}

	bottom := meta[268].Stack
	if bottom.Number != 272 || bottom.Size != 4 || bottom.Position != 1 {
		t.Errorf("PR 268: got stack #%d size %d position %d, want #272 size 4 position 1",
			bottom.Number, bottom.Size, bottom.Position)
	}
	if bottom.HeadRef != "wse-1937-id-search" {
		t.Errorf("PR 268: got head ref %q", bottom.HeadRef)
	}
	if got := meta[271].Stack.Position; got != 4 {
		t.Errorf("PR 271: got position %d, want 4", got)
	}
	if meta[259].Stack != nil {
		t.Error("PR 259 has no stack")
	}

	if !strings.Contains(*sent, `"owner":"ndlibrary"`) || !strings.Contains(*sent, `"name":"annex-ims"`) {
		t.Errorf("query variables not sent: %s", *sent)
	}
}

// A GraphQL error that survives the review-only retry must surface as an
// error so FetchAll can fall back to showing PRs ungrouped.
func TestFetchPRMetaReportsGraphQLErrors(t *testing.T) {
	c, _ := stubGraphQL(t, http.StatusOK,
		`{"errors":[{"message":"Field 'stack' doesn't exist on type 'PullRequest'"}]}`)

	if _, err := c.fetchPRMeta(context.Background(), "o", "n"); err == nil {
		t.Fatal("expected an error for a GraphQL error response")
	}
}

func TestFetchPRMetaReportsHTTPErrors(t *testing.T) {
	c, _ := stubGraphQL(t, http.StatusUnauthorized, `{}`)

	if _, err := c.fetchPRMeta(context.Background(), "o", "n"); err == nil {
		t.Fatal("expected an error for a 401 response")
	}
}
