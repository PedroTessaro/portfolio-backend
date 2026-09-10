// Package handler is the Vercel entry point. Every route is rewritten here by
// vercel.json, so the routing itself stays in internal/httpapi and the service
// is not welded to one platform.
package handler

import (
	"net/http"
	"os"
	"strings"
	"sync"

	"github.com/PedroTessaro/portfolio-backend/internal/app"
)

var (
	once     sync.Once
	routes   http.Handler
	buildErr error
)

func Handler(w http.ResponseWriter, r *http.Request) {
	// Built once per instance and reused while it stays warm.
	once.Do(func() {
		routes, buildErr = app.Build(version())
	})

	if buildErr != nil {
		http.Error(w, "service misconfigured: "+buildErr.Error(), http.StatusInternalServerError)
		return
	}

	// A rewritten request can arrive as /api/terminal.svg; the mux only knows
	// the public paths.
	if trimmed := strings.TrimPrefix(r.URL.Path, "/api"); trimmed != r.URL.Path {
		if trimmed == "" {
			trimmed = "/"
		}
		r.URL.Path = trimmed
	}

	routes.ServeHTTP(w, r)
}

// version reports the commit Vercel built from, which makes a deploy
// identifiable from /whoami without a build step of our own.
func version() string {
	if sha := os.Getenv("VERCEL_GIT_COMMIT_SHA"); len(sha) >= 7 {
		return sha[:7]
	}
	return "dev"
}
