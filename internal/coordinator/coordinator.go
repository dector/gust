package coordinator

import (
	"context"

	"github.com/dector/gust/internal/config"
	"github.com/dector/gust/internal/logger"
)

// Coordinator owns the Gust runtime.
type Coordinator struct {
	cfg config.Config
	log *logger.Logger
}

// New constructs a Coordinator.
func New(cfg config.Config, log *logger.Logger) *Coordinator {
	return &Coordinator{cfg: cfg, log: log}
}

// Run starts the coordinator loop.
func (c *Coordinator) Run(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}
