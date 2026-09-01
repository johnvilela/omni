package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"path/filepath"
)

func dbPath() string {
	if x := os.Getenv("XDG_DATA_HOME"); x != "" {
		return filepath.Join(x, "omni", "omni.db")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		log.Fatal(err)
	}
	return filepath.Join(home, ".local", "share", "omni", "omni.db")
}

func main() {
	store, err := OpenStore(dbPath())
	if err != nil {
		log.Fatal(err)
	}
	defer store.Close()

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

	addr := os.Getenv("OMNI_ADDR")
	if addr == "" {
		addr = ":8787"
	}
	log.Printf("omni server listening on %s", addr)
	log.Fatal(http.ListenAndServe(addr, srv.Handler()))
}
