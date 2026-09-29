package ratelimit

import (
	"errors"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/go-github/v72/github"

	"github.com/ericdahl-dev/git-green/internal/logx"
)

// Throttle reports one token polling slower than configured because its REST
// budget is running down.
type Throttle struct {
	Orgs      []string
	Remaining int
	Limit     int
	Reset     time.Time
	Interval  time.Duration
}

// Pacer paces every token against its own REST budget. It is keyed by token,
// never by Org: Orgs sharing a token share one budget, and pacing them
// separately would spend it twice.
//
// Each poll cycle, the poller asks Due before fetching a token's Repos,
// reports each fetch with Observe (or Failed, for a 403), and closes the
// cycle with EndCycle, which re-prices every token that spent. It is safe for
// concurrent use by the fetches of one cycle.
type Pacer struct {
	mu         sync.Mutex
	now        func() time.Time
	configured time.Duration // as of the last EndCycle, for Throttles
	tokens     map[string]*pace
}

type pace struct {
	orgs     map[string]bool
	budget   Budget
	interval time.Duration
	nextDue  time.Time // zero while healthy: the ticker alone decides
	limited  time.Time // GitHub's 403 reset; zero when not rate limited

	// This cycle's spending, folded in by EndCycle.
	spent bool
	cost  int
	low   Budget // leanest budget reported this cycle
}

// NewPacer returns a Pacer reading time from now.
func NewPacer(now func() time.Time) *Pacer {
	return &Pacer{now: now, tokens: make(map[string]*pace)}
}

func (p *Pacer) token(t string) *pace {
	pc, ok := p.tokens[t]
	if !ok {
		pc = &pace{orgs: make(map[string]bool)}
		p.tokens[t] = pc
	}
	return pc
}

// Due reports whether a token may be polled now. A token that has never
// reported a budget, or is comfortably inside it, is always due.
func (p *Pacer) Due(token string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	pc, ok := p.tokens[token]
	if !ok {
		return true
	}
	now := p.now()
	if now.Before(pc.limited) {
		return false
	}
	return pc.nextDue.IsZero() || !now.Before(pc.nextDue)
}

// LimitedUntil reports when a rate-limited token may be polled again. Unlike a
// paced token, whose Repos simply wait, a rate-limited one was refused by
// GitHub, and its Repos show that.
func (p *Pacer) LimitedUntil(token string) (time.Time, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	pc, ok := p.tokens[token]
	if !ok || !p.now().Before(pc.limited) {
		return time.Time{}, false
	}
	return pc.limited, true
}

// Observe records one fetch for org on token: the REST calls it made and the
// budget GitHub reported afterwards.
func (p *Pacer) Observe(token, org string, calls int, b Budget) {
	p.mu.Lock()
	defer p.mu.Unlock()
	pc := p.token(token)
	pc.orgs[org] = true
	pc.spent = true
	pc.cost += calls
	// Repos race, so keep the leanest reading of the cycle: it is the one
	// closest to what is actually left.
	if b.Known() && (!pc.low.Known() || b.Remaining < pc.low.Remaining) {
		pc.low = b
	}
}

// Failed records a fetch error. When GitHub refused the token for its rate
// limit, the token is held until the reset GitHub gave, and Failed reports
// true.
func (p *Pacer) Failed(token string, err error) bool {
	var rlErr *github.RateLimitError
	var abuseErr *github.AbuseRateLimitError
	var until time.Time
	switch {
	case errors.As(err, &rlErr):
		until = rlErr.Rate.Reset.Time
	case errors.As(err, &abuseErr) && abuseErr.RetryAfter != nil:
		until = p.now().Add(*abuseErr.RetryAfter)
	}
	if until.IsZero() {
		return false
	}
	p.mu.Lock()
	p.token(token).limited = until
	p.mu.Unlock()
	logx.Debug("rate limited", "until", until)
	return true
}

// EndCycle folds the cycle's spending into each token's pace. A token that
// was skipped this cycle keeps the pace it already had.
//
// The fastest pace that lasts the window is what is left after the Reserve,
// divided by the measured cost per cycle, spread over the time until reset.
// The cost is re-measured every cycle, since expanding a Repo changes it.
func (p *Pacer) EndCycle(configured time.Duration) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.configured = configured
	now := p.now()
	for _, pc := range p.tokens {
		if !pc.spent {
			continue
		}
		if pc.low.Known() {
			pc.budget = pc.low
		}
		interval, throttled := Interval(configured, pc.budget, pc.cost, now)
		pc.interval = interval
		pc.nextDue = time.Time{}
		if throttled {
			pc.nextDue = now.Add(interval)
			logx.Debug("throttling token", "orgs", sortedOrgs(pc.orgs),
				"remaining", pc.budget.Remaining, "cost", pc.cost, "interval", interval)
		}
		pc.spent, pc.cost, pc.low = false, 0, Budget{}
	}
}

// Throttles reports every token polling slower than configured, for the title
// bar. Healthy tokens are omitted.
func (p *Pacer) Throttles() []Throttle {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []Throttle
	for _, pc := range p.tokens {
		if pc.interval <= p.configured || !pc.budget.Known() {
			continue
		}
		out = append(out, Throttle{
			Orgs:      sortedOrgs(pc.orgs),
			Remaining: pc.budget.Remaining,
			Limit:     pc.budget.Limit,
			Reset:     pc.budget.Reset,
			Interval:  pc.interval,
		})
	}
	sort.Slice(out, func(a, b int) bool {
		return strings.Join(out[a].Orgs, ",") < strings.Join(out[b].Orgs, ",")
	})
	return out
}

func sortedOrgs(orgs map[string]bool) []string {
	out := make([]string, 0, len(orgs))
	for o := range orgs {
		out = append(out, o)
	}
	sort.Strings(out)
	return out
}
