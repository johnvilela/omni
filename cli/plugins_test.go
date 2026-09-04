package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestNormalizeRepo(t *testing.T) {
	cases := []struct {
		in, repo, name string
		wantErr        bool
	}{
		{"johnvilela/pecunia", "johnvilela/pecunia", "pecunia", false},
		{"github.com/johnvilela/pecunia", "johnvilela/pecunia", "pecunia", false},
		{"https://github.com/johnvilela/pecunia", "johnvilela/pecunia", "pecunia", false},
		{"https://github.com/johnvilela/pecunia.git", "johnvilela/pecunia", "pecunia", false},
		{"pecunia", "", "", true},
		{"a/b/c", "", "", true},
		{"", "", "", true},
	}
	for _, c := range cases {
		repo, name, err := normalizeRepo(c.in)
		if (err != nil) != c.wantErr || repo != c.repo || name != c.name {
			t.Errorf("normalizeRepo(%q) = %q, %q, %v; want %q, %q, err=%v", c.in, repo, name, err, c.repo, c.name, c.wantErr)
		}
	}
}

func TestPickAsset(t *testing.T) {
	arch := runtime.GOARCH
	// bare binary (memoria style) wins over the tarball
	assets := map[string]string{
		"pecunia_linux_" + arch:                   "u1",
		"pecunia_0.4.0_linux_" + arch + ".tar.gz": "u2",
		"checksums.txt":                           "u3",
	}
	if a, tarball, ok := pickAsset(assets, "pecunia"); !ok || tarball || a != "pecunia_linux_"+arch {
		t.Fatalf("pickAsset bare = %q, %v, %v", a, tarball, ok)
	}
	// goreleaser tarball only
	delete(assets, "pecunia_linux_"+arch)
	if a, tarball, ok := pickAsset(assets, "pecunia"); !ok || !tarball || a != "pecunia_0.4.0_linux_"+arch+".tar.gz" {
		t.Fatalf("pickAsset tarball = %q, %v, %v", a, tarball, ok)
	}
	// nothing for this platform
	if _, _, ok := pickAsset(map[string]string{"pecunia_darwin_amd64.tar.gz": "u"}, "pecunia"); ok {
		t.Fatal("pickAsset matched a foreign platform")
	}
}

// tarGz builds an in-memory .tar.gz with one file entry.
func tarGz(t *testing.T, name string, content []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(content))}); err != nil {
		t.Fatal(err)
	}
	tw.Write(content)
	tw.Close()
	gz.Close()
	return buf.Bytes()
}

func TestExtractTarGz(t *testing.T) {
	dir := t.TempDir()
	archive := filepath.Join(dir, "p.tar.gz")
	os.WriteFile(archive, tarGz(t, "./pecunia", []byte("BIN")), 0o644)
	dest := filepath.Join(dir, "out")
	if err := extractTarGz(archive, "pecunia", dest); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(dest)
	if err != nil || string(data) != "BIN" {
		t.Fatalf("extracted = %q, %v; want BIN", data, err)
	}
	if err := extractTarGz(archive, "missing", filepath.Join(dir, "out2")); err == nil {
		t.Fatal("extractTarGz found a member that is not there")
	}
}

func TestMergeMCPJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".mcp.json")
	// pre-existing foreign server and foreign top-level key must survive
	os.WriteFile(path, []byte(`{"mcpServers":{"memoria":{"command":"memoria"}},"other":true}`), 0o644)
	if err := mergeMCPJSON(path, "pecunia", map[string]any{"command": "/bin/pecunia", "args": []string{"mcp"}}); err != nil {
		t.Fatal(err)
	}
	var got struct {
		Servers map[string]json.RawMessage `json:"mcpServers"`
		Other   bool                       `json:"other"`
	}
	data, _ := os.ReadFile(path)
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if !got.Other || len(got.Servers) != 2 || got.Servers["memoria"] == nil || got.Servers["pecunia"] == nil {
		t.Fatalf("after add: %s", data)
	}
	if err := mergeMCPJSON(path, "pecunia", nil); err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(path)
	got.Servers = nil
	json.Unmarshal(data, &got)
	if !got.Other || len(got.Servers) != 1 || got.Servers["memoria"] == nil {
		t.Fatalf("after delete: %s", data)
	}
}

