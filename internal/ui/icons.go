package ui

import "github.com/charmbracelet/lipgloss"

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
