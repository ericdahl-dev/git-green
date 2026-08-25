package ui

import (
	"errors"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/huh"
	"github.com/ericdahl-dev/git-green/internal/config"
)

// ConfigChangedMsg is sent when the config has been mutated (add/edit/delete/toggle).
type ConfigChangedMsg struct {
	Config *config.Config
}

type manageMode int

const (
	manageModeList manageMode = iota
	manageModeForm
	manageModeConfirmDelete
)

// Manage is a Bubble Tea component for CRUD management of repos.
//
// fOwner, fName, and fBranch are heap-allocated so that huh form fields hold
// stable pointers even when the Manage value is copied during the Bubble Tea
// update cycle.
type Manage struct {
	cfg     *config.Config
	cursor  int
	mode    manageMode
	form    *huh.Form
	editIdx int // -1 = add, >=0 = edit index
	err     string

	fOwner  *string
	fName   *string
	fBranch *string
}

func NewManage(cfg *config.Config) Manage {
	owner, name, branch := "", "", ""
	return Manage{
		cfg:     cfg,
		editIdx: -1,
		fOwner:  &owner,
		fName:   &name,
		fBranch: &branch,
	}
}

func (m Manage) Init() tea.Cmd { return nil }

func (m Manage) Update(msg tea.Msg) (Manage, tea.Cmd) {
	switch m.mode {
	case manageModeForm:
		return m.updateForm(msg)
	case manageModeConfirmDelete:
		return m.updateConfirm(msg)
	default:
		return m.updateList(msg)
	}
}

func (m Manage) updateList(msg tea.Msg) (Manage, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		repos := m.cfg.Repos
		switch msg.String() {
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
			}
		case "down", "j":
			if m.cursor < len(repos)-1 {
				m.cursor++
			}
		case "e":
			if len(repos) > 0 {
				r := repos[m.cursor]
				m.editIdx = m.cursor
				*m.fOwner = r.Owner
				*m.fName = r.Name
				*m.fBranch = r.Branch
				m.form = m.buildForm("Edit repo")
				m.mode = manageModeForm
				return m, m.form.Init()
			}
		case "a":
			m.editIdx = -1
			*m.fOwner = ""
			*m.fName = ""
			*m.fBranch = ""
			m.form = m.buildForm("Add repo")
			m.mode = manageModeForm
			return m, m.form.Init()
		case "d":
			if len(repos) > 0 {
				m.mode = manageModeConfirmDelete
			}
		case "t", " ":
			if len(repos) > 0 {
				if err := m.cfg.ToggleRepo(m.cursor); err != nil {
					m.err = err.Error()
				} else {
					m.err = ""
					return m, configChangedCmd(m.cfg)
				}
			}
		case "esc":
			return m, func() tea.Msg { return BackMsg{} }
		}
	}
	return m, nil
}

func (m Manage) updateForm(msg tea.Msg) (Manage, tea.Cmd) {
	if msg, ok := msg.(tea.KeyMsg); ok && msg.String() == "esc" {
		m.mode = manageModeList
		m.form = nil
		return m, nil
	}

	form, cmd := m.form.Update(msg)
	if f, ok := form.(*huh.Form); ok {
		m.form = f
	}

	if m.form.State == huh.StateCompleted {
		owner := strings.TrimSpace(*m.fOwner)
		name := strings.TrimSpace(*m.fName)
		branch := strings.TrimSpace(*m.fBranch)

		if m.editIdx >= 0 {
			r := m.cfg.Repos[m.editIdx]
			r.Owner = owner
			r.Name = name
			r.Branch = branch
			if err := m.cfg.UpdateRepo(m.editIdx, r); err != nil {
				m.err = err.Error()
			} else {
				m.err = ""
			}
		} else {
			if err := m.cfg.AddRepo(config.Repo{Owner: owner, Name: name, Branch: branch}); err != nil {
				m.err = err.Error()
			} else {
				m.err = ""
				m.cursor = len(m.cfg.Repos) - 1
			}
		}
		m.mode = manageModeList
		m.form = nil
		return m, configChangedCmd(m.cfg)
	}

	if m.form.State == huh.StateAborted {
		m.mode = manageModeList
		m.form = nil
	}

	return m, cmd
}

func (m Manage) updateConfirm(msg tea.Msg) (Manage, tea.Cmd) {
	if msg, ok := msg.(tea.KeyMsg); ok {
		switch msg.String() {
		case "y", "Y":
			if err := m.cfg.RemoveRepo(m.cursor); err != nil {
				m.err = err.Error()
			} else {
				m.err = ""
				if m.cursor >= len(m.cfg.Repos) && m.cursor > 0 {
					m.cursor--
				}
			}
			m.mode = manageModeList
			return m, configChangedCmd(m.cfg)
		default:
			m.mode = manageModeList
		}
	}
	return m, nil
}

func (m Manage) buildForm(title string) *huh.Form {
	return huh.NewForm(
		huh.NewGroup(
			huh.NewInput().
				Title("Owner").
				Description("GitHub org or user").
				Value(m.fOwner).
				Validate(func(s string) error {
					if strings.TrimSpace(s) == "" {
						return errors.New("owner is required")
					}
					return nil
				}),
			huh.NewInput().
				Title("Repo name").
				Value(m.fName).
				Validate(func(s string) error {
					if strings.TrimSpace(s) == "" {
						return errors.New("repo name is required")
					}
					return nil
				}),
			huh.NewInput().
				Title("Branch").
				Description("Leave blank to use GitHub default branch").
				Value(m.fBranch),
		).Title(title),
	)
}

func (m Manage) View() string {
	switch m.mode {
	case manageModeForm:
		if m.form != nil {
			return m.form.View()
		}
	case manageModeConfirmDelete:
		if m.cursor < len(m.cfg.Repos) {
			r := m.cfg.Repos[m.cursor]
			return fmt.Sprintf(
				"\n  Delete %s/%s? This cannot be undone.\n\n  Press y to confirm, any other key to cancel.\n",
				r.Owner, r.Name,
			)
		}
	}

	return m.listView()
}

func (m Manage) listView() string {
	out := ""
	repos := m.cfg.Repos

	if len(repos) == 0 {
		out += staleStyle.Render("  No repos configured.") + "\n\n"
	}

	for i, r := range repos {
		enabled := r.IsEnabled()
		toggle := "✓"
		style := normalStyle
		if !enabled {
			toggle = "✗"
			style = staleStyle
		}
		name := fmt.Sprintf("%s/%s", r.Owner, r.Name)
		branch := r.Branch
		if branch == "" {
			branch = "(default)"
		}
		line := fmt.Sprintf(" %s  %-40s  %s", toggle, name, branch)
		if i == m.cursor {
			out += selectedStyle.Render("▶" + line) + "\n"
		} else {
			out += style.Render(" " + line) + "\n"
		}
	}

	if m.err != "" {
		out += "\n" + jobRed.Render("  ⚠ "+m.err) + "\n"
	}

	out += "\n" + hintStyle.Render("↑/↓ navigate  t/space toggle  a add  e edit  d delete  esc back")
	return out
}

func configChangedCmd(cfg *config.Config) tea.Cmd {
	return func() tea.Msg {
		return ConfigChangedMsg{Config: cfg}
	}
}
