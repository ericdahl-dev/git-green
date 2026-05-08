package ui

import (
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

var helpStyle = lipgloss.NewStyle().
	Border(lipgloss.RoundedBorder()).
	Padding(1, 3).
	BorderForeground(lipgloss.Color("212"))

type Help struct{}

func (h Help) View() string {
	return helpStyle.Render(`git-green keybindings

  ↑ / k         move up
  ↓ / j         move down
  enter / space  expand/collapse repo
  o              open run in browser
  r              force refresh
  ?              toggle this help
  esc            close help
  q / ctrl+c     quit`)
}

type ToggleHelpMsg struct{}

func ToggleHelpCmd() tea.Cmd {
	return func() tea.Msg { return ToggleHelpMsg{} }
}
