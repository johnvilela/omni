package main

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"time"

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
		// is the newest session (the active fallback when the pointer table is
		// empty). consolidated_until is the highest messages.id already folded
		// into long-term memory. agent sessions run the vendor CLI un-bare;
		// vendor_session_id is the CLI's own session to --resume.
		`CREATE TABLE IF NOT EXISTS sessions (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL DEFAULT '',
			consolidated_until INTEGER NOT NULL DEFAULT 0,
			agent INTEGER NOT NULL DEFAULT 0,
			provider TEXT NOT NULL DEFAULT '',
			vendor_session_id TEXT NOT NULL DEFAULT '',
			last_ctx INTEGER NOT NULL DEFAULT 0,
			unread TEXT NOT NULL DEFAULT ''
		)`,
		// one-row pointer to the active session; empty means "newest wins"
		`CREATE TABLE IF NOT EXISTS active (
			k INTEGER PRIMARY KEY CHECK (k = 1),
			session_id TEXT NOT NULL
		)`,
		// ponytail: no index/FK — single user, tiny data; add if it ever grows.
		`CREATE TABLE IF NOT EXISTS messages (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			session_id TEXT NOT NULL,
			role TEXT NOT NULL,
			content TEXT NOT NULL,
			created_at INTEGER NOT NULL
		)`,
		// scheduled jobs fired by the minute ticker; kind: message (sent
		// as-is) | prompt (answered by the llm) | agent (one-shot agent run)
		`CREATE TABLE IF NOT EXISTS crons (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			schedule TEXT NOT NULL,
			kind TEXT NOT NULL,
			text TEXT NOT NULL,
			created_at INTEGER NOT NULL
		)`,
		// queued-but-unstarted background texts; a row lives from enqueue
		// until its task starts running, so a restart replays what never ran
		`CREATE TABLE IF NOT EXISTS queue (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			session_id TEXT NOT NULL,
			text TEXT NOT NULL
		)`,
		// pinned status-dashboard message per owner chat (see server/pin.go)
		`CREATE TABLE IF NOT EXISTS pins (
			chat_id INTEGER PRIMARY KEY,
			message_id INTEGER NOT NULL,
			mode TEXT NOT NULL DEFAULT 'clean'
		)`,
		// one row per llm call omni made; cost only when the provider
		// reported one (claude CLI does, api responses don't)
		`CREATE TABLE IF NOT EXISTS usage (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			provider TEXT NOT NULL,
			input_tokens INTEGER NOT NULL DEFAULT 0,
			output_tokens INTEGER NOT NULL DEFAULT 0,
			cost REAL NOT NULL DEFAULT 0,
			created_at INTEGER NOT NULL
		)`,
		// chat replies whose TOOL lines await owner approval (approval gate);
		// a row lives from proposal until approve / deny / supersede
		`CREATE TABLE IF NOT EXISTS proposals (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			session_id TEXT NOT NULL,
			reply TEXT NOT NULL,
			created_at INTEGER NOT NULL
		)`,
		// long checkpointed tasks (server/task.go): orchestration state only —
		// checkpoint content lives in agentDir()/tasks/<id>/task.md
		`CREATE TABLE IF NOT EXISTS tasks (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			goal TEXT NOT NULL,
			status TEXT NOT NULL DEFAULT 'running',
			step INTEGER NOT NULL DEFAULT 0,
			note TEXT NOT NULL DEFAULT '',
			created_at INTEGER NOT NULL
		)`,
	} {
		if _, err := db.Exec(ddl); err != nil {
			db.Close()
			return nil, err
		}
	}
	// migrate pre-agent DBs; on fresh installs the CREATE above already has
	// the columns and these fail with "duplicate column name" — ignored.
	for _, ddl := range []string{
		`ALTER TABLE sessions ADD COLUMN agent INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE sessions ADD COLUMN provider TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE sessions ADD COLUMN vendor_session_id TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE sessions ADD COLUMN last_ctx INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE sessions ADD COLUMN unread TEXT NOT NULL DEFAULT ''`,
	} {
		if _, err := db.Exec(ddl); err != nil && !strings.Contains(err.Error(), "duplicate column name") {
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

// ActiveSession returns the session the active pointer names, falling back
// to the max id (uuid7: the newest) when no pointer was ever set; false means
// no session exists yet.
func (s *Store) ActiveSession() (Session, bool, error) {
	var id string
	err := s.db.QueryRow(`SELECT session_id FROM active WHERE k = 1`).Scan(&id)
	if err == sql.ErrNoRows {
		err = s.db.QueryRow(`SELECT id FROM sessions ORDER BY id DESC LIMIT 1`).Scan(&id)
	}
	if err == sql.ErrNoRows {
		return Session{}, false, nil
	}
	if err != nil {
		return Session{}, false, err
	}
	return s.Session(id)
}

// Session looks one session up by id; false means it doesn't exist.
func (s *Store) Session(id string) (Session, bool, error) {
	var sess Session
	err := s.db.QueryRow(`SELECT id, name, consolidated_until, agent, provider, vendor_session_id, last_ctx, unread
		FROM sessions WHERE id = ?`, id).
		Scan(&sess.ID, &sess.Name, &sess.ConsolidatedUntil, &sess.Agent, &sess.Provider, &sess.VendorSessionID, &sess.LastCtx, &sess.Unread)
	if err == sql.ErrNoRows {
		return Session{}, false, nil
	}
	if err != nil {
		return Session{}, false, err
	}
	return sess, true, nil
}

func (s *Store) AddSession(id string, agent bool, provider string) error {
	_, err := s.db.Exec(`INSERT INTO sessions (id, agent, provider) VALUES (?, ?, ?)`, id, agent, provider)
	return err
}

func (s *Store) SetActiveSession(id string) error {
	_, err := s.db.Exec(`INSERT INTO active (k, session_id) VALUES (1, ?)
		ON CONFLICT(k) DO UPDATE SET session_id = excluded.session_id`, id)
	return err
}

// SetSessionProvider pins a chat session to one provider (the sticky @pick);
// "" means the default llm.
func (s *Store) SetSessionProvider(id, provider string) error {
	_, err := s.db.Exec(`UPDATE sessions SET provider = ? WHERE id = ?`, provider, id)
	return err
}

func (s *Store) SetVendorSessionID(id, vendorID string) error {
	_, err := s.db.Exec(`UPDATE sessions SET vendor_session_id = ? WHERE id = ?`, vendorID, id)
	return err
}

// SetSessionCtx records the context size (tokens) the vendor CLI reported on
// the session's last turn — the real total behind /context.
func (s *Store) SetSessionCtx(id string, tokens int64) error {
	_, err := s.db.Exec(`UPDATE sessions SET last_ctx = ? WHERE id = ?`, tokens, id)
	return err
}

// RecentSession is one row of the /sessions listing.
type RecentSession struct {
	ID, Name, FirstMsg, Unread string
	Agent                      bool
}

// RecentSessions lists n sessions, unread ones first (a stored answer must
// never drop off the capped listings), then newest; the first user message is
// the display fallback for unnamed ones.
func (s *Store) RecentSessions(n int) ([]RecentSession, error) {
	rows, err := s.db.Query(`SELECT id, name, agent, unread,
		COALESCE((SELECT content FROM messages m WHERE m.session_id = s.id ORDER BY m.id LIMIT 1), '')
		FROM sessions s ORDER BY (unread != '') DESC, id DESC LIMIT ?`, n)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var rs []RecentSession
	for rows.Next() {
		var r RecentSession
		if err := rows.Scan(&r.ID, &r.Name, &r.Agent, &r.Unread, &r.FirstMsg); err != nil {
			return nil, err
		}
		rs = append(rs, r)
	}
	return rs, rows.Err()
}

// AppendSessionUnread accumulates an answer that finished while the session
// wasn't active; delivered and cleared on resume.
func (s *Store) AppendSessionUnread(id, text string) error {
	_, err := s.db.Exec(`UPDATE sessions SET unread = unread || ? WHERE id = ?`, text, id)
	return err
}

func (s *Store) ClearSessionUnread(id string) error {
	_, err := s.db.Exec(`UPDATE sessions SET unread = '' WHERE id = ?`, id)
	return err
}

// UnreadCount is the number of sessions holding an undelivered answer.
func (s *Store) UnreadCount() (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM sessions WHERE unread != ''`).Scan(&n)
	return n, err
}

// Pin is one pinned dashboard message: which chat, which message, clean|full.
type Pin struct {
	ChatID, MessageID int64
	Mode              string
}

func (s *Store) Pins() ([]Pin, error) {
	rows, err := s.db.Query(`SELECT chat_id, message_id, mode FROM pins`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ps []Pin
	for rows.Next() {
		var p Pin
		if err := rows.Scan(&p.ChatID, &p.MessageID, &p.Mode); err != nil {
			return nil, err
		}
		ps = append(ps, p)
	}
	return ps, rows.Err()
}

func (s *Store) SetPin(chatID, messageID int64, mode string) error {
	_, err := s.db.Exec(`INSERT INTO pins (chat_id, message_id, mode) VALUES (?, ?, ?)
		ON CONFLICT(chat_id) DO UPDATE SET message_id = excluded.message_id, mode = excluded.mode`,
		chatID, messageID, mode)
	return err
}

func (s *Store) DeletePins() error {
	_, err := s.db.Exec(`DELETE FROM pins`)
	return err
}

func (s *Store) SetPinMode(mode string) error {
	_, err := s.db.Exec(`UPDATE pins SET mode = ?`, mode)
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

// QueuedMessage is one persisted queue row, replayed on server start.
type QueuedMessage struct {
	ID        int64
	SessionID string
	Text      string
}

// AddQueued persists one queued-but-unstarted background text; deleted when
// its task starts (from then on the user turn itself is persisted).
func (s *Store) AddQueued(sessionID, text string) (int64, error) {
	var id int64
	err := s.db.QueryRow(`INSERT INTO queue (session_id, text) VALUES (?, ?) RETURNING id`,
		sessionID, text).Scan(&id)
	return id, err
}

func (s *Store) DeleteQueued(id int64) error {
	_, err := s.db.Exec(`DELETE FROM queue WHERE id = ?`, id)
	return err
}

func (s *Store) QueuedMessages() ([]QueuedMessage, error) {
	rows, err := s.db.Query(`SELECT id, session_id, text FROM queue ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var qs []QueuedMessage
	for rows.Next() {
		var q QueuedMessage
		if err := rows.Scan(&q.ID, &q.SessionID, &q.Text); err != nil {
			return nil, err
		}
		qs = append(qs, q)
	}
	return qs, rows.Err()
}

// Proposal is one pending tool approval: the raw llm reply whose TOOL lines
// run only when the owner approves.
type Proposal struct {
	ID               int64
	SessionID, Reply string
}

func (s *Store) AddProposal(sessionID, reply string) (int64, error) {
	var id int64
	err := s.db.QueryRow(`INSERT INTO proposals (session_id, reply, created_at)
		VALUES (?, ?, ?) RETURNING id`, sessionID, reply, time.Now().Unix()).Scan(&id)
	return id, err
}

// Proposal looks one pending proposal up by id; false means it was resolved
// (or never existed).
func (s *Store) Proposal(id int64) (Proposal, bool, error) {
	p := Proposal{ID: id}
	err := s.db.QueryRow(`SELECT session_id, reply FROM proposals WHERE id = ?`, id).
		Scan(&p.SessionID, &p.Reply)
	if err == sql.ErrNoRows {
		return Proposal{}, false, nil
	}
	if err != nil {
		return Proposal{}, false, err
	}
	return p, true, nil
}

func (s *Store) DeleteProposal(id int64) error {
	_, err := s.db.Exec(`DELETE FROM proposals WHERE id = ?`, id)
	return err
}

// DeleteSessionProposals removes every pending proposal for one session,
// returning how many there were (supersede-on-new-message).
func (s *Store) DeleteSessionProposals(sessionID string) (int64, error) {
	res, err := s.db.Exec(`DELETE FROM proposals WHERE session_id = ?`, sessionID)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// Task is one long checkpointed task: status running|paused|blocked|done|
// failed|cancelled; step counts completed loop iterations; note holds the
// last progress note, DONE summary, BLOCKED question or failure reason.
type Task struct {
	ID, Step          int64
	Goal, Status, Note string
}

func (s *Store) AddTask(goal string) (int64, error) {
	var id int64
	err := s.db.QueryRow(`INSERT INTO tasks (goal, created_at) VALUES (?, ?) RETURNING id`,
		goal, time.Now().Unix()).Scan(&id)
	return id, err
}

// Task looks one task up by id; false means it doesn't exist.
func (s *Store) Task(id int64) (Task, bool, error) {
	t := Task{ID: id}
	err := s.db.QueryRow(`SELECT goal, status, step, note FROM tasks WHERE id = ?`, id).
		Scan(&t.Goal, &t.Status, &t.Step, &t.Note)
	if err == sql.ErrNoRows {
		return Task{}, false, nil
	}
	if err != nil {
		return Task{}, false, err
	}
	return t, true, nil
}

// Tasks lists every task, newest first; callers filter by status.
func (s *Store) Tasks() ([]Task, error) {
	rows, err := s.db.Query(`SELECT id, goal, status, step, note FROM tasks ORDER BY id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ts []Task
	for rows.Next() {
		var t Task
		if err := rows.Scan(&t.ID, &t.Goal, &t.Status, &t.Step, &t.Note); err != nil {
			return nil, err
		}
		ts = append(ts, t)
	}
	return ts, rows.Err()
}

func (s *Store) SetTaskStatus(id int64, status, note string) error {
	_, err := s.db.Exec(`UPDATE tasks SET status = ?, note = ? WHERE id = ?`, status, note, id)
	return err
}

// BumpTaskStep counts one completed loop iteration and records its note.
func (s *Store) BumpTaskStep(id int64, note string) error {
	_, err := s.db.Exec(`UPDATE tasks SET step = step + 1, note = ? WHERE id = ?`, note, id)
	return err
}

// Cron is one scheduled job.
type Cron struct {
	ID                   int64
	Schedule, Kind, Text string
}

func (s *Store) AddCron(schedule, kind, text string) (int64, error) {
	var id int64
	err := s.db.QueryRow(`INSERT INTO crons (schedule, kind, text, created_at)
		VALUES (?, ?, ?, ?) RETURNING id`, schedule, kind, text, time.Now().Unix()).Scan(&id)
	return id, err
}

func (s *Store) Crons() ([]Cron, error) {
	rows, err := s.db.Query(`SELECT id, schedule, kind, text FROM crons ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var cs []Cron
	for rows.Next() {
		var c Cron
		if err := rows.Scan(&c.ID, &c.Schedule, &c.Kind, &c.Text); err != nil {
			return nil, err
		}
		cs = append(cs, c)
	}
	return cs, rows.Err()
}

// UpdateCron rewrites one job; false means the id doesn't exist.
func (s *Store) UpdateCron(id int64, schedule, kind, text string) (bool, error) {
	res, err := s.db.Exec(`UPDATE crons SET schedule = ?, kind = ?, text = ? WHERE id = ?`,
		schedule, kind, text, id)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// DeleteCron removes one job; false means there was none.
func (s *Store) DeleteCron(id int64) (bool, error) {
	res, err := s.db.Exec(`DELETE FROM crons WHERE id = ?`, id)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// Usage is one provider's aggregated llm consumption.
type Usage struct {
	Requests, In, Out int64
	Cost              float64
}

func (s *Store) AddUsage(provider string, in, out int64, cost float64, at int64) error {
	_, err := s.db.Exec(`INSERT INTO usage (provider, input_tokens, output_tokens, cost, created_at)
		VALUES (?, ?, ?, ?, ?)`, provider, in, out, cost, at)
	return err
}

func (s *Store) UsageSince(provider string, since int64) (Usage, error) {
	var u Usage
	err := s.db.QueryRow(`SELECT COUNT(*), COALESCE(SUM(input_tokens), 0),
		COALESCE(SUM(output_tokens), 0), COALESCE(SUM(cost), 0)
		FROM usage WHERE provider = ? AND created_at >= ?`, provider, since).
		Scan(&u.Requests, &u.In, &u.Out, &u.Cost)
	return u, err
}

func (s *Store) SetConnected(name string, v bool) error {
	_, err := s.db.Exec(`INSERT INTO channels (name, connected) VALUES (?, ?)
		ON CONFLICT(name) DO UPDATE SET connected = excluded.connected`, name, v)
	return err
}
