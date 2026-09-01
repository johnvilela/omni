package main

import (
	"database/sql"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

// Store persists channel state in SQLite.
type Store struct{ db *sql.DB }

func OpenStore(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS channels (
		name TEXT PRIMARY KEY,
		connected INTEGER NOT NULL DEFAULT 0
	)`)
	if err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) Connected(name string) (bool, error) {
	var v bool
	err := s.db.QueryRow(`SELECT connected FROM channels WHERE name = ?`, name).Scan(&v)
	if err == sql.ErrNoRows {
		return false, nil
	}
	return v, err
}

func (s *Store) SetConnected(name string, v bool) error {
	_, err := s.db.Exec(`INSERT INTO channels (name, connected) VALUES (?, ?)
		ON CONFLICT(name) DO UPDATE SET connected = excluded.connected`, name, v)
	return err
}
