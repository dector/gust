package comments

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}
func create(t *testing.T, s *Store, id string) Comment {
	t.Helper()
	c, err := s.Create(context.Background(), Input{ID: id, Path: "/page", Text: "Fix it", HTML: "<button>Go</button>", Locator: "body > button"})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestOpenAtPersistsAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "comments.db")
	ctx := context.Background()

	first, err := OpenAt(path)
	if err != nil {
		t.Fatal(err)
	}
	c := create(t, first, "kept")
	if _, err := first.SubmitCreated(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := first.NextBatch(ctx); err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	second, err := OpenAt(path)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	got, err := second.ListSeenUnfinished(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != c.ID || got[0].State != StateSeen {
		t.Fatalf("reopened comments = %+v, want one seen comment %s", got, c.ID)
	}
}

func TestSelfDevPathIsStablePerRoot(t *testing.T) {
	root := t.TempDir()
	other := t.TempDir()
	first, err := SelfDevPath(root)
	if err != nil {
		t.Fatal(err)
	}
	again, err := SelfDevPath(root)
	if err != nil {
		t.Fatal(err)
	}
	different, err := SelfDevPath(other)
	if err != nil {
		t.Fatal(err)
	}
	if first != again {
		t.Fatalf("path not stable: %q vs %q", first, again)
	}
	if first == different {
		t.Fatalf("different roots share path %q", first)
	}
	if !strings.HasSuffix(first, ".db") {
		t.Fatalf("path %q lacks .db suffix", first)
	}
}

func TestCreateSubmitAndTransitions(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	c := create(t, s, "one")
	if c.State != StateCreated || c.CreatedAt.IsZero() {
		t.Fatalf("created comment: %+v", c)
	}
	if len(c.Messages) != 0 {
		t.Fatalf("new comment messages: %+v", c.Messages)
	}
	b, err := s.SubmitCreated(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(b.Comments) != 1 || b.Comments[0].BatchID != b.ID || b.Comments[0].State != StateSubmitted {
		t.Fatalf("batch: %+v", b)
	}
	got, err := s.NextBatch(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != b.ID || got.Comments[0].State != StateSeen || got.Comments[0].SeenAt.IsZero() {
		t.Fatalf("claimed batch: %+v", got)
	}
	seen, err := s.ListSeenUnfinished(ctx)
	if err != nil || len(seen) != 1 || seen[0].ID != c.ID {
		t.Fatalf("recovery list: %v, %v", seen, err)
	}
	done, err := s.MarkDone(ctx, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if done.State != StateDone || done.FinishedAt.IsZero() {
		t.Fatalf("done: %+v", done)
	}
	if _, err = s.Reply(ctx, c.ID, AuthorHuman, "still there?"); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("reply to done accepted: %v", err)
	}
	if _, err = s.Review(ctx, c.ID, "reviewing done"); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("review of done accepted: %v", err)
	}
	if _, err = s.SubmitCreated(ctx); !errors.Is(err, ErrNoCreated) {
		t.Fatalf("empty submit: %v", err)
	}
}

func TestNextOneDrainsBatchInOrder(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	create(t, s, "first")
	create(t, s, "second")
	firstBatch, err := s.SubmitCreated(ctx)
	if err != nil {
		t.Fatal(err)
	}
	create(t, s, "third")
	secondBatch, err := s.SubmitCreated(ctx)
	if err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct{ id, batch string }{{"first", firstBatch.ID}, {"second", firstBatch.ID}, {"third", secondBatch.ID}} {
		got, err := s.NextOne(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if got.ID != tc.batch || len(got.Comments) != 1 || got.Comments[0].ID != tc.id || got.Comments[0].State != StateSeen {
			t.Fatalf("next one: %+v, want %s in %s", got, tc.id, tc.batch)
		}
		if tc.id == "first" {
			remaining, err := s.List(ctx, StateSubmitted)
			if err != nil || len(remaining) != 2 || remaining[0].ID != "second" {
				t.Fatalf("remaining: %+v, %v", remaining, err)
			}
		}
	}
}

func TestNextBatchAfterNextOneDoesNotReturnClaimedComment(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	create(t, s, "first")
	create(t, s, "second")
	if _, err := s.SubmitCreated(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := s.NextOne(ctx); err != nil {
		t.Fatal(err)
	}
	got, err := s.NextBatch(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Comments) != 1 || got.Comments[0].ID != "second" {
		t.Fatalf("remaining batch: %+v", got)
	}
}

func TestMarkDoneAllowedFromSubmittedSeenAndReview(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	// submitted -> done
	create(t, s, "submitted")
	if _, err := s.SubmitOne(ctx, "submitted"); err != nil {
		t.Fatal(err)
	}
	if c, err := s.MarkDone(ctx, "submitted"); err != nil || c.State != StateDone {
		t.Fatalf("done from submitted: %+v, %v", c, err)
	}

	// seen -> done
	create(t, s, "seen")
	if _, err := s.SubmitOne(ctx, "seen"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.NextBatch(ctx); err != nil {
		t.Fatal(err)
	}
	if c, err := s.MarkDone(ctx, "seen"); err != nil || c.State != StateDone {
		t.Fatalf("done from seen: %+v, %v", c, err)
	}

	// review -> done
	create(t, s, "review")
	if _, err := s.SubmitOne(ctx, "review"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.NextBatch(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Review(ctx, "review", "looks done"); err != nil {
		t.Fatal(err)
	}
	if c, err := s.MarkDone(ctx, "review"); err != nil || c.State != StateDone {
		t.Fatalf("done from review: %+v, %v", c, err)
	}

	// created -> done rejected
	create(t, s, "created")
	if _, err := s.MarkDone(ctx, "created"); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("done from created: %v", err)
	}
}

func TestReplyAndReviewTransitions(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	create(t, s, "t")
	if _, err := s.SubmitOne(ctx, "t"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.NextBatch(ctx); err != nil {
		t.Fatal(err)
	}

	if _, err := s.Reply(ctx, "t", AuthorAgent, "   "); !errors.Is(err, ErrTextRequired) {
		t.Fatalf("blank reply: %v", err)
	}
	if _, err := s.Review(ctx, "t", ""); !errors.Is(err, ErrTextRequired) {
		t.Fatalf("blank review: %v", err)
	}

	// agent reply in seen stays seen and appends a message
	got, err := s.Reply(ctx, "t", AuthorAgent, "working on it")
	if err != nil {
		t.Fatal(err)
	}
	if got.State != StateSeen || len(got.Messages) != 1 || got.Messages[0].Author != AuthorAgent || got.Messages[0].Text != "working on it" {
		t.Fatalf("agent reply: %+v", got)
	}

	// human reply in seen stays seen
	got, err = s.Reply(ctx, "t", AuthorHuman, "thanks")
	if err != nil {
		t.Fatal(err)
	}
	if got.State != StateSeen || len(got.Messages) != 2 || got.Messages[1].Author != AuthorHuman {
		t.Fatalf("human reply: %+v", got)
	}

	// review moves seen -> review and appends agent message
	got, err = s.Review(ctx, "t", "please verify")
	if err != nil {
		t.Fatal(err)
	}
	if got.State != StateReview || len(got.Messages) != 3 || got.Messages[2].Text != "please verify" {
		t.Fatalf("review: %+v", got)
	}
	if _, err = s.Review(ctx, "t", "again"); err != nil {
		t.Fatalf("review of review: %v", err)
	}
	if _, err = s.Review(ctx, "missing", "x"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("review missing: %v", err)
	}

	// agent reply in review keeps review state
	got, err = s.Reply(ctx, "t", AuthorAgent, "one more")
	if err != nil {
		t.Fatal(err)
	}
	if got.State != StateReview {
		t.Fatalf("agent reply in review: %+v", got)
	}
}

func TestHumanReplyReopensReviewAsSubmitted(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	create(t, s, "t")
	if _, err := s.SubmitOne(ctx, "t"); err != nil {
		t.Fatal(err)
	}
	first, err := s.NextBatch(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Review(ctx, "t", "done, please check"); err != nil {
		t.Fatal(err)
	}

	reopened, err := s.Reply(ctx, "t", AuthorHuman, "one more thing")
	if err != nil {
		t.Fatal(err)
	}
	if reopened.State != StateSubmitted || reopened.BatchID == "" || reopened.BatchID == first.ID {
		t.Fatalf("reopened: %+v (old batch %s)", reopened, first.ID)
	}
	if len(reopened.Messages) != 2 || reopened.Messages[1].Author != AuthorHuman {
		t.Fatalf("reopened messages: %+v", reopened.Messages)
	}

	// the reopened thread is claimable as a fresh batch with the same id
	again, err := s.NextBatch(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if again.ID != reopened.BatchID || len(again.Comments) != 1 || again.Comments[0].ID != "t" || again.Comments[0].State != StateSeen {
		t.Fatalf("reopened batch: %+v", again)
	}
	if len(again.Comments[0].Messages) != 2 {
		t.Fatalf("reopened batch messages: %+v", again.Comments[0].Messages)
	}
}

func TestOldSchemaMigration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	oldSchema := `
CREATE TABLE batches (id TEXT PRIMARY KEY, submitted_at INTEGER NOT NULL);
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
INSERT INTO comments(id,path,text,html,locator,state,reason,created_at,updated_at) VALUES('kept','/p','t','<b>','body','abandoned','cannot do it',1,1);
INSERT INTO comments(id,path,text,html,locator,state,reason,created_at,updated_at) VALUES('open','/q','u','<i>','body','created','',1,1);
`
	if _, err := db.Exec(oldSchema); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	s, err := OpenAt(path)
	if err != nil {
		t.Fatalf("open old db: %v", err)
	}
	defer s.Close()
	got, err := s.Get(context.Background(), "kept")
	if err != nil {
		t.Fatal(err)
	}
	if got.State != StateDone {
		t.Fatalf("migrated abandoned state = %q, want done", got.State)
	}
	if len(got.Messages) != 0 {
		t.Fatalf("migrated messages = %+v", got.Messages)
	}
	if _, err := s.Get(context.Background(), "open"); err != nil {
		t.Fatal(err)
	}
	var version int
	if err := s.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != schemaVersion {
		t.Fatalf("user_version = %d, want %d", version, schemaVersion)
	}
}

func TestDeleteDraft(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	create(t, s, "draft")
	create(t, s, "submitted")
	if _, err := s.SubmitOne(ctx, "submitted"); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteDraft(ctx, "submitted"); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("delete submitted: %v", err)
	}
	if err := s.DeleteDraft(ctx, "draft"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(ctx, "draft"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("draft still present: %v", err)
	}
	if err := s.DeleteDraft(ctx, "draft"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("delete missing: %v", err)
	}
	if _, err := s.Get(ctx, "submitted"); err != nil {
		t.Fatalf("submitted comment removed: %v", err)
	}
}

func TestDeleteDraftRemovesMessages(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	create(t, s, "draft")
	if _, err := s.Reply(ctx, "draft", AuthorHuman, "a human reply"); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get(ctx, "draft")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Messages) != 1 {
		t.Fatalf("draft messages = %+v, want one", got.Messages)
	}
	if err := s.DeleteDraft(ctx, "draft"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(ctx, "draft"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("draft still present: %v", err)
	}
	var orphans int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM messages WHERE comment_id=?`, "draft").Scan(&orphans); err != nil {
		t.Fatal(err)
	}
	if orphans != 0 {
		t.Fatalf("DeleteDraft left %d orphaned message(s)", orphans)
	}
}

func TestSubmitOneLeavesOtherDrafts(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	create(t, s, "draft")
	create(t, s, "auto")
	if _, err := s.SubmitOne(ctx, "missing"); !errors.Is(err, ErrNoCreated) {
		t.Fatalf("missing id: %v", err)
	}
	b, err := s.SubmitOne(ctx, "auto")
	if err != nil || len(b.Comments) != 1 || b.Comments[0].ID != "auto" {
		t.Fatalf("submit one: %+v, %v", b, err)
	}
	if _, err := s.SubmitOne(ctx, "auto"); !errors.Is(err, ErrNoCreated) {
		t.Fatalf("repeat submit: %v", err)
	}
	draft, err := s.Get(ctx, "draft")
	if err != nil || draft.State != StateCreated {
		t.Fatalf("other draft: %+v, %v", draft, err)
	}
	other, err := s.SubmitCreated(ctx)
	if err != nil || len(other.Comments) != 1 || other.Comments[0].ID != "draft" {
		t.Fatalf("submit drafts: %+v, %v", other, err)
	}
}

func TestSubmitGroupsOnlyCreated(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	create(t, s, "a")
	create(t, s, "b")
	b, err := s.SubmitCreated(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(b.Comments) != 2 {
		t.Fatalf("batch size %d", len(b.Comments))
	}
	if _, err = s.NextBatch(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Review(ctx, "a", "explained in reply"); err != nil {
		t.Fatal(err)
	}
	reviewed, err := s.Get(ctx, "a")
	if err != nil {
		t.Fatal(err)
	}
	if reviewed.State != StateReview || len(reviewed.Messages) != 1 || reviewed.Messages[0].Author != AuthorAgent {
		t.Fatalf("reviewed: %+v", reviewed)
	}
	unfinished, err := s.ListSeenUnfinished(ctx)
	if err != nil || len(unfinished) != 1 || unfinished[0].ID != "b" {
		t.Fatalf("unfinished: %+v %v", unfinished, err)
	}
}

func TestNextBatchOldestAndWaitCancellation(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	waitCtx, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := s.NextBatch(waitCtx); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel: %v", err)
	}
	create(t, s, "first")
	first, err := s.SubmitCreated(ctx)
	if err != nil {
		t.Fatal(err)
	}
	create(t, s, "second")
	second, err := s.SubmitCreated(ctx)
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.NextBatch(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != first.ID {
		t.Fatalf("got batch %s, want oldest %s", got.ID, first.ID)
	}
	got, err = s.NextBatch(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != second.ID {
		t.Fatalf("got batch %s, want %s", got.ID, second.ID)
	}
}

func TestNextBatchWaitsUntilSubmit(t *testing.T) {
	s := testStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	result := make(chan Batch, 1)
	errs := make(chan error, 1)
	go func() {
		b, err := s.NextBatch(ctx)
		if err != nil {
			errs <- err
			return
		}
		result <- b
	}()
	time.Sleep(10 * time.Millisecond)
	create(t, s, "waiting")
	if _, err := s.SubmitCreated(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case b := <-result:
		if len(b.Comments) != 1 || b.Comments[0].ID != "waiting" {
			t.Fatalf("batch: %+v", b)
		}
	case err := <-errs:
		t.Fatal(err)
	case <-ctx.Done():
		t.Fatal("timed out waiting for submitted batch")
	}
}

func TestNextBatchWakesWhenReviewThreadReopenedByHumanReply(t *testing.T) {
	s := testStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	create(t, s, "reviewed")
	if _, err := s.SubmitCreated(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := s.NextBatch(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Review(ctx, "reviewed", "agent reply"); err != nil {
		t.Fatal(err)
	}
	// The store now holds no submitted batch for the waiter.
	result := make(chan Batch, 1)
	errs := make(chan error, 1)
	go func() {
		b, err := s.NextBatch(ctx)
		if err != nil {
			errs <- err
			return
		}
		result <- b
	}()
	time.Sleep(20 * time.Millisecond)
	reopened, err := s.Reply(ctx, "reviewed", AuthorHuman, "please adjust")
	if err != nil {
		t.Fatal(err)
	}
	if reopened.State != StateSubmitted {
		t.Fatalf("human reply state = %s, want submitted", reopened.State)
	}
	select {
	case b := <-result:
		if len(b.Comments) != 1 || b.Comments[0].ID != "reviewed" {
			t.Fatalf("next batch = %+v, want reopened thread", b)
		}
		if b.Comments[0].State != StateSeen {
			t.Fatalf("claimed state = %s, want seen", b.Comments[0].State)
		}
	case err := <-errs:
		t.Fatal(err)
	case <-ctx.Done():
		t.Fatal("timed out waiting for reopened thread")
	}
}

func TestListUnfinishedIncludesOpenStatesOnly(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	// seen: submit and claim immediately.
	create(t, s, "seen")
	if _, err := s.SubmitCreated(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := s.NextBatch(ctx); err != nil {
		t.Fatal(err)
	}

	// done: submit, claim, then finish.
	create(t, s, "done")
	if _, err := s.SubmitCreated(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := s.NextBatch(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := s.MarkDone(ctx, "done"); err != nil {
		t.Fatal(err)
	}

	// review: submit, claim, then mark review.
	create(t, s, "review")
	if _, err := s.SubmitCreated(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := s.NextBatch(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Review(ctx, "review", "please check"); err != nil {
		t.Fatal(err)
	}

	// submitted: submit and leave unclaimed.
	create(t, s, "submitted")
	if _, err := s.SubmitCreated(ctx); err != nil {
		t.Fatal(err)
	}

	// created: never submitted.
	create(t, s, "created")

	open, err := s.ListUnfinished(ctx)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]State{}
	for _, c := range open {
		got[c.ID] = c.State
	}
	want := map[string]State{"created": StateCreated, "submitted": StateSubmitted, "seen": StateSeen, "review": StateReview}
	if len(got) != len(want) {
		t.Fatalf("ListUnfinished returned %v, want %v", got, want)
	}
	for id, state := range want {
		if got[id] != state {
			t.Fatalf("ListUnfinished[%s] = %q, want %q (all: %v)", id, got[id], state, got)
		}
	}

	seen, err := s.ListSeenUnfinished(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(seen) != 1 || seen[0].ID != "seen" {
		t.Fatalf("ListSeenUnfinished = %v, want only seen", seen)
	}
}

func TestListAndMissing(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	create(t, s, "x")
	if _, err := s.Get(ctx, "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing: %v", err)
	}
	cs, err := s.List(ctx, StateCreated)
	if err != nil || len(cs) != 1 {
		t.Fatalf("list: %v %v", cs, err)
	}
}

func TestListStates(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	create(t, s, "c")
	create(t, s, "a")
	create(t, s, "b")
	if _, err := s.SubmitOne(ctx, "a"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SubmitOne(ctx, "b"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.NextBatch(ctx); err != nil { // claims a
		t.Fatal(err)
	}
	if _, err := s.Review(ctx, "a", "looks good"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.NextBatch(ctx); err != nil { // claims b
		t.Fatal(err)
	}
	if _, err := s.MarkDone(ctx, "b"); err != nil {
		t.Fatal(err)
	}

	ids := func(cs []Comment) []string {
		out := make([]string, len(cs))
		for i, c := range cs {
			out[i] = c.ID
		}
		return out
	}

	got, err := s.ListStates(ctx, []State{StateReview, StateDone})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].ID != "a" || got[1].ID != "b" {
		t.Fatalf("review+done = %v", ids(got))
	}
	if got, err = s.ListStates(ctx, []State{StateCreated}); err != nil || len(got) != 1 || got[0].ID != "c" {
		t.Fatalf("created = %v err=%v", ids(got), err)
	}
	if got, err = s.ListStates(ctx, []State{StateSubmitted, StateSeen}); err != nil || len(got) != 0 {
		t.Fatalf("submitted+seen = %v err=%v", ids(got), err)
	}
	if got, err = s.ListStates(ctx, nil); err != nil || len(got) != 0 {
		t.Fatalf("empty = %v err=%v", ids(got), err)
	}
}
