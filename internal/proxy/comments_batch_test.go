package proxy

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dector/gust/internal/comments"
)

func batchAPIStore(t *testing.T) *comments.Store {
	t.Helper()
	s, err := comments.Open()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestCreateCommentAPIAcceptsInBatch(t *testing.T) {
	s := &Server{comments: batchAPIStore(t)}
	req := httptest.NewRequest("POST", "/__gust/comments", strings.NewReader(`{"path":"/","text":"note","html":"<button>Go</button>","locator":"body > button","inBatch":true}`))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	s.serveComments(resp, req)
	if resp.Code != 201 {
		t.Fatalf("create status %d: %s", resp.Code, resp.Body)
	}
	var created comments.Comment
	if err := json.Unmarshal(resp.Body.Bytes(), &created); err != nil || !created.InBatch {
		t.Fatalf("created batch member: %+v, %v", created, err)
	}
}

func TestCommentBatchAPIExplicitMembershipAndSubmit(t *testing.T) {
	store := batchAPIStore(t)
	s := &Server{comments: store}
	ctx := context.Background()
	seed, err := store.Create(ctx, comments.Input{ID: "0123456789abcdef0123456789abcdef", Path: "/", Text: "seed", InBatch: true})
	if err != nil {
		t.Fatal(err)
	}
	outside, err := store.Create(ctx, comments.Input{ID: "abcdef0123456789abcdef0123456789", Path: "/", Text: "outside"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Create(ctx, comments.Input{ID: "11111111111111111111111111111111", Path: "/", Text: "note"}); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest("POST", "/__gust/comments/"+outside.ID+"/batch", strings.NewReader(`{"inBatch":true}`))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	s.serveCommentBatch(resp, req)
	if resp.Code != 200 {
		t.Fatalf("add to batch status %d: %s", resp.Code, resp.Body)
	}
	var updated comments.Comment
	if err := json.Unmarshal(resp.Body.Bytes(), &updated); err != nil || !updated.InBatch {
		t.Fatalf("batch response: %+v, %v", updated, err)
	}

	req = httptest.NewRequest("POST", "/__gust/comments/submit?batch=1", strings.NewReader(`{}`))
	resp = httptest.NewRecorder()
	s.serveCommentSubmit(resp, req)
	if resp.Code != 200 {
		t.Fatalf("submit batch status %d: %s", resp.Code, resp.Body)
	}
	var submitted comments.Batch
	if err := json.Unmarshal(resp.Body.Bytes(), &submitted); err != nil {
		t.Fatal(err)
	}
	if len(submitted.Comments) != 2 || submitted.Comments[0].InBatch || submitted.Comments[1].InBatch {
		t.Fatalf("submitted explicit batch: %+v", submitted)
	}
	unselected, err := store.Get(ctx, "11111111111111111111111111111111")
	if err != nil || unselected.State != comments.StateCreated || unselected.InBatch {
		t.Fatalf("unselected draft changed: %+v, %v", unselected, err)
	}
	if got, err := store.Get(ctx, seed.ID); err != nil || got.State != comments.StateSubmitted || got.InBatch {
		t.Fatalf("seed after send: %+v, %v", got, err)
	}
}

func TestCommentBatchAPIRejectsMembershipWithoutBatchAndInvalidOrigin(t *testing.T) {
	store := batchAPIStore(t)
	s := &Server{comments: store}
	c, err := store.Create(context.Background(), comments.Input{ID: "22222222222222222222222222222222", Path: "/", Text: "draft"})
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest("POST", "/__gust/comments/"+c.ID+"/batch", strings.NewReader(`{"inBatch":true}`))
	resp := httptest.NewRecorder()
	s.serveCommentBatch(resp, req)
	if resp.Code != 409 {
		t.Fatalf("join empty batch status %d: %s", resp.Code, resp.Body)
	}

	req = httptest.NewRequest("POST", "/__gust/comments/"+c.ID+"/batch", strings.NewReader(`{"inBatch":false}`))
	req.Host = "gust.test"
	req.Header.Set("Origin", "https://other.test")
	resp = httptest.NewRecorder()
	s.serveCommentBatch(resp, req)
	if resp.Code != 403 {
		t.Fatalf("invalid origin status %d: %s", resp.Code, resp.Body)
	}
}

func TestCommentSubmitRejectsConflictingBatchQuery(t *testing.T) {
	s := &Server{comments: batchAPIStore(t)}
	for _, target := range []string{
		"/__gust/comments/submit?batch=1&id=some-id",
		"/__gust/comments/submit?batch=true",
		"/__gust/comments/submit?batch=1&batch=0",
		"/__gust/comments/submit?id=one&id=two",
		"/__gust/comments/submit?id=",
	} {
		req := httptest.NewRequest("POST", target, strings.NewReader(`{}`))
		resp := httptest.NewRecorder()
		s.serveCommentSubmit(resp, req)
		if resp.Code != 400 {
			t.Errorf("%s status %d: %s", target, resp.Code, resp.Body)
		}
	}
}
