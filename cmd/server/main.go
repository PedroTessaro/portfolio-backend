// Command server is the whole service. Vercel detects the Go project, builds
// this binary and runs it behind its own router, passing PORT in — so the thing
// running in production is the same thing that runs locally, and the Dockerfile
// builds it too.
package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/PedroTessaro/portfolio-backend/internal/app"
)

// Set at build time by the Makefile; on Vercel it comes from the environment
// instead, see version().
var buildVersion = "dev"

func main() {
	routes, err := app.Build(version())
	if err != nil {
		log.Fatal(err)
	}

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	srv := &http.Server{
		Addr:              ":" + port,
		Handler:           routes,
		ReadHeaderTimeout: 5 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 1)
	go func() {
		log.Printf("listening on %s (version %s)", srv.Addr, version())
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		log.Fatal(err)
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			log.Fatal(err)
		}
	}
}

// version prefers the commit Vercel built from, which makes a deploy
// identifiable from /whoami without a build flag.
func version() string {
	if sha := os.Getenv("VERCEL_GIT_COMMIT_SHA"); len(sha) >= 7 {
		return sha[:7]
	}
	return buildVersion
}
