package main

import (
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"omni/version"
)

func TestTransitions(t *testing.T) {
	now := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	earlier := now.Add(-10 * time.Minute).Format(time.RFC3339)
	cases := []struct {
		name      string
		prev      map[string]string
		results   []checkResult
		wantLines []string // substrings the message must contain, in any order
		wantEmpty bool
		wantState []string // check names that must be red in the next state
	}{
		{
			name:      "new red alerts once",
			prev:      map[string]string{},
			results:   []checkResult{{name: "disk", ok: false, detail: "1.4 GiB free"}},
			wantLines: []string{"⚠ disk: 1.4 GiB free"},
			wantState: []string{"disk"},
		},
		{
			name:      "stays red stays silent",
			prev:      map[string]string{"disk": earlier},
			results:   []checkResult{{name: "disk", ok: false, detail: "1.4 GiB free"}},
			wantEmpty: true,
			wantState: []string{"disk"},
		},
		{
			name:      "recovery notes duration and clears state",
			prev:      map[string]string{"disk": earlier},
			results:   []checkResult{{name: "disk", ok: true, detail: "20.0 GiB free"}},
			wantLines: []string{"✅ disk: recovered", "down 10m"},
		},
		{
			name:      "event reports even while green",
			prev:      map[string]string{},
			results:   []checkResult{{name: "server", ok: true, detail: "responding", event: "server was inactive — restarted by guardian"}},
			wantLines: []string{"⚠ server was inactive — restarted by guardian"},
		},
		{
			name:      "all green all quiet",
			prev:      map[string]string{},
			results:   []checkResult{{name: "disk", ok: true}, {name: "mem", ok: true}},
			wantEmpty: true,
		},
		{
			name: "multiple transitions batch into one message",
			prev: map[string]string{"net": earlier},
			results: []checkResult{
				{name: "net", ok: true, detail: "internet reachable"},
				{name: "mem", ok: false, detail: "100 MiB available"},
			},
			wantLines: []string{"✅ net: recovered", "⚠ mem: 100 MiB available"},
			wantState: []string{"mem"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			msg, next := transitions(c.prev, c.results, now)
			if c.wantEmpty {
				if msg != "" {
					t.Fatalf("message = %q; want empty", msg)
				}
			} else if msg == "" {
				t.Fatal("message empty; want lines")
			}
			for _, want := range c.wantLines {
				if !strings.Contains(msg, want) {
					t.Errorf("message %q missing %q", msg, want)
				}
			}
			if len(next) != len(c.wantState) {
				t.Errorf("next state = %v; want keys %v", next, c.wantState)
			}
			for _, name := range c.wantState {
				if _, ok := next[name]; !ok {
					t.Errorf("next state %v missing %q", next, name)
				}
			}
		})
	}
	// a red check keeps its original went-red timestamp
	msg, next := transitions(map[string]string{"disk": earlier},
		[]checkResult{{name: "disk", ok: false, detail: "x"}}, now)
	if msg != "" || next["disk"] != earlier {
		t.Errorf("stays-red: msg=%q since=%q; want silent with since=%q", msg, next["disk"], earlier)
	}
}

func TestParseMemAvailable(t *testing.T) {
	meminfo := "MemTotal:       32610516 kB\nMemFree:         1856256 kB\nMemAvailable:   16305258 kB\nBuffers:          510980 kB\n"
	got, err := parseMemAvailable(meminfo)
	if err != nil || got != 16305258*1024 {
		t.Errorf("parseMemAvailable = %d, %v; want %d", got, err, 16305258*1024)
	}
	for _, bad := range []string{"", "MemTotal: 1 kB\n", "MemAvailable: junk kB\n"} {
		if _, err := parseMemAvailable(bad); err == nil {
			t.Errorf("parseMemAvailable(%q) = nil error; want error", bad)
		}
	}
}

func TestParsePgrep(t *testing.T) {
	cases := []struct {
		out  string
		want int
	}{
		{"3\n", 3},
		{"0\n", 0},
		{"", 0},
		{"junk", 0},
	}
	for _, c := range cases {
		if got := parsePgrep(c.out); got != c.want {
			t.Errorf("parsePgrep(%q) = %d; want %d", c.out, got, c.want)
		}
	}
}

