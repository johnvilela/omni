package main

// /ops is a quick-actions panel: one inline keyboard over things omni already
// knows how to do, dispatched via ops:* callbacks (see gatedCallback). Not a
// subsystem — every action reuses an existing capability.

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
)

// botTokenRe scrubs telegram bot tokens that transport errors embed in URLs —
// log lines go to chat, the token must not (twin of cli/doctor.go).
var botTokenRe = regexp.MustCompile(`bot\d+:[\w-]+`)

func (s *Server) opsMenu() tgReply {
	return tgReply{Text: "🛠 omni " + version, Keyboard: [][]button{
		{{Text: "📊 Status", CallbackData: "ops:status"}, {Text: "🩺 Doctor", CallbackData: "ops:doctor"}},
		{{Text: "📜 Logs", CallbackData: "ops:logs"}, {Text: "💾 Disk", CallbackData: "ops:disk"}},
		{{Text: "🔄 Restart", CallbackData: "ops:restart"}, {Text: "🆕 Update", CallbackData: "ops:update"}},
		{{Text: "🖥 Terminal", CallbackData: "ops:terminal"}},
	}}
}

// opsAction runs one tapped quick action. The menu's keyboard is left in
// place so the panel stays reusable; only the restart confirm strips itself.
func (s *Server) opsAction(act string) tgReply {
	switch act {
	case "status":
		text, err := s.renderPin(true)
		if err != nil {
			return tgReply{Text: "⚠ " + err.Error()}
		}
		return tgReply{Text: "🛠 omni " + version + "\n" + text}
	case "doctor":
		// the "$ omni doctor" one-shot: streamed output, /interrupt works
		go s.deliverOneShot(context.Background(), "", app+" doctor")
		return tgReply{}
	case "logs":
		return opsLogs()
	case "disk":
		return opsDisk()
	case "restart":
		return tgReply{Text: "🔄 restart " + app + "-server?", Keyboard: [][]button{{
			{Text: "✅ restart", CallbackData: "ops:restart!"},
			{Text: "✖ cancel", CallbackData: "ops:cancel"},
		}}}
	case "restart!":
		return opsRestart()
	case "cancel":
		return tgReply{Text: "cancelled", StripKeyboard: true}
	case "update":
		// un-throttle (and un-ignore: an explicit check means "show me") and
		// run the guardian now; it sends the 🆕 offer if a release is newer
		os.Remove(filepath.Join(dataDir(), "updates.stamp"))
		os.Remove(filepath.Join(dataDir(), "update.ignore"))
		if err := exec.Command("systemctl", "--user", "start", "--no-block", app+"-guardian.service").Run(); err != nil {
			return tgReply{Text: "⚠ systemctl: " + err.Error()}
		}
		return tgReply{Text: "🔎 checking for updates (current " + version + ") — the 🆕 offer follows if there's a newer release; silence means up to date"}
	case "terminal":
		s.termMu.Lock()
		active := s.term != nil || s.termPending != nil
		s.termMu.Unlock()
		if active {
			return tgReply{Text: "already in terminal mode"}
		}
		r, _ := s.handleTerminal(context.Background(), "/terminal")
		return r
	}
	return tgReply{Text: "⚠ unknown action"}
}

func opsLogs() tgReply {
	out, err := exec.Command("journalctl", "--user", "-u", app+"-server",
		"-n", "30", "--no-pager", "-o", "cat", "-q").Output()
	if err != nil {
		return tgReply{Text: "⚠ journalctl: " + err.Error()}
	}
	text := strings.TrimSpace(botTokenRe.ReplaceAllString(string(out), "bot***"))
	if text == "" {
		text = "(no recent log lines)"
	}
	return tgReply{Text: "📜 " + app + "-server · last 30 lines\n\n" + text}
}

// opsDisk reports free space on the data dir's filesystem plus the db size
// (twin of guardian checkDisk — two package mains can't share).
func opsDisk() tgReply {
	var st syscall.Statfs_t
	if err := syscall.Statfs(dataDir(), &st); err != nil {
		return tgReply{Text: "⚠ statfs: " + err.Error()}
	}
	msg := "💾 " + fmtBytes(st.Bavail*uint64(st.Bsize)) + " free"
	if fi, err := os.Stat(dbPath()); err == nil {
		msg += " · db " + fmtBytes(uint64(fi.Size()))
	}
	return tgReply{Text: msg}
}

func fmtBytes(b uint64) string {
	if b >= 1<<30 {
		return fmt.Sprintf("%.1f GiB", float64(b)/(1<<30))
	}
	return fmt.Sprintf("%d MiB", b>>20)
}

// opsRestart bounces the server through a transient systemd unit: it survives
// this process's cgroup kill, and the 2s delay lets the poller issue the next
// getUpdates so telegram confirms the tap — a synchronous restart would
// replay the button after boot and loop forever.
func opsRestart() tgReply {
	if err := exec.Command("systemd-run", "--user", "--on-active=2s", "--collect",
		"systemctl", "--user", "restart", app+"-server.service").Run(); err != nil {
		return tgReply{Text: "⚠ systemd-run: " + err.Error(), StripKeyboard: true}
	}
	return tgReply{Text: "🔄 restarting " + app + "-server — back in a few seconds", StripKeyboard: true}
}
