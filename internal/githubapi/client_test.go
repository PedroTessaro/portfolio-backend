package githubapi

import (
	"context"
	"encoding/json"
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

func discardLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func testClient(t *testing.T, cache Cache, handler http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	c := New("PedroTessaro", "", 15*time.Minute, cache, discardLogger())
	c.baseURL = srv.URL
	return c
}

// memCache stands in for Redis and counts reads and writes.
type memCache struct {
	entries map[string][]byte
	gets    atomic.Int32
	sets    atomic.Int32
}

func newMemCache() *memCache { return &memCache{entries: map[string][]byte{}} }

func (m *memCache) GetCached(_ context.Context, key string, dst any) (bool, error) {
	m.gets.Add(1)
	raw, ok := m.entries[key]
	if !ok {
		return false, nil
	}
	return true, json.Unmarshal(raw, dst)
}

func (m *memCache) SetCached(_ context.Context, key string, value any, _ time.Duration) error {
	m.sets.Add(1)
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	m.entries[key] = raw
	return nil
}

func TestFetchIgnoresForksAndSumsStars(t *testing.T) {
	c := testClient(t, nil, func(w http.ResponseWriter, r *http.Request) {
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

	c := testClient(t, nil, func(w http.ResponseWriter, r *http.Request) {
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

// The whole point of the memo: a warm instance must not call GitHub again.
func TestWarmInstanceSkipsTheAPI(t *testing.T) {
	var calls atomic.Int32

	c := testClient(t, nil, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		fmt.Fprint(w, `[{"fork":false,"stargazers_count":3}]`)
	})

	ctx := context.Background()
	for i := 0; i < 5; i++ {
		if got := c.Stats(ctx).Stars; got != 3 {
			t.Fatalf("stars = %d on call %d", got, i)
		}
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("GitHub called %d times, want 1", got)
	}
}

// A cold start should inherit what a previous invocation already fetched.
func TestColdStartReadsSharedCache(t *testing.T) {
	cache := newMemCache()
	var calls atomic.Int32

	handler := func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		fmt.Fprint(w, `[{"fork":false,"stargazers_count":11}]`)
	}

	ctx := context.Background()
	first := testClient(t, cache, handler)
	if got := first.Stats(ctx).Stars; got != 11 {
		t.Fatalf("stars = %d", got)
	}
	if cache.sets.Load() != 1 {
		t.Errorf("expected the fetch to populate the cache, sets = %d", cache.sets.Load())
	}

	// A brand new client stands in for a fresh instance.
	second := testClient(t, cache, handler)
	if got := second.Stats(ctx).Stars; got != 11 {
		t.Errorf("stars = %d from the shared cache", got)
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("GitHub called %d times, want 1 with the cache warm", got)
	}
}

func TestExpiredCacheIsRefetched(t *testing.T) {
	cache := newMemCache()
	stale := Stats{Repos: 1, Stars: 1, FetchedAt: time.Now().Add(-2 * time.Hour)}
	if err := cache.SetCached(context.Background(), cacheKey, stale, time.Minute); err != nil {
		t.Fatal(err)
	}

	c := testClient(t, cache, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `[{"fork":false,"stargazers_count":99}]`)
	})

	if got := c.Stats(context.Background()).Stars; got != 99 {
		t.Errorf("stars = %d, want the refetched value 99", got)
	}
}

// If GitHub is down the README should keep showing the last good numbers.
func TestFailureKeepsLastGoodValues(t *testing.T) {
	var fail atomic.Bool

	c := testClient(t, nil, func(w http.ResponseWriter, r *http.Request) {
		if fail.Load() {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		fmt.Fprint(w, `[{"fork":false,"stargazers_count":7}]`)
	})

	ctx := context.Background()
	if got := c.Stats(ctx).Stars; got != 7 {
		t.Fatalf("stars = %d on the first call", got)
	}

	// Expire the memo so the next call has to go out to the API.
	c.mu.Lock()
	c.memo.FetchedAt = time.Now().Add(-time.Hour)
	c.mu.Unlock()
	fail.Store(true)

	got := c.Stats(ctx)
	if got.Stars != 7 {
		t.Errorf("stars = %d, want the last good value 7", got.Stars)
	}
	if !got.Stale {
		t.Error("the fallback should be flagged stale")
	}
}

func TestRateLimitProducesClearError(t *testing.T) {
	c := testClient(t, nil, func(w http.ResponseWriter, r *http.Request) {
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
	c := testClient(t, nil, func(w http.ResponseWriter, r *http.Request) {
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

// A broken cache must not break the service; it just costs an API call.
func TestBrokenCacheStillServes(t *testing.T) {
	c := testClient(t, failingCache{}, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `[{"fork":false,"stargazers_count":5}]`)
	})

	if got := c.Stats(context.Background()).Stars; got != 5 {
		t.Errorf("stars = %d, want 5 despite the cache being down", got)
	}
}

type failingCache struct{}

func (failingCache) GetCached(context.Context, string, any) (bool, error) {
	return false, fmt.Errorf("cache unreachable")
}

func (failingCache) SetCached(context.Context, string, any, time.Duration) error {
	return fmt.Errorf("cache unreachable")
}
