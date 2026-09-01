package main

import (
	"path/filepath"
	"testing"
)

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
