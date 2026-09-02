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
	// ponytail: one connection serializes all access — background goroutines
	// (session naming, memory digest) would otherwise hit SQLITE_BUSY; switch
	// to WAL + busy_timeout if contention ever matters.
	db.SetMaxOpenConns(1)
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
		// id is a uuid7: lexicographic order == chronological, so the max id
		// is the active session. consolidated_until is the highest messages.id
		// already folded into long-term memory.
		`CREATE TABLE IF NOT EXISTS sessions (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL DEFAULT '',
			consolidated_until INTEGER NOT NULL DEFAULT 0
		)`,
		// ponytail: no index/FK — single user, tiny data; add if it ever grows.
		`CREATE TABLE IF NOT EXISTS messages (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			session_id TEXT NOT NULL,
			role TEXT NOT NULL,
			content TEXT NOT NULL,
			created_at INTEGER NOT NULL
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

// ActiveSession returns the session with the max id (uuid7: the newest);
// false means no session exists yet.
func (s *Store) ActiveSession() (Session, bool, error) {
	var sess Session
	err := s.db.QueryRow(`SELECT id, name, consolidated_until FROM sessions ORDER BY id DESC LIMIT 1`).
		Scan(&sess.ID, &sess.Name, &sess.ConsolidatedUntil)
	if err == sql.ErrNoRows {
		return Session{}, false, nil
	}
	if err != nil {
		return Session{}, false, err
	}
	return sess, true, nil
}

func (s *Store) AddSession(id string) error {
	_, err := s.db.Exec(`INSERT INTO sessions (id) VALUES (?)`, id)
	return err
}

func (s *Store) SetSessionName(id, name string) error {
	_, err := s.db.Exec(`UPDATE sessions SET name = ? WHERE id = ?`, name, id)
	return err
}

func (s *Store) SetConsolidatedUntil(id string, msgID int64) error {
	_, err := s.db.Exec(`UPDATE sessions SET consolidated_until = ? WHERE id = ?`, msgID, id)
	return err
}

func (s *Store) AddMessage(sessionID, role, content string, createdAt int64) (int64, error) {
	var id int64
	err := s.db.QueryRow(`INSERT INTO messages (session_id, role, content, created_at)
		VALUES (?, ?, ?, ?) RETURNING id`, sessionID, role, content, createdAt).Scan(&id)
	return id, err
}

// Messages returns the whole session, chronological.
// ponytail: load-all; add a LIMIT if a session ever gets huge.
func (s *Store) Messages(sessionID string) ([]Message, error) {
	rows, err := s.db.Query(`SELECT id, role, content, created_at FROM messages
		WHERE session_id = ? ORDER BY id`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ms []Message
	for rows.Next() {
		var m Message
		if err := rows.Scan(&m.ID, &m.Role, &m.Content, &m.CreatedAt); err != nil {
			return nil, err
		}
		ms = append(ms, m)
	}
	return ms, rows.Err()
}

func (s *Store) SetConnected(name string, v bool) error {
	_, err := s.db.Exec(`INSERT INTO channels (name, connected) VALUES (?, ?)
		ON CONFLICT(name) DO UPDATE SET connected = excluded.connected`, name, v)
	return err
}
