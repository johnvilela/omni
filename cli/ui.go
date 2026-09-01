package main

import (
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

var (
	titleStyle    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("212"))
	cursorStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("212")).Bold(true)
	itemStyle     = lipgloss.NewStyle().PaddingLeft(2)
	selectedStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("205")).Bold(true)
	helpStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
	okStyle       = lipgloss.NewStyle().Foreground(lipgloss.Color("42")).Bold(true)
	errStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("196")).Bold(true)
	dimStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
)

// selectModel is a minimal arrow-key picker.
type selectModel struct {
	title  string
	items  []string
	cursor int
	choice string
}

func newSelectModel(title string, items []string) selectModel {
	return selectModel{title: title, items: items}
}

func (m selectModel) Init() tea.Cmd { return nil }

func (m selectModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if k, ok := msg.(tea.KeyMsg); ok {
		switch k.String() {
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
			}
		case "down", "j":
			if m.cursor < len(m.items)-1 {
				m.cursor++
			}
		case "enter":
			m.choice = m.items[m.cursor]
			return m, tea.Quit
		case "esc", "q", "ctrl+c":
			return m, tea.Quit
		}
	}
	return m, nil
}

func (m selectModel) View() string {
	s := titleStyle.Render(m.title) + "\n\n"
	for i, item := range m.items {
		if i == m.cursor {
			s += cursorStyle.Render("❯ ") + selectedStyle.Render(item) + "\n"
		} else {
			s += itemStyle.Render(item) + "\n"
		}
	}
	return s + "\n" + helpStyle.Render("↑/↓ move · enter select · esc cancel") + "\n"
}

// tokenModel prompts for the Telegram bot token.
type tokenModel struct {
	input    textinput.Model
	done     bool
	canceled bool
}

func newTokenModel() tokenModel {
	ti := textinput.New()
	ti.Placeholder = "123456:ABC-DEF..."
	ti.EchoMode = textinput.EchoPassword
	ti.Focus()
	return tokenModel{input: ti}
}

func (m tokenModel) Init() tea.Cmd { return textinput.Blink }

func (m tokenModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if k, ok := msg.(tea.KeyMsg); ok {
		switch k.String() {
		case "enter":
			m.done = true
			return m, tea.Quit
		case "esc", "ctrl+c":
			m.canceled = true
			return m, tea.Quit
		}
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

func (m tokenModel) View() string {
	return titleStyle.Render("Telegram bot token") + "\n\n" +
		"  " + m.input.View() + "\n\n" +
		helpStyle.Render("paste your BotFather token · enter confirm · esc cancel") + "\n"
}

// Token returns the entered token, or "" if the prompt was canceled.
func (m tokenModel) Token() string {
	if m.canceled {
		return ""
	}
	return m.input.Value()
}
