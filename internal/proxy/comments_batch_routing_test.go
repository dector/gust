package proxy

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dector/gust/internal/comments"
)

func TestBrowserBatchRoutesRespectOptInAndDeliverCoordinatedWork(t *testing.T) {
	app := httptest.NewServer(http.NotFoundHandler())
	defer app.Close()
	proxyURL, server := startProxyForTest(t, appPort(t, app.URL))
	defer server.Close()
	store, err := comments.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	server.SetCommentStore(store)
	ctx := context.Background()
	createDraft := func(text string, inBatch bool) comments.Comment {
		t.Helper()
		c, err := store.Create(ctx, comments.Input{Path: "/", Text: text, Locator: "body", InBatch: inBatch})
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	member := createDraft("first batch comment", true)
	older := createDraft("older draft", false)
	note := createDraft("excluded note", false)
	path := "/__gust/comments/" + older.ID + "/batch"
	request := func(method, path, body, origin string, status int) []byte {
		t.Helper()
		req, err := http.NewRequest(method, proxyURL+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Origin", origin)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		data, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != status {
			t.Fatalf("%s %s: status %d, want %d: %s", method, path, resp.StatusCode, status, data)
		}
		return data
	}

	server.SetCommentsEnabled(false)
	request(http.MethodPost, path, `{"inBatch":true}`, proxyURL, http.StatusNotFound)
	request(http.MethodPost, "/__gust/comments/submit?batch=1", `{}`, proxyURL, http.StatusNotFound)
	server.SetCommentsEnabled(true)
	request(http.MethodGet, path, "", proxyURL, http.StatusMethodNotAllowed)
	request(http.MethodPost, path, `{"inBatch":true}`, "https://other.example", http.StatusForbidden)
	request(http.MethodPost, path, `{"inBatch":true}`, proxyURL, http.StatusOK)

	var sent comments.Batch
	if err := json.Unmarshal(request(http.MethodPost, "/__gust/comments/submit?batch=1", `{}`, proxyURL, http.StatusOK), &sent); err != nil {
		t.Fatal(err)
	}
	if len(sent.Comments) != 2 {
		t.Fatalf("batch = %+v, want only the two selected drafts", sent)
	}
	claimed, err := store.NextBatch(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if claimed.ID != sent.ID || len(claimed.Comments) != 2 {
		t.Fatalf("received batch = %+v, want complete submitted batch %s", claimed, sent.ID)
	}
	ids := map[string]bool{member.ID: true, older.ID: true}
	for _, c := range claimed.Comments {
		if !ids[c.ID] || c.State != comments.StateSeen || c.BatchID != sent.ID || c.InBatch {
			t.Fatalf("unexpected received member: %+v", c)
		}
		delete(ids, c.ID)
	}
	if len(ids) != 0 {
		t.Fatalf("missing coordinated batch members: %v", ids)
	}
	unchanged, err := store.Get(ctx, note.ID)
	if err != nil || unchanged.State != comments.StateCreated || unchanged.InBatch || unchanged.BatchID != "" {
		t.Fatalf("excluded draft changed: %+v, %v", unchanged, err)
	}
}
