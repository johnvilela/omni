package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"path/filepath"

	omniversion "omni/version"
)

// app and defaultAddr are overridable at build time via -ldflags -X so a dev
// install (omni-dev, :8788) can coexist with prod without sharing port,
// config or db. The version is omni-wide, shared with the cli.
var (
	app         = "omni"
	defaultAddr = ":8787"
)

const version = omniversion.Version

func dbPath() string {
	if x := os.Getenv("XDG_DATA_HOME"); x != "" {
		return filepath.Join(x, app, "omni.db")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		log.Fatal(err)
	}
	return filepath.Join(home, ".local", "share", app, "omni.db")
}

func main() {
	store, err := OpenStore(dbPath())
	if err != nil {
		log.Fatal(err)
	}
	defer store.Close()

	if err := seedPersona(); err != nil {
		log.Printf("persona: %v", err) // dormant feature, never fatal
	}

	apiBase := os.Getenv("OMNI_TELEGRAM_API") // test/debug override
	if apiBase == "" {
		apiBase = "https://api.telegram.org"
	}
	srv := NewServer(store, apiBase)
	if ok, _ := store.Connected("telegram"); ok {
		if st, _, err := srv.ConnectTelegram(context.Background(), ""); err != nil {
			log.Printf("telegram: could not resume: %v", err)
		} else {
			log.Printf("telegram: reconnected as @%s", st.BotUsername)
		}
	}

	go srv.runCrons(context.Background())

	addr := os.Getenv("OMNI_ADDR")
	if addr == "" {
		addr = defaultAddr
	}
	log.Printf("%s server %s listening on %s", app, version, addr)
	log.Fatal(http.ListenAndServe(addr, srv.Handler()))
}
