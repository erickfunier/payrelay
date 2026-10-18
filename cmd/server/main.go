package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/example/payrelay/internal/api"
	"github.com/example/payrelay/internal/delivery"
	"github.com/example/payrelay/internal/store"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	dbPath := env("PAYRELAY_DB_PATH", "./payrelay.db")
	poll := durationEnv("PAYRELAY_WORKER_POLL", 500*time.Millisecond)
	maxAttempts := intEnv("PAYRELAY_MAX_ATTEMPTS", 5)
	timeout := durationEnv("PAYRELAY_REQUEST_TIMEOUT", 10*time.Second)

	repo, err := store.Open(dbPath)
	if err != nil {
		logger.Error("open database", "error", err)
		os.Exit(1)
	}
	defer repo.Close()

	dispatcher := delivery.NewDispatcher(repo, &http.Client{Timeout: timeout}, maxAttempts, logger)
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	go delivery.RunWorker(ctx, dispatcher, poll, logger)

	server := &http.Server{Addr: env("PAYRELAY_ADDR", ":8080"), Handler: api.NewServer(repo, dispatcher, logger), ReadHeaderTimeout: 5 * time.Second}
	go func() {
		<-ctx.Done()
		shutdown, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		_ = server.Shutdown(shutdown)
	}()
	logger.Info("payrelay started", "address", server.Addr)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		logger.Error("server failed", "error", err)
		os.Exit(1)
	}
}

func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
func durationEnv(key string, fallback time.Duration) time.Duration {
	value, err := time.ParseDuration(os.Getenv(key))
	if err == nil && value > 0 {
		return value
	}
	return fallback
}
func intEnv(key string, fallback int) int {
	value, err := strconv.Atoi(os.Getenv(key))
	if err == nil && value > 0 {
		return value
	}
	return fallback
}
