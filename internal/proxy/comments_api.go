package proxy

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"unicode"

	"github.com/dector/gust/internal/comments"
)

const maxCommentBody = 64 << 10

var (
	inlineScript = regexp.MustCompile(`(?is)<script\b[^>]*>.*?</script\s*>`)
	onAttribute  = regexp.MustCompile(`(?i)\s+on[a-z0-9_-]+\s*=\s*(?:"[^"]*"|'[^']*'|[^\s>]+)`)
	inputValue   = regexp.MustCompile(`(?i)(<input\b[^>]*?)\s+value\s*=\s*(?:"[^"]*"|'[^']*'|[^\s>]+)`)
	dataURL      = regexp.MustCompile(`(?i)(\b(?:src|href|srcset)\s*=\s*)(?:"data:[^"]*"|'data:[^']*'|data:[^\s>]+)`)
)

type apiError struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func writeAPIError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	var e apiError
	e.Error.Code, e.Error.Message = code, message
	_ = json.NewEncoder(w).Encode(e)
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func (s *Server) serveComments(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		w.Header().Set("Allow", "GET, POST")
		writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "use GET or POST")
		return
	}
	if s.comments == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "comments_unavailable", "comments are unavailable")
		return
	}
	if r.Method == http.MethodGet {
		cs, err := s.comments.List(r.Context(), "")
		if err != nil {
			writeAPIError(w, 500, "internal_error", "could not list comments")
			return
		}
		if r.URL.Query().Has("path") {
			path := r.URL.Query().Get("path")
			if !validPath(path) {
				writeAPIError(w, 400, "invalid_path", "path must be a page pathname")
				return
			}
			filtered := cs[:0]
			for _, c := range cs {
				if c.Path == path {
					filtered = append(filtered, c)
				}
			}
			cs = filtered
		}
		writeJSON(w, http.StatusOK, cs)
		return
	}
	if !sameOrigin(w, r) {
		return
	}
	var in struct {
		Path    string `json:"path"`
		Text    string `json:"text"`
		HTML    string `json:"html"`
		Locator string `json:"locator"`
		InBatch bool   `json:"inBatch"`
	}
	if !decodeComment(w, r, &in) {
		return
	}
	if !validPath(in.Path) {
		writeAPIError(w, 400, "invalid_path", "path must be a page pathname")
		return
	}
	if !validCommentText(in.Text) {
		writeAPIError(w, 400, "invalid_text", "text must be non-empty, at most 8192 bytes, and free of control characters")
		return
	}
	if len(in.HTML) > 32768 || len(in.Locator) == 0 || len(in.Locator) > 8192 {
		writeAPIError(w, 400, "invalid_context", "html or locator exceeds its allowed size")
		return
	}
	c, err := s.comments.Create(r.Context(), comments.Input{Path: in.Path, Text: in.Text, HTML: scrubCommentHTML(in.HTML), Locator: in.Locator, InBatch: in.InBatch})
	if err != nil {
		writeAPIError(w, 500, "internal_error", "could not create comment")
		return
	}
	writeJSON(w, http.StatusCreated, c)
}

func (s *Server) serveCommentDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		w.Header().Set("Allow", "DELETE")
		writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "use DELETE")
		return
	}
	if !sameOrigin(w, r) {
		return
	}
	if s.comments == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "comments_unavailable", "comments are unavailable")
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/__gust/comments/")
	if len(id) != 32 || strings.Trim(id, "0123456789abcdef") != "" {
		writeAPIError(w, http.StatusNotFound, "comment_not_found", "comment not found")
		return
	}
	err := s.comments.DeleteDraft(r.Context(), id)
	if errors.Is(err, comments.ErrNotFound) {
		writeAPIError(w, http.StatusNotFound, "comment_not_found", "comment not found")
		return
	}
	if errors.Is(err, comments.ErrInvalidState) {
		writeAPIError(w, http.StatusConflict, "not_a_draft", "only drafts can be removed")
		return
	}
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "internal_error", "could not remove draft")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) serveCommentReply(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		writeAPIError(w, 405, "method_not_allowed", "use POST")
		return
	}
	if !sameOrigin(w, r) {
		return
	}
	if s.comments == nil {
		writeAPIError(w, 503, "comments_unavailable", "comments are unavailable")
		return
	}
	id := commentActionID(r.URL.Path, "reply")
	if id == "" {
		writeAPIError(w, 404, "comment_not_found", "comment not found")
		return
	}
	var in struct {
		Text string `json:"text"`
	}
	if !decodeComment(w, r, &in) {
		return
	}
	if !validCommentText(in.Text) {
		writeAPIError(w, 400, "invalid_text", "text must be non-empty, at most 8192 bytes, and free of control characters")
		return
	}
	c, err := s.comments.Reply(r.Context(), id, comments.AuthorHuman, in.Text)
	if errors.Is(err, comments.ErrNotFound) {
		writeAPIError(w, 404, "comment_not_found", "comment not found")
		return
	}
	if errors.Is(err, comments.ErrInvalidState) {
		writeAPIError(w, 409, "invalid_state", "comment is done and cannot be replied to")
		return
	}
	if errors.Is(err, comments.ErrTextRequired) {
		writeAPIError(w, 400, "invalid_text", "text must be non-empty, at most 8192 bytes, and free of control characters")
		return
	}
	if err != nil {
		writeAPIError(w, 500, "internal_error", "could not reply to comment")
		return
	}
	writeJSON(w, 200, c)
}

