package ui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/ericdahl-dev/git-green/internal/state"
)

var (
	titleBarStyle  = lipgloss.NewStyle().Bold(true)
	throttleStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
	throttleMaxOrg = 2
)

// TitleLine renders the app title, an optional in-flight fetch spinner, and a
// notice for any token being polled slower than configured.
func TitleLine(fetching bool, spinView string, throttles []state.Throttle) string {
	title := "git-green"
	if s := strings.TrimSpace(spinView); fetching && s != "" {
		title += " " + s
	}
	line := titleBarStyle.Render(title)
	if notice := ThrottleNotice(throttles); notice != "" {
		line += "  " + throttleStyle.Render(notice)
	}
	return line + "\n"
}

// ThrottleNotice summarises why polling has slowed, or "" while every token is
// healthy. It names the budget, the pace it bought, and when it recovers, so a
// slow dashboard reads as throttled rather than broken.
func ThrottleNotice(throttles []state.Throttle) string {
	if len(throttles) == 0 {
		return ""
	}
	parts := make([]string, 0, len(throttles))
	for _, t := range throttles {
		orgs := strings.Join(t.Orgs, ", ")
		if len(t.Orgs) > throttleMaxOrg {
			orgs = fmt.Sprintf("%s +%d", strings.Join(t.Orgs[:throttleMaxOrg], ", "), len(t.Orgs)-throttleMaxOrg)
		}
		if orgs == "" {
			orgs = "token"
		}
		pct := 0
		if t.Limit > 0 {
			pct = t.Remaining * 100 / t.Limit
		}
		parts = append(parts, fmt.Sprintf("⏳ %s %d%% left · polling every %s · resets %s",
			orgs, pct, humanInterval(t.Interval), t.Reset.Local().Format("15:04")))
	}
	return strings.Join(parts, "   ")
}

// humanInterval renders a poll interval the way someone reading a status line
// would say it.
func humanInterval(d time.Duration) string {
	switch {
	case d >= time.Hour:
		return d.Round(time.Minute).String()
	case d >= time.Minute:
		return d.Round(time.Second).String()
	default:
		return d.Round(time.Second).String()
	}
}
