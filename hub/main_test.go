package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestHealthRouteWithoutAuth(t *testing.T) {
	t.Setenv("APP_PASSWORD", "test-password")
	e := newServer()

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rec.Code)
	}
	if rec.Body.String() != "ok" {
		t.Fatalf("expected body to be ok, got %q", rec.Body.String())
	}
}

func TestHomeRouteRequiresAuth(t *testing.T) {
	t.Setenv("APP_PASSWORD", "test-password")
	e := newServer()

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected status 303, got %d", rec.Code)
	}
	if location := rec.Header().Get("Location"); location != "/login?next=%2F" {
		t.Fatalf("expected redirect to login, got %q", location)
	}
}

func TestInstancesEndpointRequiresAuth(t *testing.T) {
	t.Setenv("APP_PASSWORD", "test-password")
	e := newServerWithGust(nil)
	req := httptest.NewRequest(http.MethodGet, "/instances", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/login?next=%2Finstances" {
		t.Fatalf("expected unauthenticated refresh redirect, got %d %q", rec.Code, rec.Header().Get("Location"))
	}
}

func TestInstancesEndpointSSEPatch(t *testing.T) {
	t.Setenv("APP_PASSWORD", "test-password")
	e := newServerWithGust(nil)
	req := httptest.NewRequest(http.MethodGet, "/instances", nil)
	req.AddCookie(&http.Cookie{Name: authCookieName, Value: hashPasswordForCookie("test-password")})
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); !strings.Contains(got, "text/event-stream") {
		t.Fatalf("expected SSE content type, got %q", got)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("expected no-store cache control, got %q", got)
	}
	if !strings.Contains(rec.Body.String(), "event: datastar-patch-elements") || !strings.Contains(rec.Body.String(), `id="instance-list"`) || !strings.Contains(rec.Body.String(), "Gust probe is unavailable.") {
		t.Fatalf("expected discovery patch for instance-list, got %q", rec.Body.String())
	}
}

func TestStatusAndAssetRequireAuth(t *testing.T) {
	t.Setenv("APP_PASSWORD", "test-password")
	e := newServer()

	for _, path := range []string{"/status", "/assets/datastar-v1.0.2.js"} {
		t.Run(path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, path, nil)
			rec := httptest.NewRecorder()
			e.ServeHTTP(rec, req)
			if rec.Code != http.StatusSeeOther {
				t.Fatalf("expected unauthorized request to redirect with 303, got %d", rec.Code)
			}
			want := "/login?next=" + url.QueryEscape(path)
			if got := rec.Header().Get("Location"); got != want {
				t.Fatalf("expected redirect to %q, got %q", want, got)
			}
		})
	}
}

func TestLoginFlowAllowsAccess(t *testing.T) {
	t.Setenv("APP_PASSWORD", "test-password")
	e := newServer()

	form := url.Values{}
	form.Set("password", "test-password")
	form.Set("next", "/")
	loginReq := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(form.Encode()))
	loginReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	loginRec := httptest.NewRecorder()
	e.ServeHTTP(loginRec, loginReq)

	if loginRec.Code != http.StatusSeeOther {
		t.Fatalf("expected login status 303, got %d", loginRec.Code)
	}
	cookies := loginRec.Result().Cookies()
	if len(cookies) == 0 {
		t.Fatalf("expected auth cookie to be set")
	}

	homeReq := httptest.NewRequest(http.MethodGet, "/", nil)
	homeReq.AddCookie(cookies[0])
	homeRec := httptest.NewRecorder()
	e.ServeHTTP(homeRec, homeReq)

	if homeRec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", homeRec.Code)
	}
	if got := homeRec.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("expected no-store index cache control, got %q", got)
	}
	if !strings.Contains(homeRec.Body.String(), "<h1>Hub</h1>") {
		t.Fatalf("expected response body to contain page heading, got %q", homeRec.Body.String())
	}
	if !strings.Contains(homeRec.Body.String(), "@get('/status')") {
		t.Fatalf("expected home page to include Datastar status action, got %q", homeRec.Body.String())
	}
	if !strings.Contains(homeRec.Body.String(), `data-on:click="@get('/instances')"`) || !strings.Contains(homeRec.Body.String(), `data-indicator:scanning`) {
		t.Fatalf("expected async rescan button, got %q", homeRec.Body.String())
	}

	statusReq := httptest.NewRequest(http.MethodGet, "/status", nil)
	statusReq.AddCookie(cookies[0])
	statusRec := httptest.NewRecorder()
	e.ServeHTTP(statusRec, statusReq)
	if statusRec.Code != http.StatusOK {
		t.Fatalf("expected status endpoint status 200, got %d: %s", statusRec.Code, statusRec.Body.String())
	}
	if got := statusRec.Header().Get("Content-Type"); !strings.Contains(got, "text/event-stream") {
		t.Fatalf("expected SSE content type, got %q", got)
	}
	if got := statusRec.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("expected no-store cache control, got %q", got)
	}
	if !strings.Contains(statusRec.Body.String(), "Server is healthy.") || !strings.Contains(statusRec.Body.String(), `id="server-status"`) {
		t.Fatalf("expected healthy status patch, got %q", statusRec.Body.String())
	}

	assetReq := httptest.NewRequest(http.MethodGet, "/assets/datastar-v1.0.2.js", nil)
	assetReq.AddCookie(cookies[0])
	assetRec := httptest.NewRecorder()
	e.ServeHTTP(assetRec, assetReq)
	if assetRec.Code != http.StatusOK || !strings.Contains(assetRec.Header().Get("Content-Type"), "javascript") {
		t.Fatalf("expected authenticated JavaScript asset, got status %d and content type %q", assetRec.Code, assetRec.Header().Get("Content-Type"))
	}
	if !strings.Contains(assetRec.Body.String(), "Datastar") {
		t.Fatalf("expected vendored Datastar script, got %q", assetRec.Body.String()[:min(len(assetRec.Body.String()), 200)])
	}
}
