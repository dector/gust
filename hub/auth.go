package main

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/labstack/echo/v5"
)

const (
	authCookieName = "hub_auth"
	authCookieSalt = "hub-simple-auth-salt-v1"
)

func resolveAuthPassword() string {
	if password := os.Getenv("APP_PASSWORD"); password != "" {
		return password
	}
	if password := os.Getenv("AUTH_PASSWORD"); password != "" {
		return password
	}
	return "hub"
}

func simpleAuthMiddleware(password string) echo.MiddlewareFunc {
	expectedCookieValue := hashPasswordForCookie(password)

	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			path := c.Request().URL.Path
			if path == "/health" || path == "/login" {
				return next(c)
			}

			if hasValidAuthCookie(c, expectedCookieValue) {
				return next(c)
			}

			loginURL := "/login?next=" + url.QueryEscape(currentRequestPath(c.Request()))
			return c.Redirect(http.StatusSeeOther, loginURL)
		}
	}
}

func registerAuthRoutes(e *echo.Echo, password string) {
	expectedCookieValue := hashPasswordForCookie(password)

	e.GET("/login", func(c *echo.Context) error {
		nextPath := sanitizeNextPath(c.QueryParam("next"))
		if hasValidAuthCookie(c, expectedCookieValue) {
			return c.Redirect(http.StatusSeeOther, nextPath)
		}
		return c.HTML(http.StatusOK, loginPageHTML(nextPath, false))
	})

	e.POST("/login", func(c *echo.Context) error {
		nextPath := sanitizeNextPath(c.FormValue("next"))
		providedPassword := c.FormValue("password")
		if subtle.ConstantTimeCompare([]byte(providedPassword), []byte(password)) != 1 {
			return c.HTML(http.StatusUnauthorized, loginPageHTML(nextPath, true))
		}

		c.SetCookie(&http.Cookie{
			Name:     authCookieName,
			Value:    expectedCookieValue,
			Path:     "/",
			HttpOnly: true,
			SameSite: http.SameSiteLaxMode,
			Secure:   c.Scheme() == "https",
			MaxAge:   60 * 60 * 24 * 30,
		})

		return c.Redirect(http.StatusSeeOther, nextPath)
	})
}

func hasValidAuthCookie(c *echo.Context, expectedCookieValue string) bool {
	cookie, err := c.Cookie(authCookieName)
	if err != nil {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(cookie.Value), []byte(expectedCookieValue)) == 1
}

func hashPasswordForCookie(password string) string {
	sum := sha256.Sum256([]byte(authCookieSalt + ":" + password))
	return hex.EncodeToString(sum[:])
}

func currentRequestPath(req *http.Request) string {
	if req.URL.RawQuery == "" {
		return req.URL.Path
	}
	return req.URL.Path + "?" + req.URL.RawQuery
}

func sanitizeNextPath(nextPath string) string {
	if nextPath == "" {
		return "/"
	}
	if !strings.HasPrefix(nextPath, "/") || strings.HasPrefix(nextPath, "//") {
		return "/"
	}
	return nextPath
}

func loginPageHTML(nextPath string, invalidPassword bool) string {
	errorLine := ""
	if invalidPassword {
		errorLine = `<p class="error">Invalid password</p>`
	}

	nextEscaped := html.EscapeString(nextPath)
	return fmt.Sprintf(`<!doctype html>
<html lang="en">
  <head>
    <meta charset="UTF-8"/>
    <meta name="viewport" content="width=device-width, initial-scale=1.0"/>
    <title>Login</title>
    <style>
      :root {
        color-scheme: light dark;
        font-family: Inter, ui-sans-serif, system-ui, -apple-system, Segoe UI, Roboto, Helvetica, Arial, sans-serif;
      }
      * { box-sizing: border-box; }
      body {
        margin: 0;
        min-height: 100vh;
        display: grid;
        place-items: center;
        background: radial-gradient(circle at top left, #4f46e5 0%%, #0f172a 40%%, #020617 100%%);
        color: #e2e8f0;
        padding: 24px;
      }
      form {
        width: min(360px, 100%%);
        border-radius: 14px;
        padding: 24px;
        background: rgba(15, 23, 42, 0.72);
        border: 1px solid rgba(148, 163, 184, 0.25);
        display: grid;
        gap: 12px;
      }
      h1 {
        margin: 0 0 4px;
        font-size: 1.4rem;
      }
      input, button {
        width: 100%%;
        border-radius: 10px;
        border: 1px solid rgba(148, 163, 184, 0.35);
        padding: 10px 12px;
        font-size: 1rem;
      }
      button {
        cursor: pointer;
        background: #4338ca;
        color: white;
        border: 0;
      }
      .error {
        margin: 0;
        color: #fecaca;
      }
      .hint {
        margin: 0;
        color: #cbd5e1;
        font-size: 0.9rem;
      }
    </style>
  </head>
  <body>
    <form method="post" action="/login">
      <h1>Enter password</h1>
      <input type="hidden" name="next" value="%s"/>
      %s
      <input type="password" name="password" autocomplete="current-password" required autofocus/>
      <button type="submit">Continue</button>
      <p class="hint">Protected area</p>
    </form>
  </body>
</html>`, nextEscaped, errorLine)
}
