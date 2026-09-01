package main

import (
	"context"
	"strings"
	"testing"
)

func TestPairingGate(t *testing.T) {
	srv, store := newTestServer(t)
	ctx := context.Background()

	// first contact: full pairing message with id, code and approve command
	first := srv.gatedAnswer(ctx, 99, "hello")
	for _, want := range []string{"access not configured", "99", "pairing approve telegram"} {
		if !strings.Contains(first, want) {
			t.Errorf("first contact missing %q in:\n%s", want, first)
		}
	}
	p, ok, _ := store.Pairing("telegram", "99")
	if !ok || p.Approved || len(p.Code) != 8 {
		t.Fatalf("pairing after first contact = %+v, ok %v; want pending 8-char code", p, ok)
	}
	if !strings.Contains(first, p.Code) {
		t.Errorf("first contact does not show the code %q:\n%s", p.Code, first)
	}

	// later messages: short error with the same code, not the full message
	second := srv.gatedAnswer(ctx, 99, "again")
	for _, want := range []string{"awaiting approval", p.Code} {
		if !strings.Contains(second, want) {
			t.Errorf("second contact missing %q in:\n%s", want, second)
		}
	}
	if strings.Contains(second, "access not configured") {
		t.Error("second contact repeats the full pairing message")
	}

	// approved: the gate passes through to the answer flow
	if id, _ := store.ApprovePairing("telegram", p.Code); id != "99" {
		t.Fatalf("ApprovePairing = %q, want 99", id)
	}
	third := srv.gatedAnswer(ctx, 99, "now?")
	if strings.Contains(third, "access not configured") || strings.Contains(third, "awaiting approval") {
		t.Errorf("approved user still gated:\n%s", third)
	}
	if !strings.Contains(third, "llm") { // test env has no llm → the answer flow's own notice
		t.Errorf("approved user did not reach the answer flow:\n%s", third)
	}
}

func TestPairingEndpoints(t *testing.T) {
	srv, store := newTestServer(t)
	h := srv.Handler()

	if code, _, list := doJSON(t, h, "GET", "/pairing/telegram", ""); code != 200 || len(list) != 0 {
		t.Fatalf("GET /pairing/telegram = %d, %v; want 200 empty", code, list)
	}

	if code, obj, _ := doJSON(t, h, "POST", "/pairing/telegram/approve", `{"code":"NOPE1234"}`); code != 404 || obj["error"] != "unknown_code" {
		t.Fatalf("approve unknown = %d, %v; want 404 unknown_code", code, obj)
	}

	store.AddPairing("telegram", "99", "CODE1234")
	code, obj, _ := doJSON(t, h, "POST", "/pairing/telegram/approve", `{"code":"CODE1234"}`)
	if code != 200 || obj["user_id"] != "99" || obj["approved"] != true {
		t.Fatalf("approve = %d, %v; want 200 user 99 approved", code, obj)
	}

	if _, _, list := doJSON(t, h, "GET", "/pairing/telegram", ""); len(list) != 1 || list[0]["approved"] != true {
		t.Fatalf("list after approve = %v; want one approved row", list)
	}

	if code, _, _ := doJSON(t, h, "POST", "/pairing/telegram/revoke", `{"user_id":"99"}`); code != 200 {
		t.Fatalf("revoke = %d, want 200", code)
	}
	if code, obj, _ := doJSON(t, h, "POST", "/pairing/telegram/revoke", `{"user_id":"99"}`); code != 404 || obj["error"] != "unknown_user" {
		t.Fatalf("revoke again = %d, %v; want 404 unknown_user", code, obj)
	}
}
