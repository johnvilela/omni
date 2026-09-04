// Plugin install/remove: a plugin is one Go binary on a GitHub release that
// answers `<bin> omni-manifest` (see PLUGINS.md). Install wires its MCP server
// and skills into the agent workspace and enables its telegram commands; the
// server just reads the manifest snapshots this file writes. Everything here
// is server-less by design (like the guardian commands) — a best-effort
// /plugins/sync tells a running server to refresh the telegram menu.
package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"gopkg.in/yaml.v3"
)

// pluginMCP and pluginCommand mirror the omni-manifest contract; the struct
// is duplicated in server/plugin.go (project pattern: no shared packages).
type pluginMCP struct {
	Command string   `json:"command"`
	Args    []string `json:"args,omitempty"`
}

type pluginCommand struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Argv        []string `json:"argv"`
}

type pluginManifest struct {
	Name        string          `json:"name"`
	Version     string          `json:"version"`
	Description string          `json:"description"`
	MCP         *pluginMCP      `json:"mcp,omitempty"`
	Skills      bool            `json:"skills,omitempty"`
	Commands    []pluginCommand `json:"commands,omitempty"`
	Repo        string          `json:"repo,omitempty"`       // omni-managed: owner/name
	SkillDirs   []string        `json:"skill_dirs,omitempty"` // omni-managed: dirs omni-skills produced
}

// builtinTgCommands are the names a plugin command may never take — keep in
// sync with registerCommands in server/telegram.go (plus /exit).
var builtinTgCommands = []string{"new", "clear", "agent", "task", "tasks", "sessions",
	"usage", "context", "crons", "pin", "terminal", "exit", "interrupt", "ops", "plan", "memory"}

var pluginCmdRe = regexp.MustCompile(`^[a-z0-9_]{1,32}$`)

// agentDir is the agent session workspace — twin of server/agent.go.
func agentDir() string {
	if x := os.Getenv("XDG_DATA_HOME"); x != "" {
		return filepath.Join(x, app, "agent")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".local", "share", app, "agent")
}

// binDir matches scripts/install.sh — twin of guardian/main.go.
func binDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".local", "bin")
}

func pluginsDir() string { return filepath.Join(dataDir(), "plugins") }

// normalizeRepo accepts owner/name, github.com/owner/name and the https URL.
func normalizeRepo(s string) (repo, name string, err error) {
	s = strings.TrimPrefix(s, "https://")
	s = strings.TrimPrefix(s, "http://")
	s = strings.TrimPrefix(s, "github.com/")
	s = strings.TrimSuffix(strings.TrimSuffix(s, "/"), ".git")
	parts := strings.Split(s, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("plugin repo must be owner/name or github.com/owner/name")
	}
	return parts[0] + "/" + parts[1], parts[1], nil
}

