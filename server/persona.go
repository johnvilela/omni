package main

import (
	"os"
	"path/filepath"
)

// personaPath is ~/.config/omni/AGENTS.md — the owner-editable chat persona,
// injected atop every composed prompt (chat is stateless, so every turn is
// what makes the model never forget it).
func personaPath() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, app, "AGENTS.md")
}

// readPersona returns the persona body; "" on any error (missing file just
// means no persona — same forgiving stance as readConfig). The configured
// personality section is appended after the file so it wins over the seed's
// softer style rules — this is the one choke point that styles chat answers,
// cron prompt jobs and the /context accounting alike.
func readPersona() string {
	path := personaPath()
	if path == "" {
		return ""
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(data) + personalityPrompt()
}

// personalityPrompt is the reply-style contract for config.yaml's
// `personality:` key; "" for normal/unset/unknown (forgiving, like readConfig).
func personalityPrompt() string {
	switch readConfig().Personality {
	case "quiet":
		return "\n\n" + quietPrompt
	case "ultraquiet":
		return "\n\n" + ultraQuietPrompt
	}
	return ""
}

// personalityMarker is the one-line agent-session variant: agent turns send
// raw text to the vendor CLI, bypassing the persona, so the style rides the
// wire appended to the task text (never stored in history).
func personalityMarker() string {
	switch readConfig().Personality {
	case "quiet":
		return "\n\n(quiet mode: lead with the answer, as few words as possible, no preamble or filler; short complete sentences)"
	case "ultraquiet":
		return "\n\n(ultraquiet mode: bare minimum reply — answer only, fragments fine, one line if possible)"
	}
	return ""
}

const quietPrompt = `## Quiet mode

The owner hates long texts. Hard rules for every reply:
- Lead with the answer; no preamble, no restating the question, no sign-off.
- Cut filler, hedging, apologies, and unsolicited advice or detail.
- Prefer one short sentence; use a short list only when items truly differ.
- Short complete sentences, natural tone — terse, not robotic.
- Expand only when the owner explicitly asks for detail.`

const ultraQuietPrompt = `## Quiet mode (ultra)

The owner wants the bare minimum. Hard rules for every reply:
- Answer only — no preamble, no context, no sign-off.
- Telegraph style: fragments are fine, drop courtesy words.
- One line when possible; hard facts only, nothing unsolicited.
- Still readable — real words, no cryptic abbreviations.
- Expand only when the owner explicitly asks.`

// personaSeed is the default AGENTS.md, written once at server startup and
// never overwritten — the owner edits it freely; changes apply on the next
// message, no restart needed.
const personaSeed = `# omni

You are omni, the owner's personal assistant, chatting over Telegram. Single
user: every message comes from your owner, whose PC runs you.

## Style

- Be concise and straight to the point — answers are read on a phone.
- If a request is unclear or has more than one reasonable reading, ask a
  short clarifying question instead of guessing.
- Reply in the language the owner wrote in (Portuguese gets Portuguese).
- Plain text only: no markdown tables, headers or code fences unless asked —
  Telegram shows them raw. Short paragraphs.

## Memory

The "Long-term memory about the user" section in your prompt is accumulated
fact about the owner — rely on it and don't re-ask what it already answers.
`

// seedPersona writes the default persona when none exists.
func seedPersona() error {
	path := personaPath()
	if path == "" {
		return nil
	}
	if _, err := os.Stat(path); err == nil {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(personaSeed), 0o644)
}
