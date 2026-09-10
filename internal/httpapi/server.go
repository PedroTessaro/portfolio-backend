// Package httpapi serves the SVG and its supporting endpoints.
package httpapi

import (
	"context"
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

// StatsSource is the slice of the GitHub client this package needs. Narrowing
// it keeps the network out of the handler tests.
type StatsSource interface {
	Stats(ctx context.Context) githubapi.Stats
}

type Server struct {
	cfg     *config.Config
	github  StatsSource
	store   *store.Store
	log     *slog.Logger
	region  string
	version string

	requests atomic.Int64
}

type Options struct {
	Config  *config.Config
	GitHub  StatsSource
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

// firstRequest reports whether this instance had served nothing before now,
// which is the closest thing to "was this a cold start" available from inside.
func (s *Server) firstRequest() bool { return s.requests.Load() <= 1 }

func (s *Server) handleTerminal(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	cold := s.firstRequest()

	// A failing counter must not take the README down with it.
	views, err := s.store.Hit(r.Context(), start)
	if err != nil {
		s.log.Error("recording visit", "err", err)
	}

	data := svgterm.Data{
		Cfg:      s.cfg,
		Stats:    s.github.Stats(r.Context()),
		Views:    views,
		HasViews: s.store.Enabled(),
		Region:   s.region,
		Cold:     cold,
		ServedIn: time.Since(start),
		Static:   r.URL.Query().Has("static"),
	}
	svg := svgterm.Render(data, svgterm.ThemeByName(r.URL.Query().Get("theme")))

	w.Header().Set("Content-Type", "image/svg+xml; charset=utf-8")
	// Camo caches hard, and so does Vercel's own edge. Without these the image
	// freezes and the numbers stop meaning anything.
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate, max-age=0")
	w.Header().Set("CDN-Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("Expires", "0")
	w.Header().Set("X-Served-By", s.region)

	if _, err := w.Write(svg); err != nil {
		s.log.Error("writing svg", "err", err)
	}
}

// handleWhoami answers the request the terminal claims to make, so the command
// on screen is actually reproducible by anyone who copies it.
func (s *Server) handleWhoami(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	stats := s.github.Stats(r.Context())

	views, err := s.store.Snapshot(r.Context(), start)
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
			"cache_age":         stats.Age().Round(time.Second).String(),
			"stale":             stats.Stale,
		},
		"server": map[string]any{
			"region":    s.region,
			"version":   s.version,
			"cold":      s.firstRequest(),
			"served_in": time.Since(start).Round(time.Microsecond).String(),
		},
	}
	if s.store.Enabled() {
		payload["readme_views"] = map[string]int{"total": views.Total, "today": views.Today}
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
	fmt.Fprintf(w, `{"status":"ok","version":%q,"store":%t}`+"\n", s.version, s.store.Enabled())
}

type metric struct {
	name, help, kind string
	value            int64
}

// handleMetrics speaks the Prometheus exposition format, so a real scraper can
// pick this up unmodified.
func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	stats := s.github.Stats(r.Context())
	views, err := s.store.Snapshot(r.Context(), time.Now())
	if err != nil {
		s.log.Error("reading counter", "err", err)
	}

	metrics := []metric{
		{"portfolio_requests_total", "HTTP requests served by this instance.", "counter", s.requests.Load()},
		{"portfolio_github_repos", "Own public repositories.", "gauge", int64(stats.Repos)},
		{"portfolio_github_stars", "Stars across own repositories.", "gauge", int64(stats.Stars)},
		{"portfolio_github_cache_age_seconds", "Age of the cached GitHub numbers.", "gauge", int64(stats.Age().Seconds())},
	}
	if s.store.Enabled() {
		metrics = append(metrics,
			metric{"portfolio_readme_views_total", "README SVG renders.", "counter", int64(views.Total)},
			metric{"portfolio_readme_views_today", "README SVG renders today, UTC.", "gauge", int64(views.Today)},
		)
	}

	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	for _, m := range metrics {
		fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s %s\n%s %d\n", m.name, m.help, m.name, m.kind, m.name, m.value)
	}
}
