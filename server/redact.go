package main

import (
	"errors"
	"regexp"
	"strings"
	"sync"
)

// Secrets omni holds (bot token, api token, provider keys) register here the
// moment they are resolved, so any text that leaves the process — chat
// replies, the server log, ai-memory capture, API error bodies — can be
// scrubbed with redactSecrets. Transport errors are the usual carrier: Go's
// *url.Error prints the full request URL, and Telegram's URL embeds the token.

var (
	secretsMu sync.RWMutex
	secrets   []string
)

// botTokenRe scrubs telegram bot tokens by shape, for text whose token was
// never registered here (an old journal line). Twin in cli/doctor.go.
var botTokenRe = regexp.MustCompile(`bot\d+:[\w-]+`)

// registerSecret remembers a value to scrub. Short values are ignored: they
// would redact ordinary words.
func registerSecret(v string) {
	v = strings.TrimSpace(v)
	if len(v) < 8 {
		return
	}
	secretsMu.Lock()
	defer secretsMu.Unlock()
	for _, s := range secrets {
		if s == v {
			return
		}
	}
	secrets = append(secrets, v)
}

// redactSecrets replaces every registered secret (and anything shaped like a
// bot token) with ***.
func redactSecrets(s string) string {
	secretsMu.RLock()
	for _, v := range secrets {
		s = strings.ReplaceAll(s, v, "***")
	}
	secretsMu.RUnlock()
	return botTokenRe.ReplaceAllString(s, "bot***")
}

// redactedError carries a scrubbed message while still unwrapping to the
// original, so errors.Is(err, context.DeadlineExceeded) keeps working.
type redactedError struct {
	msg string
	err error
}

func (e *redactedError) Error() string { return e.msg }
func (e *redactedError) Unwrap() error { return e.err }

// redactErr returns err with its message scrubbed (nil stays nil).
func redactErr(err error) error {
	if err == nil {
		return nil
	}
	msg := redactSecrets(err.Error())
	if msg == err.Error() {
		return err
	}
	var re *redactedError
	if errors.As(err, &re) {
		return &redactedError{msg: msg, err: re.err}
	}
	return &redactedError{msg: msg, err: err}
}
