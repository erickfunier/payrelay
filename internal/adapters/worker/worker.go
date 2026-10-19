package worker

import (
	"context"
	"log/slog"
	"time"
)

type Dispatcher interface {
	DispatchOne(context.Context) (bool, error)
}

// Run polls an application dispatcher from a process boundary. Scheduling and
// logging are adapter concerns; the core remains transport-agnostic.
func Run(ctx context.Context, dispatcher Dispatcher, interval time.Duration, logger *slog.Logger) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		for {
			found, err := dispatcher.DispatchOne(ctx)
			if err != nil {
				logger.Error("delivery dispatch failed", "error", err)
				break
			}
			if !found {
				break
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
