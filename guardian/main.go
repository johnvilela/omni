// Omni guardian: out-of-process watchdog run by a systemd user timer
// (<app>-guardian.timer, oneshot). It checks the machine and the omni
// server without any LLM and messages every approved telegram pairing on
// state changes: one alert when a check goes red, one recovery notice when
// it goes green again (state in <data>/guardian.json). Its only heal action
// is restarting <app>-server when it stops responding.
//
// It is a separate binary because the failure it most needs to catch is the
// server itself being dead — so it re-derives the telegram token (env or
// config.yaml) and the recipient list (pairings table, read-only) instead
// of going through the server.
package main

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"maps"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"gopkg.in/yaml.v3"
	_ "modernc.org/sqlite"

	"omni/version"
)

// app and defaultAddr are overridable at build time via -ldflags -X so a dev
// install (omni-dev, :8788) can coexist with prod — the var names must match
// scripts/build.sh's -X main.app / -X main.defaultAddr or the stamps no-op.
var (
	app         = "omni"
	defaultAddr = ":8787"
)

const (
	minDiskFree   = 2 << 30       // bytes free on the data-dir filesystem
	minMemAvail   = 300 << 20     // /proc/meminfo MemAvailable
	maxAgentProcs = 5             // claude -p / codex exec subprocesses
	maxDBSize     = 256 << 20     // omni.db size
	netProbeAddr  = "1.1.1.1:443" // TCP dial target, no DNS dependency
)

type checkResult struct {
	name   string
	ok     bool
	detail string // human line for the alert/recovery message
	event  string // non-empty: report this run regardless of prior state
}

func dataDir() string {
	if x := os.Getenv("XDG_DATA_HOME"); x != "" {
		return filepath.Join(x, app)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		log.Fatal(err)
	}
	return filepath.Join(home, ".local", "share", app)
}

func fmtBytes(b uint64) string {
	if b >= 1<<30 {
		return fmt.Sprintf("%.1f GiB", float64(b)/(1<<30))
	}
	return fmt.Sprintf("%d MiB", b>>20)
}

// ---- checks ----------------------------------------------------------------

func checkDisk() checkResult {
	var st syscall.Statfs_t
	dir := dataDir()
	if err := syscall.Statfs(dir, &st); err != nil {
		// fresh install: data dir may not exist yet — its filesystem is home's
		home, _ := os.UserHomeDir()
		if err = syscall.Statfs(home, &st); err != nil {
			return checkResult{name: "disk", detail: "statfs: " + err.Error()}
		}
	}
	free := st.Bavail * uint64(st.Bsize)
	return checkResult{name: "disk", ok: free >= minDiskFree,
		detail: fmt.Sprintf("%s free (min %s)", fmtBytes(free), fmtBytes(minDiskFree))}
}

func checkMem() checkResult {
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return checkResult{name: "mem", detail: err.Error()}
	}
	avail, err := parseMemAvailable(string(data))
	if err != nil {
		return checkResult{name: "mem", detail: err.Error()}
	}
	return checkResult{name: "mem", ok: avail >= minMemAvail,
		detail: fmt.Sprintf("%s available (min %s)", fmtBytes(avail), fmtBytes(minMemAvail))}
}

func parseMemAvailable(meminfo string) (uint64, error) {
	for _, line := range strings.Split(meminfo, "\n") {
		rest, ok := strings.CutPrefix(line, "MemAvailable:")
		if !ok {
			continue
		}
		f := strings.Fields(rest)
		if len(f) < 1 {
			break
		}
		kb, err := strconv.ParseUint(f[0], 10, 64)
		if err != nil {
			return 0, fmt.Errorf("meminfo: bad MemAvailable %q", rest)
		}
		return kb * 1024, nil
	}
	return 0, fmt.Errorf("meminfo: no MemAvailable line")
}

func checkLoad() checkResult {
	data, err := os.ReadFile("/proc/loadavg")
	f := strings.Fields(string(data))
	if err != nil || len(f) < 2 {
		return checkResult{name: "load", detail: "cannot read /proc/loadavg"}
	}
	load5, err := strconv.ParseFloat(f[1], 64)
	if err != nil {
		return checkResult{name: "load", detail: "bad /proc/loadavg: " + f[1]}
	}
	max := 2 * float64(runtime.NumCPU())
	return checkResult{name: "load", ok: load5 <= max,
		detail: fmt.Sprintf("5-min load %.1f (max %.0f)", load5, max)}
}

