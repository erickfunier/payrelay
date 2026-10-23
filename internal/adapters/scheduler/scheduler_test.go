package scheduler

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"
)

func TestRun_GivenCancelledContext_WhenPolling_ThenStopsCleanly(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	Run(ctx, fakeDispatcher{}, time.Millisecond, slog.New(slog.NewTextHandler(io.Discard, nil)))
	Run(ctx, fakeDispatcher{err: errors.New("store unavailable")}, time.Millisecond, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

type fakeDispatcher struct{ err error }

func (f fakeDispatcher) DispatchOne(context.Context) (bool, error) { return false, f.err }
