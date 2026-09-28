package githubclient

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDeriveReview(t *testing.T) {
	cases := []struct {
		name     string
		decision string
		latest   []string
		requests int
		want     Review
	}{
		{"protected, approved", "APPROVED", nil, 0, ReviewApproved},
		{"protected, changes requested", "CHANGES_REQUESTED", []string{"APPROVED"}, 0, ReviewChangesRequested},
		{"protected, not yet reviewed", "REVIEW_REQUIRED", nil, 0, ReviewWaiting},
		{"unprotected, approved", "", []string{"COMMENTED", "APPROVED"}, 0, ReviewApproved},
		{"unprotected, changes beat approval", "", []string{"APPROVED", "CHANGES_REQUESTED"}, 0, ReviewChangesRequested},
		{"unprotected, reviewer requested", "", []string{"COMMENTED"}, 1, ReviewWaiting},
		{"unprotected, approval beats a pending request", "", []string{"APPROVED"}, 1, ReviewApproved},
		{"unprotected, nothing", "", []string{"COMMENTED", "DISMISSED"}, 0, ReviewNone},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := deriveReview(tc.decision, tc.latest, tc.requests); got != tc.want {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestFetchPRMetaReadsReviewState(t *testing.T) {
	c, _ := stubGraphQL(t, http.StatusOK, `{"data":{"repository":{"pullRequests":{"nodes":[
	  {"number":7,"reviewDecision":"APPROVED","latestReviews":{"nodes":[]},"reviewRequests":{"totalCount":0}},
	  {"number":9,"reviewDecision":null,"latestReviews":{"nodes":[]},"reviewRequests":{"totalCount":2}},
	  {"number":12,"reviewDecision":null,"latestReviews":{"nodes":[]},"reviewRequests":{"totalCount":0}}
	]}}}}`)

	meta, err := c.fetchPRMeta(context.Background(), "o", "n")
	if err != nil {
		t.Fatalf("fetchPRMeta: %v", err)
	}
	if got := meta[7].Review; got != ReviewApproved {
		t.Errorf("PR 7: got %v, want approved", got)
	}
	if got := meta[9].Review; got != ReviewWaiting {
		t.Errorf("PR 9: got %v, want waiting", got)
	}
	if got := meta[12].Review; got != ReviewNone {
		t.Errorf("PR 12: got %v, want none", got)
	}
	if meta[7].Stack != nil {
		t.Error("PR 7 has no stack")
	}
}

// A host that does not know about stacks rejects the whole query. Review
// state must survive that, so the client asks again without the stack fields.
func TestFetchPRMetaRetriesWithoutStacks(t *testing.T) {
	var queries []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(buf)
		queries = append(queries, string(buf))
		if strings.Contains(string(buf), "stackEntry") {
			_, _ = w.Write([]byte(`{"errors":[{"message":"Field 'stack' doesn't exist on type 'PullRequest'"}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"data":{"repository":{"pullRequests":{"nodes":[
		  {"number":3,"reviewDecision":"CHANGES_REQUESTED","latestReviews":{"nodes":[]},"reviewRequests":{"totalCount":0}}
		]}}}}`))
	}))
	t.Cleanup(srv.Close)
	c := &Client{http: srv.Client(), graphQLURL: srv.URL}

	meta, err := c.fetchPRMeta(context.Background(), "o", "n")
	if err != nil {
		t.Fatalf("fetchPRMeta: %v", err)
	}
	if len(queries) != 2 {
		t.Fatalf("got %d queries, want the full one then a review-only retry", len(queries))
	}
	if got := meta[3].Review; got != ReviewChangesRequested {
		t.Errorf("PR 3: got %v, want changes requested", got)
	}
	if meta[3].Stack != nil {
		t.Error("a host without stacks must not produce one")
	}
}
