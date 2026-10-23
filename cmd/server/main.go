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

	"github.com/example/payrelay/internal/adapters/httpapi"
	"github.com/example/payrelay/internal/adapters/scheduler"
	"github.com/example/payrelay/internal/adapters/sqlite"
	"github.com/example/payrelay/internal/adapters/webhook"
	"github.com/example/payrelay/internal/core/application"
	"github.com/example/payrelay/internal/security/ratelimit"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	dbPath := env("PAYRELAY_DB_PATH", "./payrelay.db")
	poll := durationEnv("PAYRELAY_WORKER_POLL", 500*time.Millisecond)
	maxAttempts := intEnv("PAYRELAY_MAX_ATTEMPTS", 5)
	circuitThreshold := intEnv("PAYRELAY_CIRCUIT_FAILURE_THRESHOLD", 5)
	circuitCooldown := durationEnv("PAYRELAY_CIRCUIT_COOLDOWN", time.Minute)
	timeout := durationEnv("PAYRELAY_REQUEST_TIMEOUT", 10*time.Second)
	rateLimit := intEnv("PAYRELAY_TENANT_RATE_LIMIT", 60)
	authenticator, err := httpapi.NewAPIKeyAuthenticator(os.Getenv("PAYRELAY_API_KEYS"))
	if err != nil {
		logger.Error("configure API authentication", "error", err)
		os.Exit(1)
	}

	repo, err := sqlite.Open(dbPath)
	if err != nil {
		logger.Error("open database", "error", err)
		os.Exit(1)
	}
	defer repo.Close()

	service := application.NewServiceWithCircuitPolicy(repo, webhook.NewHTTPSender(webhook.NewSafeHTTPClient(timeout)), maxAttempts, application.CircuitPolicy{FailureThreshold: circuitThreshold, Cooldown: circuitCooldown})
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	go scheduler.Run(ctx, service, poll, logger)

	limiter := ratelimit.NewTenantLimiter(rateLimit, time.Minute)
	server := &http.Server{Addr: env("PAYRELAY_ADDR", ":8080"), Handler: httpapi.NewServerWithRateLimiter(service, logger, authenticator, limiter), ReadHeaderTimeout: 5 * time.Second}
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