func checkNet() checkResult {
	conn, err := net.DialTimeout("tcp", netProbeAddr, 3*time.Second)
	if err != nil {
		return checkResult{name: "net", detail: "cannot reach " + netProbeAddr}
	}
	conn.Close()
	return checkResult{name: "net", ok: true, detail: "internet reachable"}
}

func checkAgents() checkResult {
	n := pgrepCount("claude -p") + pgrepCount("codex exec")
	return checkResult{name: "agents", ok: n <= maxAgentProcs,
		detail: fmt.Sprintf("%d agent processes (max %d)", n, maxAgentProcs)}
}

// ponytail: pgrep -f matches any command line, so a hand-run `claude -p`
// counts too; match children of the server pid if strays ever matter.
func pgrepCount(pattern string) int {
	out, _ := exec.Command("pgrep", "-c", "-f", pattern).Output()
	return parsePgrep(string(out)) // pgrep exits 1 on zero matches — not an error
}

func parsePgrep(out string) int {
	n, err := strconv.Atoi(strings.TrimSpace(out))
	if err != nil {
		return 0
	}
	return n
}

func checkSQLite(db *sql.DB) checkResult {
	var res string
	if err := db.QueryRow(`PRAGMA quick_check`).Scan(&res); err != nil {
		return checkResult{name: "sqlite", detail: "omni.db: " + err.Error()}
	}
	if res != "ok" {
		return checkResult{name: "sqlite", detail: "omni.db quick_check: " + res}
	}
	return checkResult{name: "sqlite", ok: true, detail: "omni.db quick_check ok"}
}

func checkDBSize() checkResult {
	fi, err := os.Stat(filepath.Join(dataDir(), "omni.db"))
	if err != nil {
		// missing db is the sqlite check's alert, not this one's
		return checkResult{name: "db-size", ok: true, detail: "omni.db not found"}
	}
	return checkResult{name: "db-size", ok: uint64(fi.Size()) <= maxDBSize,
		detail: fmt.Sprintf("omni.db %s (max %s)", fmtBytes(uint64(fi.Size())), fmtBytes(maxDBSize))}
}

// llmProviders: connected channels row -> how the provider authenticates
// (vendor CLI oauth file, config.yaml key, env var — any one is enough).
var llmProviders = []struct{ row, name, oauth, cfgKey, envKey string }{
	{"llm:openai", "openai", ".codex/auth.json", "openai_key", "OPENAI_API_KEY"},
	{"llm:claude", "claude", ".claude/.credentials.json", "anthropic_key", "ANTHROPIC_API_KEY"},
	{"llm:gemini", "gemini", ".gemini/oauth_creds.json", "gemini_key", "GEMINI_API_KEY"},
}

func checkAuth(db *sql.DB, cfg config) checkResult {
	rows, err := db.Query(`SELECT name FROM channels WHERE name LIKE 'llm:%' AND connected = 1`)
	if err != nil {
		// db trouble is the sqlite check's alert
		return checkResult{name: "auth", ok: true, detail: "skipped: " + err.Error()}
	}
	defer rows.Close()
	connected := map[string]bool{}
	for rows.Next() {
		var name string
		if rows.Scan(&name) == nil {
			connected[name] = true
		}
	}
	home, _ := os.UserHomeDir()
	cfgKeys := map[string]string{"openai_key": cfg.OpenAIKey, "anthropic_key": cfg.AnthropicKey, "gemini_key": cfg.GeminiKey}
	var missing []string
	for _, p := range llmProviders {
		if !connected[p.row] {
			continue
		}
		if _, err := os.Stat(filepath.Join(home, p.oauth)); err == nil {
			continue
		}
		if cfgKeys[p.cfgKey] != "" || os.Getenv(p.envKey) != "" {
			continue
		}
		missing = append(missing, p.name+" (~/"+p.oauth+")")
	}
	if len(missing) > 0 {
		return checkResult{name: "auth", detail: "no credentials for " + strings.Join(missing, ", ")}
	}
	return checkResult{name: "auth", ok: true, detail: "llm credentials present"}
}

// ---- companion updates -------------------------------------------------------

// updateEvery throttles the release lookups to a few times a day — a new
// release can wait hours, and unauthenticated GitHub API calls are limited.
// The stamp is a plain file mtime: guardian.json keys all render as alerts in
// omni doctor / guardian status, so the schedule cannot live there.
const updateEvery = 6 * time.Hour

