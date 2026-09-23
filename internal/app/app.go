package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/dector/gust/internal/cli"
	"github.com/dector/gust/internal/coordinator"
	"github.com/dector/gust/internal/ctl"
	"github.com/dector/gust/internal/exposure"
	"github.com/dector/gust/internal/logger"
	"github.com/dector/gust/internal/man"
	"github.com/dector/gust/internal/proxy"
	"github.com/dector/gust/internal/socket"
	"github.com/dector/gust/internal/term"
)

// Main runs Gust or the ctl client and exits the process with a status code.
func Main() {
	os.Exit(MainArgs(os.Args[1:]))
}

// MainArgs dispatches between the control client and the Gust server.
func MainArgs(args []string) int {
	if len(args) > 0 && args[0] == "ctl" {
		return ctl.Run(context.Background(), args[1:], os.Stdout, os.Stderr)
	}
	if len(args) > 0 && args[0] == "man" {
		return man.Run(args[1:], os.Stdout, os.Stderr)
	}
	if err := Run(context.Background(), args); err != nil {
		fmt.Fprintf(os.Stderr, "[gust] fatal: %v\n", err)
		return 1
	}
	return 0
}

// Run starts Gust with the provided context and command-line arguments.
func Run(ctx context.Context, args []string) error {
	cfg, err := cli.Parse(args)
	if err != nil {
		return err
	}

	log := logger.New(os.Stderr, cfg.Verbose)
	coord := coordinator.New(cfg, log)
	if cfg.Tailscale {
		expose := exposure.New(cfg.Root, cfg.ExposurePort(), log)
		defer expose.Close()
		coord.SetReadyHook(expose.StartReady)
	}

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
		proxyServer.SetStatusProvider(coord.Status)
	}

	socketServer, err := socket.Start(serviceCtx, cfg, log, coord)
	if err != nil {
		return err
	}
	defer socketServer.Close()

	keysEnabled := term.IsTerminal(os.Stdin)
	var toggleDebug func()
	if proxyServer != nil {
		toggleDebug = func() {
			if proxyServer.BrowserHub().ToggleDebug() {
				log.Printf("debug lines enabled")
			} else {
				log.Printf("debug lines disabled")
			}
		}
	}
	termCtl := term.New(os.Stdin, log,
		func() { coord.Trigger(coordinator.TriggerManual, "keyboard") },
		func() { coord.ToggleAutoReload() },
		toggleDebug,
		func() { coord.ToggleInfo() },
		func() {
			coord.Shutdown()
			cancel()
		},
	)
	log.PrintStartup(logger.StartupConfig{
		Exec:         cfg.Exec,
		Before:       cfg.Before,
		After:        cfg.After,
		HasAppPort:   cfg.HasAppPort,
		AppPort:      cfg.AppPort,
		ProxyEnabled: cfg.ProxyEnabled,
		ProxyPort:    cfg.ProxyPort,
		HealthPath:   cfg.HealthPath,
	}, socketServer.Path(), keysEnabled)

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
