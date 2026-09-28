package ui

import (
	"github.com/charmbracelet/lipgloss"

	githubclient "github.com/ericdahl-dev/git-green/internal/github"
)

var (
	jobGreen  = lipgloss.NewStyle().Foreground(lipgloss.Color("10"))
	jobRed    = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))
	jobYellow = lipgloss.NewStyle().Foreground(lipgloss.Color("11"))
	jobFaint  = lipgloss.NewStyle().Faint(true)
)

func workflowStatusIcon(status string) string {
	switch status {
	case "success", "neutral", "skipped":
		return jobGreen.Render("✓")
	case "failure", "timed_out", "action_required":
		return jobRed.Render("✗")
	case "queued", "in_progress":
		return jobYellow.Render("●")
	default:
		return jobFaint.Render("○")
	}
}

func jobStatusIcon(status string) string {
	return workflowStatusIcon(status)
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
