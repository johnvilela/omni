package main

import (
	"context"
	"strings"
	"testing"
	"time"
)

func mustParseCron(t *testing.T, expr string) cronExpr {
	t.Helper()
	c, err := parseCron(expr)
	if err != nil {
		t.Fatalf("parseCron(%q): %v", expr, err)
	}
	return c
}

func TestParseCronInvalid(t *testing.T) {
	for _, expr := range []string{
		"", "* * * *", "* * * * * *", "60 * * * *", "* 24 * * *",
		"* * 0 * *", "* * 32 * *", "* * * 13 *", "* * * * 8",
		"a * * * *", "*/0 * * * *", "5-1 * * * *",
	} {
		if _, err := parseCron(expr); err == nil {
			t.Errorf("parseCron(%q) = nil error; want invalid", expr)
		}
	}
}

func TestCronMatches(t *testing.T) {
	at := func(s string) time.Time {
		tm, err := time.ParseInLocation("2006-01-02 15:04", s, time.Local)
		if err != nil {
			t.Fatal(err)
		}
		return tm
	}
	cases := []struct {
		expr string
		time string
		want bool
	}{
		{"* * * * *", "2026-09-02 10:30", true},
		{"0 8 * * *", "2026-09-02 08:00", true},
		{"0 8 * * *", "2026-09-02 08:01", false},
		{"*/15 * * * *", "2026-09-02 10:45", true},
		{"*/15 * * * *", "2026-09-02 10:50", false},
		{"1-5 * * * *", "2026-09-02 10:03", true},
		{"1-5 * * * *", "2026-09-02 10:06", false},
		{"0 9,18 * * *", "2026-09-02 18:00", true},
		{"0 9,18 * * *", "2026-09-02 12:00", false},
		// 2026-09-02 is a wednesday (dow 3)
		{"0 10 * * 3", "2026-09-02 10:00", true},
		{"0 10 * * 3", "2026-09-03 10:00", false},
		// dow 7 == sunday == 0; 2026-09-06 is a sunday
		{"0 10 * * 7", "2026-09-06 10:00", true},
		{"0 10 * * 0", "2026-09-06 10:00", true},
		// month + day-of-month
		{"0 0 1 1 *", "2026-01-01 00:00", true},
		{"0 0 1 1 *", "2026-02-01 00:00", false},
		// vixie rule: both dom and dow restricted → either matches
		{"0 10 2 * 5", "2026-09-02 10:00", true},  // dom 2 matches, dow friday doesn't
		{"0 10 15 * 3", "2026-09-02 10:00", true}, // dow wednesday matches, dom 15 doesn't
		{"0 10 15 * 5", "2026-09-02 10:00", false},
		// dom restricted, dow * → dom alone decides
		{"0 10 15 * *", "2026-09-02 10:00", false},
		{"1-10/3 * * * *", "2026-09-02 10:04", true}, // 1,4,7,10
		{"1-10/3 * * * *", "2026-09-02 10:05", false},
	}
	for _, c := range cases {
		if got := mustParseCron(t, c.expr).matches(at(c.time)); got != c.want {
			t.Errorf("%q matches %s = %v; want %v", c.expr, c.time, got, c.want)
		}
	}
}

// TestFireCrons: a matching message cron reaches every approved pairing;
// non-matching ones stay silent.
func TestFireCrons(t *testing.T) {
	srv, store := newTestServer(t)
	sent := make(chan map[string]any, 2)
	fake := fakeTelegram(t, sent, nil, nil, "")
	t.Cleanup(fake.Close)
	tg := NewTelegram(fake.URL, "TOKEN")
	srv.mu.Lock()
	srv.tg = tg
	srv.mu.Unlock()

	store.AddPairing("telegram", "42", "CODE1111")
	store.ApprovePairing("telegram", "CODE1111")
	store.AddPairing("telegram", "77", "CODE2222") // unapproved: no delivery

	store.AddCron("0 8 * * *", "message", "drink water")
	store.AddCron("0 9 * * *", "message", "later")

	at, _ := time.ParseInLocation("2006-01-02 15:04", "2026-09-02 08:00", time.Local)
	srv.fireCrons(context.Background(), at)

	select {
	case body := <-sent:
		if body["chat_id"] != float64(42) || !strings.Contains(body["text"].(string), "drink water") {
			t.Fatalf("cron sent %v", body)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no cron message within 3s")
	}
	select {
	case body := <-sent:
		t.Fatalf("unexpected second send: %v", body)
	case <-time.After(300 * time.Millisecond):
	}
}
