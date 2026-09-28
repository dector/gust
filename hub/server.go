package main

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/a-h/templ"
	"github.com/dector/gust/pkg/probe"
	"github.com/labstack/echo/v5"
	"github.com/labstack/echo/v5/middleware"
	"github.com/starfederation/datastar-go/datastar"

	"github.com/dector/gust/hub/views"
)

func newServer() *echo.Echo {
	return newServerWithGust(probe.Scan)
}

func newServerWithGust(scan gustScanner) *echo.Echo {
	e := echo.New()
	e.Use(middleware.RequestLogger())
	e.Use(middleware.Recover())

	authPassword := resolveAuthPassword()
	e.Use(simpleAuthMiddleware(authPassword))
	registerAuthRoutes(e, authPassword)

	e.GET("/health", func(c *echo.Context) error {
		return c.String(http.StatusOK, "ok")
	})

	homeDir, _ := os.UserHomeDir()
	e.GET("/", func(c *echo.Context) error {
		c.Response().Header().Set(echo.HeaderCacheControl, "no-store")
		instances, discoveryError := discoverPageInstances(c, scan, homeDir)
		return render(c, http.StatusOK, views.HomePage(instances, discoveryError))
	})
	e.GET("/instances", func(c *echo.Context) error {
		instances, discoveryError := discoverPageInstances(c, scan, homeDir)
		sse := datastar.NewSSE(noStoreWriter{ResponseWriter: c.Response()}, c.Request())
		return sse.PatchElementTempl(views.InstanceList(instances, discoveryError))
	})
	e.GET("/status", handleServerStatus)
	e.GET("/assets/datastar-v1.0.2.js", handleDatastarAsset)

	return e
}

// displayWorkdir keeps projects under the user's home readable as short titles.
func displayWorkdir(workdir, home string) string {
	if home == "" {
		return workdir
	}
	rel, err := filepath.Rel(home, workdir)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return workdir
	}
	if rel == "." {
		return filepath.Base(home)
	}
	return rel
}

func discoverPageInstances(c *echo.Context, scan gustScanner, homeDir string) ([]views.Instance, string) {
	found, discoveryError := loadGustInstances(c.Request().Context(), scan, localProbeClient())
	instances := make([]views.Instance, len(found))
	for i, instance := range found {
		instances[i] = views.Instance{Title: displayWorkdir(instance.Workdir, homeDir), Workdir: instance.Workdir, State: instance.State, TailscaleURL: instance.TailscaleURL, AppPort: instance.AppPort, ProxyPort: instance.ProxyPort, Status: instance.Status, Alive: instance.Alive}
	}
	return instances, discoveryError
}

func handleServerStatus(c *echo.Context) error {
	sse := datastar.NewSSE(noStoreWriter{ResponseWriter: c.Response()}, c.Request())
	if err := sse.PatchElementTempl(views.ServerStatusHealthy()); err != nil {
		return err
	}
	return nil
}

// noStoreWriter ensures Datastar's initial flush keeps the event stream private
// from caches and commits through Echo before flushing its response writer.
type noStoreWriter struct {
	http.ResponseWriter
}

func (w noStoreWriter) FlushError() error {
	w.ResponseWriter.Header().Set(echo.HeaderCacheControl, "no-store")
	if r, ok := w.ResponseWriter.(*echo.Response); ok && !r.Committed {
		r.WriteHeader(http.StatusOK)
	}
	return http.NewResponseController(w.ResponseWriter).Flush()
}

func render(c *echo.Context, status int, component templ.Component) error {
	resp := c.Response()
	resp.Header().Set(echo.HeaderContentType, echo.MIMETextHTMLCharsetUTF8)
	resp.WriteHeader(status)
	return component.Render(c.Request().Context(), resp)
}

func resolveHost() string {
	if host := os.Getenv("APP_HOST"); host != "" {
		return host
	}
	return "127.0.0.1"
}

func resolvePort() string {
	if port := os.Getenv("APP_PORT"); port != "" {
		return port
	}
	if port := os.Getenv("PORT"); port != "" {
		return port
	}
	return "8080"
}
