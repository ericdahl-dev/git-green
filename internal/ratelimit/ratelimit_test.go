package ratelimit

import (
	"testing"
	"time"
)

var now = time.Date(2026, 8, 27, 15, 0, 0, 0, time.UTC)

func budget(remaining int, toReset time.Duration) Budget {
	return Budget{Remaining: remaining, Limit: 5000, Reset: now.Add(toReset)}
}

func TestIntervalLeavesHealthyTokensAlone(t *testing.T) {
	got, throttled := Interval(60*time.Second, budget(4200, 50*time.Minute), 30, now)
	if throttled {
		t.Error("a token with 84% of its budget left must not be throttled")
	}
	if got != 60*time.Second {
		t.Errorf("got %s, want the configured 60s", got)
	}
}

func TestIntervalStretchesAsBudgetThins(t *testing.T) {
	// 300 left, minus the reserve, is 200 — 6 more cycles at 30 calls each,
	// spread over the 30 minutes until reset.
	got, throttled := Interval(60*time.Second, budget(300, 30*time.Minute), 30, now)
	if !throttled {
		t.Fatal("expected throttling with 6% of the budget left")
	}
	if want := 5 * time.Minute; got != want {
		t.Errorf("got %s, want %s", got, want)
	}
	if got <= 60*time.Second {
		t.Error("a throttled interval must be longer than the configured one")
	}
}

func TestIntervalWaitsOutTheWindowWhenSpent(t *testing.T) {
	got, throttled := Interval(60*time.Second, budget(60, 12*time.Minute), 30, now)
	if !throttled {
		t.Fatal("expected throttling with nothing spendable left")
	}
	if want := 12 * time.Minute; got != want {
		t.Errorf("got %s, want a wait to the reset at %s", got, want)
	}
}

// The reserve is what keeps `gh` and everything else sharing the token alive,
// so it is never spent even when that means idling.
func TestIntervalNeverSpendsTheReserve(t *testing.T) {
	got, throttled := Interval(60*time.Second, budget(Reserve, time.Hour), 1, now)
	if !throttled || got != time.Hour {
		t.Errorf("got (%s, %v), want the full window wait", got, throttled)
	}
}

func TestIntervalFallsBackWhenNothingIsKnown(t *testing.T) {
	cases := map[string]struct {
		b    Budget
		cost int
	}{
		"no response yet":  {Budget{}, 30},
		"no cost measured": {budget(100, time.Hour), 0},
		"window rolled":    {budget(100, -time.Minute), 30},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got, throttled := Interval(60*time.Second, tc.b, tc.cost, now)
			if throttled || got != 60*time.Second {
				t.Errorf("got (%s, %v), want the configured interval untouched", got, throttled)
			}
		})
	}
}

func TestUsedReportsConsumption(t *testing.T) {
	if got := budget(4000, time.Hour).Used(); got != 20 {
		t.Errorf("got %d%%, want 20%%", got)
	}
	if got := (Budget{}).Used(); got != 0 {
		t.Errorf("got %d%% for an unknown budget, want 0%%", got)
	}
}
