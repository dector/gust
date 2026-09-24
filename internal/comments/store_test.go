package comments

import (
	"context"
	"errors"
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

func TestCreateSubmitAndTransitions(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	c := create(t, s, "one")
	if c.State != StateCreated || c.CreatedAt.IsZero() {
		t.Fatalf("created comment: %+v", c)
	}
	b, err := s.SubmitCreated(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(b.Comments) != 1 || b.Comments[0].BatchID != b.ID || b.Comments[0].State != StateSubmitted {
		t.Fatalf("batch: %+v", b)
	}
	if _, err = s.MarkDone(ctx, c.ID); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("done before seen: %v", err)
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
	if _, err = s.Abandon(ctx, c.ID, "not needed"); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("terminal move accepted: %v", err)
	}
	if _, err = s.SubmitCreated(ctx); !errors.Is(err, ErrNoCreated) {
		t.Fatalf("empty submit: %v", err)
	}
}

func TestSubmitGroupsOnlyCreatedAndAbandonReason(t *testing.T) {
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
	if _, err = s.Abandon(ctx, "a", ""); err == nil {
		t.Fatal("empty reason accepted")
	}
	abandoned, err := s.Abandon(ctx, "a", "duplicate")
	if err != nil {
		t.Fatal(err)
	}
	if abandoned.State != StateAbandoned || abandoned.Reason != "duplicate" {
		t.Fatalf("abandoned: %+v", abandoned)
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