func (s *Server) serveCommentReattach(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		writeAPIError(w, 405, "method_not_allowed", "use POST")
		return
	}
	if !sameOrigin(w, r) {
		return
	}
	if s.comments == nil {
		writeAPIError(w, 503, "comments_unavailable", "comments are unavailable")
		return
	}
	id := commentActionID(r.URL.Path, "reattach")
	if id == "" {
		writeAPIError(w, 404, "comment_not_found", "comment not found")
		return
	}
	var in struct {
		Path    string `json:"path"`
		HTML    string `json:"html"`
		Locator string `json:"locator"`
	}
	if !decodeComment(w, r, &in) {
		return
	}
	if !validPath(in.Path) {
		writeAPIError(w, 400, "invalid_path", "path must be a page pathname")
		return
	}
	if len(in.HTML) > 32768 || len(in.Locator) == 0 || len(in.Locator) > 8192 {
		writeAPIError(w, 400, "invalid_context", "html or locator exceeds its allowed size")
		return
	}
	c, err := s.comments.Reattach(r.Context(), id, in.Path, scrubCommentHTML(in.HTML), in.Locator)
	if errors.Is(err, comments.ErrNotFound) {
		writeAPIError(w, 404, "comment_not_found", "comment not found")
		return
	}
	if errors.Is(err, comments.ErrInvalidState) {
		writeAPIError(w, 409, "invalid_state", "finished comments cannot be reattached")
		return
	}
	if err != nil {
		writeAPIError(w, 500, "internal_error", "could not reattach comment")
		return
	}
	writeJSON(w, 200, c)
}

func (s *Server) serveCommentResolve(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		writeAPIError(w, 405, "method_not_allowed", "use POST")
		return
	}
	if !sameOrigin(w, r) {
		return
	}
	if s.comments == nil {
		writeAPIError(w, 503, "comments_unavailable", "comments are unavailable")
		return
	}
	id := commentActionID(r.URL.Path, "resolve")
	if id == "" {
		writeAPIError(w, 404, "comment_not_found", "comment not found")
		return
	}
	c, err := s.comments.MarkDone(r.Context(), id)
	if errors.Is(err, comments.ErrNotFound) {
		writeAPIError(w, 404, "comment_not_found", "comment not found")
		return
	}
	if errors.Is(err, comments.ErrInvalidState) {
		writeAPIError(w, 409, "invalid_state", "comment cannot be resolved from its current state")
		return
	}
	if err != nil {
		writeAPIError(w, 500, "internal_error", "could not resolve comment")
		return
	}
	writeJSON(w, 200, c)
}

func (s *Server) serveCommentSubmit(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		writeAPIError(w, 405, "method_not_allowed", "use POST")
		return
	}
	if !sameOrigin(w, r) {
		return
	}
	if s.comments == nil {
		writeAPIError(w, 503, "comments_unavailable", "comments are unavailable")
		return
	}
	query := r.URL.Query()
	var batch comments.Batch
	var err error
	switch {
	case query.Has("id") && len(query) == 1 && len(query["id"]) == 1 && query.Get("id") != "":
		batch, err = s.comments.SubmitOne(r.Context(), query.Get("id"))
	case query.Has("batch") && len(query) == 1 && len(query["batch"]) == 1 && query.Get("batch") == "1":
		batch, err = s.comments.SubmitDraftBatch(r.Context())
	case len(query) == 0:
		batch, err = s.comments.SubmitCreated(r.Context())
	default:
		writeAPIError(w, 400, "invalid_query", "use either ?id=<comment-id>, ?batch=1, or no query parameters")
		return
	}
	if errors.Is(err, comments.ErrNoCreated) {
		writeAPIError(w, 409, "no_created_comments", "there are no created comments to submit")
		return
	}
	if err != nil {
		writeAPIError(w, 500, "internal_error", "could not submit comments")
		return
	}
	writeJSON(w, 200, batch)
}