// latestReleaseAssets fetches the repo's latest release in one call: the tag
// and every asset name → download url (the guardian does this in two).
func latestReleaseAssets(repo string) (string, map[string]string, error) {
	base := os.Getenv("OMNI_GITHUB_API") // test/debug override
	if base == "" {
		base = "https://api.github.com"
	}
	c := http.Client{Timeout: 30 * time.Second}
	resp, err := c.Get(base + "/repos/" + repo + "/releases/latest")
	if err != nil {
		return "", nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", nil, fmt.Errorf("github %s: %s — does the repo have a release?", repo, resp.Status)
	}
	var r struct {
		TagName string `json:"tag_name"`
		Assets  []struct {
			Name string `json:"name"`
			URL  string `json:"browser_download_url"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil || r.TagName == "" {
		return "", nil, fmt.Errorf("github %s: no tag_name (%v)", repo, err)
	}
	assets := map[string]string{}
	for _, a := range r.Assets {
		assets[a.Name] = a.URL
	}
	return r.TagName, assets, nil
}

// pickAsset finds this platform's binary: a bare name_linux_<arch> (memoria
// style) wins, else a name_*linux_<arch>.tar.gz (goreleaser style).
// ponytail: linux-only, no zip — omni targets linux PCs.
func pickAsset(assets map[string]string, name string) (asset string, tarball, ok bool) {
	bare := name + "_linux_" + runtime.GOARCH
	if _, ok := assets[bare]; ok {
		return bare, false, true
	}
	for a := range assets {
		if strings.HasPrefix(a, name+"_") && strings.HasSuffix(a, "linux_"+runtime.GOARCH+".tar.gz") {
			return a, true, true
		}
	}
	return "", false, false
}

// downloadAsset streams one release asset to dest — twin of guardian/main.go.
func downloadAsset(url, dest string) error {
	c := http.Client{Timeout: 5 * time.Minute} // follows the github → objects redirect
	resp, err := c.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s: %s", url, resp.Status)
	}
	f, err := os.Create(dest)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, resp.Body); err != nil {
		f.Close()
		os.Remove(dest)
		return err
	}
	return f.Close()
}

// verifyChecksum compares the downloaded asset against its checksums.txt line
// (`<sha256hex>  <name>`); a release without checksums installs with a note.
func verifyChecksum(path, assetName, sums string) error {
	for _, line := range strings.Split(sums, "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 || filepath.Base(fields[1]) != assetName {
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(data)
		if hex.EncodeToString(sum[:]) != fields[0] {
			return fmt.Errorf("checksum mismatch for %s", assetName)
		}
		return nil
	}
	fmt.Println(dimStyle.Render("note: " + assetName + " not listed in checksums.txt — skipping verification"))
	return nil
}

// extractTarGz writes the archive member whose basename is memberBase to dest.
func extractTarGz(archive, memberBase, dest string) error {
	f, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return fmt.Errorf("%s not found in %s", memberBase, filepath.Base(archive))
		}
		if err != nil {
			return err
		}
		if hdr.Typeflag != tar.TypeReg || filepath.Base(hdr.Name) != memberBase {
			continue
		}
		out, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o755)
		if err != nil {
			return err
		}
		if _, err := io.Copy(out, tr); err != nil {
			out.Close()
			return err
		}
		return out.Close()
	}
}

// runPluginBin runs the plugin binary with a timeout, returning stdout.
func runPluginBin(bin string, timeout time.Duration, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	var out, errb bytes.Buffer
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		// first stderr line only — a usage dump is noise here
		if msg := strings.TrimSpace(errb.String()); msg != "" {
			msg, _, _ = strings.Cut(msg, "\n")
			return "", fmt.Errorf("%s %s: %v: %s", filepath.Base(bin), args[0], err, msg)
		}
		return "", fmt.Errorf("%s %s: %v", filepath.Base(bin), args[0], err)
	}
	return out.String(), nil
}

// installedManifests reads every plugin snapshot, skipping unparsable ones.
func installedManifests() []pluginManifest {
	files, _ := filepath.Glob(filepath.Join(pluginsDir(), "*.json"))
	var ms []pluginManifest
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		var m pluginManifest
		if json.Unmarshal(data, &m) != nil || m.Name == "" {
			continue
		}
		ms = append(ms, m)
	}
	return ms
}

// validateManifest enforces the PLUGINS.md rules: name matches the repo,
// command names are telegram-legal and collide with nothing already taken.
func validateManifest(m pluginManifest, name string) error {
	if m.Name != name {
		return fmt.Errorf("manifest name %q does not match repo name %q", m.Name, name)
	}
	taken := map[string]string{}
	for _, b := range builtinTgCommands {
		taken[b] = "omni"
	}
	for _, other := range installedManifests() {
		if other.Name == name {
			continue // reinstall/upgrade replaces its own commands
		}
		for _, c := range other.Commands {
			taken[c.Name] = other.Name
		}
	}
	for _, c := range m.Commands {
		if !pluginCmdRe.MatchString(c.Name) {
			return fmt.Errorf("command %q: names are 1-32 chars of a-z, 0-9 and _", c.Name)
		}
		if len(c.Argv) == 0 {
			return fmt.Errorf("command %q: argv must not be empty", c.Name)
		}
		if owner, ok := taken[c.Name]; ok {
			return fmt.Errorf("command /%s is already taken by %s", c.Name, owner)
		}
	}
	return nil
}

// mergeMCPJSON sets (entry != nil) or deletes one server in the agent
// workspace .mcp.json, leaving foreign servers and top-level keys intact.
func mergeMCPJSON(path, name string, entry any) error {
	top := map[string]json.RawMessage{}
	if data, err := os.ReadFile(path); err == nil {
		json.Unmarshal(data, &top) // unreadable: start fresh
	}
	servers := map[string]json.RawMessage{}
	if raw, ok := top["mcpServers"]; ok {
		json.Unmarshal(raw, &servers)
	}
	if entry == nil {
		delete(servers, name)
	} else {
		raw, err := json.Marshal(entry)
		if err != nil {
			return err
		}
		servers[name] = raw
	}
	raw, err := json.Marshal(servers)
	if err != nil {
		return err
	}
	top["mcpServers"] = raw
	data, err := json.MarshalIndent(top, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

// updateConfigList adds/removes one string in a config.yaml list key through
// map[string]any, so non-string keys (token_budget) survive the round-trip.
func updateConfigList(key, add, remove string) error {
	dir, err := os.UserConfigDir()
	if err != nil {
		return err
	}
	dir = filepath.Join(dir, app)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	path := filepath.Join(dir, "config.yaml")
	cfg := map[string]any{}
	if data, err := os.ReadFile(path); err == nil {
		yaml.Unmarshal(data, &cfg) // unreadable config: start fresh
	}
	var list []string
	if v, ok := cfg[key].([]any); ok {
		for _, x := range v {
			if s, ok := x.(string); ok && s != remove {
				list = append(list, s)
			}
		}
	}
	if add != "" && !slices.Contains(list, add) {
		list = append(list, add)
	}
	cfg[key] = list
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

// runPluginInstall installs (or upgrades) one plugin from its GitHub latest
// release. Validation runs against the staged download, so a bad plugin never
// replaces an installed one.
func runPluginInstall(c *Client, repoArg string) int {
	repo, name, err := normalizeRepo(repoArg)
	if err != nil {
		return fail(err)
	}
	tag, assets, err := latestReleaseAssets(repo)
	if err != nil {
		return fail(err)
	}
	asset, tarball, ok := pickAsset(assets, name)
	if !ok {
		return fail(fmt.Errorf("no linux/%s release asset for %s — see PLUGINS.md", runtime.GOARCH, name))
	}
	stage, err := os.MkdirTemp("", "omni-plugin-*")
	if err != nil {
		return fail(err)
	}
	defer os.RemoveAll(stage)
	apath := filepath.Join(stage, asset)
	if err := downloadAsset(assets[asset], apath); err != nil {
		return fail(err)
	}
	if url, ok := assets["checksums.txt"]; ok {
		spath := filepath.Join(stage, "checksums.txt")
		if err := downloadAsset(url, spath); err != nil {
			return fail(err)
		}
		sums, _ := os.ReadFile(spath)
		if err := verifyChecksum(apath, asset, string(sums)); err != nil {
			return fail(err)
		}
	} else {
		fmt.Println(dimStyle.Render("note: release has no checksums.txt — skipping verification"))
	}
	staged := filepath.Join(stage, name)
	if tarball {
		if err := extractTarGz(apath, name, staged); err != nil {
			return fail(err)
		}
	} else {
		staged = apath
	}
	if err := os.Chmod(staged, 0o755); err != nil {
		return fail(err)
	}

	// the manifest gates everything: validated before the binary is installed
	out, err := runPluginBin(staged, 10*time.Second, "omni-manifest")
	if err != nil {
		return fail(fmt.Errorf("not an omni plugin (see PLUGINS.md): %v", err))
	}
	var m pluginManifest
	if err := json.Unmarshal([]byte(out), &m); err != nil {
		return fail(fmt.Errorf("not an omni plugin (see PLUGINS.md): bad omni-manifest json: %v", err))
	}
	if err := validateManifest(m, name); err != nil {
		return fail(err)
	}

	// install the binary: write-then-rename, atomic and ETXTBSY-safe
	bins := binDir()
	if err := os.MkdirAll(bins, 0o755); err != nil {
		return fail(err)
	}
	dst := filepath.Join(bins, name)
	data, err := os.ReadFile(staged)
	if err != nil {
		return fail(err)
	}
	if err := os.WriteFile(dst+".tmp", data, 0o755); err != nil {
		return fail(err)
	}
	if err := os.Rename(dst+".tmp", dst); err != nil {
		return fail(err)
	}

	// upgrade path: the old snapshot's skill dirs go before the new ones land
	var old pluginManifest
	if raw, err := os.ReadFile(filepath.Join(pluginsDir(), name+".json")); err == nil {
		json.Unmarshal(raw, &old)
	}
	skillsRoot := filepath.Join(agentDir(), ".claude", "skills")
	for _, d := range old.SkillDirs {
		os.RemoveAll(filepath.Join(skillsRoot, d))
	}

	if m.Skills {
		if err := os.MkdirAll(skillsRoot, 0o755); err != nil {
			return fail(err)
		}
		// stage inside skillsRoot so the renames stay on one filesystem
		sstage, err := os.MkdirTemp(skillsRoot, ".stage-*")
		if err != nil {
			return fail(err)
		}
		defer os.RemoveAll(sstage)
		if _, err := runPluginBin(dst, 60*time.Second, "omni-skills", sstage); err != nil {
			return fail(err)
		}
		entries, _ := os.ReadDir(sstage)
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			target := filepath.Join(skillsRoot, e.Name())
			os.RemoveAll(target)
			if err := os.Rename(filepath.Join(sstage, e.Name()), target); err != nil {
				return fail(err)
			}
			m.SkillDirs = append(m.SkillDirs, e.Name())
		}
	}

	if m.MCP != nil {
		cmd := m.MCP.Command
		if !strings.ContainsRune(cmd, os.PathSeparator) {
			cmd = filepath.Join(bins, cmd) // absolute: the server's systemd PATH lacks ~/.local/bin
		}
		entry := map[string]any{"command": cmd}
		if len(m.MCP.Args) > 0 {
			entry["args"] = m.MCP.Args
		}
		if err := mergeMCPJSON(filepath.Join(agentDir(), ".mcp.json"), name, entry); err != nil {
			return fail(err)
		}
	}

	m.Repo = repo
	if err := os.MkdirAll(pluginsDir(), 0o700); err != nil {
		return fail(err)
	}
	snap, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fail(err)
	}
	if err := os.WriteFile(filepath.Join(pluginsDir(), name+".json"), snap, 0o644); err != nil {
		return fail(err)
	}
	if err := updateConfigList("update_repos", repo, ""); err != nil {
		return fail(err)
	}
	if err := c.SyncPlugins(); err != nil {
		fmt.Println(dimStyle.Render("note: server not reached — telegram menu refreshes on its next restart"))
	}

	fmt.Println(okStyle.Render("✓") + " " + name + " " + tag + " installed")
	if m.MCP != nil {
		fmt.Println(dimStyle.Render("  mcp server wired into the agent workspace"))
	}
	if n := len(m.SkillDirs); n > 0 {
		fmt.Println(dimStyle.Render(fmt.Sprintf("  %d skill(s) installed", n)))
	}
	for _, cmd := range m.Commands {
		fmt.Println(dimStyle.Render("  /" + cmd.Name + " — " + cmd.Description))
	}
	return 0
}

// runPluginRemove undoes everything runPluginInstall did for one plugin.
func runPluginRemove(c *Client, name string) int {
	snapPath := filepath.Join(pluginsDir(), name+".json")
	raw, err := os.ReadFile(snapPath)
	if err != nil {
		return fail(fmt.Errorf("plugin %q is not installed — see `omni plugins`", name))
	}
	var m pluginManifest
	json.Unmarshal(raw, &m)
	mcpPath := filepath.Join(agentDir(), ".mcp.json")
	if _, err := os.Stat(mcpPath); err == nil {
		if err := mergeMCPJSON(mcpPath, name, nil); err != nil {
			return fail(err)
		}
	}
	for _, d := range m.SkillDirs {
		os.RemoveAll(filepath.Join(agentDir(), ".claude", "skills", d))
	}
	os.Remove(filepath.Join(binDir(), name))
	if m.Repo != "" {
		if err := updateConfigList("update_repos", "", m.Repo); err != nil {
			return fail(err)
		}
	}
	if err := os.Remove(snapPath); err != nil {
		return fail(err)
	}
	if err := c.SyncPlugins(); err != nil {
		fmt.Println(dimStyle.Render("note: server not reached — telegram menu refreshes on its next restart"))
	}
	fmt.Println(okStyle.Render("✓") + " " + name + " removed")
	return 0
}

// runPlugins lists installed plugins and offers removal via the picker.
func runPlugins(c *Client) int {
	ms := installedManifests()
	if len(ms) == 0 {
		fmt.Println(dimStyle.Render("no plugins installed — add one with `omni plugins install <owner/repo>`"))
		return 0
	}
	items := make([]string, len(ms))
	for i, m := range ms {
		items[i] = m.Name + " " + m.Version + " — " + m.Description
	}
	final, err := tea.NewProgram(newSelectModel("Installed plugins — enter to remove one", items)).Run()
	if err != nil {
		return fail(err)
	}
	choice := final.(selectModel).choice
	if choice == "" {
		return 0
	}
	name := strings.Fields(choice)[0]
	confirm, err := tea.NewProgram(newSelectModel("Remove "+name+"?", []string{"cancel", "remove " + name})).Run()
	if err != nil {
		return fail(err)
	}
	if !strings.HasPrefix(confirm.(selectModel).choice, "remove") {
		fmt.Println(dimStyle.Render("canceled"))
		return 0
	}
	return runPluginRemove(c, name)
}
