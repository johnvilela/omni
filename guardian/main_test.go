package main

import (
	"strings"
	"testing"
	"time"
)

func TestTransitions(t *testing.T) {
	now := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	earlier := now.Add(-10 * time.Minute).Format(time.RFC3339)
	cases := []struct {
		name      string
		prev      map[string]string
		results   []checkResult
		wantLines []string // substrings the message must contain, in any order
		wantEmpty bool
		wantState []string // check names that must be red in the next state
	}{
		{
			name:      "new red alerts once",
			prev:      map[string]string{},
			results:   []checkResult{{name: "disk", ok: false, detail: "1.4 GiB free"}},
			wantLines: []string{"⚠ disk: 1.4 GiB free"},
			wantState: []string{"disk"},
		},
		{
			name:      "stays red stays silent",
			prev:      map[string]string{"disk": earlier},
			results:   []checkResult{{name: "disk", ok: false, detail: "1.4 GiB free"}},
			wantEmpty: true,
			wantState: []string{"disk"},
		},
		{
			name:      "recovery notes duration and clears state",
			prev:      map[string]string{"disk": earlier},
			results:   []checkResult{{name: "disk", ok: true, detail: "20.0 GiB free"}},
			wantLines: []string{"✅ disk: recovered", "down 10m"},
		},
		{
			name:      "event reports even while green",
			prev:      map[string]string{},
			results:   []checkResult{{name: "server", ok: true, detail: "responding", event: "server was inactive — restarted by guardian"}},
			wantLines: []string{"⚠ server was inactive — restarted by guardian"},
		},
		{
			name:      "all green all quiet",
			prev:      map[string]string{},
			results:   []checkResult{{name: "disk", ok: true}, {name: "mem", ok: true}},
			wantEmpty: true,
		},
		{
			name: "multiple transitions batch into one message",
			prev: map[string]string{"net": earlier},
			results: []checkResult{
				{name: "net", ok: true, detail: "internet reachable"},
				{name: "mem", ok: false, detail: "100 MiB available"},
			},
			wantLines: []string{"✅ net: recovered", "⚠ mem: 100 MiB available"},
			wantState: []string{"mem"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			msg, next := transitions(c.prev, c.results, now)
			if c.wantEmpty {
				if msg != "" {
					t.Fatalf("message = %q; want empty", msg)
				}
			} else if msg == "" {
				t.Fatal("message empty; want lines")
			}
			for _, want := range c.wantLines {
				if !strings.Contains(msg, want) {
					t.Errorf("message %q missing %q", msg, want)
				}
			}
			if len(next) != len(c.wantState) {
				t.Errorf("next state = %v; want keys %v", next, c.wantState)
			}
			for _, name := range c.wantState {
				if _, ok := next[name]; !ok {
					t.Errorf("next state %v missing %q", next, name)
				}
			}
		})
	}
	// a red check keeps its original went-red timestamp
	msg, next := transitions(map[string]string{"disk": earlier},
		[]checkResult{{name: "disk", ok: false, detail: "x"}}, now)
	if msg != "" || next["disk"] != earlier {
		t.Errorf("stays-red: msg=%q since=%q; want silent with since=%q", msg, next["disk"], earlier)
	}
}

func TestParseMemAvailable(t *testing.T) {
	meminfo := "MemTotal:       32610516 kB\nMemFree:         1856256 kB\nMemAvailable:   16305258 kB\nBuffers:          510980 kB\n"
	got, err := parseMemAvailable(meminfo)
	if err != nil || got != 16305258*1024 {
		t.Errorf("parseMemAvailable = %d, %v; want %d", got, err, 16305258*1024)
	}
	for _, bad := range []string{"", "MemTotal: 1 kB\n", "MemAvailable: junk kB\n"} {
		if _, err := parseMemAvailable(bad); err == nil {
			t.Errorf("parseMemAvailable(%q) = nil error; want error", bad)
		}
	}
}

func TestParsePgrep(t *testing.T) {
	cases := []struct {
		out  string
		want int
	}{
		{"3\n", 3},
		{"0\n", 0},
		{"", 0},
		{"junk", 0},
	}
	for _, c := range cases {
		if got := parsePgrep(c.out); got != c.want {
			t.Errorf("parsePgrep(%q) = %d; want %d", c.out, got, c.want)
		}
	}
}
