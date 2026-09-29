package ui

import (
	"github.com/charmbracelet/lipgloss"

	"github.com/ericdahl-dev/git-green/internal/aggregator"
	githubclient "github.com/ericdahl-dev/git-green/internal/github"
)

var (
	jobGreen  = lipgloss.NewStyle().Foreground(lipgloss.Color("10"))
	jobRed    = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))
	jobYellow = lipgloss.NewStyle().Foreground(lipgloss.Color("11"))
	jobFaint  = lipgloss.NewStyle().Faint(true)
)

// runIcon marks a Run or Job by its effective status, in its Stoplight colour.
func runIcon(status string) string {
	switch aggregator.Of(status) {
	case aggregator.StoplightGreen:
		return jobGreen.Render("✓")
	case aggregator.StoplightRed:
		return jobRed.Render("✗")
	case aggregator.StoplightYellow:
		return jobYellow.Render("●")
	default:
		return jobFaint.Render("○")
	}
}

// reviewGlyph marks a PR's review state after its number, with a leading
// space, or returns nothing when there is no review signal.
func reviewGlyph(r githubclient.Review) string {
	switch r {
	case githubclient.ReviewApproved:
		return " " + jobGreen.Render("✓")
	case githubclient.ReviewChangesRequested:
		return " " + jobRed.Render("±")
	case githubclient.ReviewWaiting:
		return " " + jobYellow.Render("○")
	default:
		return ""
	}
}
