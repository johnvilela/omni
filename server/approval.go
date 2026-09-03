package main

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
)

// The approval gate: privileged TOOL lines in a chat reply are parked as a
// proposal and sent to the owner with approve / always / deny / edit buttons;
// applyTools runs them only on approval. Agent sessions are untouched — their
// tools run inside the vendor CLI.

// defaultApprovalTools are the privileged chat tools gated behind owner
// approval; read_file and send_file stay free. task_start is gated because it
// spawns hours of full-permission agent runs.
var defaultApprovalTools = []string{"write_file", "edit_file", "delete_file",
	"cron_add", "cron_edit", "cron_delete", "analyze_file", "task_start"}

// gatedTools is the effective set needing approval: approval_tools (default
// set when unset) minus approval_skip; nil when approvals is "off".
func gatedTools() []string {
	cfg := readConfig()
	if cfg.Approvals == "off" {
		return nil
	}
	tools := defaultApprovalTools
	if cfg.ApprovalTools != nil { // explicit empty list gates nothing
		tools = cfg.ApprovalTools
	}
	var out []string
	for _, t := range tools {
		if !slices.Contains(cfg.ApprovalSkip, t) {
			out = append(out, t)
		}
	}
	return out
}

// gatedNames lists (deduped) gated tool names present in reply's TOOL lines,
// parsing exactly like applyTools so gate and executor always agree — a line
// applyTools would skip must never be gated.
func gatedNames(reply string) []string {
	if !strings.Contains(reply, "TOOL:") {
		return nil
	}
	gated := gatedTools()
	if len(gated) == 0 {
		return nil
	}
	var names []string
	for _, line := range strings.Split(reply, "\n") {
		name, _, ok := strings.Cut(strings.TrimSpace(line), " ")
		if !ok || !strings.HasPrefix(name, "TOOL:") {
			continue
		}
		n := strings.TrimPrefix(name, "TOOL:")
		if slices.Contains(gated, n) && !slices.Contains(names, n) {
			names = append(names, n)
		}
	}
	return names
}

// toolLines extracts just the TOOL: lines of a reply, in order, for delayed
// execution on approval.
func toolLines(reply string) string {
	var lines []string
	for _, line := range strings.Split(reply, "\n") {
		name, _, ok := strings.Cut(strings.TrimSpace(line), " ")
		if ok && strings.HasPrefix(name, "TOOL:") {
			lines = append(lines, strings.TrimSpace(line))
		}
	}
	return strings.Join(lines, "\n")
}

// proposeTools parks a reply whose TOOL lines need owner approval: history
// gets the un-executed turn, a proposals row persists it across restarts, and
// the owner gets the proposal with buttons immediately — bypassing the
// active/unread delivery, approval is urgent either way.
func (s *Server) proposeTools(ctx context.Context, sess Session, raw string) error {
	id, err := s.store.AddProposal(sess.ID, raw)
	if err != nil {
		return err
	}
	if _, err := s.store.AddMessage(sess.ID, "assistant",
		fmt.Sprintf("%s\n\n⏳ proposal #%d — the TOOL lines above were NOT executed; awaiting owner approval", raw, id),
		time.Now().Unix()); err != nil {
		return err
	}
	d := strconv.FormatInt(id, 10)
	// ponytail: no re-notify if telegram is down — the owner's next message
	// supersedes the stuck proposal, so it self-heals
	s.notifyOwner(ctx, tgReply{
		Text: "⏳ approval needed:\n\n" + raw,
		Keyboard: [][]button{
			{{Text: "✅ approve", CallbackData: "appr:" + d}, {Text: "✅ always", CallbackData: "alws:" + d}},
			{{Text: "🚫 deny", CallbackData: "deny:" + d}, {Text: "✏ edit", CallbackData: "edit:" + d}},
		},
	})
	return nil
}

// claimProposal parses a callback id and removes the row, so a double tap
// never acts twice; false means the proposal is gone (or the id is garbage).
func (s *Server) claimProposal(data string) (Proposal, tgReply, bool) {
	id, err := strconv.ParseInt(data, 10, 64)
	if err != nil {
		return Proposal{}, tgReply{Text: "⚠ bad proposal id"}, false
	}
	p, ok, err := s.store.Proposal(id)
	if err != nil {
		return Proposal{}, tgReply{Text: "⚠ " + err.Error()}, false
	}
	if !ok {
		// resolved rows have dead buttons — strip them off the tapped message
		return Proposal{}, tgReply{Text: "⚠ proposal already resolved", StripKeyboard: true}, false
	}
	if err := s.store.DeleteProposal(id); err != nil {
		return Proposal{}, tgReply{Text: "⚠ " + err.Error()}, false
	}
	return p, tgReply{}, true
}

// approveProposal claims one proposal and runs it in the background: the
// poller must not block on an analyze_file agent run. always additionally
// whitelists the proposal's gated tools in config.yaml.
func (s *Server) approveProposal(data string, always bool) tgReply {
	p, fail, ok := s.claimProposal(data)
	if !ok {
		return fail
	}
	ack := "✅ approved — running"
	if always {
		names := gatedNames(p.Reply)
		skip := readConfig().ApprovalSkip
		for _, n := range names {
			if !slices.Contains(skip, n) {
				skip = append(skip, n)
			}
		}
		if err := saveConfigValue("approval_skip", skip); err != nil {
			ack = "✅ approved — running (⚠ whitelist not saved: " + err.Error() + ")"
		} else {
			ack = "✅ approved — " + strings.Join(names, ", ") + " whitelisted, won't ask again — running"
		}
	}
	go s.runProposal(p)
	return tgReply{Text: ack, StripKeyboard: true}
}

// runProposal executes an approved proposal's TOOL lines in order (free lines
// included — approval restores the reply's original contract) and delivers
// the confirmations straight to the owner, who just tapped. Own context like
// runTask: per-call timeouts live in runCLI/askAPI.
// ponytail: runs outside the session queue — an approval landing mid-turn can
// interleave history writes; acceptable for one owner. No follow-up llm round
// for approved read_file lines; the model reads the 📄 dump from history.
func (s *Server) runProposal(p Proposal) {
	ctx := context.Background()
	stop := s.typingOwner(ctx)
	defer stop()
	res := s.applyTools(ctx, toolLines(p.Reply))
	s.store.AddMessage(p.SessionID, "assistant",
		fmt.Sprintf("✅ owner approved proposal #%d — executed:\n%s", p.ID, res), time.Now().Unix())
	s.notifyOwner(ctx, tgReply{Text: res})
}

func (s *Server) denyProposal(data string) tgReply {
	p, fail, ok := s.claimProposal(data)
	if !ok {
		return fail
	}
	s.store.AddMessage(p.SessionID, "user",
		fmt.Sprintf("🚫 owner denied proposal #%d — those actions were NOT run", p.ID), time.Now().Unix())
	return tgReply{Text: "🚫 denied — nothing was executed", StripKeyboard: true}
}

// editProposal deletes nothing: the next plain message hits the supersede
// block in chatAnswer, which is the entire edit mechanism.
func (s *Server) editProposal(data string) tgReply {
	id, err := strconv.ParseInt(data, 10, 64)
	if err != nil {
		return tgReply{Text: "⚠ bad proposal id"}
	}
	if _, ok, err := s.store.Proposal(id); err != nil || !ok {
		return tgReply{Text: "⚠ proposal already resolved", StripKeyboard: true}
	}
	return tgReply{Text: "✏ send your changes as a normal message — the pending proposal will be cancelled and revised"}
}
