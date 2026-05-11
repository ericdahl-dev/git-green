package webhooks_test

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ericdahl-dev/git-green/internal/config"
	"github.com/ericdahl-dev/git-green/internal/webhooks"
)

func collectRequests(t *testing.T) (*httptest.Server, *[]*http.Request, *[][]byte) {
	t.Helper()
	var reqs []*http.Request
	var bodies [][]byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var buf []byte
		if r.Body != nil {
			buf = make([]byte, r.ContentLength)
			_, _ = r.Body.Read(buf)
		}
		reqs = append(reqs, r)
		bodies = append(bodies, buf)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	return srv, &reqs, &bodies
}

func TestDispatchPostsJSON(t *testing.T) {
	srv, reqs, _ := collectRequests(t)

	cfg := []config.Webhook{{URL: srv.URL}}
	dispatcher := webhooks.New(cfg)

	stuckSince := time.Now().Add(-35 * time.Minute)
	evt := webhooks.Event{
		Event:      "pr_stuck",
		Reason:     "conflict",
		Repo:       "ericdahl-dev/git-green",
		PR:         &webhooks.PRInfo{Number: 42, Title: "feat: foo", URL: "https://github.com/x"},
		Workflow:   "CI",
		RunURL:     "https://github.com/x/runs/1",
		StuckSince: stuckSince,
		Timestamp:  time.Now(),
	}

	dispatcher.Dispatch(evt)

	if len(*reqs) != 1 {
		t.Fatalf("expected 1 request, got %d", len(*reqs))
	}
	if (*reqs)[0].Method != http.MethodPost {
		t.Errorf("expected POST, got %s", (*reqs)[0].Method)
	}
	if ct := (*reqs)[0].Header.Get("Content-Type"); ct != "application/json" {
		t.Errorf("expected application/json, got %s", ct)
	}
}

func TestDispatchHMACSignature(t *testing.T) {
	srv, reqs, bodies := collectRequests(t)

	secret := "topsecret"
	cfg := []config.Webhook{{URL: srv.URL, Secret: secret}}
	dispatcher := webhooks.New(cfg)

	evt := webhooks.Event{
		Event:      "branch_stuck",
		Reason:     "prolonged_failure",
		Repo:       "ericdahl-dev/git-green",
		Workflow:   "CI",
		RunURL:     "https://github.com/x/runs/2",
		StuckSince: time.Now().Add(-40 * time.Minute),
		Timestamp:  time.Now(),
	}

	dispatcher.Dispatch(evt)

	if len(*reqs) != 1 {
		t.Fatalf("expected 1 request, got %d", len(*reqs))
	}

	sig := (*reqs)[0].Header.Get("X-Git-Green-Signature")
	if sig == "" {
		t.Fatal("expected X-Git-Green-Signature header")
	}

	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write((*bodies)[0])
	expected := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	if sig != expected {
		t.Errorf("signature mismatch: got %s, want %s", sig, expected)
	}
}

func TestDispatchNoSignatureWithoutSecret(t *testing.T) {
	srv, reqs, _ := collectRequests(t)

	cfg := []config.Webhook{{URL: srv.URL}}
	dispatcher := webhooks.New(cfg)

	evt := webhooks.Event{
		Event:      "branch_stuck",
		Reason:     "prolonged_in_progress",
		Repo:       "ericdahl-dev/git-green",
		Workflow:   "CI",
		RunURL:     "https://github.com/x/runs/3",
		StuckSince: time.Now().Add(-40 * time.Minute),
		Timestamp:  time.Now(),
	}

	dispatcher.Dispatch(evt)

	if len(*reqs) != 1 {
		t.Fatalf("expected 1 request, got %d", len(*reqs))
	}
	if sig := (*reqs)[0].Header.Get("X-Git-Green-Signature"); sig != "" {
		t.Errorf("expected no signature header, got %s", sig)
	}
}

func TestDispatchPayloadShape(t *testing.T) {
	var received webhooks.Event
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&received)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	cfg := []config.Webhook{{URL: srv.URL}}
	dispatcher := webhooks.New(cfg)

	stuckSince := time.Now().Add(-35 * time.Minute).Truncate(time.Second)
	evt := webhooks.Event{
		Event:      "pr_stuck",
		Reason:     "conflict",
		Repo:       "ericdahl-dev/git-green",
		PR:         &webhooks.PRInfo{Number: 7, Title: "fix: auth", URL: "https://github.com/x/pull/7"},
		Workflow:   "CI",
		RunURL:     "https://github.com/x/runs/4",
		StuckSince: stuckSince,
		Timestamp:  time.Now().Truncate(time.Second),
	}

	dispatcher.Dispatch(evt)

	if received.Event != "pr_stuck" {
		t.Errorf("event: got %q", received.Event)
	}
	if received.Reason != "conflict" {
		t.Errorf("reason: got %q", received.Reason)
	}
	if received.PR == nil || received.PR.Number != 7 {
		t.Errorf("pr: got %+v", received.PR)
	}
}

func TestDispatchFailureSilent(t *testing.T) {
	cfg := []config.Webhook{{URL: "http://localhost:1"}} // nothing listening
	dispatcher := webhooks.New(cfg)

	evt := webhooks.Event{
		Event:      "branch_stuck",
		Reason:     "prolonged_failure",
		Repo:       "ericdahl-dev/git-green",
		Workflow:   "CI",
		RunURL:     "https://github.com/x/runs/5",
		StuckSince: time.Now(),
		Timestamp:  time.Now(),
	}

	// Should not panic or block.
	dispatcher.Dispatch(evt)
}

func TestDispatchMultipleWebhooks(t *testing.T) {
	srv1, reqs1, _ := collectRequests(t)
	srv2, reqs2, _ := collectRequests(t)

	cfg := []config.Webhook{{URL: srv1.URL}, {URL: srv2.URL}}
	dispatcher := webhooks.New(cfg)

	evt := webhooks.Event{
		Event:      "branch_stuck",
		Reason:     "prolonged_failure",
		Repo:       "ericdahl-dev/git-green",
		Workflow:   "CI",
		RunURL:     "https://github.com/x/runs/6",
		StuckSince: time.Now(),
		Timestamp:  time.Now(),
	}

	dispatcher.Dispatch(evt)

	if len(*reqs1) != 1 {
		t.Errorf("srv1: expected 1 request, got %d", len(*reqs1))
	}
	if len(*reqs2) != 1 {
		t.Errorf("srv2: expected 1 request, got %d", len(*reqs2))
	}
}