func updatesDue(now time.Time) bool {
	fi, err := os.Stat(filepath.Join(dataDir(), "updates.stamp"))
	return err != nil || now.Sub(fi.ModTime()) >= updateEvery
}

func stampUpdates() {
	if err := os.WriteFile(filepath.Join(dataDir(), "updates.stamp"), nil, 0o600); err != nil {
		log.Printf("updates: stamp: %v", err)
	}
}

var versionRe = regexp.MustCompile(`v?[0-9]+\.[0-9]+(\.[0-9]+)*`)

// installedVersion asks the tool itself; "" means not installed (or no
// parsable version) — nothing to compare.
func installedVersion(bin string) string {
	out, err := exec.Command(bin, "--version").CombinedOutput()
	if err != nil {
		return ""
	}
	return versionRe.FindString(string(out))
}

// latestRelease is the repo's newest GitHub release tag.
func latestRelease(repo string) (string, error) {
	base := os.Getenv("OMNI_GITHUB_API") // test/debug override
	if base == "" {
		base = "https://api.github.com"
	}
	c := http.Client{Timeout: 10 * time.Second}
	resp, err := c.Get(base + "/repos/" + repo + "/releases/latest")
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("github %s: %s", repo, resp.Status)
	}
	var r struct {
		TagName string `json:"tag_name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil || r.TagName == "" {
		return "", fmt.Errorf("github %s: no tag_name (%v)", repo, err)
	}
	return r.TagName, nil
}

// semverLess reports a < b for dotted numeric versions; leading v and
// pre-release suffixes are ignored, missing/non-numeric segments count as 0 —
// so a source build ahead of the latest release never alerts.
func semverLess(a, b string) bool {
	num := func(v string, i int) int {
		v, _, _ = strings.Cut(strings.TrimPrefix(v, "v"), "-")
		parts := strings.Split(v, ".")
		if i >= len(parts) {
			return 0
		}
		n, _ := strconv.Atoi(parts[i])
		return n
	}
	for i := range 3 {
		if x, y := num(a, i), num(b, i); x != y {
			return x < y
		}
	}
	return false
}

// checkUpdates compares each watched repo (config.yaml update_repos, GitHub
// "owner/name") against what is installed: the binary named like the repo,
// except omni itself, whose version is compiled in (the CLI has no --version).
// A stale prod omni is returned as omniTag for the one-tap update offer
// instead of joining the text alert (unless that tag is ignored); dev builds
// keep the text alert, since release assets are prod-named. definitive is
// false when any lookup failed — the caller then keeps the previous alert
// state instead of faking a recovery.
func checkUpdates(repos []string) (r checkResult, omniTag string, definitive bool) {
	ignored := ""
	if data, err := os.ReadFile(filepath.Join(dataDir(), "update.ignore")); err == nil {
		ignored = strings.TrimSpace(string(data))
	}
	var stale []string
	for _, repo := range repos {
		bin := filepath.Base(repo)
		cur := installedVersion(bin)
		if bin == "omni" {
			cur = version.Version
		}
		if cur == "" {
			continue // not installed — nothing to compare
		}
		latest, err := latestRelease(repo)
		if err != nil {
			log.Printf("updates: %v", err)
			return checkResult{}, "", false
		}
		if !semverLess(cur, latest) {
			continue
		}
		if bin == "omni" && app == "omni" {
			if latest != ignored {
				omniTag = latest
			}
			continue
		}
		entry := fmt.Sprintf("%s %s → %s", bin, cur, latest)
		// a binary installed as an omni plugin has its own reinstall command
		if data, err := os.ReadFile(filepath.Join(dataDir(), "plugins", bin+".json")); err == nil {
			var m struct {
				Repo string `json:"repo"`
			}
			if json.Unmarshal(data, &m) == nil && m.Repo != "" {
				entry += " — omni plugins install " + m.Repo
			}
		}
		stale = append(stale, entry)
	}
	if len(stale) > 0 {
		return checkResult{name: "updates", detail: strings.Join(stale, ", ") + " — rerun scripts/install.sh"}, omniTag, true
	}
	return checkResult{name: "updates", ok: true, detail: "watched packages current"}, omniTag, true
}

// ---- one-tap update executor -----------------------------------------------
// The server writes <data>/update.request when the owner taps ⬆ Update and
// starts this unit; the guardian does the download/install/rollback out of
// process, because the server cannot restart itself and survive to report.

func binDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		log.Fatal(err)
	}
	return filepath.Join(home, ".local", "bin") // matches scripts/install.sh
}

// claimUpdateRequest atomically takes ownership of a pending update.request
// via rename — systemd serializes guardian starts, this is belt-and-braces.
func claimUpdateRequest() (string, bool) {
	req := filepath.Join(dataDir(), "update.request")
	if os.Rename(req, req+".run") != nil {
		return "", false
	}
	defer os.Remove(req + ".run")
	data, err := os.ReadFile(req + ".run")
	tag := strings.TrimSpace(string(data))
	return tag, err == nil && tag != ""
}

// releaseAssets maps asset name → download url for one release tag; decodes
// only what it needs, like latestRelease.
func releaseAssets(repo, tag string) (map[string]string, error) {
	base := os.Getenv("OMNI_GITHUB_API") // test/debug override
	if base == "" {
		base = "https://api.github.com"
	}
	c := http.Client{Timeout: 10 * time.Second}
	resp, err := c.Get(base + "/repos/" + repo + "/releases/tags/" + tag)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("github %s %s: %s", repo, tag, resp.Status)
	}
	var r struct {
		Assets []struct {
			Name string `json:"name"`
			URL  string `json:"browser_download_url"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return nil, err
	}
	assets := map[string]string{}
	for _, a := range r.Assets {
		assets[a.Name] = a.URL
	}
	return assets, nil
}

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

