// Package comments provides an in-memory store for element comments.
package comments

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
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
	StateDone      State = "done"
	StateAbandoned State = "abandoned"
)

var (
	ErrNotFound     = errors.New("comment not found")
	ErrInvalidState = errors.New("invalid comment state transition")
	ErrNoCreated    = errors.New("no created comments to submit")
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
	Reason      string    `json:"reason,omitempty"`
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

// Store is an in-memory SQLite comment database. Close it when its owner exits.
type Store struct {
	db      *sql.DB
	mu      sync.Mutex
	changed chan struct{}
	closed  bool
}

// Open creates a fresh in-memory store.
func Open() (*Store, error) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	s := &Store{db: db, changed: make(chan struct{})}
	_, err = db.Exec(`
CREATE TABLE batches (
 id TEXT PRIMARY KEY,
 submitted_at INTEGER NOT NULL
);
CREATE TABLE comments (
 id TEXT PRIMARY KEY,
 batch_id TEXT REFERENCES batches(id),
 path TEXT NOT NULL,
 text TEXT NOT NULL,
 html TEXT NOT NULL,
 locator TEXT NOT NULL,
 state TEXT NOT NULL CHECK (state IN ('created','submitted','seen','done','abandoned')),
 reason TEXT NOT NULL DEFAULT '',
 created_at INTEGER NOT NULL,
 updated_at INTEGER NOT NULL,
 submitted_at INTEGER,
 seen_at INTEGER,
 finished_at INTEGER
);
CREATE INDEX comments_state_batch ON comments(state, batch_id);
`)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
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
	for {
		if err := ctx.Err(); err != nil {
			return Batch{}, err
		}
		s.mu.Lock()
		if err := s.checkOpen(); err != nil {
			s.mu.Unlock()
			return Batch{}, err
		}
		batch, found, err := s.claimOldest(ctx)
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

// ListUnfinished returns all non-closed comments (created, submitted, and seen).
func (s *Store) ListUnfinished(ctx context.Context) ([]Comment, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.checkOpen(); err != nil {
		return nil, err
	}
	return s.queryComments(ctx, `SELECT `+columns+` FROM comments WHERE state IN ('created','submitted','seen') ORDER BY created_at,id`)
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

// MarkDone finishes a seen comment.
func (s *Store) MarkDone(ctx context.Context, id string) (Comment, error) {
	return s.finish(ctx, id, StateDone, "")
}

// Abandon finishes a seen comment with a non-empty reason.
func (s *Store) Abandon(ctx context.Context, id, reason string) (Comment, error) {
	if reason == "" {
		return Comment{}, fmt.Errorf("abandon reason is required")
	}
	return s.finish(ctx, id, StateAbandoned, reason)
}

func (s *Store) finish(ctx context.Context, id string, target State, reason string) (Comment, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.checkOpen(); err != nil {
		return Comment{}, err
	}
	now := stamp(time.Now().UTC())
	res, err := s.db.ExecContext(ctx, `UPDATE comments SET state=?,reason=?,finished_at=?,updated_at=? WHERE id=? AND state='seen'`, target, reason, now, now, id)
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

func (s *Store) claimOldest(ctx context.Context) (Batch, bool, error) {
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
	if _, err = tx.ExecContext(ctx, `UPDATE comments SET state='seen',seen_at=?,updated_at=? WHERE batch_id=? AND state='submitted'`, now, now, b.ID); err != nil {
		return Batch{}, false, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT `+columns+` FROM comments WHERE batch_id=? ORDER BY created_at,id`, b.ID)
	if err != nil {
		return Batch{}, false, err
	}
	b.Comments, err = scanComments(rows)
	closeErr := rows.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return Batch{}, false, err
	}
	if err = tx.Commit(); err != nil {
		return Batch{}, false, err
	}
	return b, true, nil
}

const columns = `id,batch_id,path,text,html,locator,state,reason,created_at,updated_at,submitted_at,seen_at,finished_at`

func (s *Store) queryComments(ctx context.Context, q string, args ...any) ([]Comment, error) {
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanComments(rows)
}
func scanComments(rows *sql.Rows) ([]Comment, error) {
	out := []Comment{}
	for rows.Next() {
		var c Comment
		var batchID sql.NullString
		var state string
		var created, updated int64
		var submitted, seen, finished sql.NullInt64
		if err := rows.Scan(&c.ID, &batchID, &c.Path, &c.Text, &c.HTML, &c.Locator, &state, &c.Reason, &created, &updated, &submitted, &seen, &finished); err != nil {
			return nil, err
		}
		if batchID.Valid {
			c.BatchID = batchID.String
		}
		c.State = State(state)
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
