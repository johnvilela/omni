package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// quotaWindow is one subscription rate-limit window (5h / weekly).
type quotaWindow struct {
	Pct    float64
	Resets time.Time
}

// claudeQuota reads the Max-plan usage windows from Anthropic's oauth usage
// endpoint using the token the claude CLI stored; ok=false on any failure
// (missing creds, expired token, endpoint drift) — the /usage line is
// simply omitted then.
func claudeQuota(ctx context.Context) (fiveH, sevenD quotaWindow, ok bool) {
	home, err := os.UserHomeDir()
	if err != nil {
		return
	}
	raw, err := os.ReadFile(filepath.Join(home, ".claude", ".credentials.json"))
	if err != nil {
		return
	}
	var creds struct {
		ClaudeAiOauth struct {
			AccessToken string `json:"accessToken"`
		} `json:"claudeAiOauth"`
	}
	if json.Unmarshal(raw, &creds) != nil || creds.ClaudeAiOauth.AccessToken == "" {
		return
	}
	base := os.Getenv("OMNI_CLAUDE_OAUTH_API")
	if base == "" {
		base = "https://api.anthropic.com"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/api/oauth/usage", nil)
	if err != nil {
		return
	}
	req.Header.Set("Authorization", "Bearer "+creds.ClaudeAiOauth.AccessToken)
	req.Header.Set("anthropic-beta", "oauth-2025-04-20")
	resp, err := llmHTTP.Do(req)
	if err != nil {
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return
	}
	var r struct {
		FiveHour *struct {
			Utilization float64 `json:"utilization"`
			ResetsAt    string  `json:"resets_at"`
		} `json:"five_hour"`
		SevenDay *struct {
			Utilization float64 `json:"utilization"`
			ResetsAt    string  `json:"resets_at"`
		} `json:"seven_day"`
	}
	if json.NewDecoder(resp.Body).Decode(&r) != nil || r.FiveHour == nil || r.SevenDay == nil {
		return
	}
	t5, _ := time.Parse(time.RFC3339, r.FiveHour.ResetsAt)
	t7, _ := time.Parse(time.RFC3339, r.SevenDay.ResetsAt)
	return quotaWindow{r.FiveHour.Utilization, t5}, quotaWindow{r.SevenDay.Utilization, t7}, true
}

// codexQuota reads the last rate-limit snapshot codex wrote to its newest
// session rollouts (~/.codex/sessions/YYYY/MM/DD/rollout-*.jsonl) — a local
// file, fresh as of the last codex run. primary = 5h window, secondary =
// weekly.
func codexQuota() (primary, secondary quotaWindow, ok bool) {
	home, err := os.UserHomeDir()
	if err != nil {
		return
	}
	files, _ := filepath.Glob(filepath.Join(home, ".codex", "sessions", "*", "*", "*", "*.jsonl"))
	if len(files) == 0 {
		return
	}
	sort.Strings(files) // date-shaped paths: lexicographic == chronological
	// newest few files: a rollout without a completed turn has no snapshot
	for i := len(files) - 1; i >= 0 && i >= len(files)-3; i-- {
		raw, err := os.ReadFile(files[i])
		if err != nil {
			continue
		}
		var found bool
		for _, line := range strings.Split(string(raw), "\n") {
			if !strings.Contains(line, `"rate_limits"`) {
				continue
			}
			var ev struct {
				Payload struct {
					RateLimits *struct {
						Primary, Secondary *struct {
							UsedPercent float64 `json:"used_percent"`
							ResetsAt    int64   `json:"resets_at"`
						}
					} `json:"rate_limits"`
				} `json:"payload"`
			}
			if json.Unmarshal([]byte(line), &ev) != nil || ev.Payload.RateLimits == nil {
				continue
			}
			rl := ev.Payload.RateLimits
			if rl.Primary == nil || rl.Secondary == nil {
				continue
			}
			primary = quotaWindow{rl.Primary.UsedPercent, time.Unix(rl.Primary.ResetsAt, 0)}
			secondary = quotaWindow{rl.Secondary.UsedPercent, time.Unix(rl.Secondary.ResetsAt, 0)}
			found = true
		}
		if found {
			return primary, secondary, true
		}
	}
	return
}

// openaiMonthCost asks OpenAI's org costs endpoint for this month's spend.
// Needs an ADMIN api key — a regular key 401s and the line is omitted.
// ponytail: response shape unverified against a real admin key; parse
// defensively and drop the line on any mismatch.
func openaiMonthCost(ctx context.Context, key string) (float64, bool) {
	start := monthStart().Unix()
	url := fmt.Sprintf("%s/v1/organization/costs?start_time=%d&limit=31", llmAPIBase("openai"), start)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, false
	}
	req.Header.Set("Authorization", "Bearer "+key)
	resp, err := llmHTTP.Do(req)
	if err != nil {
		return 0, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, false
	}
	var r struct {
		Data []struct {
			Results []struct {
				Amount struct {
					Value float64 `json:"value"`
				} `json:"amount"`
			} `json:"results"`
		} `json:"data"`
	}
	if json.NewDecoder(resp.Body).Decode(&r) != nil || len(r.Data) == 0 {
		return 0, false // wrong shape or empty: omit rather than claim $0
	}
	var total float64
	for _, d := range r.Data {
		for _, res := range d.Results {
			total += res.Amount.Value
		}
	}
	return total, true
}

