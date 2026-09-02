package main

import (
	"os"
	"strings"
	"testing"
)

func TestSeedPersona(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	if err := seedPersona(); err != nil {
		t.Fatalf("seedPersona: %v", err)
	}
	got := readPersona()
	if !strings.Contains(got, "omni") || !strings.Contains(got, "concise") {
		t.Fatalf("seeded persona = %q; want the default content", got)
	}

	// owner edits are never clobbered
	if err := os.WriteFile(personaPath(), []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := seedPersona(); err != nil {
		t.Fatalf("seedPersona(again): %v", err)
	}
	if readPersona() != "mine" {
		t.Fatal("seedPersona overwrote an edited AGENTS.md")
	}
}

func TestReadPersonaMissing(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if got := readPersona(); got != "" {
		t.Fatalf("readPersona(missing) = %q; want empty", got)
	}
}
