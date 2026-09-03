package main

import (
	"database/sql"
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
	if err := s.AddSession("a", false, ""); err != nil {
		t.Fatalf("AddSession: %v", err)
	}
	if err := s.AddSession("b", false, ""); err != nil {
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

func TestStoreActivePointer(t *testing.T) {
	s, err := OpenStore(filepath.Join(t.TempDir(), "omni.db"))
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	defer s.Close()

	if err := s.AddSession("a", false, ""); err != nil {
		t.Fatalf("AddSession: %v", err)
	}
	if err := s.AddSession("b", true, "claude"); err != nil {
		t.Fatalf("AddSession: %v", err)
	}

	// no pointer yet: max id fallback, agent fields round-trip
	sess, ok, err := s.ActiveSession()
	if err != nil || !ok || sess.ID != "b" || !sess.Agent || sess.Provider != "claude" {
		t.Fatalf("ActiveSession = %+v, ok %v, %v; want agent session b", sess, ok, err)
	}

	// pointer beats max id
	if err := s.SetActiveSession("a"); err != nil {
		t.Fatalf("SetActiveSession: %v", err)
	}
	if sess, _, _ = s.ActiveSession(); sess.ID != "a" || sess.Agent {
		t.Fatalf("ActiveSession = %+v; want chat session a", sess)
	}

	// pointer is an upsert: re-point back
	if err := s.SetActiveSession("b"); err != nil {
		t.Fatalf("SetActiveSession: %v", err)
	}
	if sess, _, _ = s.ActiveSession(); sess.ID != "b" {
		t.Fatalf("ActiveSession = %+v; want b again", sess)
	}

	// vendor session id persists
	if err := s.SetVendorSessionID("b", "vend-123"); err != nil {
		t.Fatalf("SetVendorSessionID: %v", err)
	}
	sess, ok, err = s.Session("b")
	if err != nil || !ok || sess.VendorSessionID != "vend-123" {
		t.Fatalf("Session(b) = %+v, ok %v, %v; want vend-123", sess, ok, err)
	}
	if _, ok, err = s.Session("nope"); err != nil || ok {
		t.Fatalf("Session(unknown) = ok %v, %v; want false, nil", ok, err)
	}
}

func TestStoreSetSessionProvider(t *testing.T) {
	s, err := OpenStore(filepath.Join(t.TempDir(), "omni.db"))
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	defer s.Close()

	s.AddSession("a", false, "")
	if err := s.SetSessionProvider("a", "openai"); err != nil {
		t.Fatalf("SetSessionProvider: %v", err)
	}
	if sess, _, _ := s.Session("a"); sess.Provider != "openai" {
		t.Fatalf("Session = %+v; want provider openai", sess)
	}
}

func TestStoreRecentSessions(t *testing.T) {
	s, err := OpenStore(filepath.Join(t.TempDir(), "omni.db"))
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	defer s.Close()

	for _, id := range []string{"a", "b", "c", "d", "e", "f"} {
		if err := s.AddSession(id, id == "f", ""); err != nil {
			t.Fatalf("AddSession: %v", err)
		}
	}
	s.SetSessionName("e", "trip planning")
	s.AddMessage("d", "user", "first message of d", 1)

	got, err := s.RecentSessions(5)
	if err != nil || len(got) != 5 {
		t.Fatalf("RecentSessions = %d rows, %v; want 5", len(got), err)
	}
	if got[0].ID != "f" || !got[0].Agent {
		t.Fatalf("RecentSessions[0] = %+v; want newest agent session f", got[0])
	}
	if got[1].ID != "e" || got[1].Name != "trip planning" {
		t.Fatalf("RecentSessions[1] = %+v; want named e", got[1])
	}
	if got[2].ID != "d" || got[2].FirstMsg != "first message of d" {
		t.Fatalf("RecentSessions[2] = %+v; want d with first-message fallback", got[2])
	}
	if got[4].ID != "b" {
		t.Fatalf("RecentSessions[4] = %+v; want b (a dropped)", got[4])
	}

	// unread sessions float above newer ones so stored answers stay visible
	s.AppendSessionUnread("b", "psst")
	got, err = s.RecentSessions(5)
	if err != nil || got[0].ID != "b" || got[1].ID != "f" {
		t.Fatalf("RecentSessions with unread = %+v, %v; want b first", got, err)
	}
}

// TestStoreMigratesOldSchema: a DB created before the agent columns existed
// must gain them via the guarded ALTERs on reopen.
func TestStoreMigratesOldSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "omni.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE sessions (
		id TEXT PRIMARY KEY,
		name TEXT NOT NULL DEFAULT '',
		consolidated_until INTEGER NOT NULL DEFAULT 0
	)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO sessions (id, name) VALUES ('old', 'kept')`); err != nil {
		t.Fatal(err)
	}
	db.Close()

	s, err := OpenStore(path)
	if err != nil {
		t.Fatalf("OpenStore over old schema: %v", err)
	}
	defer s.Close()
	sess, ok, err := s.ActiveSession()
	if err != nil || !ok || sess.ID != "old" || sess.Name != "kept" || sess.Agent {
		t.Fatalf("ActiveSession = %+v, ok %v, %v; want migrated old session", sess, ok, err)
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

func TestStoreCrons(t *testing.T) {
	s, err := OpenStore(filepath.Join(t.TempDir(), "omni.db"))
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	defer s.Close()

	id, err := s.AddCron("0 8 * * *", "message", "drink water")
	if err != nil || id == 0 {
		t.Fatalf("AddCron = %d, %v", id, err)
	}
	id2, _ := s.AddCron("0 9 * * 3", "prompt", "motivate me")

	cs, err := s.Crons()
	if err != nil || len(cs) != 2 || cs[0].ID != id || cs[0].Kind != "message" || cs[0].Text != "drink water" {
		t.Fatalf("Crons = %+v, %v", cs, err)
	}

	if ok, err := s.UpdateCron(id, "30 8 * * *", "message", "hydrate"); err != nil || !ok {
		t.Fatalf("UpdateCron = %v, %v", ok, err)
	}
	cs, _ = s.Crons()
	if cs[0].Schedule != "30 8 * * *" || cs[0].Text != "hydrate" {
		t.Fatalf("after update = %+v", cs[0])
	}
	if ok, _ := s.UpdateCron(999, "* * * * *", "message", "x"); ok {
		t.Fatal("UpdateCron(unknown) = true")
	}

	if ok, err := s.DeleteCron(id2); err != nil || !ok {
		t.Fatalf("DeleteCron = %v, %v", ok, err)
	}
	if ok, _ := s.DeleteCron(id2); ok {
		t.Fatal("DeleteCron(gone) = true")
	}
	if cs, _ = s.Crons(); len(cs) != 1 {
		t.Fatalf("Crons after delete = %+v", cs)
	}
}

func TestStoreUsage(t *testing.T) {
	s, err := OpenStore(filepath.Join(t.TempDir(), "omni.db"))
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	defer s.Close()

	s.AddUsage("claude", 100, 20, 0.5, 1000)
	s.AddUsage("claude", 200, 30, 0, 2000)
	s.AddUsage("openai", 999, 999, 9, 2000) // other provider, not counted

	u, err := s.UsageSince("claude", 1500)
	if err != nil || u.Requests != 1 || u.In != 200 || u.Out != 30 || u.Cost != 0 {
		t.Fatalf("UsageSince(1500) = %+v, %v; want the newer row only", u, err)
	}
	u, _ = s.UsageSince("claude", 0)
	if u.Requests != 2 || u.In != 300 || u.Out != 50 || u.Cost != 0.5 {
		t.Fatalf("UsageSince(0) = %+v; want both rows summed", u)
	}
	if u, _ = s.UsageSince("gemini", 0); u.Requests != 0 {
		t.Fatalf("UsageSince(gemini) = %+v; want zero", u)
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
