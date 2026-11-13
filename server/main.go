package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"

	omniversion "omni/version"
)

// app and defaultAddr are overridable at build time via -ldflags -X so a dev
// install (omni-dev, :8788) can coexist with prod without sharing port,
// config or db. A bare ":port" binds loopback outside the container, see
// listenAddr. The version is omni-wide, shared with the cli.
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

	srv.replayQueue() // texts accepted but never started by the previous run
	srv.resumeTasks() // long tasks resume from their checkpoints
	go srv.runCrons(context.Background())

	addr := listenAddr()
	log.Printf("%s server %s listening on %s; %s", app, version, addr, apiTokenHint())
	hs := &http.Server{
		Addr:              addr,
		Handler:           http.MaxBytesHandler(srv.Handler(), 1<<20),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       60 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    64 << 10,
	}
	log.Fatal(hs.ListenAndServe())
}
