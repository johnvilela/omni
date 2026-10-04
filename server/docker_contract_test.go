package main

import (
	"os"
	"strings"
	"testing"
)

// TestDockerImageMatchesInstaller: the Dockerfile + entrypoint reproduce the
// ai-memory contract of install.sh and the container-mode contract the Go
// binaries rely on (OMNI_CONTAINER=1, procps for pgrep/pkill, a guardian loop
// cadence, vendor clis resolved at runtime rather than baked in).
func TestDockerImageMatchesInstaller(t *testing.T) {
	read := func(path string) string {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		return string(raw)
	}
	dockerfile := read("../Dockerfile")
	entrypoint := read("../scripts/docker-entrypoint.sh")
	both := dockerfile + entrypoint

	for _, want := range append(aiMemoryContract,
		"--bind 127.0.0.1:49374",
		"--workspace omni --project assistant",
		"ai-memory.env",
		"OMNI_GUARDIAN_INTERVAL",
		"OMNI_VENDOR_INSTALL",
		"omni-server",
		"omni-guardian",
	) {
		if !strings.Contains(both, want) {
			t.Errorf("Dockerfile + entrypoint missing %q", want)
		}
	}
	for _, want := range []string{"OMNI_CONTAINER=1", "procps", "OMNI_AI_MEMORY_URL=http://127.0.0.1:49374"} {
		if !strings.Contains(dockerfile, want) {
			t.Errorf("Dockerfile missing %q", want)
		}
	}
	if strings.Contains(dockerfile, "@anthropic-ai/claude-code") {
		t.Error("Dockerfile bakes in a vendor cli; the host's is mounted or the entrypoint installs it")
	}
	if strings.Contains(entrypoint, "systemctl") {
		t.Error("entrypoint calls systemctl; there is no systemd in the image")
	}
	if strings.Contains(both, "johnvilela/memoria") {
		t.Fatal("docker image still installs memoria")
	}
}