func TestSemverLess(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"0.5.0", "0.6.0", true},
		{"v0.9.9", "v0.10.1", true},
		{"0.6.0", "0.6.0", false},
		{"v0.11.0", "v0.10.2", false}, // source build ahead of release
		{"0.6.0-dev", "0.6.0", false},
		{"1.0", "1.0.1", true},
	}
	for _, c := range cases {
		if got := semverLess(c.a, c.b); got != c.want {
			t.Errorf("semverLess(%q, %q) = %v; want %v", c.a, c.b, got, c.want)
		}
	}
}

// TestCheckUpdates: a watched repo with a newer release goes red naming the
// tool; a stale omni is returned as a tag for the button offer instead of
// joining the text alert (unless ignored); a failed lookup is indeterminate,
// never a fake verdict.
func TestCheckUpdates(t *testing.T) {
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/owner/memoria/releases/latest":
			fmt.Fprint(w, `{"tag_name":"v9.9.9"}`)
		case "/repos/owner/current/releases/latest":
			fmt.Fprint(w, `{"tag_name":"v0.1.0"}`)
		case "/repos/owner/omni/releases/latest":
			fmt.Fprint(w, `{"tag_name":"v99.9.9"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer fake.Close()
	t.Setenv("OMNI_GITHUB_API", fake.URL)
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	bin := t.TempDir()
	for name, v := range map[string]string{"memoria": "memoria 0.1.0", "current": "current 0.1.0", "broken": "broken 0.1.0"} {
		script := "#!/bin/sh\necho '" + v + "'\n"
		if err := os.WriteFile(filepath.Join(bin, name), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin)

	r, tag, ok := checkUpdates([]string{"owner/memoria", "owner/current", "owner/omni"})
	if !ok || r.ok || !strings.Contains(r.detail, "memoria 0.1.0 → v9.9.9") {
		t.Fatalf("checkUpdates = %+v, %v; want a memoria-stale red", r, ok)
	}
	if strings.Contains(r.detail, "current 0.1.0") {
		t.Fatalf("detail = %q; up-to-date tool must not be listed", r.detail)
	}
	if tag != "v99.9.9" || strings.Contains(r.detail, "omni ") {
		t.Fatalf("tag = %q, detail = %q; stale omni must become the offer tag, not alert text", tag, r.detail)
	}

	// ignored tag: no offer; an even newer release would offer again
	if err := os.MkdirAll(dataDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir(), "update.ignore"), []byte("v99.9.9\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, tag, ok := checkUpdates([]string{"owner/omni"}); !ok || tag != "" {
		t.Fatalf("tag = %q with v99.9.9 ignored; want no offer", tag)
	}
	os.WriteFile(filepath.Join(dataDir(), "update.ignore"), []byte("v98.0.0"), 0o600)
	if _, tag, _ := checkUpdates([]string{"owner/omni"}); tag != "v99.9.9" {
		t.Fatalf("tag = %q with an older tag ignored; want v99.9.9 offered", tag)
	}

	// not installed: skipped entirely, everything else current -> green
	if r, _, ok := checkUpdates([]string{"owner/current", "owner/ghost"}); !ok || !r.ok {
		t.Fatalf("checkUpdates(current+missing) = %+v, %v; want green", r, ok)
	}

	// lookup failure: indeterminate, not green and not red
	if _, _, ok := checkUpdates([]string{"owner/broken"}); ok { // installed, but no release route
		t.Fatal("checkUpdates with failing lookup = definitive; want indeterminate")
	}
}

// TestSendUpdateOffer: the offer message carries the three callback buttons.
func TestSendUpdateOffer(t *testing.T) {
	var body string
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		body = string(b)
		fmt.Fprint(w, `{"ok":true}`)
	}))
	defer fake.Close()
	t.Setenv("OMNI_TELEGRAM_API", fake.URL)

	if !sendUpdateOffer("tok", []int64{42}, "v9.9.9") {
		t.Fatal("sendUpdateOffer = false; want sent")
	}
	for _, want := range []string{"inline_keyboard", "upd:v9.9.9", "updlog:v9.9.9", "updign:v9.9.9", "🆕 omni v9.9.9"} {
		if !strings.Contains(body, want) {
			t.Errorf("send body %q missing %q", body, want)
		}
	}
}

// TestClaimUpdateRequest: first claim wins and consumes the file.
func TestClaimUpdateRequest(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	if err := os.MkdirAll(dataDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, ok := claimUpdateRequest(); ok {
		t.Fatal("claim with no request; want none")
	}
	os.WriteFile(filepath.Join(dataDir(), "update.request"), []byte("v9.9.9\n"), 0o600)
	if tag, ok := claimUpdateRequest(); !ok || tag != "v9.9.9" {
		t.Fatalf("claim = %q, %v; want v9.9.9", tag, ok)
	}
	if _, ok := claimUpdateRequest(); ok {
		t.Fatal("second claim succeeded; want consumed")
	}
}

// updateHarness fakes everything runUpdate touches: home bin dir with "old"
// binaries, github releases + assets, telegram reports, a systemctl PATH shim
// and the /status probe. statusVersion is what the "restarted" server reports.
func updateHarness(t *testing.T, statusVersion string, breakChecksums bool) (report *string, sysLog string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	if err := os.MkdirAll(filepath.Join(home, ".local", "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dataDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, b := range []string{"omni", "omni-server", "omni-guardian"} {
		os.WriteFile(filepath.Join(home, ".local", "bin", b), []byte("old"), 0o755)
	}

	shim := t.TempDir()
	sysLog = filepath.Join(shim, "log")
	os.WriteFile(filepath.Join(shim, "systemctl"), []byte("#!/bin/sh\necho \"$@\" >> "+sysLog+"\n"), 0o755)
	t.Setenv("PATH", shim)

	status := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"app":"omni","version":%q}`, statusVersion)
	}))
	t.Cleanup(status.Close)
	t.Setenv("OMNI_ADDR", strings.TrimPrefix(status.URL, "http://"))

	assets := map[string][]byte{
		"omni_linux_" + runtime.GOARCH:          []byte("new-cli"),
		"omni-server_linux_" + runtime.GOARCH:   []byte("new-server"),
		"omni-guardian_linux_" + runtime.GOARCH: []byte("new-guardian"),
	}
	var sums strings.Builder
	for name, body := range assets {
		sum := fmt.Sprintf("%x", sha256.Sum256(body))
		if breakChecksums {
			sum = strings.Repeat("0", 64)
		}
		fmt.Fprintf(&sums, "%s  %s\n", sum, name)
	}
	var gh *httptest.Server
	gh = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if name, ok := strings.CutPrefix(r.URL.Path, "/dl/"); ok {
			if name == "checksums.txt" {
				fmt.Fprint(w, sums.String())
				return
			}
			w.Write(assets[name])
			return
		}
		if r.URL.Path != "/repos/o/omni/releases/tags/v99.9.9" {
			http.NotFound(w, r)
			return
		}
		var list strings.Builder
		fmt.Fprintf(&list, `{"name":"checksums.txt","browser_download_url":%q}`, gh.URL+"/dl/checksums.txt")
		for name := range assets {
			fmt.Fprintf(&list, `,{"name":%q,"browser_download_url":%q}`, name, gh.URL+"/dl/"+name)
		}
		fmt.Fprintf(w, `{"assets":[%s]}`, list.String())
	}))
	t.Cleanup(gh.Close)
	t.Setenv("OMNI_GITHUB_API", gh.URL)

	report = new(string)
	tg := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		*report += string(b)
		fmt.Fprint(w, `{"ok":true}`)
	}))
	t.Cleanup(tg.Close)
	t.Setenv("OMNI_TELEGRAM_API", tg.URL)

	tries, delay := probeTries, probeDelay
	probeTries, probeDelay = 2, time.Millisecond
	t.Cleanup(func() { probeTries, probeDelay = tries, delay })
	return report, sysLog
}