// claudeMonthCost asks Anthropic's org cost report for this month's spend.
// Needs an ADMIN api key — same unverified-shape caveat as openaiMonthCost.
func claudeMonthCost(ctx context.Context, key string) (float64, bool) {
	url := llmAPIBase("claude") + "/v1/organizations/cost_report?starting_at=" +
		monthStart().UTC().Format(time.RFC3339)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, false
	}
	req.Header.Set("x-api-key", key)
	req.Header.Set("anthropic-version", "2023-06-01")
	resp, err := llmHTTP.Do(req)
	if err != nil {
		return 0, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, false
	}
	var r struct {
		Data []struct {
			Results []struct {
				Amount json.Number `json:"amount"`
			} `json:"results"`
		} `json:"data"`
	}
	if json.NewDecoder(resp.Body).Decode(&r) != nil || len(r.Data) == 0 {
		return 0, false // wrong shape or empty: omit rather than claim $0
	}
	var total float64
	for _, d := range r.Data {
		for _, res := range d.Results {
			v, _ := res.Amount.Float64()
			total += v
		}
	}
	return total, true
}

func monthStart() time.Time {
	now := time.Now()
	return time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location())
}

// fmtTok renders a token count compactly: 950, 12.3k, 1.2M.
func fmtTok(n int64) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	case n >= 1000:
		return fmt.Sprintf("%.1fk", float64(n)/1000)
	}
	return fmt.Sprintf("%d", n)
}

// fmtReset renders a reset time: today's stay short ("04:30"), later ones
// carry the date ("Sep 7 23:00").
func fmtReset(t time.Time) string {
	t = t.Local()
	now := time.Now()
	if t.Year() == now.Year() && t.YearDay() == now.YearDay() {
		return t.Format("15:04")
	}
	return t.Format("Jan 2 15:04")
}

// bar renders a 10-segment progress bar: ███░░░░░░░
func bar(pct float64) string {
	filled := int(pct/10 + 0.5)
	filled = max(0, min(10, filled))
	return strings.Repeat("█", filled) + strings.Repeat("░", 10-filled)
}

func fmtQuota(label string, w quotaWindow) string {
	return fmt.Sprintf("%s %s %.0f%%\n↳ resets %s\n", label, bar(w.Pct), w.Pct, fmtReset(w.Resets))
}

// usageDivider separates the header / consumption / limits sections.
const usageDivider = "───────────────\n"

// providerDot color-codes the provider header line.
func providerDot(name string) string {
	switch name {
	case "openai":
		return "🟢"
	case "claude":
		return "🟠"
	case "gemini":
		return "🔵"
	}
	return "⚪"
}

// listUsage renders /usage: one block per connected provider — omni's own
// tracked consumption (today + last 7 days), the subscription quota windows
// for oauth claude/codex, and this month's billed cost for admin api keys.
func (s *Server) listUsage(ctx context.Context) tgReply {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	now := time.Now()
	midnight := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location()).Unix()
	weekAgo := now.AddDate(0, 0, -7).Unix()

	var b strings.Builder
	for _, ls := range s.llmStatuses() {
		if !ls.Connected {
			continue
		}
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		fmt.Fprintf(&b, "%s %s — %s\n", providerDot(ls.Name), ls.Name, ls.Source)
		for _, p := range []struct {
			label string
			since int64
		}{{"☀️ today", midnight}, {"🗓 7d", weekAgo}} {
			u, err := s.store.UsageSince(ls.Name, p.since)
			if err != nil {
				continue
			}
			fmt.Fprintf(&b, "%s: %d req · %s in / %s out", p.label, u.Requests, fmtTok(u.In), fmtTok(u.Out))
			if u.Cost > 0 {
				fmt.Fprintf(&b, " · 💰 $%.2f", u.Cost)
			}
			b.WriteString("\n")
		}
		var extra string // third section: subscription limits or billed cost
		switch {
		case ls.Name == "claude" && ls.Source != "api_key":
			if h5, d7, ok := claudeQuota(ctx); ok {
				extra = fmtQuota("⏱ 5h", h5) + usageDivider + fmtQuota("📅 7d", d7)
			}
		case ls.Name == "openai" && ls.Source == "oauth":
			if h5, d7, ok := codexQuota(); ok {
				extra = fmtQuota("⏱ 5h", h5) + usageDivider + fmtQuota("📅 7d", d7)
			}
		case ls.Name == "openai" && ls.Source == "api_key":
			if _, key := resolveLLM("openai", ""); key != "" {
				if cost, ok := openaiMonthCost(ctx, key); ok {
					extra = fmt.Sprintf("💳 month billed: $%.2f\n", cost)
				}
			}
		case ls.Name == "claude" && ls.Source == "api_key":
			if _, key := resolveLLM("claude", ""); key != "" {
				if cost, ok := claudeMonthCost(ctx, key); ok {
					extra = fmt.Sprintf("💳 month billed: $%.2f\n", cost)
				}
			}
		}
		if extra != "" {
			b.WriteString(usageDivider + extra)
		}
	}
	if b.Len() == 0 {
		return tgReply{Text: "no llm providers connected — run: omni llm connect"}
	}
	return tgReply{Text: strings.TrimSpace(b.String())}
}
