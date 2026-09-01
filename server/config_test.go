package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveToken(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmp)
	t.Setenv("TELEGRAM_BOT_TOKEN", "")

	// nothing set anywhere
	if got := ResolveToken(""); got != "" {
		t.Fatalf("ResolveToken with nothing set = %q, want empty", got)
	}

	// config file only
	dir := filepath.Join(tmp, "omni")
	os.MkdirAll(dir, 0o700)
	os.WriteFile(filepath.Join(dir, "config.yaml"), []byte("telegram_token: from-file\n"), 0o600)
	if got := ResolveToken(""); got != "from-file" {
		t.Fatalf("ResolveToken from config = %q, want %q", got, "from-file")
	}

	// env beats config
	t.Setenv("TELEGRAM_BOT_TOKEN", "from-env")
	if got := ResolveToken(""); got != "from-env" {
		t.Fatalf("ResolveToken with env = %q, want %q", got, "from-env")
	}

	// explicit request token beats both
	if got := ResolveToken("from-request"); got != "from-request" {
		t.Fatalf("ResolveToken with request = %q, want %q", got, "from-request")
	}
}
