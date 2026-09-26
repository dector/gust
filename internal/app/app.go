package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/dector/gust/internal/cli"
	"github.com/dector/gust/internal/comments"
	"github.com/dector/gust/internal/coordinator"
	"github.com/dector/gust/internal/ctl"
	"github.com/dector/gust/internal/exposure"
	"github.com/dector/gust/internal/logger"
	"github.com/dector/gust/internal/man"
	"github.com/dector/gust/internal/proxy"
	"github.com/dector/gust/internal/skill"
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
	if len(args) > 0 && args[0] == "skill" {
		return skill.Run(args[1:], os.Stdout, os.Stderr)
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
	var expose *exposure.Manager
	if cfg.Tailscale {
		expose = exposure.New(cfg.Root, cfg.ExposurePort(), log)
		defer expose.Close()
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
	var tailscaleURL string
	var tailscaleURLReady <-chan string
	if expose != nil {
		tailscaleURLReady = expose.StartReady()
	}

	var commentStore *comments.Store
	if cfg.CommentsEnabled {
		// Comments persist across restarts. dev:self keeps them on tmpfs so its
		// own rebuilds do not lose them; normal runs use the durable state dir.
		pathFunc := comments.DurablePath
		if cfg.SelfDev {
			pathFunc = comments.SelfDevPath
		}
		path, pathErr := pathFunc(cfg.Root)
		if pathErr != nil {
			return pathErr
		}
		commentStore, err = comments.OpenAt(path)
		if err != nil {
			return err
		}
		defer commentStore.Close()
		if proxyServer != nil {
			proxyServer.SetCommentStore(commentStore)
		}
	}
	var socketServer *socket.Server
	if commentStore != nil {
		socketServer, err = socket.Start(serviceCtx, cfg, log, coord, commentStore)
	} else {
		socketServer, err = socket.Start(serviceCtx, cfg, log, coord)
	}
	if err != nil {
		return err
	}
	defer socketServer.Close()

	if tailscaleURLReady != nil {
		select {
		case tailscaleURL = <-tailscaleURLReady:
			socketServer.SetTailscaleURL(tailscaleURL)
		case <-runCtx.Done():
		}
	}
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
		TailscaleURL: tailscaleURL,
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
