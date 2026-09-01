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

var cmdStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("205")).Bold(true)

func row(c, desc string) string {
	return "  " + cmdStyle.Render(c) + "\n      " + desc + "\n"
}

// helpText is the root screen: banner + top-level commands only.
func helpText() string {
	bannerStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("212"))

	return bannerStyle.Render(banner) + "\n" +
		dimStyle.Render("  a simplified, self-hosted messaging hub · cli "+version) + "\n\n" +
		titleStyle.Render("  COMMANDS") + "\n\n" +
		row("omni status", "server, channels and alerts at a glance") +
		row("omni channels", "manage message channels") +
		row("omni help", "show this help (also: omni --help, omni -h)") +
		"\n" + helpStyle.Render("      run `omni <command> --help` for flags, subcommands and examples") + "\n" +
		"\n" + titleStyle.Render("  SERVER") + "\n\n" +
		"      start it with " + cmdStyle.Render(app+"-server") + "\n" +
		"      " + helpStyle.Render("listens on "+defaultAddr+" — override with OMNI_ADDR") + "\n\n"
}

func helpStatus() string {
	return "\n" + titleStyle.Render("  USAGE") + "\n\n" +
		"      " + cmdStyle.Render("omni status") + "\n\n" +
		"      server, channels and alerts at a glance\n\n"
}

func helpChannels() string {
	return "\n" + titleStyle.Render("  USAGE") + "\n\n" +
		"      " + cmdStyle.Render("omni channels [<name> | connect]") + "\n\n" +
		titleStyle.Render("  SUBCOMMANDS") + "\n\n" +
		row("omni channels", "list all channels and their status") +
		row("omni channels <name>", "show one channel's status") +
		row("omni channels connect", "connect a channel (see connect --help)") +
		"\n" + titleStyle.Render("  EXAMPLES") + "\n\n" +
		"      omni channels telegram\n" +
		"      omni channels connect -c telegram\n\n"
}

func helpConnect() string {
	return "\n" + titleStyle.Render("  USAGE") + "\n\n" +
		"      " + cmdStyle.Render("omni channels connect [-c <channel>]") + "\n\n" +
		titleStyle.Render("  FLAGS") + "\n\n" +
		row("-c <channel>", "channel to connect, skipping the picker") +
		"\n" + titleStyle.Render("  EXAMPLES") + "\n\n" +
		"      omni channels connect\n" +
		"      omni channels connect -c telegram\n\n" +
		helpStyle.Render("      the bot token is read from TELEGRAM_BOT_TOKEN or prompted for\n"+
			"      and saved to ~/.config/"+app+"/config.yaml") + "\n\n"
}
