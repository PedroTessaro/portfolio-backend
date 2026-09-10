package store

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// fakeRedis answers the pipeline endpoint, recording the commands it received.
func fakeRedis(t *testing.T, reply func(commands [][]string) []map[string]any) *Store {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
			t.Errorf("Authorization = %q", got)
		}

		body, _ := io.ReadAll(r.Body)
		var commands [][]string
		if err := json.Unmarshal(body, &commands); err != nil {
			t.Fatalf("decoding commands: %v", err)
		}
		json.NewEncoder(w).Encode(reply(commands))
	}))
	t.Cleanup(srv.Close)

	return &Store{baseURL: srv.URL, token: "test-token", http: srv.Client()}
}

func TestHitIncrementsBothCounters(t *testing.T) {
	var seen [][]string

	s := fakeRedis(t, func(commands [][]string) []map[string]any {
		seen = commands
		return []map[string]any{{"result": 42.0}, {"result": 7.0}}
	})

	views, err := s.Hit(context.Background(), time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("Hit: %v", err)
	}
	if views.Total != 42 || views.Today != 7 {
		t.Errorf("got total=%d today=%d, want 42 and 7", views.Total, views.Today)
	}

	if len(seen) != 2 {
		t.Fatalf("sent %d commands, want 2 in one pipeline", len(seen))
	}
	if seen[0][0] != "INCR" || seen[1][0] != "INCR" {
		t.Errorf("expected two INCRs, got %v", seen)
	}
	if seen[1][1] != "views:day:2026-09-10" {
		t.Errorf("day key = %q", seen[1][1])
	}
}

func TestSnapshotReadsWithoutWriting(t *testing.T) {
	var seen [][]string

	s := fakeRedis(t, func(commands [][]string) []map[string]any {
		seen = commands
		return []map[string]any{{"result": "1234"}, {"result": "37"}}
	})

	views, err := s.Snapshot(context.Background(), time.Now())
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	// GET returns strings where INCR returns numbers.
	if views.Total != 1234 || views.Today != 37 {
		t.Errorf("got total=%d today=%d, want 1234 and 37", views.Total, views.Today)
	}
	for _, cmd := range seen {
		if cmd[0] != "GET" {
			t.Errorf("Snapshot issued %q, it must not write", cmd[0])
		}
	}
}

// A day with no visits yet has no key at all.
func TestMissingKeysCountAsZero(t *testing.T) {
	s := fakeRedis(t, func([][]string) []map[string]any {
		return []map[string]any{{"result": nil}, {"result": nil}}
	})

	views, err := s.Snapshot(context.Background(), time.Now())
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if views.Total != 0 || views.Today != 0 {
		t.Errorf("got total=%d today=%d, want zeros", views.Total, views.Today)
	}
}

func TestCacheRoundTrip(t *testing.T) {
	var stored string

	s := fakeRedis(t, func(commands [][]string) []map[string]any {
		if commands[0][0] == "SET" {
			stored = commands[0][2]
			if commands[0][3] != "EX" || commands[0][4] != "900" {
				t.Errorf("expected a 900s expiry, got %v", commands[0][3:])
			}
			return []map[string]any{{"result": "OK"}}
		}
		return []map[string]any{{"result": stored}}
	})

	type payload struct {
		Repos int `json:"repos"`
	}
	ctx := context.Background()

	if err := s.SetCached(ctx, "k", payload{Repos: 27}, 15*time.Minute); err != nil {
		t.Fatalf("SetCached: %v", err)
	}

	var got payload
	found, err := s.GetCached(ctx, "k", &got)
	if err != nil {
		t.Fatalf("GetCached: %v", err)
	}
	if !found || got.Repos != 27 {
		t.Errorf("found=%v repos=%d, want true and 27", found, got.Repos)
	}
}

func TestCacheMissIsNotAnError(t *testing.T) {
	s := fakeRedis(t, func([][]string) []map[string]any {
		return []map[string]any{{"result": nil}}
	})

	var dst struct{}
	found, err := s.GetCached(context.Background(), "missing", &dst)
	if err != nil {
		t.Fatalf("a cache miss should not be an error: %v", err)
	}
	if found {
		t.Error("found = true for a key that does not exist")
	}
}

func TestRedisErrorSurfaces(t *testing.T) {
	s := fakeRedis(t, func([][]string) []map[string]any {
		return []map[string]any{{"error": "WRONGTYPE"}}
	})

	if _, err := s.Snapshot(context.Background(), time.Now()); err == nil {
		t.Error("expected the redis error to surface")
	}
}

// Without credentials every call has to degrade quietly, since the service is
// meant to run with the counter switched off.
func TestNilStoreIsSafe(t *testing.T) {
	var s *Store
	ctx := context.Background()

	if s.Enabled() {
		t.Error("a nil store should report itself disabled")
	}
	if _, err := s.Hit(ctx, time.Now()); err != nil {
		t.Errorf("Hit on nil store: %v", err)
	}
	if _, err := s.Snapshot(ctx, time.Now()); err != nil {
		t.Errorf("Snapshot on nil store: %v", err)
	}
	if err := s.SetCached(ctx, "k", 1, time.Minute); err != nil {
		t.Errorf("SetCached on nil store: %v", err)
	}
	var dst struct{}
	if found, err := s.GetCached(ctx, "k", &dst); found || err != nil {
		t.Errorf("GetCached on nil store = (%v, %v)", found, err)
	}
}

func TestFromEnvRequiresBothVariables(t *testing.T) {
	t.Setenv("KV_REST_API_URL", "https://example.upstash.io")
	t.Setenv("KV_REST_API_TOKEN", "")
	if FromEnv().Enabled() {
		t.Error("a missing token should leave the store disabled")
	}

	t.Setenv("KV_REST_API_TOKEN", "token")
	if !FromEnv().Enabled() {
		t.Error("both variables present should enable the store")
	}
}
