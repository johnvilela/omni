package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// Plans are wiki pages in memoria's global wiki: no SQL table, the frontmatter
// (status: active|done, tag "long") is the whole state machine. /plan runs an
// interview in the chat session; plan_save (approval-gated) writes the page and
// nothing else; plan_start is the explicit go signal.
const plansDir = "omni-bot/plans"

// planSlug reduces a plan title to a filename slug.
func planSlug(title string) string {
	slug := strings.Trim(strings.ToLower(sanitizeName(title)), "-")
	if slug == "" {
		return "plan"
	}
	return slug
}

// planPath is the page's absolute path for one slug.
func planPath(wiki, slug string) string {
	return filepath.Join(wiki, plansDir, slug+".md")
}

// planMeta scrapes the two facts the server cares about from a page's
// frontmatter: finished, and multi-day ("long" tag).
func planMeta(raw string) (done, long bool) {
	fm := raw
	if rest, ok := strings.CutPrefix(raw, "---\n"); ok {
		fm, _, _ = strings.Cut(rest, "\n---")
	}
	return strings.Contains(fm, "status: done"), strings.Contains(fm, "long")
}

// plansPrompt is the saved-plans section injected into every chat prompt: the
// live plan list plus the start contract. Sibling of cronPrompt/taskPrompt.
func plansPrompt() string {
	var b strings.Builder
	b.WriteString("## Plans\n\nSaved plans:\n")
	n := 0
	if wiki := memoriaWiki(); wiki != "" {
		if entries, err := os.ReadDir(filepath.Join(wiki, plansDir)); err == nil {
			for _, e := range entries {
				slug, ok := strings.CutSuffix(e.Name(), ".md")
				if !ok {
					continue
				}
				raw, err := os.ReadFile(planPath(wiki, slug))
				if err != nil {
					continue
				}
				done, long := planMeta(string(raw))
				status := "active"
				if done {
					status = "done"
				}
				line := "- " + slug + " · " + status
				if long {
					line += " · #long"
				}
				b.WriteString(line + "\n")
				n++
			}
		}
	}
	if n == 0 {
		b.WriteString("none yet\n")
	}
	b.WriteString(`
The owner creates plans with /plan <goal>. When the owner asks to start or
execute a saved plan, reply with this line alone on its own line:
TOOL:plan_start {"slug":"..."}
A #long plan registers a daily 09:00 agent job that does the next action and
reports progress (reschedule it via the scheduled-jobs tools); other plans run
as one long task (/tasks). Never start a plan unasked.`)
	return b.String()
}

// planContract is the interview contract injected only while a session is in
// planning mode (/plan). Named apart from task.go's planPrompt.
func planContract() string {
	return `## Planning mode

The owner started a planning interview with /plan; their goal is in the
conversation. Ask ONE clarifying question per turn (target, deadline,
constraints, cadence) until you can write a complete plan. To offer tap
answers, end a question turn with this line alone on its own line (options
short, under 50 characters each):
TOOL:ask {"question":"...","options":["...","..."]}
The owner may tap an option or just type a reply.

When you know enough, propose the finished plan with this line alone:
TOOL:plan_save {"title":"...","tags":["..."],"body":"..."}
body is the full markdown page: ## Goal, ## Steps (numbered), ## Target (how
completion is measured), ## Progress (empty at first). Add the tag "long"
when the plan spans multiple days and suits a daily agent job. Saving needs
owner approval and does NOT start the plan.`
}

// parseAsk finds a TOOL:ask line in a chat reply; ok only with options to
// render as buttons (an option-less ask is just a question, no keyboard).
func parseAsk(reply string) (question string, options []string, ok bool) {
	for _, line := range strings.Split(reply, "\n") {
		args, found := strings.CutPrefix(strings.TrimSpace(line), "TOOL:ask ")
		if !found {
			continue
		}
		var a struct {
			Question string
			Options  []string
		}
		if json.Unmarshal([]byte(args), &a) != nil || len(a.Options) == 0 {
			return "", nil, false
		}
		return a.Question, a.Options, true
	}
	return "", nil, false
}

// askVisible is the ask turn as the owner sees it: the reply minus its TOOL
// line, with the parsed question appended when the prose left it out.
func askVisible(reply, question string) string {
	var lines []string
	for _, line := range strings.Split(reply, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "TOOL:ask ") {
			continue
		}
		lines = append(lines, line)
	}
	v := strings.TrimSpace(strings.Join(lines, "\n"))
	if v == "" {
		return question
	}
	if question != "" && !strings.Contains(v, question) {
		v += "\n\n" + question
	}
	return v
}

// truncBytes caps s at n bytes on a rune boundary (telegram callback data
// caps at 64 bytes).
func truncBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// dailyPlanPrompt is the agent prompt a started #long plan runs every day.
func dailyPlanPrompt(path string) string {
	return fmt.Sprintf(`Daily run of the plan at %s. Read that file FIRST. Execute today's next
action from ## Steps, then update ## Progress in the file: what you did and
how close ## Target is. Reply with a short progress report for the owner. If
the target is now reached, set "status: done" in the file's frontmatter and
make the LAST line of your reply exactly:
PLAN DONE`, path)
}

// planDone reports a daily plan run signalling completion: last non-empty
// line only, like parseControl (whose verb table deliberately excludes PLAN).
func planDone(reply string) bool {
	lines := strings.Split(reply, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if line := strings.TrimSpace(lines[i]); line != "" {
			return line == "PLAN DONE"
		}
	}
	return false
}

// handlePlan is the /plan command: flags the active chat session as a
// planning interview and feeds it the goal.
func (s *Server) handlePlan(arg string) tgReply {
	if arg == "" {
		return tgReply{Text: "usage: /plan <goal> — I'll interview you, then propose a plan for approval"}
	}
	if memoriaWiki() == "" {
		return tgReply{Text: "⚠ memoria not set up — run: memoria bootstrap --global"}
	}
	sess, err := s.ensureSession()
	if err != nil {
		return tgReply{Text: "⚠ " + err.Error()}
	}
	if sess.Agent {
		return tgReply{Text: "⚠ /plan needs a chat session — /new first"}
	}
	if err := s.store.SetSessionPlan(sess.ID, true); err != nil {
		return tgReply{Text: "⚠ " + err.Error()}
	}
	s.enqueue(sess.ID, arg)
	return tgReply{Text: "📋 planning mode — I'll ask a few questions first"}
}
