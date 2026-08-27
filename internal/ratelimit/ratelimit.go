// Package ratelimit paces polling against a GitHub token's hourly REST budget.
//
// GitHub reports the budget on every response, so the poller always knows how
// much is left and when the window resets. Rather than sprinting until a 403
// and then going dark for the rest of the hour, the pacer spreads what is left
// across the time remaining: it slows down only as far as it has to, and only
// for the token that is actually short.
package ratelimit

import "time"

// Reserve is the slice of a token's budget git-green refuses to spend. A token
// is usually shared with `gh` and whatever else the user runs, so a dashboard
// that polls its own token to zero takes those down with it.
const Reserve = 100

// Budget is the core REST allowance GitHub reported for one token.
type Budget struct {
	Remaining int
	Limit     int
	Reset     time.Time
}

// Known reports whether GitHub has told us anything yet. Before the first
// response there is nothing to pace against.
func (b Budget) Known() bool { return b.Limit > 0 && !b.Reset.IsZero() }

// Used returns how much of the budget is gone, as a percentage. Zero when the
// budget is unknown.
func (b Budget) Used() int {
	if b.Limit <= 0 {
		return 0
	}
	return (b.Limit - b.Remaining) * 100 / b.Limit
}

// Interval returns how long to wait before polling this token again, and
// whether that is longer than the configured interval.
//
// The affordable number of cycles is what is left after the reserve, divided
// by what a cycle costs; spreading those over the time until reset gives the
// fastest pace that still lasts the window. While there is plenty of budget
// this comes out below the configured interval and the configured one wins, so
// a healthy token is never slowed down.
func Interval(configured time.Duration, b Budget, costPerCycle int, now time.Time) (time.Duration, bool) {
	if !b.Known() || costPerCycle <= 0 {
		return configured, false
	}
	toReset := b.Reset.Sub(now)
	if toReset <= 0 {
		// The window has rolled over; the next response will report the new one.
		return configured, false
	}

	spendable := b.Remaining - Reserve
	if spendable < costPerCycle {
		// Not even one more cycle to spare. Wait the window out rather than
		// spending the reserve or collecting 403s.
		return toReset, true
	}

	interval := toReset / time.Duration(spendable/costPerCycle)
	if interval <= configured {
		return configured, false
	}
	return interval, true
}