func omniRepo(repos []string) string {
	for _, r := range repos {
		if filepath.Base(r) == "omni" {
			return r
		}
	}
	return ""
}

// rollbackBins puts the .prev backups back; best-effort.
func rollbackBins(bins []string) {
	for _, b := range bins {
		dst := filepath.Join(binDir(), b)
		if err := os.Rename(dst+".prev", dst); err != nil {
			log.Printf("update: rollback %s: %v", b, err)
		}
	}
}

// runUpdate downloads release tag's binaries, verifies them against
// checksums.txt, swaps them in with .prev backups, restarts the server and
// health-checks it — restoring the backups if the new server never reports
// the new version. Every outcome is reported to the owner.
func runUpdate(cfg config, token string, ids []int64, tag string) {
	report := func(m string) {
		log.Print(m)
		sendAll(token, ids, m, nil)
	}
	if version.Version == tag {
		log.Printf("update: already on %s", tag) // re-delivered tap after restart
		return
	}
	repo := omniRepo(cfg.UpdateRepos)
	if repo == "" {
		report("⚠ omni update to " + tag + " failed: omni not in update_repos — nothing was changed")
		return
	}
	assets, err := releaseAssets(repo, tag)
	if err != nil {
		report("⚠ omni update to " + tag + " failed: " + err.Error() + " — nothing was changed")
		return
	}

	stage := filepath.Join(dataDir(), "update-stage")
	os.RemoveAll(stage)
	if err := os.MkdirAll(stage, 0o700); err != nil {
		report("⚠ omni update to " + tag + " failed: " + err.Error() + " — nothing was changed")
		return
	}
	defer os.RemoveAll(stage)

	bins := []string{"omni", "omni-server", "omni-guardian"}
	names := map[string]string{} // bin → asset name
	for _, b := range append([]string{"checksums.txt"}, bins...) {
		name := b
		if b != "checksums.txt" {
			name = b + "_linux_" + runtime.GOARCH
			names[b] = name
		}
		url, ok := assets[name]
		if !ok {
			report("⚠ omni update to " + tag + " failed: release has no asset " + name + " — nothing was changed")
			return
		}
		if err := downloadAsset(url, filepath.Join(stage, name)); err != nil {
			report("⚠ omni update to " + tag + " failed: " + err.Error() + " — nothing was changed")
			return
		}
	}

	sums := map[string]string{} // asset name → sha256 hex, plain `sha256sum` lines
	data, _ := os.ReadFile(filepath.Join(stage, "checksums.txt"))
	for _, line := range strings.Split(string(data), "\n") {
		if f := strings.Fields(line); len(f) == 2 {
			sums[f[1]] = f[0]
		}
	}
	for _, b := range bins {
		body, err := os.ReadFile(filepath.Join(stage, names[b]))
		if err != nil {
			report("⚠ omni update to " + tag + " failed: " + err.Error() + " — nothing was changed")
			return
		}
		if sums[names[b]] != fmt.Sprintf("%x", sha256.Sum256(body)) {
			report("⚠ omni update to " + tag + " aborted: checksum mismatch for " + names[b] + " — nothing was changed")
			return
		}
	}

	var installed []string
	for _, b := range bins {
		dst := filepath.Join(binDir(), b)
		os.Rename(dst, dst+".prev") // backup; ENOENT fine (nothing to back up)
		// stage may be on another filesystem: copy to a .tmp sibling, then
		// rename — atomic, and never opens the busy binary (no ETXTBSY)
		body, _ := os.ReadFile(filepath.Join(stage, names[b]))
		err := os.WriteFile(dst+".tmp", body, 0o755)
		if err == nil {
			err = os.Rename(dst+".tmp", dst)
		}
		if err != nil {
			rollbackBins(installed)
			report("⚠ omni update to " + tag + " failed: " + err.Error() + " — previous binaries restored")
			return
		}
		installed = append(installed, b)
	}

	exec.Command("systemctl", "--user", "restart", app+"-server.service").Run()
	if waitServer(tag) {
		st := loadState()
		delete(st, "omni-update") // clear the doctor alert now, not in 6h
		saveState(st)
		report("✅ omni updated " + version.Version + " → " + tag)
		return
	}
	rollbackBins(bins)
	exec.Command("systemctl", "--user", "restart", app+"-server.service").Run()
	if waitServer("") { // any healthy omni counts — don't demand an exact old version
		report("⚠ omni " + tag + " failed its health check — rolled back to " + version.Version + ", server healthy")
	} else {
		report("🚨 omni " + tag + " failed AND the rollback is not responding — check: systemctl --user status " + app + "-server")
	}
}

