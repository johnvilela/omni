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
	for _, ddl := range []string{
		`CREATE TABLE IF NOT EXISTS channels (
			name TEXT PRIMARY KEY,
			connected INTEGER NOT NULL DEFAULT 0
		)`,
		`CREATE TABLE IF NOT EXISTS pairings (
			channel TEXT NOT NULL,
			user_id TEXT NOT NULL,
			code TEXT NOT NULL,
			approved INTEGER NOT NULL DEFAULT 0,
			PRIMARY KEY (channel, user_id)
		)`,
	} {
		if _, err := db.Exec(ddl); err != nil {
			db.Close()
			return nil, err
		}
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

func (s *Store) Pairing(channel, userID string) (Pairing, bool, error) {
	p := Pairing{UserID: userID}
	err := s.db.QueryRow(`SELECT code, approved FROM pairings WHERE channel = ? AND user_id = ?`,
		channel, userID).Scan(&p.Code, &p.Approved)
	if err == sql.ErrNoRows {
		return Pairing{}, false, nil
	}
	if err != nil {
		return Pairing{}, false, err
	}
	return p, true, nil
}

func (s *Store) AddPairing(channel, userID, code string) error {
	_, err := s.db.Exec(`INSERT INTO pairings (channel, user_id, code) VALUES (?, ?, ?)`,
		channel, userID, code)
	return err
}

// ApprovePairing marks the pairing with this code approved and returns its
// user id; "" means the code is unknown.
func (s *Store) ApprovePairing(channel, code string) (string, error) {
	var id string
	err := s.db.QueryRow(`UPDATE pairings SET approved = 1 WHERE channel = ? AND code = ? RETURNING user_id`,
		channel, code).Scan(&id)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return id, err
}

func (s *Store) PendingPairings(channel string) (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM pairings WHERE channel = ? AND approved = 0`, channel).Scan(&n)
	return n, err
}

func (s *Store) Pairings(channel string) ([]Pairing, error) {
	rows, err := s.db.Query(`SELECT user_id, code, approved FROM pairings WHERE channel = ? ORDER BY user_id`, channel)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ps []Pairing
	for rows.Next() {
		var p Pairing
		if err := rows.Scan(&p.UserID, &p.Code, &p.Approved); err != nil {
			return nil, err
		}
		ps = append(ps, p)
	}
	return ps, rows.Err()
}

// RevokePairing deletes the pairing; false means there was none.
func (s *Store) RevokePairing(channel, userID string) (bool, error) {
	res, err := s.db.Exec(`DELETE FROM pairings WHERE channel = ? AND user_id = ?`, channel, userID)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

func (s *Store) SetConnected(name string, v bool) error {
	_, err := s.db.Exec(`INSERT INTO channels (name, connected) VALUES (?, ?)
		ON CONFLICT(name) DO UPDATE SET connected = excluded.connected`, name, v)
	return err
}