// serveCommentBatch changes explicit unsent batch membership for one draft.
func (s *Server) serveCommentBatch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		writeAPIError(w, 405, "method_not_allowed", "use POST")
		return
	}
	if !sameOrigin(w, r) {
		return
	}
	if s.comments == nil {
		writeAPIError(w, 503, "comments_unavailable", "comments are unavailable")
		return
	}
	id := commentActionID(r.URL.Path, "batch")
	if id == "" {
		writeAPIError(w, 404, "comment_not_found", "comment not found")
		return
	}
	var in struct {
		InBatch *bool `json:"inBatch"`
	}
	if !decodeComment(w, r, &in) {
		return
	}
	if in.InBatch == nil {
		writeAPIError(w, 400, "invalid_request", "inBatch must be a boolean")
		return
	}
	c, err := s.comments.SetInBatch(r.Context(), id, *in.InBatch)
	if errors.Is(err, comments.ErrNotFound) {
		writeAPIError(w, 404, "comment_not_found", "comment not found")
		return
	}
	if errors.Is(err, comments.ErrInvalidState) {
		writeAPIError(w, 409, "invalid_state", "only drafts can join an existing unsent batch")
		return
	}
	if err != nil {
		writeAPIError(w, 500, "internal_error", "could not update batch membership")
		return
	}
	writeJSON(w, 200, c)
}

func decodeComment(w http.ResponseWriter, r *http.Request, out any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxCommentBody)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(out); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			writeAPIError(w, 413, "body_too_large", "request body exceeds 64 KiB")
		} else {
			writeAPIError(w, 400, "invalid_json", "request body must be a valid comment JSON object")
		}
		return false
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		writeAPIError(w, 400, "invalid_json", "request body must contain one JSON object")
		return false
	}
	return true
}

// Missing Origin is allowed for non-browser clients. When present, the origin
// must be a valid HTTP(S) origin whose host exactly matches Request.Host. This
// supports Tailscale hostnames while blocking cross-host browser mutations.
func sameOrigin(w http.ResponseWriter, r *http.Request) bool {
	raw := r.Header.Get("Origin")
	if raw == "" {
		return true
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || !strings.EqualFold(u.Host, r.Host) {
		writeAPIError(w, 403, "origin_mismatch", "Origin must match request host")
		return false
	}
	return true
}

// commentActionID extracts the 32-hex comment id from a path shaped like
// /__gust/comments/<id>/<action>. It returns "" when the path does not match.
func commentActionID(path, action string) string {
	const prefix = "/__gust/comments/"
	suffix := "/" + action
	if !strings.HasPrefix(path, prefix) || !strings.HasSuffix(path, suffix) {
		return ""
	}
	id := strings.TrimSuffix(strings.TrimPrefix(path, prefix), suffix)
	if len(id) != 32 || strings.Trim(id, "0123456789abcdef") != "" {
		return ""
	}
	return id
}

// validCommentText applies the shared comment/reply text rules: non-empty,
// at most 8192 bytes, and free of control characters.
func validCommentText(text string) bool {
	return strings.TrimSpace(text) != "" && len(text) <= 8192 && !hasControl(text)
}

func validPath(path string) bool {
	if len(path) == 0 || len(path) > 4096 || path[0] != '/' || strings.ContainsAny(path, "?#") {
		return false
	}
	for _, r := range path {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

// hasControl reports whether s contains a control character other than the
// newlines and tabs that are valid inside a multi-line comment.
func hasControl(s string) bool {
	for _, r := range s {
		if r == '\n' || r == '\r' || r == '\t' {
			continue
		}
		if unicode.IsControl(r) {
			return true
		}
	}
	return false
}

func scrubCommentHTML(html string) string {
	html = inlineScript.ReplaceAllString(html, "")
	html = onAttribute.ReplaceAllString(html, "")
	html = inputValue.ReplaceAllString(html, "$1")
	return dataURL.ReplaceAllString(html, `${1}"[removed data URL]"`)
}
