package webhooks

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/ericdahl-dev/git-green/internal/config"
	"github.com/ericdahl-dev/git-green/internal/logx"
)

// PRInfo carries PR metadata in an event payload.
type PRInfo struct {
	Number int    `json:"number"`
	Title  string `json:"title"`
	URL    string `json:"url"`
}

// Event is the JSON payload POSTed to each webhook.
type Event struct {
	Event      string    `json:"event"`
	Reason     string    `json:"reason"`
	Repo       string    `json:"repo"`
	PR         *PRInfo   `json:"pr,omitempty"`
	Workflow   string    `json:"workflow"`
	RunURL     string    `json:"run_url"`
	StuckSince time.Time `json:"stuck_since"`
	Timestamp  time.Time `json:"timestamp"`
}

// Dispatcher sends webhook events to configured endpoints.
type Dispatcher struct {
	hooks  []config.Webhook
	client *http.Client
}

// New creates a Dispatcher for the given webhook configs.
func New(hooks []config.Webhook) *Dispatcher {
	return &Dispatcher{
		hooks:  hooks,
		client: &http.Client{Timeout: 10 * time.Second},
	}
}

// Send delivers evts to every configured webhook in the background and
// returns at once, so a slow or hanging endpoint never holds up polling. The
// returned channel closes when delivery has finished, for callers (tests) that
// need to wait.
func (d *Dispatcher) Send(evts []Event) <-chan struct{} {
	done := make(chan struct{})
	if len(d.hooks) == 0 || len(evts) == 0 {
		close(done)
		return done
	}
	go func() {
		defer close(done)
		for _, evt := range evts {
			d.Dispatch(evt)
		}
	}()
	return done
}

// Dispatch POSTs evt to all configured webhooks and waits for them. Failures
// are logged but not returned: an alert that cannot be delivered must not
// interrupt polling. Logging goes through logx, never plain stderr, which
// would scribble over the full-screen dashboard.
func (d *Dispatcher) Dispatch(evt Event) {
	if len(d.hooks) == 0 {
		return
	}

	body, err := json.Marshal(evt)
	if err != nil {
		logx.Debug("webhook marshal failed", "err", err)
		return
	}

	for _, wh := range d.hooks {
		if err := d.post(wh, body); err != nil {
			logx.Debug("webhook POST failed", "url", wh.URL, "err", err)
		}
	}
}

func (d *Dispatcher) post(wh config.Webhook, body []byte) error {
	req, err := http.NewRequest(http.MethodPost, wh.URL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("building request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	if wh.Secret != "" {
		mac := hmac.New(sha256.New, []byte(wh.Secret))
		mac.Write(body)
		req.Header.Set("X-Git-Green-Signature", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	}

	resp, err := d.client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("server returned %d", resp.StatusCode)
	}
	return nil
}