// ---- server probe + heal ---------------------------------------------------

var probeTries, probeDelay = 15, 3 * time.Second // vars so tests shrink the wait

// probeVersion GETs /status and returns the server's version; the app-name
// check catches a dev/prod port mixup.
func probeVersion() (string, error) {
	addr := os.Getenv("OMNI_ADDR")
	if addr == "" {
		addr = defaultAddr
	}
	if strings.HasPrefix(addr, ":") {
		addr = "127.0.0.1" + addr
	}
	c := http.Client{Timeout: 5 * time.Second}
	resp, err := c.Get("http://" + addr + "/status")
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var st struct {
		App     string `json:"app"`
		Version string `json:"version"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&st); err != nil {
		return "", err
	}
	if st.App != app {
		return "", fmt.Errorf("unexpected app %q on %s", st.App, addr)
	}
	return st.Version, nil
}

func probeServer() error {
	_, err := probeVersion()
	return err
}

// waitServer polls until the server responds (and, when want != "", reports
// exactly that version — proof the intended binary took). systemctl restart
// returns before the port is bound (wiki/install.md), and startup can block
// ~30s on the telegram resume when telegram is unreachable — hence the long
// budget.
func waitServer(want string) bool {
	for range probeTries {
		time.Sleep(probeDelay)
		if v, err := probeVersion(); err == nil && (want == "" || v == want) {
			return true
		}
	}
	return false
}

// checkServer probes /status and, when the server doesn't answer, restarts
// the unit and re-probes — the only heal action the guardian takes. The
// restart re-runs on every red run (idempotent); alerting stays once-per-
// incident via the transition logic.
func checkServer() checkResult {
	if probeServer() == nil {
		return checkResult{name: "server", ok: true, detail: "responding"}
	}
	unit := app + "-server.service"
	out, _ := exec.Command("systemctl", "--user", "is-active", unit).Output()
	was := strings.TrimSpace(string(out))
	if was == "active" {
		was = "active but not responding"
	}
	if err := exec.Command("systemctl", "--user", "restart", unit).Run(); err != nil {
		return checkResult{name: "server", detail: fmt.Sprintf("down (%s), restart failed: %v", was, err)}
	}
	if waitServer("") {
		return checkResult{name: "server", ok: true, detail: "responding",
			event: fmt.Sprintf("server was %s — restarted by guardian", was)}
	}
	return checkResult{name: "server", detail: fmt.Sprintf("down (%s), restarted but still not responding", was)}
}

// ---- telegram --------------------------------------------------------------

type config struct {
	TelegramToken string   `yaml:"telegram_token"`
	OpenAIKey     string   `yaml:"openai_key"`
	AnthropicKey  string   `yaml:"anthropic_key"`
	GeminiKey     string   `yaml:"gemini_key"`
	UpdateRepos   []string `yaml:"update_repos"` // GitHub owner/name repos to watch for new releases
}

// readConfig loads ~/.config/<app>/config.yaml; zero config on any error.
func readConfig() config {
	var cfg config
	dir, err := os.UserConfigDir()
	if err != nil {
		return cfg
	}
	data, err := os.ReadFile(filepath.Join(dir, app, "config.yaml"))
	if err != nil {
		return cfg
	}
	yaml.Unmarshal(data, &cfg)
	return cfg
}

func resolveToken(cfg config) string {
	if env := os.Getenv("TELEGRAM_BOT_TOKEN"); env != "" {
		return env
	}
	return cfg.TelegramToken
}

// tgCall POSTs one Bot API method. The guardian only ever uses getMe and
// sendMessage — never getUpdates, which would steal the server's long-poll.
func tgCall(token, method string, body, out any) error {
	buf, err := json.Marshal(body)
	if err != nil {
		return err
	}
	base := os.Getenv("OMNI_TELEGRAM_API") // test/debug override, as in the server
	if base == "" {
		base = "https://api.telegram.org"
	}
	c := http.Client{Timeout: 10 * time.Second}
	resp, err := c.Post(base+"/bot"+token+"/"+method, "application/json", bytes.NewReader(buf))
	if err != nil {
		// the url in transport errors contains the token — keep it out of the
		// journal and out of the alert text
		return fmt.Errorf("telegram %s: %s", method, strings.ReplaceAll(err.Error(), token, "<token>"))
	}
	defer resp.Body.Close()
	var env struct {
		OK          bool            `json:"ok"`
		Description string          `json:"description"`
		Result      json.RawMessage `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		return err
	}
	if !env.OK {
		return fmt.Errorf("telegram %s: %s", method, env.Description)
	}
	if out != nil {
		return json.Unmarshal(env.Result, out)
	}
	return nil
}

