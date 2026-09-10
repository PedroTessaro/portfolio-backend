package httpapi

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/PedroTessaro/portfolio-backend/internal/config"
	"github.com/PedroTessaro/portfolio-backend/internal/githubapi"
	"github.com/PedroTessaro/portfolio-backend/internal/store"
)

func newTestServer(t *testing.T) http.Handler {
	t.Helper()

	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	// The client is never started, so Stats() returns the empty snapshot and we
	// exercise the path where GitHub hasn't answered yet.
	return New(Options{
		Config: &config.Config{
			Identity: config.Identity{Name: "Pedro Tessaro", Role: "Backend Engineer", Location: "Brazil", GitHubUser: "PedroTessaro"},
			Stack:    []string{"Go", "Java"},
			Terminal: config.Terminal{Host: "example.fly.dev", User: "tessaro", Machine: "portfolio"},
		},
		GitHub:  githubapi.New("PedroTessaro", "", time.Minute, log),
		Store:   db,
		Logger:  log,
		Region:  "gru",
		Version: "test",
	}).Routes()
}

func get(t *testing.T, h http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

func TestTerminalSVG(t *testing.T) {
	rec := get(t, newTestServer(t), "/terminal.svg")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "image/svg+xml") {
		t.Errorf("Content-Type = %q", ct)
	}
	if !strings.Contains(rec.Body.String(), "<svg") {
		t.Error("body does not look like an SVG")
	}
}

// Without these headers Camo freezes the image and the numbers stop moving.
func TestTerminalIsNotCacheable(t *testing.T) {
	cc := get(t, newTestServer(t), "/terminal.svg").Header().Get("Cache-Control")

	for _, want := range []string{"no-cache", "no-store", "max-age=0"} {
		if !strings.Contains(cc, want) {
			t.Errorf("Cache-Control %q is missing %q", cc, want)
		}
	}
}

// A broken SVG is worse than stale numbers.
func TestTerminalWorksWithoutGitHubData(t *testing.T) {
	rec := get(t, newTestServer(t), "/terminal.svg")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d with no GitHub data", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "Backend Engineer") {
		t.Error("config-sourced content should still render")
	}
}

func TestTerminalCountsViews(t *testing.T) {
	h := newTestServer(t)
	for i := 0; i < 3; i++ {
		get(t, h, "/terminal.svg")
	}

	var payload struct {
		ReadmeViews struct{ Total int } `json:"readme_views"`
	}
	if err := json.Unmarshal(get(t, h, "/whoami").Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode whoami: %v", err)
	}
	if payload.ReadmeViews.Total != 3 {
		t.Errorf("total views = %d, want 3", payload.ReadmeViews.Total)
	}
}

func TestStaticModeSkipsAnimation(t *testing.T) {
	if body := get(t, newTestServer(t), "/terminal.svg?static=1").Body.String(); strings.Contains(body, "<animate") {
		t.Error("?static=1 should not emit animation")
	}
}

func TestThemeParam(t *testing.T) {
	const lightBG = "#e1e2e7"

	if body := get(t, newTestServer(t), "/terminal.svg?theme=light").Body.String(); !strings.Contains(body, lightBG) {
		t.Error("light theme not applied")
	}
	if body := get(t, newTestServer(t), "/terminal.svg").Body.String(); strings.Contains(body, lightBG) {
		t.Error("dark should be the default")
	}
}

func TestWhoamiIsValidJSON(t *testing.T) {
	rec := get(t, newTestServer(t), "/whoami")

	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type = %q", ct)
	}

	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	for _, key := range []string{"name", "role", "stack", "github", "readme_views", "server"} {
		if _, ok := payload[key]; !ok {
			t.Errorf("missing key %q", key)
		}
	}
}

func TestHealthz(t *testing.T) {
	rec := get(t, newTestServer(t), "/healthz")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"status":"ok"`) {
		t.Errorf("body = %q", rec.Body.String())
	}
}

func TestMetricsExposesPrometheusFormat(t *testing.T) {
	body := get(t, newTestServer(t), "/metrics").Body.String()

	for _, want := range []string{
		"# HELP portfolio_uptime_seconds",
		"# TYPE portfolio_requests_total counter",
		"portfolio_readme_views_total",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("metrics missing %q", want)
		}
	}
}

func TestRootRedirectsToTerminal(t *testing.T) {
	rec := get(t, newTestServer(t), "/")
	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/terminal.svg" {
		t.Errorf("Location = %q", loc)
	}
}
