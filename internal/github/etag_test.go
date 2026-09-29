package githubclient

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/google/go-github/v72/github"
)

// etagServer answers with a fixed ETag and 304s any request that presents it.
// It counts full responses, which are the ones that cost quota.
func etagServer(t *testing.T, body string) (*httptest.Server, *int32) {
	t.Helper()
	var full int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-RateLimit-Limit", "5000")
		if r.Header.Get("If-None-Match") == `"v1"` {
			w.Header().Set("X-RateLimit-Remaining", "4999")
			w.WriteHeader(http.StatusNotModified)
			return
		}
		atomic.AddInt32(&full, 1)
		w.Header().Set("ETag", `"v1"`)
		w.Header().Set("X-RateLimit-Remaining", "4000")
		_, _ = io.WriteString(w, body+" "+r.URL.Path)
	}))
	t.Cleanup(srv.Close)
	return srv, &full
}

func get(t *testing.T, c *http.Client, url string) *http.Response {
	t.Helper()
	resp, err := c.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func readBody(t *testing.T, resp *http.Response) string {
	t.Helper()
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestETagTransportReplaysUnchangedResponses(t *testing.T) {
	srv, full := etagServer(t, "runs")
	c := &http.Client{Transport: newETagTransport(http.DefaultTransport, 10)}

	first := get(t, c, srv.URL+"/a")
	if got := readBody(t, first); got != "runs /a" || fromCache(first) {
		t.Fatalf("first response: %q, cached=%v", got, fromCache(first))
	}

	second := get(t, c, srv.URL+"/a")
	if second.StatusCode != http.StatusOK {
		t.Errorf("replayed status %d, want 200 so callers see the data", second.StatusCode)
	}
	if got := readBody(t, second); got != "runs /a" {
		t.Errorf("replayed body %q", got)
	}
	if !fromCache(second) {
		t.Error("a replayed response must be marked as cached")
	}
	// The budget comes from the fresh 304, not the stale cached headers.
	if got := second.Header.Get("X-RateLimit-Remaining"); got != "4999" {
		t.Errorf("rate limit header %q, want the 304's 4999", got)
	}
	if n := atomic.LoadInt32(full); n != 1 {
		t.Errorf("server sent %d full responses, want 1", n)
	}
}

func TestETagTransportOnlyCachesGET(t *testing.T) {
	srv, full := etagServer(t, "gql")
	c := &http.Client{Transport: newETagTransport(http.DefaultTransport, 10)}
	for i := 0; i < 2; i++ {
		resp, err := c.Post(srv.URL+"/graphql", "application/json", strings.NewReader("{}"))
		if err != nil {
			t.Fatal(err)
		}
		readBody(t, resp)
	}
	if n := atomic.LoadInt32(full); n != 2 {
		t.Errorf("server sent %d full responses to POSTs, want 2", n)
	}
}

func TestETagTransportEvictsLeastRecentlyUsed(t *testing.T) {
	srv, full := etagServer(t, "x")
	c := &http.Client{Transport: newETagTransport(http.DefaultTransport, 2)}
	for _, p := range []string{"/a", "/b", "/a", "/c"} { // /b is least recent when /c arrives
		readBody(t, get(t, c, srv.URL+p))
	}
	before := atomic.LoadInt32(full)
	readBody(t, get(t, c, srv.URL+"/a")) // still cached
	readBody(t, get(t, c, srv.URL+"/b")) // evicted: full fetch
	if got := atomic.LoadInt32(full) - before; got != 1 {
		t.Errorf("%d full fetches, want 1 (only the evicted /b)", got)
	}
}

// Replayed responses cost no quota, so they must not count toward the cost
// Pacing measures for a cycle — or it would pace far more slowly than needed.
func TestFetchStatsSkipCachedResponses(t *testing.T) {
	var s fetchStats
	fresh := &http.Response{Header: http.Header{}}
	cached := &http.Response{Header: http.Header{cacheHeader: []string{"1"}}}
	s.observe(&github.Response{Response: fresh})
	s.observe(&github.Response{Response: cached})
	if s.calls != 1 {
		t.Errorf("counted %d calls, want 1", s.calls)
	}
}

func TestPoolReusesOneClientPerToken(t *testing.T) {
	var p Pool
	first, again := p.For("a"), p.For("a")
	if first != again {
		t.Error("the same token must get the same client, or its ETag cache is lost every cycle")
	}
	if first == p.For("b") {
		t.Error("tokens must not share a client: cached responses are per token")
	}
}
