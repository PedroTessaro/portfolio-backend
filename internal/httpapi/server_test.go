package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/PedroTessaro/portfolio-backend/internal/config"
	"github.com/PedroTessaro/portfolio-backend/internal/githubapi"
	"github.com/PedroTessaro/portfolio-backend/internal/store"
)

// stubStats stands in for the GitHub client so the handlers never touch the
// network.
type stubStats struct{}

func (stubStats) Stats(context.Context) githubapi.Stats {
	return githubapi.Stats{
		Repos: 27, Stars: 9, FetchedAt: time.Now(),
		Projects: []githubapi.Project{
			{Name: "portfolio-backend", Language: "Go", Stars: 3, PushedAt: time.Now()},
		},
	}
}

// newTestServer builds a server with no counter configured, which is also the
// path a deploy without KV credentials takes.
func newTestServer(t *testing.T) http.Handler {
	t.Helper()
	return newServerWith(t, nil)
}

// newCountingServer backs the counter with a stub Redis that just increments.
func newCountingServer(t *testing.T) http.Handler {
	t.Helper()

	var total, today atomic.Int64
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var commands [][]string
		if err := json.NewDecoder(r.Body).Decode(&commands); err != nil {
			t.Errorf("decoding redis commands: %v", err)
			return
		}
		replies := make([]map[string]any, 0, len(commands))
		for _, cmd := range commands {
			counter := &total
			if strings.HasPrefix(cmd[1], "views:day:") {
				counter = &today
			}
			if cmd[0] == "INCR" {
				replies = append(replies, map[string]any{"result": float64(counter.Add(1))})
				continue
			}
			replies = append(replies, map[string]any{"result": float64(counter.Load())})
		}
		json.NewEncoder(w).Encode(replies)
	}))
	t.Cleanup(stub.Close)

	return newServerWith(t, store.New(stub.URL, "stub-token"))
}

func newServerWith(t *testing.T, kv *store.Store) http.Handler {
	t.Helper()

	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	return New(Options{
		Config: &config.Config{
			Identity: config.Identity{Name: "Pedro Tessaro", Role: "Backend Engineer", Location: "Brazil", GitHubUser: "PedroTessaro"},
			Stack:    []string{"Go", "Java"},
			Terminal: config.Terminal{Host: "example.fly.dev", User: "tessaro", Machine: "portfolio"},
		},
		GitHub:  stubStats{},
		Store:   kv,
		Logger:  log,
		Region:  "gru",
		Version: "test",
	}).Routes()
}

// newPublishingServer backs the store with a stub Redis that remembers the one
// key the CI writes.
func newPublishingServer(t *testing.T, token string) http.Handler {
	t.Helper()

	var stored string
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var commands [][]string
		json.NewDecoder(r.Body).Decode(&commands)

		replies := make([]map[string]any, 0, len(commands))
		for _, cmd := range commands {
			switch {
			case cmd[0] == "SET" && cmd[1] == "ci:status":
				stored = cmd[2]
				replies = append(replies, map[string]any{"result": "OK"})
			case cmd[0] == "GET" && cmd[1] == "ci:status":
				replies = append(replies, map[string]any{"result": stored})
			case cmd[0] == "LRANGE":
				replies = append(replies, map[string]any{"result": []any{}})
			default:
				replies = append(replies, map[string]any{"result": 1.0})
			}
		}
		json.NewEncoder(w).Encode(replies)
	}))
	t.Cleanup(stub.Close)

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	return New(Options{
		Config: &config.Config{
			Identity: config.Identity{Name: "Pedro Tessaro", Role: "Backend Engineer", GitHubUser: "PedroTessaro"},
			Stack:    []string{"Go"},
			Terminal: config.Terminal{Host: "example.dev", User: "tessaro", Machine: "portfolio"},
		},
		GitHub:       stubStats{},
		Store:        store.New(stub.URL, "stub-token"),
		Logger:       log,
		Region:       "gru",
		Version:      "test",
		PublishToken: token,
	}).Routes()
}