func restarts(t *testing.T, sysLog string) int {
	t.Helper()
	data, _ := os.ReadFile(sysLog)
	return strings.Count(string(data), "restart")
}

func binContent(t *testing.T, name string) string {
	t.Helper()
	data, _ := os.ReadFile(filepath.Join(binDir(), name))
	return string(data)
}

func TestRunUpdateHappyPath(t *testing.T) {
	report, sysLog := updateHarness(t, "v99.9.9", false)
	saveState(map[string]string{"omni-update": "v99.9.9"})

	runUpdate(config{UpdateRepos: []string{"o/omni"}}, "tok", []int64{42}, "v99.9.9")

	if got := binContent(t, "omni-server"); got != "new-server" {
		t.Fatalf("omni-server = %q; want the new binary", got)
	}
	if got := binContent(t, "omni-server.prev"); got != "old" {
		t.Fatalf("omni-server.prev = %q; want the old binary kept", got)
	}
	if n := restarts(t, sysLog); n != 1 {
		t.Fatalf("restarts = %d; want 1", n)
	}
	if !strings.Contains(*report, "✅ omni updated") || !strings.Contains(*report, "v99.9.9") {
		t.Fatalf("report = %q; want the success notice", *report)
	}
	if _, ok := loadState()["omni-update"]; ok {
		t.Fatal("omni-update key survived a successful update")
	}
}

