package main

import (
	"path/filepath"
	"testing"
)

func TestStorePairings(t *testing.T) {
	s, err := OpenStore(filepath.Join(t.TempDir(), "omni.db"))
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	defer s.Close()

	// unknown user has no pairing
	if _, ok, err := s.Pairing("telegram", "42"); err != nil || ok {
		t.Fatalf("Pairing(new) = ok %v, %v; want false, nil", ok, err)
	}

	// first contact records a pending pairing
	if err := s.AddPairing("telegram", "42", "CODE1234"); err != nil {
		t.Fatalf("AddPairing: %v", err)
	}
	p, ok, _ := s.Pairing("telegram", "42")
	if !ok || p.Code != "CODE1234" || p.Approved {
		t.Fatalf("Pairing = %+v, ok %v; want pending CODE1234", p, ok)
	}

	// approve by code returns the user id
	if id, err := s.ApprovePairing("telegram", "CODE1234"); err != nil || id != "42" {
		t.Fatalf("ApprovePairing = %q, %v; want 42", id, err)
	}
	if p, _, _ = s.Pairing("telegram", "42"); !p.Approved {
		t.Fatal("not approved after ApprovePairing")
	}

	// unknown code approves nothing
	if id, err := s.ApprovePairing("telegram", "NOPE"); err != nil || id != "" {
		t.Fatalf("ApprovePairing(unknown) = %q, %v; want empty", id, err)
	}

	if ps, _ := s.Pairings("telegram"); len(ps) != 1 || ps[0].UserID != "42" {
		t.Fatalf("Pairings = %+v; want one row for 42", ps)
	}

	// revoke deletes; a second revoke reports missing
	if ok, err := s.RevokePairing("telegram", "42"); err != nil || !ok {
		t.Fatalf("RevokePairing = %v, %v; want true", ok, err)
	}
	if ok, _ := s.RevokePairing("telegram", "42"); ok {
		t.Fatal("RevokePairing(gone) = true, want false")
	}
	if _, ok, _ := s.Pairing("telegram", "42"); ok {
		t.Fatal("pairing still present after revoke")
	}
}

func TestStoreSessions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "omni.db")
	s, err := OpenStore(path)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	defer s.Close()

	// empty store has no active session
	if _, ok, err := s.ActiveSession(); err != nil || ok {
		t.Fatalf("ActiveSession(empty) = ok %v, %v; want false, nil", ok, err)
	}

	// max id wins: ids are opaque strings, uuid7 order == lexicographic
	if err := s.AddSession("a"); err != nil {
		t.Fatalf("AddSession: %v", err)
	}
	if err := s.AddSession("b"); err != nil {
		t.Fatalf("AddSession: %v", err)
	}
	sess, ok, err := s.ActiveSession()
	if err != nil || !ok || sess.ID != "b" || sess.Name != "" || sess.ConsolidatedUntil != 0 {
		t.Fatalf("ActiveSession = %+v, ok %v, %v; want fresh session b", sess, ok, err)
	}

	if err := s.SetSessionName("b", "trip planning"); err != nil {
		t.Fatalf("SetSessionName: %v", err)
	}
	if err := s.SetConsolidatedUntil("b", 7); err != nil {
		t.Fatalf("SetConsolidatedUntil: %v", err)
	}

	// survives reopen
	s.Close()
	s2, err := OpenStore(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer s2.Close()
	sess, _, _ = s2.ActiveSession()
	if sess.ID != "b" || sess.Name != "trip planning" || sess.ConsolidatedUntil != 7 {
		t.Fatalf("ActiveSession after reopen = %+v", sess)
	}
}

func TestStoreMessages(t *testing.T) {
	s, err := OpenStore(filepath.Join(t.TempDir(), "omni.db"))
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	defer s.Close()

	// empty session has no messages
	if ms, err := s.Messages("a"); err != nil || len(ms) != 0 {
		t.Fatalf("Messages(empty) = %v, %v; want none", ms, err)
	}

	// ids increase; sessions are isolated
	id1, err := s.AddMessage("a", "user", "ping", 100)
	if err != nil {
		t.Fatalf("AddMessage: %v", err)
	}
	id2, _ := s.AddMessage("a", "assistant", "pong", 101)
	if id2 <= id1 {
		t.Fatalf("ids not increasing: %d then %d", id1, id2)
	}
	if _, err := s.AddMessage("other", "user", "hi", 102); err != nil {
		t.Fatalf("AddMessage: %v", err)
	}

	ms, err := s.Messages("a")
	if err != nil || len(ms) != 2 {
		t.Fatalf("Messages = %v, %v; want 2", ms, err)
	}
	if ms[0].Role != "user" || ms[0].Content != "ping" || ms[0].ID != id1 || ms[0].CreatedAt != 100 {
		t.Fatalf("first message = %+v", ms[0])
	}
	if ms[1].Role != "assistant" || ms[1].Content != "pong" {
		t.Fatalf("second message = %+v", ms[1])
	}
}

func TestStoreConnected(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "omni.db")
	s, err := OpenStore(path)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	defer s.Close()

	// unknown channel defaults to disconnected
	if got, err := s.Connected("telegram"); err != nil || got {
		t.Fatalf("Connected(new) = %v, %v; want false, nil", got, err)
	}

	if err := s.SetConnected("telegram", true); err != nil {
		t.Fatalf("SetConnected: %v", err)
	}
	if got, _ := s.Connected("telegram"); !got {
		t.Fatal("Connected = false after SetConnected(true)")
	}

	// survives reopen
	s.Close()
	s2, err := OpenStore(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer s2.Close()
	if got, _ := s2.Connected("telegram"); !got {
		t.Fatal("Connected = false after reopen, want true")
	}
}