func post(t *testing.T, h http.Handler, path, auth, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
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
	h := newCountingServer(t)
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
	for _, key := range []string{"name", "role", "stack", "projects", "github", "server"} {
		if _, ok := payload[key]; !ok {
			t.Errorf("missing key %q", key)
		}
	}
	if _, ok := payload["readme_views"]; ok {
		t.Error("readme_views should be absent when no counter is configured")
	}
}

// Without KV the views line has to disappear rather than render zeros.
func TestViewsLineOmittedWithoutStore(t *testing.T) {
	if body := get(t, newTestServer(t), "/terminal.svg").Body.String(); strings.Contains(body, "readme views") {
		t.Error("views line should be dropped when the counter is disabled")
	}
	if body := get(t, newCountingServer(t), "/terminal.svg").Body.String(); !strings.Contains(body, "readme views") {
		t.Error("views line should render when the counter is configured")
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
		"# HELP portfolio_github_repos",
		"# TYPE portfolio_requests_total counter",
		"portfolio_github_cache_age_seconds",
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

// The terminal lists projects, so the JSON behind it has to carry them too.
func TestWhoamiCarriesProjects(t *testing.T) {
	var payload struct {
		Projects []struct {
			Name     string `json:"name"`
			Language string `json:"language"`
		} `json:"projects"`
	}
	if err := json.Unmarshal(get(t, newTestServer(t), "/whoami").Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode whoami: %v", err)
	}

	if len(payload.Projects) != 1 {
		t.Fatalf("got %d projects, want 1", len(payload.Projects))
	}
	if payload.Projects[0].Name != "portfolio-backend" || payload.Projects[0].Language != "Go" {
		t.Errorf("project = %+v", payload.Projects[0])
	}
}

// The publish endpoint is the only way anything writes into this service, so
// its refusals matter more than its successes.
func TestPublishCIRejectsBadTokens(t *testing.T) {
	h := newPublishingServer(t, "right-token")

	for name, header := range map[string]string{
		"no header":     "",
		"wrong token":   "Bearer wrong-token",
		"missing":       "Bearer ",
		"not a bearer":  "right-token",
		"case mismatch": "Bearer RIGHT-TOKEN",
	} {
		t.Run(name, func(t *testing.T) {
			rec := post(t, h, "/internal/ci", header, `{"status":"passing","tests":10}`)
			if rec.Code != http.StatusUnauthorized {
				t.Errorf("status = %d, want 401", rec.Code)
			}
		})
	}
}

// With no token configured the endpoint must not be open, it must be closed.
func TestPublishCIDisabledWithoutToken(t *testing.T) {
	rec := post(t, newPublishingServer(t, ""), "/internal/ci", "Bearer anything", `{"status":"passing"}`)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", rec.Code)
	}
}

func TestPublishCIRejectsBadBodies(t *testing.T) {
	h := newPublishingServer(t, "right-token")

	for name, body := range map[string]string{
		"not json":      `{`,
		"empty status":  `{"tests":10}`,
		"wrong shape":   `["passing"]`,
		"empty payload": ``,
	} {
		t.Run(name, func(t *testing.T) {
			rec := post(t, h, "/internal/ci", "Bearer right-token", body)
			if rec.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want 400", rec.Code)
			}
		})
	}
}

func TestPublishCIStoresAndSurfaces(t *testing.T) {
	h := newPublishingServer(t, "right-token")

	rec := post(t, h, "/internal/ci", "Bearer right-token", `{"status":"passing","sha":"abc1234","tests":55,"coverage":83.4}`)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204: %s", rec.Code, rec.Body.String())
	}

	if body := get(t, h, "/terminal.svg").Body.String(); !strings.Contains(body, "passing") || !strings.Contains(body, "55") {
		t.Error("published CI status should show up in the SVG")
	}
}
