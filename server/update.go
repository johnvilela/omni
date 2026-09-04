// One-tap omni update: the guardian offers buttons (it can't receive taps —
// it never long-polls), so these handlers record the owner's choice as files
// in the data dir for the guardian executor, which does the download/install/
// rollback out of process — a server can't restart itself and survive to
// report the outcome.
package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"time"
)

func dataDir() string { return filepath.Dir(dbPath()) } // twin of guardian dataDir

// ponytail: update.request/update.ignore are unauthenticated local files —
// same localhost-trust model as the :8787 API
var tagRe = regexp.MustCompile(`^v?[0-9][0-9A-Za-z.\-]{0,30}$`)

// startUpdate queues the tapped tag for the guardian and fires it now; if the
// start fails, the 2-minute guardian timer picks the request up anyway.
func (s *Server) startUpdate(tag string) tgReply {
	if !tagRe.MatchString(tag) {
		return tgReply{Text: "⚠ bad version tag"}
	}
	if err := os.MkdirAll(dataDir(), 0o700); err != nil {
		return tgReply{Text: "⚠ could not queue update: " + err.Error()}
	}
	if err := os.WriteFile(filepath.Join(dataDir(), "update.request"), []byte(tag), 0o600); err != nil {
		return tgReply{Text: "⚠ could not queue update: " + err.Error()}
	}
	if err := exec.Command("systemctl", "--user", "start", "--no-block", app+"-guardian.service").Run(); err != nil {
		log.Printf("update: start guardian: %v", err)
	}
	return tgReply{Text: "⏳ updating omni to " + tag + " — downloading, verifying, restarting; report follows", StripKeyboard: true}
}

// updateChangelog posts the release notes into the chat; the offer's buttons
// stay live so the owner can still tap Update or Ignore.
func (s *Server) updateChangelog(tag string) tgReply {
	if !tagRe.MatchString(tag) {
		return tgReply{Text: "⚠ bad version tag"}
	}
	repo := omniRepo()
	if repo == "" {
		return tgReply{Text: "⚠ omni is not in update_repos"}
	}
	notes, err := releaseNotes(repo, tag)
	if err != nil {
		return tgReply{Text: "⚠ changelog: " + err.Error()}
	}
	if notes == "" {
		notes = "(no release notes)"
	}
	return tgReply{Text: "📋 omni " + tag + "\n\n" + notes}
}

// ignoreUpdate remembers the skipped tag and un-throttles the guardian's
// update check so its omni-update state clears within a couple of minutes;
// the next newer release alerts again.
func (s *Server) ignoreUpdate(tag string) tgReply {
	if !tagRe.MatchString(tag) {
		return tgReply{Text: "⚠ bad version tag"}
	}
	if err := os.MkdirAll(dataDir(), 0o700); err != nil {
		return tgReply{Text: "⚠ could not save: " + err.Error()}
	}
	if err := os.WriteFile(filepath.Join(dataDir(), "update.ignore"), []byte(tag), 0o600); err != nil {
		return tgReply{Text: "⚠ could not save: " + err.Error()}
	}
	os.Remove(filepath.Join(dataDir(), "updates.stamp"))
	return tgReply{Text: "🙈 ignoring omni " + tag + " — you'll hear about the next release", StripKeyboard: true}
}

func omniRepo() string {
	for _, r := range readConfig().UpdateRepos {
		if filepath.Base(r) == "omni" {
			return r
		}
	}
	return ""
}

// releaseNotes fetches one release's notes by tag — twin of the guardian's
// releaseAssets, decoding only body.
func releaseNotes(repo, tag string) (string, error) {
	base := os.Getenv("OMNI_GITHUB_API") // test/debug override
	if base == "" {
		base = "https://api.github.com"
	}
	c := http.Client{Timeout: 10 * time.Second}
	resp, err := c.Get(base + "/repos/" + repo + "/releases/tags/" + tag)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("github %s %s: %s", repo, tag, resp.Status)
	}
	var r struct {
		Body string `json:"body"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return "", err
	}
	return r.Body, nil
}
