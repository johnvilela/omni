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
		dimStyle.Render("  a simplified, self-hosted messaging hub · "+version) + "\n\n" +
		titleStyle.Render("  COMMANDS") + "\n\n" +
		row("omni status", "server, channels, llm providers and alerts at a glance") +
		row("omni channels", "manage message channels") +
		row("omni llm", "manage llm providers (openai, claude, gemini)") +
		row("omni pairing", "control who may talk to the bot") +
		row("omni help", "show this help (also: omni --help, omni -h)") +
		"\n" + helpStyle.Render("      run `omni <command> --help` for flags, subcommands and examples") + "\n" +
		"\n" + titleStyle.Render("  SERVER") + "\n\n" +
		"      start it with " + cmdStyle.Render(app+"-server") + "\n" +
		"      " + helpStyle.Render("listens on "+defaultAddr+" — override with OMNI_ADDR") + "\n\n"
}

func helpStatus() string {
	return "\n" + titleStyle.Render("  USAGE") + "\n\n" +
		"      " + cmdStyle.Render("omni status") + "\n\n" +
		"      server, channels, llm providers and alerts at a glance\n" +
		helpStyle.Render("      alerts warn when the cli and server versions differ") + "\n\n"
}

func helpPairing() string {
	return "\n" + titleStyle.Render("  USAGE") + "\n\n" +
		"      " + cmdStyle.Render("omni pairing [approve | revoke]") + "\n\n" +
		titleStyle.Render("  SUBCOMMANDS") + "\n\n" +
		row("omni pairing", "list paired and pending telegram users") +
		row("omni pairing approve telegram <code>", "authorize the user who got this code") +
		row("omni pairing revoke telegram <user-id>", "remove a paired user") +
		"\n" + titleStyle.Render("  EXAMPLES") + "\n\n" +
		"      omni pairing approve telegram ABCD2345\n" +
		"      omni pairing revoke telegram 123456789\n\n" +
		helpStyle.Render("      an unpaired user who messages the bot receives a pairing code;\n"+
			"      the bot answers nothing else until you approve it") + "\n\n"
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

func helpLLM() string {
	return "\n" + titleStyle.Render("  USAGE") + "\n\n" +
		"      " + cmdStyle.Render("omni llm [<provider> | connect]") + "\n\n" +
		titleStyle.Render("  SUBCOMMANDS") + "\n\n" +
		row("omni llm", "list all llm providers and their status") +
		row("omni llm <provider>", "show one provider's status") +
		row("omni llm connect", "connect a provider (see connect --help)") +
		row("omni llm set-default", "pick the default provider (see set-default --help)") +
		"\n" + titleStyle.Render("  EXAMPLES") + "\n\n" +
		"      omni llm openai\n" +
		"      omni llm connect -p claude\n" +
		"      omni llm set-default -p claude\n\n"
}

func helpLLMSetDefault() string {
	return "\n" + titleStyle.Render("  USAGE") + "\n\n" +
		"      " + cmdStyle.Render("omni llm set-default [-p <provider>]") + "\n\n" +
		titleStyle.Render("  FLAGS") + "\n\n" +
		row("-p <provider>", "provider to make the default, skipping the picker") +
		"\n" + titleStyle.Render("  EXAMPLES") + "\n\n" +
		"      omni llm set-default\n" +
		"      omni llm set-default -p openai\n\n" +
		helpStyle.Render("      saved as default_llm in ~/.config/"+app+"/config.yaml —\n"+
			"      back up that file to carry keys and the default to a new PC") + "\n\n"
}

func helpLLMConnect() string {
	return "\n" + titleStyle.Render("  USAGE") + "\n\n" +
		"      " + cmdStyle.Render("omni llm connect [-p <provider>]") + "\n\n" +
		titleStyle.Render("  FLAGS") + "\n\n" +
		row("-p <provider>", "provider to connect, skipping the picker") +
		"\n" + titleStyle.Render("  EXAMPLES") + "\n\n" +
		"      omni llm connect\n" +
		"      omni llm connect -p gemini\n\n" +
		helpStyle.Render("      logins from the codex, claude and gemini CLIs are reused\n"+
			"      automatically; otherwise the key is read from OPENAI_API_KEY,\n"+
			"      ANTHROPIC_API_KEY or GEMINI_API_KEY, or prompted for and saved\n"+
			"      to ~/.config/"+app+"/config.yaml") + "\n\n"
}
