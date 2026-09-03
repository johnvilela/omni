package main

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"
)

// cronExpr is a parsed 5-field cron expression; each field is a bitmask of
// allowed values. ponytail: hand-rolled instead of robfig/cron — ~60 lines
// covers *, N, N-M, */S, N-M/S and lists, which is all an llm ever emits.
type cronExpr struct {
	min, hour, dom, month, dow uint64
	domStar, dowStar           bool // "*" as written, for the vixie dom/dow rule
}

// cronField parses one field into a bitmask over [lo, hi].
func cronField(s string, lo, hi int) (uint64, error) {
	var mask uint64
	for _, part := range strings.Split(s, ",") {
		rng, stepStr, hasStep := strings.Cut(part, "/")
		step := 1
		if hasStep {
			n, err := strconv.Atoi(stepStr)
			if err != nil || n < 1 {
				return 0, fmt.Errorf("bad step %q", part)
			}
			step = n
		}
		from, to := lo, hi
		if rng != "*" {
			a, b, isRange := strings.Cut(rng, "-")
			n, err := strconv.Atoi(a)
			if err != nil {
				return 0, fmt.Errorf("bad value %q", part)
			}
			from, to = n, n
			if isRange {
				m, err := strconv.Atoi(b)
				if err != nil {
					return 0, fmt.Errorf("bad range %q", part)
				}
				to = m
			} else if hasStep {
				to = hi // "N/S" means N to max by S
			}
		}
		if from < lo || to > hi || from > to {
			return 0, fmt.Errorf("value out of range in %q", part)
		}
		for v := from; v <= to; v += step {
			mask |= 1 << v
		}
	}
	return mask, nil
}

// parseCron parses "min hour dom month dow"; dow 0 and 7 are both sunday.
func parseCron(expr string) (cronExpr, error) {
	f := strings.Fields(expr)
	if len(f) != 5 {
		return cronExpr{}, fmt.Errorf("want 5 fields, got %d", len(f))
	}
	var c cronExpr
	var err error
	if c.min, err = cronField(f[0], 0, 59); err != nil {
		return cronExpr{}, err
	}
	if c.hour, err = cronField(f[1], 0, 23); err != nil {
		return cronExpr{}, err
	}
	if c.dom, err = cronField(f[2], 1, 31); err != nil {
		return cronExpr{}, err
	}
	if c.month, err = cronField(f[3], 1, 12); err != nil {
		return cronExpr{}, err
	}
	if c.dow, err = cronField(f[4], 0, 7); err != nil {
		return cronExpr{}, err
	}
	if c.dow&(1<<7) != 0 {
		c.dow |= 1 // 7 == sunday == 0
	}
	c.domStar, c.dowStar = f[2] == "*", f[4] == "*"
	return c, nil
}

func (c cronExpr) matches(t time.Time) bool {
	if c.min&(1<<t.Minute()) == 0 || c.hour&(1<<t.Hour()) == 0 || c.month&(1<<int(t.Month())) == 0 {
		return false
	}
	domOK := c.dom&(1<<t.Day()) != 0
	dowOK := c.dow&(1<<int(t.Weekday())) != 0
	// vixie rule: both restricted → either matches; otherwise both must
	if !c.domStar && !c.dowStar {
		return domOK || dowOK
	}
	return domOK && dowOK
}

// runCrons fires due jobs once per minute until ctx ends. Fires during
// downtime are skipped — classic cron semantics.
func (s *Server) runCrons(ctx context.Context) {
	for last := time.Now().Truncate(time.Minute); ctx.Err() == nil; {
		next := last.Add(time.Minute)
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Until(next)):
		}
		last = next
		s.fireCrons(ctx, next)
	}
}

func (s *Server) fireCrons(ctx context.Context, t time.Time) {
	cs, err := s.store.Crons()
	if err != nil {
		log.Printf("cron: %v", err)
		return
	}
	for _, c := range cs {
		expr, err := parseCron(c.Schedule)
		if err != nil || !expr.matches(t) {
			continue
		}
		go s.fireCron(ctx, c)
	}
}

// fireCron runs one due job and delivers the result to every approved
// telegram pairing.
func (s *Server) fireCron(ctx context.Context, c Cron) {
	var text string
	switch c.Kind {
	case "prompt":
		budget, _ := chatBudget(s.chatProvider(Session{})) // Answer uses the default llm
		prompt, _ := composePrompt(readPersona(), "", nil, c.Text, budget)
		reply, err := s.Answer(ctx, prompt)
		if err != nil {
			text = fmt.Sprintf("⚠ cron #%d failed: %v", c.ID, err)
		} else {
			text = reply
		}
	case "agent":
		provider, _ := agentProvider()
		if err := ensureAgentDir(); err != nil {
			text = fmt.Sprintf("⚠ cron #%d failed: %v", c.ID, err)
			break
		}
		var reply string
		var u callUsage
		var err error
		if provider == "openai" {
			reply, _, u, err = runCodexAgent(ctx, "", c.Text)
		} else {
			reply, _, u, err = runClaudeAgent(ctx, "", c.Text)
		}
		if err != nil {
			text = fmt.Sprintf("⚠ cron #%d failed: %v", c.ID, err)
			break
		}
		s.recordUsage(provider, u)
		// same trust domain as agent sessions: scheduled runs can attach files
		// and start long tasks
		text = s.applyAgentTools(ctx, strings.TrimSpace(reply))
	default: // "message"
		text = "⏰ " + c.Text
	}
	if text == "" {
		text = "(empty reply)"
	}
	s.notifyOwner(ctx, tgReply{Text: text})
}

// notifyOwner sends a proactive message to every approved telegram pairing.
func (s *Server) notifyOwner(ctx context.Context, r tgReply) {
	s.mu.Lock()
	tg := s.tg
	s.mu.Unlock()
	if tg == nil {
		log.Printf("notify: telegram not connected, dropping: %.60s", r.Text)
		return
	}
	for _, id := range s.ownerChats() {
		tg.send(ctx, id, r)
	}
}

// ownerChats lists the chat ids of every approved telegram pairing (private
// chats: chat id == user id).
func (s *Server) ownerChats() []int64 {
	ps, err := s.store.Pairings("telegram")
	if err != nil {
		log.Printf("notify: %v", err)
		return nil
	}
	var ids []int64
	for _, p := range ps {
		if !p.Approved {
			continue
		}
		id, err := strconv.ParseInt(p.UserID, 10, 64)
		if err != nil {
			continue
		}
		ids = append(ids, id)
	}
	return ids
}
