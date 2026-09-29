package ratelimit

import (
	"errors"
	"testing"
	"time"

	"github.com/google/go-github/v72/github"
)

const configured = time.Minute

type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

func newTestPacer() (*Pacer, *clock) {
	c := &clock{t: time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)}
	return NewPacer(c.now), c
}

// cycle runs one poll cycle: each org on the token spends calls against b.
func cycle(p *Pacer, token string, calls int, b Budget, orgs ...string) {
	for _, org := range orgs {
		p.Observe(token, org, calls, b)
	}
	p.EndCycle(configured)
}

func TestPacerNeverHoldsBackAHealthyToken(t *testing.T) {
	p, c := newTestPacer()
	cycle(p, "tok", 20, Budget{Remaining: 4800, Limit: 5000, Reset: c.t.Add(50 * time.Minute)}, "acme")

	if !p.Due("tok") {
		t.Error("a healthy token must stay due")
	}
	if got := p.Throttles(); len(got) != 0 {
		t.Errorf("healthy token reported as throttled: %+v", got)
	}
}

func TestPacerSlowsAThinTokenForEveryOrgOnIt(t *testing.T) {
	p, c := newTestPacer()
	cycle(p, "tok", 150, Budget{Remaining: 400, Limit: 5000, Reset: c.t.Add(30 * time.Minute)}, "acme", "globex")

	if p.Due("tok") {
		t.Fatal("a thin token must sit out the next cycle")
	}
	th := p.Throttles()
	if len(th) != 1 {
		t.Fatalf("got %d throttles, want 1", len(th))
	}
	if got := th[0].Orgs; len(got) != 2 || got[0] != "acme" || got[1] != "globex" {
		t.Errorf("throttle names %v, want both orgs sharing the token", got)
	}
	if th[0].Interval <= configured {
		t.Errorf("interval %s is not slower than configured", th[0].Interval)
	}

	c.t = c.t.Add(th[0].Interval + time.Second)
	if !p.Due("tok") {
		t.Error("the token must be due once its interval has passed")
	}
}

// A cycle that skipped the token keeps the pace it already had rather than
// reading zero spend as a healthy budget.
func TestPacerKeepsPaceAcrossSkippedCycles(t *testing.T) {
	p, c := newTestPacer()
	cycle(p, "tok", 150, Budget{Remaining: 400, Limit: 5000, Reset: c.t.Add(30 * time.Minute)}, "acme")
	p.EndCycle(configured) // nothing spent
	if p.Due("tok") {
		t.Error("an idle cycle must not reset the pace")
	}
}

func TestPacerHoldsARateLimitedTokenUntilReset(t *testing.T) {
	p, c := newTestPacer()
	reset := c.t.Add(30 * time.Minute)
	if !p.Failed("tok", &github.RateLimitError{Rate: github.Rate{Reset: github.Timestamp{Time: reset}}}) {
		t.Fatal("a RateLimitError must be recognised")
	}

	if p.Due("tok") {
		t.Error("a rate-limited token must not be due")
	}
	if until, ok := p.LimitedUntil("tok"); !ok || !until.Equal(reset) {
		t.Errorf("LimitedUntil = %v, %v; want %v", until, ok, reset)
	}

	c.t = reset.Add(time.Second)
	if !p.Due("tok") {
		t.Error("the token must be due after the reset")
	}
	if _, ok := p.LimitedUntil("tok"); ok {
		t.Error("the limit must clear after the reset")
	}
}

func TestPacerHonoursRetryAfter(t *testing.T) {
	p, c := newTestPacer()
	wait := 90 * time.Second
	p.Failed("tok", &github.AbuseRateLimitError{RetryAfter: &wait})
	if until, ok := p.LimitedUntil("tok"); !ok || !until.Equal(c.t.Add(wait)) {
		t.Errorf("LimitedUntil = %v, %v; want %v", until, ok, c.t.Add(wait))
	}
}

func TestPacerIgnoresOtherErrors(t *testing.T) {
	p, _ := newTestPacer()
	if p.Failed("tok", errors.New("boom")) {
		t.Error("an ordinary error is not a rate limit")
	}
	if !p.Due("tok") {
		t.Error("an ordinary error must not hold the token back")
	}
}

// GitHub's secondary limit does not always say how long to wait. Backing off
// anyway is what stops every following cycle from tripping it again.
func TestPacerBacksOffASecondaryLimitWithoutRetryAfter(t *testing.T) {
	p, c := newTestPacer()
	if !p.Failed("tok", &github.AbuseRateLimitError{}) {
		t.Fatal("a secondary limit must be recognised even without Retry-After")
	}
	if until, ok := p.LimitedUntil("tok"); !ok || !until.Equal(c.t.Add(SecondaryBackoff)) {
		t.Errorf("LimitedUntil = %v, %v; want %v", until, ok, c.t.Add(SecondaryBackoff))
	}
}

// A rate-limited token shows once in the title bar, not as an error on every
// Repo that rides it.
func TestPacerReportsRateLimitedTokens(t *testing.T) {
	p, c := newTestPacer()
	cycle(p, "tok", 10, Budget{Remaining: 4000, Limit: 5000, Reset: c.t.Add(time.Hour)}, "acme")
	reset := c.t.Add(10 * time.Minute)
	p.Failed("tok", &github.RateLimitError{Rate: github.Rate{Reset: github.Timestamp{Time: reset}}})

	th := p.Throttles()
	if len(th) != 1 || !th[0].Limited || !th[0].Reset.Equal(reset) || th[0].Orgs[0] != "acme" {
		t.Fatalf("got %+v, want one limited throttle for acme resetting at %v", th, reset)
	}

	c.t = reset.Add(time.Second)
	if th := p.Throttles(); len(th) != 0 {
		t.Errorf("still reported after the reset: %+v", th)
	}
}
