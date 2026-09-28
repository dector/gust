package main

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
)

func main() {
	e := newServer()
	host := resolveHost()
	port := resolvePort()
	address := fmt.Sprintf("%s:%s", host, port)

	slog.Info("starting server", "url", fmt.Sprintf("http://%s:%s", host, port), "bind", address)
	if err := e.Start(address); err != nil && !errors.Is(err, http.ErrServerClosed) {
		slog.Error("failed to start server", "error", err)
	}
}
