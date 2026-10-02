package proxy

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dector/gust/internal/comments"
)

const retryCommentID = "0123456789abcdef0123456789abcdef"

func postCommentCreate(s *Server, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("POST", "/__gust/comments", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	s.serveComments(resp, req)
	return resp
}

func decodeCreatedComment(t *testing.T, resp *httptest.ResponseRecorder) comments.Comment {
	t.Helper()
	var c comments.Comment
	if err := json.Unmarshal(resp.Body.Bytes(), &c); err != nil {
		t.Fatalf("decode response: %v (%s)", err, resp.Body)
	}
	return c
}

func TestCreateCommentAPIRetryReturnsOriginal(t *testing.T) {
	store := batchAPIStore(t)
	s := &Server{comments: store}
	body := `{"id":"` + retryCommentID + `","path":"/page","text":" note ","html":"<p onclick=\"secret\">text</p>","locator":"body > p","inBatch":true}`
	first := postCommentCreate(s, body)
	if first.Code != 201 {
		t.Fatalf("first create status %d: %s", first.Code, first.Body)
	}
	original := decodeCreatedComment(t, first)

	// Membership is deliberately different on retry. It must not mutate the
	// existing comment or its state.
	retry := postCommentCreate(s, `{"id":"`+retryCommentID+`","path":"/page","text":"note","html":"<p>text</p>","locator":"body > p","inBatch":false}`)
	if retry.Code != 200 {
		t.Fatalf("retry status %d: %s", retry.Code, retry.Body)
	}
	got := decodeCreatedComment(t, retry)
	if got.ID != original.ID || got.CreatedAt != original.CreatedAt || got.State != original.State || got.InBatch != original.InBatch || got.Text != original.Text || got.HTML != original.HTML {
		t.Fatalf("retry changed original comment: original=%+v retry=%+v", original, got)
	}
	all, err := store.List(context.Background(), "")
	if err != nil || len(all) != 1 {
		t.Fatalf("expected one stored comment, got %d, %v", len(all), err)
	}
}

func TestCreateCommentAPIRetryAfterSubmissionPreservesOriginal(t *testing.T) {
	store := batchAPIStore(t)
	s := &Server{comments: store}
	first := postCommentCreate(s, `{"id":"`+retryCommentID+`","path":"/page","text":"note","html":"<p>text</p>","locator":"body > p","inBatch":true}`)
	if first.Code != 201 {
		t.Fatalf("first create status %d: %s", first.Code, first.Body)
	}
	created := decodeCreatedComment(t, first)

	req := httptest.NewRequest("POST", "/__gust/comments/submit?batch=1", strings.NewReader(`{}`))
	resp := httptest.NewRecorder()
	s.serveCommentSubmit(resp, req)
	if resp.Code != 200 {
		t.Fatalf("submit status %d: %s", resp.Code, resp.Body)
	}

	retry := postCommentCreate(s, `{"id":"`+retryCommentID+`","path":"/page","text":"note","html":"<p>text</p>","locator":"body > p","inBatch":true}`)
	if retry.Code != 200 {
		t.Fatalf("retry status %d: %s", retry.Code, retry.Body)
	}
	got := decodeCreatedComment(t, retry)
	if got.ID != created.ID || got.State != comments.StateSubmitted || got.BatchID == "" || got.InBatch {
		t.Fatalf("retry did not return current submitted comment: %+v", got)
	}
	stored, err := store.Get(context.Background(), retryCommentID)
	if err != nil || stored.State != got.State || stored.BatchID != got.BatchID || stored.InBatch != got.InBatch {
		t.Fatalf("stored comment changed on retry: stored=%+v retry=%+v err=%v", stored, got, err)
	}
}

func TestCreateCommentAPIRejectsConflictingOrInvalidID(t *testing.T) {
	s := &Server{comments: batchAPIStore(t)}
	first := postCommentCreate(s, `{"id":"`+retryCommentID+`","path":"/page","text":"note","html":"","locator":"body"}`)
	if first.Code != 201 {
		t.Fatalf("first create status %d: %s", first.Code, first.Body)
	}
	conflict := postCommentCreate(s, `{"id":"`+retryCommentID+`","path":"/other","text":"note","html":"","locator":"body"}`)
	if conflict.Code != 409 {
		t.Fatalf("conflicting ID status %d: %s", conflict.Code, conflict.Body)
	}
	invalid := postCommentCreate(s, `{"id":"0123456789ABCDEF0123456789abcdef","path":"/page","text":"note","html":"","locator":"body"}`)
	if invalid.Code != 400 {
		t.Fatalf("invalid ID status %d: %s", invalid.Code, invalid.Body)
	}
}

func TestCreateCommentAPIIdempotencyStillChecksOrigin(t *testing.T) {
	s := &Server{comments: batchAPIStore(t)}
	req := httptest.NewRequest("POST", "/__gust/comments", strings.NewReader(`{"id":"`+retryCommentID+`","path":"/page","text":"note","html":"","locator":"body"}`))
	req.Host = "gust.test"
	req.Header.Set("Origin", "https://other.test")
	resp := httptest.NewRecorder()
	s.serveComments(resp, req)
	if resp.Code != 403 {
		t.Fatalf("cross-origin create status %d: %s", resp.Code, resp.Body)
	}
}
