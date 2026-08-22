package main

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/dector/gust/internal/cli"
	"github.com/dector/gust/internal/coordinator"
	"github.com/dector/gust/internal/logger"
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
	if err := coord.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		return err
	}
	return nil
}
