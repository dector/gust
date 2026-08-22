package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/dector/gust/internal/cli"
	"github.com/dector/gust/internal/coordinator"
	"github.com/dector/gust/internal/logger"
	"github.com/dector/gust/internal/proxy"
	"github.com/dector/gust/internal/socket"
	"github.com/dector/gust/internal/term"
)

func main() {
	if err := run(context.Background(), os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "[gust] fatal: %v\n", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string) error {
	cfg, err := cli.Parse(args)
	if err != nil {
		return err
	}

	log := logger.New(os.Stderr, cfg.Verbose)
	coord := coordinator.New(cfg, log)

	runCtx, stopSignals := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer stopSignals()
	runCtx, cancel := context.WithCancel(runCtx)
	defer cancel()

	serviceCtx, stopServices := context.WithCancel(ctx)
	defer stopServices()

	proxyServer, err := proxy.Start(serviceCtx, cfg, log)
	if err != nil {
		return err
	}
	defer proxyServer.Close()
	if proxyServer != nil {
		coord.SetBrowserNotifier(proxyServer.BrowserHub())
	}

	socketServer, err := socket.Start(serviceCtx, cfg, log, coord)
	if err != nil {
		return err
	}
	defer socketServer.Close()

	termCtl := term.New(os.Stdin, log,
		func() { coord.Trigger(coordinator.TriggerManual, "keyboard") },
		func() {
			coord.Shutdown()
			cancel()
		},
	)
	if err := termCtl.Start(runCtx); err != nil {
		return err
	}
	defer termCtl.Restore()

	coord.SetShutdownHooks(coordinator.ShutdownHooks{
		StopSocketAccepts: socketServer.StopAccepting,
		CloseProxy:        proxyServer.Close,
		RestoreTerminal:   termCtl.Restore,
		RemoveSocket:      socketServer.Remove,
	})

	if err := coord.Run(runCtx); err != nil && !errors.Is(err, context.Canceled) {
		return err
	}
	return nil
}
