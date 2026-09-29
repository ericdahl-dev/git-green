package githubclient

import (
	"bytes"
	"io"
	"net/http"
	"strings"
	"sync"
)

// cacheHeader marks a response replayed from the ETag cache. GitHub does not
// count a 304 against the REST budget, so neither does Pacing.
const cacheHeader = "X-Git-Green-Cache"

func fromCache(resp *http.Response) bool {
	return resp != nil && resp.Header.Get(cacheHeader) != ""
}

// maxCachedResponses bounds one token's ETag cache. A cycle touches a few URLs
// per Repo and per open PR, so this comfortably holds a large dashboard while
// letting URLs for closed PRs and old head SHAs age out.
const maxCachedResponses = 2000

// etagTransport makes unchanged GET responses free. It sends the ETag from the
// last response for a URL as If-None-Match; when GitHub answers 304 Not
// Modified, it replays the stored body as a 200 so callers see ordinary data.
//
// One transport serves one token, so cached responses never cross tokens.
type etagTransport struct {
	base http.RoundTripper
	max  int

	mu      sync.Mutex
	entries map[string]*cachedResponse
	clock   uint64 // bumped on every use, for least-recently-used eviction
}

type cachedResponse struct {
	etag   string
	header http.Header
	body   []byte
	used   uint64
}

func newETagTransport(base http.RoundTripper, max int) *etagTransport {
	return &etagTransport{base: base, max: max, entries: make(map[string]*cachedResponse)}
}

func (t *etagTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Method != http.MethodGet {
		return t.base.RoundTrip(req)
	}
	key := req.URL.String()

	t.mu.Lock()
	cached := t.entries[key]
	if cached != nil {
		t.clock++
		cached.used = t.clock
	}
	t.mu.Unlock()

	if cached != nil {
		req = req.Clone(req.Context())
		req.Header.Set("If-None-Match", cached.etag)
	}

	resp, err := t.base.RoundTrip(req)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode == http.StatusNotModified && cached != nil {
		_ = resp.Body.Close()
		return replay(req, resp, cached), nil
	}

	etag := resp.Header.Get("ETag")
	if resp.StatusCode != http.StatusOK || etag == "" {
		return resp, nil
	}
	body, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		return nil, err
	}
	resp.Body = io.NopCloser(bytes.NewReader(body))
	t.store(key, &cachedResponse{etag: etag, header: resp.Header.Clone(), body: body})
	return resp, nil
}

// replay builds a 200 from the cached body, taking the rate-limit headers from
// the fresh 304 so the budget Pacing sees is current.
func replay(req *http.Request, notModified *http.Response, cached *cachedResponse) *http.Response {
	header := cached.header.Clone()
	for k, v := range notModified.Header {
		if strings.HasPrefix(strings.ToLower(k), "x-ratelimit-") {
			header[k] = v
		}
	}
	header.Set(cacheHeader, "1")
	return &http.Response{
		Status:        "200 OK",
		StatusCode:    http.StatusOK,
		Proto:         notModified.Proto,
		ProtoMajor:    notModified.ProtoMajor,
		ProtoMinor:    notModified.ProtoMinor,
		Header:        header,
		Body:          io.NopCloser(bytes.NewReader(cached.body)),
		ContentLength: int64(len(cached.body)),
		Request:       req,
	}
}

func (t *etagTransport) store(key string, c *cachedResponse) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.clock++
	c.used = t.clock
	t.entries[key] = c
	if len(t.entries) <= t.max {
		return
	}
	oldest, oldestUse := "", ^uint64(0)
	for k, e := range t.entries {
		if e.used < oldestUse {
			oldest, oldestUse = k, e.used
		}
	}
	delete(t.entries, oldest)
}

// Pool hands out one Client per token, so each token's ETag cache survives
// from one poll cycle to the next. The zero value is ready to use.
type Pool struct {
	mu      sync.Mutex
	clients map[string]*Client
}

// For returns the Client for token, creating it on first use.
func (p *Pool) For(token string) *Client {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.clients == nil {
		p.clients = make(map[string]*Client)
	}
	c, ok := p.clients[token]
	if !ok {
		c = New(token)
		p.clients[token] = c
	}
	return c
}
