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

// TestPersonality: the config personality key appends the matching style
// section after the owner's persona; unset or garbage stays normal.
func TestPersonality(t *testing.T) {
	for _, tc := range []struct{ value, want string }{
		{"quiet", "## Quiet mode"},
		{"ultraquiet", "## Quiet mode (ultra)"},
		{"", ""},
		{"shouty", ""}, // unknown value = normal, forgiving like readConfig
	} {
		t.Run("personality="+tc.value, func(t *testing.T) {
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			if err := seedPersona(); err != nil {
				t.Fatal(err)
			}
			if tc.value != "" {
				if err := os.WriteFile(ConfigPath(), []byte("personality: "+tc.value+"\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			got := readPersona()
			if !strings.HasPrefix(got, "# omni") {
				t.Fatalf("persona body no longer first: %q", got)
			}
			if tc.want == "" {
				if strings.Contains(got, "## Quiet mode") {
					t.Fatalf("personality %q leaked a quiet section: %q", tc.value, got)
				}
				return
			}
			if !strings.Contains(got, tc.want) {
				t.Fatalf("personality %q: persona = %q; want %s appended", tc.value, got, tc.want)
			}
			// marker variant for agent sessions follows the same switch
			if personalityMarker() == "" {
				t.Fatalf("personality %q: agent marker empty", tc.value)
			}
		})
	}
}