func TestRunUpdateChecksumMismatch(t *testing.T) {
	report, sysLog := updateHarness(t, "v99.9.9", true)

	runUpdate(config{UpdateRepos: []string{"o/omni"}}, "tok", []int64{42}, "v99.9.9")

	if got := binContent(t, "omni-server"); got != "old" {
		t.Fatalf("omni-server = %q; binaries must be untouched", got)
	}
	if n := restarts(t, sysLog); n != 0 {
		t.Fatalf("restarts = %d; want 0", n)
	}
	if !strings.Contains(*report, "checksum mismatch") {
		t.Fatalf("report = %q; want a checksum mismatch abort", *report)
	}
}

func TestRunUpdateUnhealthyRollsBack(t *testing.T) {
	// the "restarted" server keeps reporting the old version — new binary bad
	report, sysLog := updateHarness(t, version.Version, false)

	runUpdate(config{UpdateRepos: []string{"o/omni"}}, "tok", []int64{42}, "v99.9.9")

	if got := binContent(t, "omni-server"); got != "old" {
		t.Fatalf("omni-server = %q; want the rollback restore", got)
	}
	if n := restarts(t, sysLog); n != 2 {
		t.Fatalf("restarts = %d; want restart + rollback restart", n)
	}
	if !strings.Contains(*report, "rolled back") {
		t.Fatalf("report = %q; want the rollback notice", *report)
	}
}

// TestCheckUpdatesPluginHint: a stale binary installed as an omni plugin gets
// its reinstall command as the fix hint instead of the generic install.sh tail.
func TestCheckUpdatesPluginHint(t *testing.T) {
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"tag_name":"v9.9.9"}`)
	}))
	defer fake.Close()
	t.Setenv("OMNI_GITHUB_API", fake.URL)
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	bin := t.TempDir()
	script := "#!/bin/sh\necho 'pecunia 0.1.0'\n"
	if err := os.WriteFile(filepath.Join(bin, "pecunia"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	if err := os.MkdirAll(filepath.Join(dataDir(), "plugins"), 0o700); err != nil {
		t.Fatal(err)
	}
	snap := `{"name":"pecunia","repo":"johnvilela/pecunia"}`
	if err := os.WriteFile(filepath.Join(dataDir(), "plugins", "pecunia.json"), []byte(snap), 0o644); err != nil {
		t.Fatal(err)
	}

	r, _, ok := checkUpdates([]string{"johnvilela/pecunia"})
	if !ok || r.ok || !strings.Contains(r.detail, "omni plugins install johnvilela/pecunia") {
		t.Fatalf("checkUpdates = %+v, %v; want the plugin reinstall hint", r, ok)
	}
}
