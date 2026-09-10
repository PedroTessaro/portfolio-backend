// Package app wires the pieces together, so the serverless entry point and the
// local server share exactly one construction path.
package app

import (
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/PedroTessaro/portfolio-backend/internal/config"
	"github.com/PedroTessaro/portfolio-backend/internal/githubapi"
	"github.com/PedroTessaro/portfolio-backend/internal/httpapi"
	"github.com/PedroTessaro/portfolio-backend/internal/store"
)

// statsTTL is how long the GitHub numbers are reused. Only the request that
// finds the cache empty pays for the API call, which keeps the service well
// inside the unauthenticated rate limit of 60 requests an hour.
const statsTTL = 30 * time.Minute

func Build(version string) (http.Handler, error) {
	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	cfg, err := config.Load(os.Getenv("CONFIG_PATH"))
	if err != nil {
		return nil, err
	}

	// Absent credentials disable the counter rather than failing the boot: the
	// SVG is still worth serving without it.
	kv := store.FromEnv()

	// Assigned through the interface only when real, so a nil *Store never
	// reaches githubapi wrapped in a non-nil interface value.
	var cache githubapi.Cache
	if kv.Enabled() {
		cache = kv
	} else {
		log.Warn("no KV credentials, README view counter disabled")
	}

	gh := githubapi.New(cfg.Identity.GitHubUser, os.Getenv("GITHUB_TOKEN"), cfg.Projects, statsTTL, cache, log)

	return httpapi.New(httpapi.Options{
		Config:       cfg,
		GitHub:       gh,
		Store:        kv,
		Logger:       log,
		Region:       region(),
		Version:      version,
		PublishToken: os.Getenv("CI_PUBLISH_TOKEN"),
	}).Routes(), nil
}

func region() string {
	if r := os.Getenv("VERCEL_REGION"); r != "" {
		return r
	}
	return "local"
}