func TestUpdateConfigList(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmp)
	cfgPath := filepath.Join(tmp, app, "config.yaml")
	os.MkdirAll(filepath.Dir(cfgPath), 0o700)
	// int keys must survive the round-trip (save-config-key gotcha)
	os.WriteFile(cfgPath, []byte("token_budget: 9000\nupdate_repos:\n  - johnvilela/omni\n"), 0o600)

	if err := updateConfigList("update_repos", "johnvilela/pecunia", ""); err != nil {
		t.Fatal(err)
	}
	if err := updateConfigList("update_repos", "johnvilela/pecunia", ""); err != nil { // dedupe
		t.Fatal(err)
	}
	cfg := map[string]any{}
	data, _ := os.ReadFile(cfgPath)
	yaml.Unmarshal(data, &cfg)
	if b, ok := cfg["token_budget"].(int); !ok || b != 9000 {
		t.Fatalf("token_budget = %v (%T); want int 9000", cfg["token_budget"], cfg["token_budget"])
	}
	if got := fmt.Sprint(cfg["update_repos"]); got != "[johnvilela/omni johnvilela/pecunia]" {
		t.Fatalf("update_repos after add = %v", got)
	}

	if err := updateConfigList("update_repos", "", "johnvilela/pecunia"); err != nil {
		t.Fatal(err)
	}
	cfg = map[string]any{}
	data, _ = os.ReadFile(cfgPath)
	yaml.Unmarshal(data, &cfg)
	if got := fmt.Sprint(cfg["update_repos"]); got != "[johnvilela/omni]" {
		t.Fatalf("update_repos after remove = %v", got)
	}
}

// fakePluginScript is the "binary" a fake release serves: answers
// omni-manifest and omni-skills. printf, never echo (dash mangles escapes).
func fakePluginScript(t *testing.T, manifest string) []byte {
	t.Helper()
	return []byte(`#!/bin/sh
case "$1" in
omni-manifest) printf '%s' '` + manifest + `' ;;
omni-skills) mkdir -p "$2/pec-overview" && printf 'skill' > "$2/pec-overview/SKILL.md" ;;
--version) printf '0.4.0\n' ;;
esac
`)
}

// fakeGitHub serves a latest release with the given assets (name → bytes).
func fakeGitHub(t *testing.T, repo string, assets map[string][]byte) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	var srv *httptest.Server
	mux.HandleFunc("/repos/"+repo+"/releases/latest", func(w http.ResponseWriter, r *http.Request) {
		type asset struct {
			Name string `json:"name"`
			URL  string `json:"browser_download_url"`
		}
		var list []asset
		for name := range assets {
			list = append(list, asset{Name: name, URL: srv.URL + "/dl/" + name})
		}
		json.NewEncoder(w).Encode(map[string]any{"tag_name": "v0.4.0", "assets": list})
	})
	mux.HandleFunc("/dl/", func(w http.ResponseWriter, r *http.Request) {
		data, ok := assets[strings.TrimPrefix(r.URL.Path, "/dl/")]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Write(data)
	})
	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	t.Setenv("OMNI_GITHUB_API", srv.URL)
	return srv
}

// pluginTestEnv isolates HOME/XDG dirs and returns the fake home.
func pluginTestEnv(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	return home
}

const fakeManifest = `{"name":"pecunia","version":"0.4.0","description":"personal finance",` +
	`"mcp":{"command":"pecunia","args":["mcp"]},"skills":true,` +
	`"commands":[{"name":"pecunia_today","description":"today summary","argv":["pecunia","today"]}]}`

