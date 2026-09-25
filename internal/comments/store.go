// Package comments provides a store for element comments. It is in-memory by
// default; dev:self can back it with a tmpfs file so comments survive restarts.
package comments

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

// State is the workflow state of a comment.
type State string

const (
	StateCreated   State = "created"
	StateSubmitted State = "submitted"
	StateSeen      State = "seen"
	StateReview    State = "review"
	StateDone      State = "done"
)

// Author identifies who wrote a thread message.
type Author string

const (
	AuthorHuman Author = "human"
	AuthorAgent Author = "agent"
)

// Message is a single reply in a comment thread.
type Message struct {
	ID        string    `json:"id"`
	Author    Author    `json:"author"`
	Text      string    `json:"text"`
	CreatedAt time.Time `json:"createdAt"`
}

var (
	ErrNotFound     = errors.New("comment not found")
	ErrInvalidState = errors.New("invalid comment state transition")
	ErrNoCreated    = errors.New("no created comments to submit")
	ErrTextRequired = errors.New("comment text is required")
)

// Comment contains user-authored text and the selected element context.
type Comment struct {
	ID          string    `json:"id"`
	BatchID     string    `json:"batchId,omitempty"`
	Path        string    `json:"path"`
	Text        string    `json:"text"`
	HTML        string    `json:"html"`
	Locator     string    `json:"locator"`
	State       State     `json:"state"`
	Messages    []Message `json:"messages"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
	SubmittedAt time.Time `json:"submittedAt,omitempty"`
	SeenAt      time.Time `json:"seenAt,omitempty"`
	FinishedAt  time.Time `json:"finishedAt,omitempty"`
}

// Input holds the data needed to create a comment.
type Input struct {
	ID      string
	Path    string
	Text    string
	HTML    string
	Locator string
}

// Batch groups comments submitted together.
type Batch struct {
	ID          string    `json:"id"`
	SubmittedAt time.Time `json:"submittedAt"`
	Comments    []Comment `json:"comments"`
}

// Store is a SQLite comment database. In-memory by default; a file path keeps
// comments across instances. Close it when its owner exits.
type Store struct {
	db      *sql.DB
	mu      sync.Mutex
	changed chan struct{}
	closed  bool
}

// Open creates a fresh in-memory store. Comments are lost when it closes.
func Open() (*Store, error) {
	return OpenAt(":memory:")
}

// OpenAt opens a store backed by the SQLite database at path, creating the
// schema when needed. ":memory:" gives a transient store; a file path keeps
// comments across store instances.
func OpenAt(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	s := &Store{db: db, changed: make(chan struct{})}
	if _, err = db.Exec(schema); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err = migrate(db); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

const schema = `
CREATE TABLE IF NOT EXISTS batches (
 id TEXT PRIMARY KEY,
 submitted_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS comments (
 id TEXT PRIMARY KEY,
 batch_id TEXT REFERENCES batches(id),
 path TEXT NOT NULL,
 text TEXT NOT NULL,
 html TEXT NOT NULL,
 locator TEXT NOT NULL,
 state TEXT NOT NULL CHECK (state IN ('created','submitted','seen','review','done')),
 created_at INTEGER NOT NULL,
 updated_at INTEGER NOT NULL,
 submitted_at INTEGER,
 seen_at INTEGER,
 finished_at INTEGER
);
CREATE TABLE IF NOT EXISTS messages (
 id TEXT PRIMARY KEY,
 comment_id TEXT NOT NULL REFERENCES comments(id),
 author TEXT NOT NULL CHECK (author IN ('human','agent')),
 text TEXT NOT NULL,
 created_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS comments_state_batch ON comments(state, batch_id);
CREATE INDEX IF NOT EXISTS messages_comment ON messages(comment_id, created_at, id);
`

const schemaVersion = 2

// migrate upgrades a database created before threads. The old schema stored a
// reason column and allowed state='abandoned'; both are gone. Rebuilding the
// comments table keeps old tmpfs-backed dev:self databases openable.
func migrate(db *sql.DB) error {
	hasReason, err := columnExists(db, "comments", "reason")
	if err != nil {
		return err
	}
	if hasReason {
		tx, err := db.Begin()
		if err != nil {
			return err
		}
		defer tx.Rollback()
		stmts := []string{
			`CREATE TABLE comments_new (
 id TEXT PRIMARY KEY,
 batch_id TEXT REFERENCES batches(id),
 path TEXT NOT NULL,
 text TEXT NOT NULL,
 html TEXT NOT NULL,
 locator TEXT NOT NULL,
 state TEXT NOT NULL CHECK (state IN ('created','submitted','seen','review','done')),
 created_at INTEGER NOT NULL,
 updated_at INTEGER NOT NULL,
 submitted_at INTEGER,
 seen_at INTEGER,
 finished_at INTEGER
)`,
			`INSERT INTO comments_new(id,batch_id,path,text,html,locator,state,created_at,updated_at,submitted_at,seen_at,finished_at)
 SELECT id,batch_id,path,text,html,locator,CASE state WHEN 'abandoned' THEN 'done' ELSE state END,created_at,updated_at,submitted_at,seen_at,finished_at FROM comments`,
			`DROP TABLE comments`,
			`ALTER TABLE comments_new RENAME TO comments`,
			`CREATE INDEX IF NOT EXISTS comments_state_batch ON comments(state, batch_id)`,
		}
		for _, stmt := range stmts {
			if _, err = tx.Exec(stmt); err != nil {
				return err
			}
		}
		if err = tx.Commit(); err != nil {
			return err
		}
	}
	_, err = db.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, schemaVersion))
	return err
}

func columnExists(db *sql.DB, table, column string) (bool, error) {
	rows, err := db.Query(`PRAGMA table_info(` + table + `)`)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var cid, notNull, pk int
		var name, typ string
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &typ, &notNull, &dflt, &pk); err != nil {
			return false, err
		}
		if name == column {
			return true, nil
		}
	}
	return false, rows.Err()
}

const stateDirMode = 0o700

// SelfDevPath returns the tmpfs-backed SQLite path dev:self uses to keep
// comments across Gust restarts. It is keyed by project root so a restart
// reopens the same store, and prefers RAM-backed storage so nothing is written
// to disk. Normal Gust usage keeps the in-memory store instead.
func SelfDevPath(root string) (string, error) {
	dir, err := stateDir()
	if err != nil {
		return "", err
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(abs))
	return filepath.Join(dir, hex.EncodeToString(sum[:])[:32]+".db"), nil
}

// stateDir returns a per-user directory on RAM-backed storage, preferring
// tmpfs when the platform provides it.
func stateDir() (string, error) {
	base := os.TempDir()
	if info, err := os.Stat("/dev/shm"); err == nil && info.IsDir() {
		base = "/dev/shm"
	}
	dir := filepath.Join(base, fmt.Sprintf("gust-%d", os.Getuid()))
	if err := os.MkdirAll(dir, stateDirMode); err != nil {
		return "", err
	}
	return dir, nil
}

// Close releases the database. It is safe to call more than once.
func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	return s.db.Close()
}

// Create saves a new unsent comment. An ID is generated if Input.ID is empty.
func (s *Store) Create(ctx context.Context, in Input) (Comment, error) {
	if in.ID == "" {
		in.ID = newID()
	}
	now := time.Now().UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.checkOpen(); err != nil {
		return Comment{}, err
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO comments(id,path,text,html,locator,state,created_at,updated_at) VALUES(?,?,?,?,?,'created',?,?)`, in.ID, in.Path, in.Text, in.HTML, in.Locator, stamp(now), stamp(now))
	if err != nil {
		return Comment{}, err
	}
	return s.getLocked(ctx, in.ID)
}

// Get returns a comment by ID.
func (s *Store) Get(ctx context.Context, id string) (Comment, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.checkOpen(); err != nil {
		return Comment{}, err
	}
	return s.getLocked(ctx, id)
}

// List returns comments in creation order, optionally restricted to state.
func (s *Store) List(ctx context.Context, state State) ([]Comment, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.checkOpen(); err != nil {
		return nil, err
	}
	query := `SELECT ` + columns + ` FROM comments`
	args := []any{}
	if state != "" {
		query += ` WHERE state=?`
		args = append(args, state)
	}
	query += ` ORDER BY created_at,id`
	return s.queryComments(ctx, query, args...)
}

// DeleteDraft removes only a created comment. Submitted work cannot be deleted here.
func (s *Store) DeleteDraft(ctx context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.checkOpen(); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `DELETE FROM comments WHERE id=? AND state='created'`, id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		var state State
		err := tx.QueryRowContext(ctx, `SELECT state FROM comments WHERE id=?`, id).Scan(&state)
		if err == sql.ErrNoRows {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		return ErrInvalidState
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM messages WHERE comment_id=?`, id); err != nil {
		return err
	}
	return tx.Commit()
}

// SubmitCreated atomically groups all created comments into a newly submitted batch.
func (s *Store) SubmitCreated(ctx context.Context) (Batch, error) {
	return s.submit(ctx, "")
}

// SubmitOne submits only the specified draft, leaving other drafts untouched.
func (s *Store) SubmitOne(ctx context.Context, id string) (Batch, error) {
	if id == "" {
		return Batch{}, ErrNoCreated
	}
	return s.submit(ctx, id)
}

func (s *Store) submit(ctx context.Context, id string) (Batch, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.checkOpen(); err != nil {
		return Batch{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Batch{}, err
	}
	defer tx.Rollback()
	where := "state='created'"
	args := []any{}
	if id != "" {
		where += " AND id=?"
		args = append(args, id)
	}
	var count int
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM comments WHERE `+where, args...).Scan(&count); err != nil {
		return Batch{}, err
	}
	if count == 0 {
		return Batch{}, ErrNoCreated
	}
	batch := Batch{ID: newID(), SubmittedAt: time.Now().UTC()}
	if _, err = tx.ExecContext(ctx, `INSERT INTO batches(id,submitted_at) VALUES(?,?)`, batch.ID, stamp(batch.SubmittedAt)); err != nil {
		return Batch{}, err
	}
	updateArgs := append([]any{batch.ID, stamp(batch.SubmittedAt), stamp(batch.SubmittedAt)}, args...)
	if _, err = tx.ExecContext(ctx, `UPDATE comments SET state='submitted',batch_id=?,submitted_at=?,updated_at=? WHERE `+where, updateArgs...); err != nil {
		return Batch{}, err
	}
	if err = tx.Commit(); err != nil {
		return Batch{}, err
	}
	s.signalLocked()
	batch.Comments, err = s.queryComments(ctx, `SELECT `+columns+` FROM comments WHERE batch_id=? ORDER BY created_at,id`, batch.ID)
	return batch, err
}

// NextBatch waits for and atomically claims the oldest submitted batch. Claiming
// changes its comments to seen; cancellation does not change any state.
func (s *Store) NextBatch(ctx context.Context) (Batch, error) {
	return s.next(ctx, false)
}

// NextOne waits for and atomically claims one submitted comment from the oldest
// batch with pending work. Other comments in that batch stay submitted.
func (s *Store) NextOne(ctx context.Context) (Batch, error) {
	return s.next(ctx, true)
}

func (s *Store) next(ctx context.Context, one bool) (Batch, error) {
	for {
		if err := ctx.Err(); err != nil {
			return Batch{}, err
		}
		s.mu.Lock()
		if err := s.checkOpen(); err != nil {
			s.mu.Unlock()
			return Batch{}, err
		}
		batch, found, err := s.claimOldest(ctx, one)
		wait := s.changed
		s.mu.Unlock()
		if err != nil {
			return Batch{}, err
		}
		if found {
			return batch, nil
		}
		select {
		case <-ctx.Done():
			return Batch{}, ctx.Err()
		case <-wait:
		}
	}
}

// ListUnfinished returns all non-closed comments (created, submitted, seen,
// and review).
func (s *Store) ListUnfinished(ctx context.Context) ([]Comment, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.checkOpen(); err != nil {
		return nil, err
	}
	return s.queryComments(ctx, `SELECT `+columns+` FROM comments WHERE state IN ('created','submitted','seen','review') ORDER BY created_at,id`)
}

// ListSeenUnfinished returns seen comments for recovery after interrupted work.
func (s *Store) ListSeenUnfinished(ctx context.Context) ([]Comment, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.checkOpen(); err != nil {
		return nil, err
	}
	return s.queryComments(ctx, `SELECT `+columns+` FROM comments WHERE state='seen' ORDER BY seen_at,created_at,id`)
}

// MarkDone finishes a submitted, seen, or review comment. Only the human does this.
func (s *Store) MarkDone(ctx context.Context, id string) (Comment, error) {
	return s.finish(ctx, id)
}

func (s *Store) finish(ctx context.Context, id string) (Comment, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.checkOpen(); err != nil {
		return Comment{}, err
	}
	now := stamp(time.Now().UTC())
	res, err := s.db.ExecContext(ctx, `UPDATE comments SET state='done',finished_at=?,updated_at=? WHERE id=? AND state IN ('submitted','seen','review')`, now, now, id)
	if err != nil {
		return Comment{}, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return Comment{}, err
	}
	if n == 0 {
		return Comment{}, s.stateError(ctx, id)
	}
	return s.getLocked(ctx, id)
}

// Reply appends a message from author to the thread. A human reply to a review
// thread reopens it as submitted and creates a fresh batch for the agent inbox.
func (s *Store) Reply(ctx context.Context, id string, author Author, text string) (Comment, error) {
	if strings.TrimSpace(text) == "" {
		return Comment{}, ErrTextRequired
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.checkOpen(); err != nil {
		return Comment{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Comment{}, err
	}
	defer tx.Rollback()
	var state State
	if err = tx.QueryRowContext(ctx, `SELECT state FROM comments WHERE id=?`, id).Scan(&state); err != nil {
		if err == sql.ErrNoRows {
			return Comment{}, ErrNotFound
		}
		return Comment{}, err
	}
	if state == StateDone {
		return Comment{}, fmt.Errorf("%w: cannot reply to comment in %q", ErrInvalidState, state)
	}
	now := time.Now().UTC()
	if _, err = tx.ExecContext(ctx, `INSERT INTO messages(id,comment_id,author,text,created_at) VALUES(?,?,?,?,?)`, newID(), id, string(author), text, stamp(now)); err != nil {
		return Comment{}, err
	}
	reopened := author == AuthorHuman && state == StateReview
	if reopened {
		batchID := newID()
		if _, err = tx.ExecContext(ctx, `INSERT INTO batches(id,submitted_at) VALUES(?,?)`, batchID, stamp(now)); err != nil {
			return Comment{}, err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE comments SET state='submitted',batch_id=?,submitted_at=?,updated_at=? WHERE id=?`, batchID, stamp(now), stamp(now), id); err != nil {
			return Comment{}, err
		}
	} else {
		if _, err = tx.ExecContext(ctx, `UPDATE comments SET updated_at=? WHERE id=?`, stamp(now), id); err != nil {
			return Comment{}, err
		}
	}
	if err = tx.Commit(); err != nil {
		return Comment{}, err
	}
	if reopened {
		s.signalLocked()
	}
	return s.getLocked(ctx, id)
}

// Review appends an agent message and moves a seen (or already review) thread
// to review. Only the human can resolve it afterwards.
func (s *Store) Review(ctx context.Context, id, text string) (Comment, error) {
	if strings.TrimSpace(text) == "" {
		return Comment{}, ErrTextRequired
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.checkOpen(); err != nil {
		return Comment{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Comment{}, err
	}
	defer tx.Rollback()
	var state State
	if err = tx.QueryRowContext(ctx, `SELECT state FROM comments WHERE id=?`, id).Scan(&state); err != nil {
		if err == sql.ErrNoRows {
			return Comment{}, ErrNotFound
		}
		return Comment{}, err
	}
	if state != StateSeen && state != StateReview {
		return Comment{}, fmt.Errorf("%w: cannot review comment in %q", ErrInvalidState, state)
	}
	now := time.Now().UTC()
	if _, err = tx.ExecContext(ctx, `INSERT INTO messages(id,comment_id,author,text,created_at) VALUES(?,?,?,?,?)`, newID(), id, string(AuthorAgent), text, stamp(now)); err != nil {
		return Comment{}, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE comments SET state='review',updated_at=? WHERE id=?`, stamp(now), id); err != nil {
		return Comment{}, err
	}
	if err = tx.Commit(); err != nil {
		return Comment{}, err
	}
	return s.getLocked(ctx, id)
}

func (s *Store) claimOldest(ctx context.Context, one bool) (Batch, bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Batch{}, false, err
	}
	defer tx.Rollback()
	var b Batch
	var ts int64
	err = tx.QueryRowContext(ctx, `SELECT b.id,b.submitted_at FROM batches b WHERE EXISTS (SELECT 1 FROM comments c WHERE c.batch_id=b.id AND c.state='submitted') ORDER BY b.submitted_at,b.id LIMIT 1`).Scan(&b.ID, &ts)
	if err == sql.ErrNoRows {
		return Batch{}, false, nil
	}
	if err != nil {
		return Batch{}, false, err
	}
	b.SubmittedAt = fromStamp(ts)
	now := stamp(time.Now().UTC())
	if one {
		var id string
		err = tx.QueryRowContext(ctx, `SELECT id FROM comments WHERE batch_id=? AND state='submitted' ORDER BY created_at,id LIMIT 1`, b.ID).Scan(&id)
		if err != nil {
			return Batch{}, false, err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE comments SET state='seen',seen_at=?,updated_at=? WHERE id=? AND state='submitted'`, now, now, id); err != nil {
			return Batch{}, false, err
		}
		b.Comments, err = queryClaimed(ctx, tx, `SELECT `+columns+` FROM comments WHERE id=?`, id)
	} else {
		if _, err = tx.ExecContext(ctx, `UPDATE comments SET state='seen',seen_at=?,updated_at=? WHERE batch_id=? AND state='submitted'`, now, now, b.ID); err != nil {
			return Batch{}, false, err
		}
		b.Comments, err = queryClaimed(ctx, tx, `SELECT `+columns+` FROM comments WHERE batch_id=? AND state='seen' AND seen_at=? ORDER BY created_at,id`, b.ID, now)
	}
	if err != nil {
		return Batch{}, false, err
	}
	if err = tx.Commit(); err != nil {
		return Batch{}, false, err
	}
	return b, true, nil
}

func queryClaimed(ctx context.Context, tx *sql.Tx, query string, args ...any) ([]Comment, error) {
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	cs, err := scanComments(rows)
	closeErr := rows.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return nil, err
	}
	if err = loadMessages(ctx, tx, cs); err != nil {
		return nil, err
	}
	return cs, nil
}

const columns = `id,batch_id,path,text,html,locator,state,created_at,updated_at,submitted_at,seen_at,finished_at`

// querier is satisfied by both *sql.DB and *sql.Tx.
type querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

func (s *Store) queryComments(ctx context.Context, q string, args ...any) ([]Comment, error) {
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cs, err := scanComments(rows)
	if err != nil {
		return nil, err
	}
	if err := loadMessages(ctx, s.db, cs); err != nil {
		return nil, err
	}
	return cs, nil
}

// loadMessages populates Messages for cs, ordered by created_at then id.
func loadMessages(ctx context.Context, q querier, cs []Comment) error {
	for i := range cs {
		cs[i].Messages = []Message{}
	}
	if len(cs) == 0 {
		return nil
	}
	byID := make(map[string]*Comment, len(cs))
	placeholders := make([]string, 0, len(cs))
	args := make([]any, 0, len(cs))
	for i := range cs {
		byID[cs[i].ID] = &cs[i]
		placeholders = append(placeholders, "?")
		args = append(args, cs[i].ID)
	}
	rows, err := q.QueryContext(ctx, `SELECT id,comment_id,author,text,created_at FROM messages WHERE comment_id IN (`+strings.Join(placeholders, ",")+`) ORDER BY created_at,id`, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var m Message
		var commentID, author string
		var created int64
		if err := rows.Scan(&m.ID, &commentID, &author, &m.Text, &created); err != nil {
			return err
		}
		m.Author = Author(author)
		m.CreatedAt = fromStamp(created)
		if c := byID[commentID]; c != nil {
			c.Messages = append(c.Messages, m)
		}
	}
	return rows.Err()
}

func scanComments(rows *sql.Rows) ([]Comment, error) {
	out := []Comment{}
	for rows.Next() {
		var c Comment
		var batchID sql.NullString
		var state string
		var created, updated int64
		var submitted, seen, finished sql.NullInt64
		if err := rows.Scan(&c.ID, &batchID, &c.Path, &c.Text, &c.HTML, &c.Locator, &state, &created, &updated, &submitted, &seen, &finished); err != nil {
			return nil, err
		}
		if batchID.Valid {
			c.BatchID = batchID.String
		}
		c.State = State(state)
		c.Messages = []Message{}
		c.CreatedAt = fromStamp(created)
		c.UpdatedAt = fromStamp(updated)
		if submitted.Valid {
			c.SubmittedAt = fromStamp(submitted.Int64)
		}
		if seen.Valid {
			c.SeenAt = fromStamp(seen.Int64)
		}
		if finished.Valid {
			c.FinishedAt = fromStamp(finished.Int64)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
func (s *Store) getLocked(ctx context.Context, id string) (Comment, error) {
	cs, err := s.queryComments(ctx, `SELECT `+columns+` FROM comments WHERE id=?`, id)
	if err != nil {
		return Comment{}, err
	}
	if len(cs) == 0 {
		return Comment{}, ErrNotFound
	}
	return cs[0], nil
}
func (s *Store) stateError(ctx context.Context, id string) error {
	c, err := s.getLocked(ctx, id)
	if err != nil {
		return err
	}
	return fmt.Errorf("%w: cannot finish comment in %q", ErrInvalidState, c.State)
}
func (s *Store) checkOpen() error {
	if s.closed {
		return errors.New("comment store is closed")
	}
	return nil
}
func (s *Store) signalLocked() { close(s.changed); s.changed = make(chan struct{}) }
func newID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b[:])
}
func stamp(t time.Time) int64     { return t.UTC().UnixNano() }
func fromStamp(n int64) time.Time { return time.Unix(0, n).UTC() }
