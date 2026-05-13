package ui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

var titleBarStyle = lipgloss.NewStyle().Bold(true)

// TitleLine renders the app title and optional in-flight fetch spinner.
func TitleLine(fetching bool, spinView string) string {
	s := strings.TrimSpace(spinView)
	if fetching && s != "" {
		return titleBarStyle.Render("git-green "+s) + "\n"
	}
	return titleBarStyle.Render("git-green") + "\n"
}
