package githubapi

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func testClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	c := New("PedroTessaro", "", time.Minute, slog.New(slog.NewTextHandler(io.Discard, nil)))
	c.baseURL = srv.URL
	return c
}

func TestFetchIgnoresForksAndSumsStars(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `[
            {"fork": false, "stargazers_count": 5},
            {"fork": true,  "stargazers_count": 900},
            {"fork": false, "stargazers_count": 4}
        ]`)
	})

	stats, err := c.fetch(context.Background())
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if stats.Repos != 2 {
		t.Errorf("repos = %d, want 2 with the fork skipped", stats.Repos)
	}
	if stats.Stars != 9 {
		t.Errorf("stars = %d, want 9", stats.Stars)
	}
}

func TestFetchPaginates(t *testing.T) {
	var calls atomic.Int32

	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			// A full page means the client should ask for the next one.
			w.Write([]byte("["))
			for i := 0; i < 100; i++ {
				if i > 0 {
					w.Write([]byte(","))
				}
				fmt.Fprint(w, `{"fork":false,"stargazers_count":1}`)
			}
			w.Write([]byte("]"))
			return
		}
		fmt.Fprint(w, `[{"fork":false,"stargazers_count":1}]`)
	})

	stats, err := c.fetch(context.Background())
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if stats.Repos != 101 {
		t.Errorf("repos = %d, want 101 across two pages", stats.Repos)
	}
	if got := calls.Load(); got != 2 {
		t.Errorf("calls = %d, want 2", got)
	}
}

func TestStatsBeforeFirstRefresh(t *testing.T) {
	c := New("PedroTessaro", "", time.Minute, slog.New(slog.NewTextHandler(io.Discard, nil)))

	stats := c.Stats()
	if !stats.Stale {
		t.Error("a snapshot with no refresh yet should be marked stale")
	}
	if stats.Repos != 0 {
		t.Errorf("repos = %d, want 0", stats.Repos)
	}
}

// If GitHub goes down the README should keep showing the last good numbers
// rather than zeros.
func TestStatsKeepsLastGoodValueOnFailure(t *testing.T) {
	var fail atomic.Bool

	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if fail.Load() {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		fmt.Fprint(w, `[{"fork":false,"stargazers_count":7}]`)
	})

	ctx := context.Background()
	stats, err := c.fetch(ctx)
	if err != nil {
		t.Fatalf("initial fetch: %v", err)
	}
	c.mu.Lock()
	c.cached, c.valid = stats, true
	c.mu.Unlock()

	fail.Store(true)
	if _, err := c.fetch(ctx); err == nil {
		t.Fatal("expected an error while the API is down")
	}

	if got := c.Stats(); got.Stars != 7 {
		t.Errorf("stars = %d, want the last good value (7) to survive", got.Stars)
	}
}

func TestRateLimitProducesClearError(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-RateLimit-Remaining", "0")
		w.Header().Set("X-RateLimit-Reset", "1757520000")
		w.WriteHeader(http.StatusForbidden)
	})

	_, err := c.fetch(context.Background())
	if err == nil {
		t.Fatal("expected a rate limit error")
	}
	if !strings.Contains(err.Error(), "rate limit") {
		t.Errorf("error = %q, want it to mention the rate limit", err)
	}
}

func TestCommitsRequireToken(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `[{"fork":false,"stargazers_count":1}]`)
	})

	stats, err := c.fetch(context.Background())
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if stats.HasCommits {
		t.Error("HasCommits should be false without a token")
	}
}

func TestStartRefreshesInBackground(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `[{"fork":false,"stargazers_count":3}]`)
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c.Start(ctx)

	deadline := time.After(5 * time.Second)
	for {
		if c.Stats().Stars == 3 {
			return
		}
		select {
		case <-deadline:
			t.Fatal("background goroutine never populated the cache")
		case <-time.After(10 * time.Millisecond):
		}
	}
}
