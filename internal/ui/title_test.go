package ui

import (
	"strings"
	"testing"
	"time"

	"github.com/ericdahl-dev/git-green/internal/state"
)

func TestTitleLineStaysQuietWhileHealthy(t *testing.T) {
	got := TitleLine(false, "", nil)
	if strings.Contains(got, "⏳") {
		t.Errorf("healthy title carried a throttle notice: %q", got)
	}
	if !strings.Contains(got, "git-green") {
		t.Errorf("title lost its name: %q", got)
	}
}

func TestThrottleNoticeNamesBudgetPaceAndRecovery(t *testing.T) {
	reset := time.Date(2026, 8, 27, 15, 12, 0, 0, time.Local)
	got := ThrottleNotice([]state.Throttle{{
		Orgs:      []string{"ndlibrary"},
		Remaining: 200,
		Limit:     5000,
		Reset:     reset,
		Interval:  5 * time.Minute,
	}})

	for _, want := range []string{"ndlibrary", "4%", "5m0s", "15:12"} {
		if !strings.Contains(got, want) {
			t.Errorf("got %q, want it to contain %q", got, want)
		}
	}
}

// A token shared by many orgs must not run the title line off the screen.
func TestThrottleNoticeTrimsLongOrgLists(t *testing.T) {
	got := ThrottleNotice([]state.Throttle{{
		Orgs:      []string{"a", "b", "c", "d"},
		Remaining: 100,
		Limit:     5000,
		Interval:  time.Hour,
	}})
	if !strings.Contains(got, "+2") {
		t.Errorf("got %q, want the extra orgs summarised as +2", got)
	}
	if strings.Contains(got, "d") && strings.Contains(got, "c") {
		t.Errorf("got %q, want only the first two orgs named", got)
	}
}

func TestThrottleNoticeEmptyWhenNothingThrottled(t *testing.T) {
	if got := ThrottleNotice(nil); got != "" {
		t.Errorf("got %q, want empty", got)
	}
}
