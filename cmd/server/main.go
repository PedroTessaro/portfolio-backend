package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/PedroTessaro/portfolio-backend/internal/config"
	"github.com/PedroTessaro/portfolio-backend/internal/githubapi"
	"github.com/PedroTessaro/portfolio-backend/internal/httpapi"
	"github.com/PedroTessaro/portfolio-backend/internal/store"
)

// Overridden at build time: -ldflags "-X main.version=$(git rev-parse --short HEAD)"
var version = "dev"

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	if err := run(log); err != nil {
		log.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger) error {
	cfg, err := config.Load(env("CONFIG_PATH", "config.yaml"))
	if err != nil {
		return err
	}

	dbPath := env("DB_PATH", "data/portfolio.db")
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		return err
	}
	db, err := store.Open(dbPath)
	if err != nil {
		return err
	}
	defer db.Close()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Loose enough to stay inside the unauthenticated rate limit (60/hour) even
	// with the README being opened constantly.
	gh := githubapi.New(cfg.Identity.GitHubUser, os.Getenv("GITHUB_TOKEN"), 15*time.Minute, log)
	gh.Start(ctx)

	srv := &http.Server{
		Addr: ":" + env("PORT", "8080"),
		Handler: httpapi.New(httpapi.Options{
			Config:  cfg,
			GitHub:  gh,
			Store:   db,
			Logger:  log,
			Region:  env("FLY_REGION", "local"),
			Version: version,
		}).Routes(),
		ReadHeaderTimeout: 5 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		log.Info("listening", "addr", srv.Addr, "version", version, "db", dbPath)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		// Fly sends SIGTERM when recycling the machine: drain in-flight
		// requests, then let the deferred Close flush SQLite.
		log.Info("shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	}
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
