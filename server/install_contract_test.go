package main

import (
	"os"
	"strings"
	"testing"
)

// aiMemoryContract is the low-resource ai-memory setup every install path must
// reproduce: install.sh's user units and the docker image + entrypoint.
var aiMemoryContract = []string{
	"akitaonrails/ai-memory",
	"AI_MEMORY_DECAY__OBSERVATION_RETENTION_DAYS=30",
	"AI_MEMORY_AUTO_IMPROVE__SCHEDULER__ENABLED=false",
	"AI_MEMORY_MAINTENANCE__EMBEDDING_BACKFILL_INTERVAL_SECS=0",
	"AI_MEMORY_EMBEDDING_PROVIDER=none",
	"install-hooks --agent claude-code --capture-assistant --apply",
	"install-hooks --agent codex --capture-assistant --apply",
	"install-mcp --client claude-code --apply",
	"install-mcp --client codex --apply",
}

func TestInstallerUsesLowResourceAIMemory(t *testing.T) {
	raw, err := os.ReadFile("../scripts/install.sh")
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	for _, want := range append([]string{"ai-memory.service"}, aiMemoryContract...) {
		if !strings.Contains(s, want) {
			t.Errorf("installer missing %q", want)
		}
	}
	if strings.Contains(s, "johnvilela/memoria") {
		t.Fatal("installer still installs memoria")
	}
}
