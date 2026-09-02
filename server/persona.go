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
// means no persona — same forgiving stance as readConfig).
func readPersona() string {
	path := personaPath()
	if path == "" {
		return ""
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(data)
}

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
