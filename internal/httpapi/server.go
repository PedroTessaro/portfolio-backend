// Package httpapi serves the SVG and its supporting endpoints.
package httpapi

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/PedroTessaro/portfolio-backend/internal/config"
	"github.com/PedroTessaro/portfolio-backend/internal/githubapi"
	"github.com/PedroTessaro/portfolio-backend/internal/store"
	"github.com/PedroTessaro/portfolio-backend/internal/svgterm"
)

type Server struct {
	cfg     *config.Config
	github  *githubapi.Client
	store   *store.Store
	log     *slog.Logger
	started time.Time
	region  string
	version string

	requests atomic.Int64
}

type Options struct {
	Config  *config.Config
	GitHub  *githubapi.Client
	Store   *store.Store
	Logger  *slog.Logger
	Region  string
	Version string
}

func New(opts Options) *Server {
	return &Server{
		cfg:     opts.Config,
		github:  opts.GitHub,
		store:   opts.Store,
		log:     opts.Logger,
		started: time.Now(),
		region:  opts.Region,
		version: opts.Version,
	}
}

func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /terminal.svg", s.handleTerminal)
	mux.HandleFunc("GET /whoami", s.handleWhoami)
	mux.HandleFunc("GET /healthz", s.handleHealth)
	mux.HandleFunc("GET /metrics", s.handleMetrics)
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/terminal.svg", http.StatusFound)
	})
	return s.withLogging(mux)
}

func (s *Server) withLogging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		s.requests.Add(1)
		next.ServeHTTP(w, r)
		s.log.Info("request",
			"method", r.Method,
			"path", r.URL.Path,
			"duration", time.Since(start).String(),
			"ua", r.UserAgent(),
		)
	})
}

func (s *Server) handleTerminal(w http.ResponseWriter, r *http.Request) {
	start := time.Now()

	// A broken counter must not take the README down with it.
	views, err := s.store.Hit(start)
	if err != nil {
		s.log.Error("recording visit", "err", err)
	}

	data := svgterm.Data{
		Cfg:      s.cfg,
		Stats:    s.github.Stats(),
		Views:    views,
		Region:   s.region,
		Uptime:   time.Since(s.started),
		ServedIn: time.Since(start),
		Static:   r.URL.Query().Has("static"),
	}
	svg := svgterm.Render(data, svgterm.ThemeByName(r.URL.Query().Get("theme")))

	w.Header().Set("Content-Type", "image/svg+xml; charset=utf-8")
	// Camo caches hard. Without these the image freezes and the numbers stop
	// meaning anything.
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate, max-age=0")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("Expires", "0")
	w.Header().Set("X-Served-By", s.region)

	if _, err := w.Write(svg); err != nil {
		s.log.Error("writing svg", "err", err)
	}
}

// handleWhoami answers the request the terminal claims to make, so the command
// on screen is actually reproducible by anyone who copies it.
func (s *Server) handleWhoami(w http.ResponseWriter, _ *http.Request) {
	start := time.Now()
	stats := s.github.Stats()

	views, err := s.store.Snapshot(start)
	if err != nil {
		s.log.Error("reading counter", "err", err)
	}

	payload := map[string]any{
		"name":     s.cfg.Identity.Name,
		"role":     s.cfg.Identity.Role,
		"location": s.cfg.Identity.Location,
		"stack":    s.cfg.Stack,
		"github": map[string]any{
			"user":              s.cfg.Identity.GitHubUser,
			"repos":             stats.Repos,
			"stars":             stats.Stars,
			"commits_this_year": stats.Commits,
			"cached":            stats.Stale,
		},
		"readme_views": map[string]int{"total": views.Total, "today": views.Today},
		"server": map[string]any{
			"region":    s.region,
			"version":   s.version,
			"uptime":    time.Since(s.started).Round(time.Second).String(),
			"served_in": time.Since(start).Round(time.Microsecond).String(),
		},
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(payload); err != nil {
		s.log.Error("writing whoami", "err", err)
	}
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"status":"ok","uptime":%q,"version":%q}`+"\n",
		time.Since(s.started).Round(time.Second), s.version)
}

// handleMetrics speaks the Prometheus exposition format, so a real scraper can
// pick this up unmodified.
func (s *Server) handleMetrics(w http.ResponseWriter, _ *http.Request) {
	stats := s.github.Stats()
	views, err := s.store.Snapshot(time.Now())
	if err != nil {
		s.log.Error("reading counter", "err", err)
	}

	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	for _, m := range []struct {
		name, help, kind string
		value            int64
	}{
		{"portfolio_uptime_seconds", "Seconds since process start.", "gauge", int64(time.Since(s.started).Seconds())},
		{"portfolio_requests_total", "HTTP requests served.", "counter", s.requests.Load()},
		{"portfolio_readme_views_total", "README SVG renders.", "counter", int64(views.Total)},
		{"portfolio_readme_views_today", "README SVG renders today, UTC.", "gauge", int64(views.Today)},
		{"portfolio_github_repos", "Own public repositories.", "gauge", int64(stats.Repos)},
		{"portfolio_github_stars", "Stars across own repositories.", "gauge", int64(stats.Stars)},
	} {
		fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s %s\n%s %d\n", m.name, m.help, m.name, m.kind, m.name, m.value)
	}
}