func checkTelegram(token string) checkResult {
	if token == "" {
		return checkResult{name: "telegram", ok: true, detail: "no token configured"}
	}
	var me struct {
		Username string `json:"username"`
	}
	if err := tgCall(token, "getMe", struct{}{}, &me); err != nil {
		return checkResult{name: "telegram", detail: err.Error()}
	}
	return checkResult{name: "telegram", ok: true, detail: "@" + me.Username + " reachable"}
}

// recipients lists the chat ids of every approved telegram pairing (private
// chats: chat id == user id) — same query as the server's ownerChats.
func recipients(db *sql.DB) []int64 {
	rows, err := db.Query(`SELECT user_id FROM pairings WHERE channel = 'telegram' AND approved = 1`)
	if err != nil {
		log.Printf("recipients: %v", err)
		return nil
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var s string
		if rows.Scan(&s) != nil {
			continue
		}
		if id, err := strconv.ParseInt(s, 10, 64); err == nil {
			ids = append(ids, id)
		}
	}
	return ids
}

func sendAll(token string, ids []int64, msg string, kb any) bool {
	sent := false
	for _, id := range ids {
		body := map[string]any{"chat_id": id, "text": msg}
		if kb != nil {
			body["reply_markup"] = map[string]any{"inline_keyboard": kb}
		}
		if err := tgCall(token, "sendMessage", body, nil); err != nil {
			log.Printf("send to %d: %v", id, err)
			continue
		}
		sent = true
	}
	return sent
}

// sendUpdateOffer posts the one-tap update message. The taps come back to the
// server (the guardian never polls getUpdates), which records the choice as
// update.request / update.ignore in the data dir for the next guardian run.
func sendUpdateOffer(token string, ids []int64, tag string) bool {
	kb := [][]map[string]string{{
		{"text": "⬆ Update", "callback_data": "upd:" + tag},
		{"text": "📋 Changelog", "callback_data": "updlog:" + tag},
		{"text": "🙈 Ignore", "callback_data": "updign:" + tag},
	}}
	return sendAll(token, ids, "🆕 omni "+tag+" available (current "+version.Version+")", kb)
}

// ---- alert state (once per incident + recovery) ----------------------------

func statePath() string { return filepath.Join(dataDir(), "guardian.json") }

func loadState() map[string]string {
	st := map[string]string{}
	if data, err := os.ReadFile(statePath()); err == nil {
		json.Unmarshal(data, &st) // corrupt state: start fresh
	}
	return st
}

