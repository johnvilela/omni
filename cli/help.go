package main

import "github.com/charmbracelet/lipgloss"

const banner = `
 ██████╗ ███╗   ███╗███╗   ██╗██╗
██╔═══██╗████╗ ████║████╗  ██║██║
██║   ██║██╔████╔██║██╔██╗ ██║██║
██║   ██║██║╚██╔╝██║██║╚██╗██║██║
╚██████╔╝██║ ╚═╝ ██║██║ ╚████║██║
 ╚═════╝ ╚═╝     ╚═╝╚═╝  ╚═══╝╚═╝
`

func helpText() string {
	bannerStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("212"))
	cmd := lipgloss.NewStyle().Foreground(lipgloss.Color("205")).Bold(true)

	row := func(c, desc string) string {
		return "  " + cmd.Render(c) + "\n      " + desc + "\n"
	}

	return bannerStyle.Render(banner) + "\n" +
		dimStyle.Render("  a simplified, self-hosted messaging hub") + "\n\n" +
		titleStyle.Render("  USAGE") + "\n\n" +
		row("omni help", "show this help (also: omni --help, omni -h)") +
		row("omni channels", "list all channels and their status") +
		row("omni channels telegram", "show the telegram channel status") +
		row("omni channels connect", "pick a channel to connect") +
		row("omni channels connect -c telegram", "connect telegram directly, skipping the picker") +
		"\n" + titleStyle.Render("  SERVER") + "\n\n" +
		"      start it with " + cmd.Render("go run ./server") + "\n" +
		"      " + helpStyle.Render("listens on :8787 — override with OMNI_ADDR") + "\n\n"
}