func TestPluginInstall(t *testing.T) {
	home := pluginTestEnv(t)
	script := fakePluginScript(t, fakeManifest)
	sum := sha256.Sum256(script)
	assetName := "pecunia_linux_" + runtime.GOARCH
	fakeGitHub(t, "johnvilela/pecunia", map[string][]byte{
		assetName:       script,
		"checksums.txt": []byte(hex.EncodeToString(sum[:]) + "  " + assetName + "\n"),
	})

	if code := runPluginInstall(NewClient("http://localhost:0"), "johnvilela/pecunia"); code != 0 {
		t.Fatalf("install exit = %d", code)
	}

	bin := filepath.Join(home, ".local", "bin", "pecunia")
	if fi, err := os.Stat(bin); err != nil || fi.Mode()&0o100 == 0 {
		t.Fatalf("binary not installed executable: %v", err)
	}
	var snap pluginManifest
	data, err := os.ReadFile(filepath.Join(dataDir(), "plugins", "pecunia.json"))
	if err != nil {
		t.Fatal(err)
	}
	json.Unmarshal(data, &snap)
	if snap.Repo != "johnvilela/pecunia" || len(snap.SkillDirs) != 1 || snap.SkillDirs[0] != "pec-overview" {
		t.Fatalf("snapshot = %+v", snap)
	}
	// skills landed in the agent workspace
	if _, err := os.Stat(filepath.Join(agentDir(), ".claude", "skills", "pec-overview", "SKILL.md")); err != nil {
		t.Fatal(err)
	}
	// mcp entry with the absolute binary path
	mcp, err := os.ReadFile(filepath.Join(agentDir(), ".mcp.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(mcp), `"command": "`+bin+`"`) {
		t.Fatalf(".mcp.json = %s", mcp)
	}
	// guardian watches the repo now
	if !strings.Contains(readConfigRaw(t), "johnvilela/pecunia") {
		t.Fatal("repo not in update_repos")
	}
}

func readConfigRaw(t *testing.T) string {
	t.Helper()
	dir, _ := os.UserConfigDir()
	data, _ := os.ReadFile(filepath.Join(dir, app, "config.yaml"))
	return string(data)
}

func TestPluginInstallTarball(t *testing.T) {
	home := pluginTestEnv(t)
	script := fakePluginScript(t, fakeManifest)
	assetName := "pecunia_0.4.0_linux_" + runtime.GOARCH + ".tar.gz"
	fakeGitHub(t, "johnvilela/pecunia", map[string][]byte{assetName: tarGz(t, "pecunia", script)})

	if code := runPluginInstall(NewClient("http://localhost:0"), "johnvilela/pecunia"); code != 0 {
		t.Fatalf("install exit = %d", code)
	}
	out, err := os.ReadFile(filepath.Join(home, ".local", "bin", "pecunia"))
	if err != nil || !bytes.Equal(out, script) {
		t.Fatalf("installed binary mismatch: %v", err)
	}
}

func TestPluginInstallChecksumMismatch(t *testing.T) {
	home := pluginTestEnv(t)
	script := fakePluginScript(t, fakeManifest)
	assetName := "pecunia_linux_" + runtime.GOARCH
	fakeGitHub(t, "johnvilela/pecunia", map[string][]byte{
		assetName:       script,
		"checksums.txt": []byte(strings.Repeat("0", 64) + "  " + assetName + "\n"),
	})
	if code := runPluginInstall(NewClient("http://localhost:0"), "johnvilela/pecunia"); code == 0 {
		t.Fatal("install accepted a bad checksum")
	}
	if _, err := os.Stat(filepath.Join(home, ".local", "bin", "pecunia")); err == nil {
		t.Fatal("binary installed despite bad checksum")
	}
}

func TestPluginInstallManifestNameMismatch(t *testing.T) {
	pluginTestEnv(t)
	script := fakePluginScript(t, strings.Replace(fakeManifest, `"name":"pecunia"`, `"name":"impostor"`, 1))
	fakeGitHub(t, "johnvilela/pecunia", map[string][]byte{"pecunia_linux_" + runtime.GOARCH: script})
	if code := runPluginInstall(NewClient("http://localhost:0"), "johnvilela/pecunia"); code == 0 {
		t.Fatal("install accepted a name mismatch")
	}
	if _, err := os.Stat(filepath.Join(dataDir(), "plugins", "pecunia.json")); err == nil {
		t.Fatal("snapshot written despite mismatch")
	}
}

func TestPluginInstallCommandCollision(t *testing.T) {
	pluginTestEnv(t)
	// a built-in name is refused
	script := fakePluginScript(t, strings.Replace(fakeManifest, `"name":"pecunia_today"`, `"name":"clear"`, 1))
	fakeGitHub(t, "johnvilela/pecunia", map[string][]byte{"pecunia_linux_" + runtime.GOARCH: script})
	if code := runPluginInstall(NewClient("http://localhost:0"), "johnvilela/pecunia"); code == 0 {
		t.Fatal("install accepted a built-in command name")
	}
	// another installed plugin's command is refused too
	os.MkdirAll(filepath.Join(dataDir(), "plugins"), 0o700)
	other := `{"name":"other","commands":[{"name":"pecunia_today","description":"x","argv":["other"]}]}`
	os.WriteFile(filepath.Join(dataDir(), "plugins", "other.json"), []byte(other), 0o644)
	script = fakePluginScript(t, fakeManifest)
	fakeGitHub(t, "johnvilela/pecunia", map[string][]byte{"pecunia_linux_" + runtime.GOARCH: script})
	if code := runPluginInstall(NewClient("http://localhost:0"), "johnvilela/pecunia"); code == 0 {
		t.Fatal("install accepted a command already taken by another plugin")
	}
}

// TestValidateManifestPromptCommands: a command declares exactly one of argv
// and prompt.
func TestValidateManifestPromptCommands(t *testing.T) {
	pluginTestEnv(t)
	mk := func(c pluginCommand) pluginManifest {
		return pluginManifest{Name: "pecunia", Commands: []pluginCommand{c}}
	}
	if err := validateManifest(mk(pluginCommand{Name: "pecunia_coach", Prompt: "You are the coach."}), "pecunia"); err != nil {
		t.Fatalf("prompt-only command rejected: %v", err)
	}
	if err := validateManifest(mk(pluginCommand{Name: "pecunia_coach", Argv: []string{"pecunia"}, Prompt: "x"}), "pecunia"); err == nil {
		t.Fatal("argv+prompt accepted")
	}
	if err := validateManifest(mk(pluginCommand{Name: "pecunia_coach"}), "pecunia"); err == nil {
		t.Fatal("command with neither argv nor prompt accepted")
	}
}

func TestPluginRemove(t *testing.T) {
	home := pluginTestEnv(t)
	script := fakePluginScript(t, fakeManifest)
	fakeGitHub(t, "johnvilela/pecunia", map[string][]byte{"pecunia_linux_" + runtime.GOARCH: script})
	if code := runPluginInstall(NewClient("http://localhost:0"), "johnvilela/pecunia"); code != 0 {
		t.Fatal("install failed")
	}
	// a foreign mcp server must survive the removal
	mergeMCPJSON(filepath.Join(agentDir(), ".mcp.json"), "memoria", map[string]any{"command": "memoria"})

	if code := runPluginRemove(NewClient("http://localhost:0"), "pecunia"); code != 0 {
		t.Fatal("remove failed")
	}
	if _, err := os.Stat(filepath.Join(home, ".local", "bin", "pecunia")); err == nil {
		t.Fatal("binary still installed")
	}
	if _, err := os.Stat(filepath.Join(dataDir(), "plugins", "pecunia.json")); err == nil {
		t.Fatal("snapshot still present")
	}
	if _, err := os.Stat(filepath.Join(agentDir(), ".claude", "skills", "pec-overview")); err == nil {
		t.Fatal("skill dir still present")
	}
	mcp, _ := os.ReadFile(filepath.Join(agentDir(), ".mcp.json"))
	if strings.Contains(string(mcp), "pecunia") || !strings.Contains(string(mcp), "memoria") {
		t.Fatalf(".mcp.json after remove = %s", mcp)
	}
	if strings.Contains(readConfigRaw(t), "johnvilela/pecunia") {
		t.Fatal("repo still in update_repos")
	}
	// removing an unknown plugin errors
	if code := runPluginRemove(NewClient("http://localhost:0"), "ghost"); code == 0 {
		t.Fatal("remove of unknown plugin succeeded")
	}
}