func saveState(st map[string]string) {
	data, _ := json.Marshal(st)
	// ponytail: no atomic rename — a crash mid-write costs one duplicate alert
	if err := os.WriteFile(statePath(), data, 0o600); err != nil {
		log.Printf("state: %v", err)
	}
}

// transitions turns this run's results plus the previous alert state (check
// name -> RFC3339 went-red time) into the message to send (empty = nothing
// changed) and the next state. Red & new -> alert; red & known -> silent;
// green & known -> recovery with outage duration; an event line is always
// included (self-heal notices).
func transitions(prev map[string]string, results []checkResult, now time.Time) (string, map[string]string) {
	next := map[string]string{}
	var lines []string
	for _, r := range results {
		if r.event != "" {
			lines = append(lines, "⚠ "+r.event)
		}
		since, wasRed := prev[r.name]
		switch {
		case !r.ok && !wasRed:
			lines = append(lines, "⚠ "+r.name+": "+r.detail)
			next[r.name] = now.Format(time.RFC3339)
		case !r.ok && wasRed:
			next[r.name] = since
		case r.ok && wasRed:
			line := "✅ " + r.name + ": recovered — " + r.detail
			if t, err := time.Parse(time.RFC3339, since); err == nil {
				d := now.Sub(t)
				if d >= time.Minute {
					d = d.Round(time.Minute)
				} else {
					d = d.Round(time.Second)
				}
				line += " (down " + d.String() + ")"
			}
			lines = append(lines, line)
		}
	}
	if len(lines) == 0 {
		return "", next
	}
	return "🛡 " + app + " guardian\n" + strings.Join(lines, "\n"), next
}

// ---- main ------------------------------------------------------------------

func main() {
	log.SetFlags(0) // journald adds its own timestamps

	cfg := readConfig()
	token := resolveToken(cfg)
	// read-only + own busy_timeout: the server runs this db with a single
	// serialized connection and no WAL, so waiting is on us
	db, err := sql.Open("sqlite", "file:"+filepath.Join(dataDir(), "omni.db")+"?mode=ro&_pragma=busy_timeout(5000)")
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)

	if app == "omni" { // release assets are prod-named; dev never writes requests
		if tag, ok := claimUpdateRequest(); ok {
			runUpdate(cfg, token, recipients(db), tag)
			return // server was just restarted+probed; normal checks resume next tick
		}
	}

	results := []checkResult{
		checkDisk(), checkMem(), checkLoad(), checkNet(), checkAgents(),
		checkSQLite(db), checkDBSize(), checkAuth(db, cfg),
		checkServer(), checkTelegram(token),
	}

	now := time.Now()
	updatesRan, omniTag := false, ""
	if len(cfg.UpdateRepos) > 0 && updatesDue(now) {
		if r, tag, ok := checkUpdates(cfg.UpdateRepos); ok {
			results, omniTag, updatesRan = append(results, r), tag, true
		}
		stampUpdates() // even indeterminate: retry in hours, not every 2 minutes
	}

	prev := loadState()
	msg, next := transitions(prev, results, now)
	if !updatesRan { // standing update state must survive throttled runs
		for _, k := range []string{"updates", "omni-update"} {
			if since, ok := prev[k]; ok {
				next[k] = since
			}
		}
	} else if omniTag != "" {
		next["omni-update"] = omniTag // value is the offered tag, not a time
	}
	offer := omniTag != "" && prev["omni-update"] != omniTag // one send per tag
	if msg == "" && !offer && maps.Equal(prev, next) {
		return // nothing changed: no message, no state write
	}
	if msg != "" {
		log.Print(msg) // the journal always gets a copy
	}

	ids := recipients(db)
	if token == "" || len(ids) == 0 {
		log.Print("no telegram token or approved pairings — alert logged only")
		saveState(next) // journal counts as delivered; don't re-log every run
		return
	}
	sent := true
	if msg != "" {
		sent = sendAll(token, ids, msg, nil)
	}
	if offer && !sendUpdateOffer(token, ids, omniTag) {
		// offer not delivered: revert the key so the next updates-due run retries
		if since, ok := prev["omni-update"]; ok {
			next["omni-update"] = since
		} else {
			delete(next, "omni-update")
		}
	}
	if sent {
		saveState(next)
	} else {
		log.Print("telegram send failed — state kept, retrying next run")
	}
	// a red check is a message, not a unit failure: always exit 0
}
