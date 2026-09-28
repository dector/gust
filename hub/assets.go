package main

import (
	"embed"
	"net/http"

	"github.com/labstack/echo/v5"
)

//go:embed assets/datastar-v1.0.2.js
var assetFiles embed.FS

func handleDatastarAsset(c *echo.Context) error {
	data, err := assetFiles.ReadFile("assets/datastar-v1.0.2.js")
	if err != nil {
		return echo.NewHTTPError(http.StatusNotFound, "Asset not found")
	}
	return c.Blob(http.StatusOK, "text/javascript; charset=utf-8", data)
}
