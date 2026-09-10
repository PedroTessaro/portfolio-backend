package store

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"
)

// fakeRedis answers the pipeline endpoint from a per-command reply function.
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

// replyPerCommand gives each command a plausible answer so tests can focus on
// the one thing they care about.
func replyPerCommand(commands [][]string, overrides map[string]any) []map[string]any {
	replies := make([]map[string]any, 0, len(commands))
	for _, cmd := range commands {
		if v, ok := overrides[cmd[0]]; ok {
			replies = append(replies, map[string]any{"result": v})
			continue
		}
		switch cmd[0] {
		case "INCR":
			replies = append(replies, map[string]any{"result": 1.0})
		case "LRANGE":
			replies = append(replies, map[string]any{"result": []any{}})
		default:
			replies = append(replies, map[string]any{"result": nil})
		}
	}
	return replies
}

func TestRecordCountsAndReadsInOneRoundTrip(t *testing.T) {
	var trips int
	var seen [][]string

	s := fakeRedis(t, func(commands [][]string) []map[string]any {
		trips++
		seen = commands
		return replyPerCommand(commands, map[string]any{"INCR": 42.0})
	})

	snap, err := s.Record(context.Background(), time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC), 1500*time.Microsecond)
	if err != nil {
		t.Fatalf("Record: %v", err)
	}
	if trips != 1 {
		t.Errorf("made %d round trips, want 1 — the pipeline exists to avoid this", trips)
	}
	if snap.Views.Total != 42 {
		t.Errorf("total = %d, want 42", snap.Views.Total)
	}

	var kinds []string
	for _, cmd := range seen {
		kinds = append(kinds, cmd[0])
	}
	for _, want := range []string{"INCR", "LPUSH", "LTRIM", "LRANGE", "GET"} {
		found := false
		for _, k := range kinds {
			if k == want {
				found = true
			}
		}
		if !found {
			t.Errorf("pipeline missing %s, got %v", want, kinds)
		}
	}
}

// The first request an instance serves has nothing buffered yet.
func TestRecordSkipsPushWithoutASample(t *testing.T) {
	var seen [][]string
	s := fakeRedis(t, func(commands [][]string) []map[string]any {
		seen = commands
		return replyPerCommand(commands, nil)
	})

	if _, err := s.Record(context.Background(), time.Now(), 0); err != nil {
		t.Fatalf("Record: %v", err)
	}
	for _, cmd := range seen {
		if cmd[0] == "LPUSH" {
			t.Error("nothing to sample, so nothing should be pushed")
		}
	}
}

func TestReadDoesNotWrite(t *testing.T) {
	var seen [][]string
	s := fakeRedis(t, func(commands [][]string) []map[string]any {
		seen = commands
		return replyPerCommand(commands, map[string]any{"GET": "1234"})
	})

	if _, err := s.Read(context.Background(), time.Now()); err != nil {
		t.Fatalf("Read: %v", err)
	}
	for _, cmd := range seen {
		switch cmd[0] {
		case "INCR", "LPUSH", "LTRIM", "SET":
			t.Errorf("Read issued %q, it must not write", cmd[0])
		}
	}
}

func TestPercentilesFromWindow(t *testing.T) {
	// 1..100 ms, so the nearest-rank p50 and p95 are easy to state.
	samples := make([]any, 0, 100)
	for i := 1; i <= 100; i++ {
		samples = append(samples, strconv.Itoa(i*1000))
	}

	got := percentiles(samples)
	if got.Samples != 100 {
		t.Errorf("samples = %d, want 100", got.Samples)
	}
	if got.P50 != 51*time.Millisecond {
		t.Errorf("p50 = %v, want 51ms", got.P50)
	}
	if got.P95 != 96*time.Millisecond {
		t.Errorf("p95 = %v, want 96ms", got.P95)
	}
}

func TestPercentilesOnEmptyWindow(t *testing.T) {
	if got := percentiles([]any{}); got.Samples != 0 || got.P95 != 0 {
		t.Errorf("empty window gave %+v, want zeros", got)
	}
	if got := percentiles(nil); got.Samples != 0 {
		t.Errorf("nil window gave %+v, want zeros", got)
	}
}

func TestDecodeCI(t *testing.T) {
	raw := `{"status":"passing","sha":"abc1234","tests":55,"coverage":83.4,"finished_at":"2026-09-10T12:00:00Z"}`

	got, ok := decodeCI(raw)
	if !ok {
		t.Fatal("expected a decoded status")
	}
	if got.Status != "passing" || got.Tests != 55 || got.Coverage != 83.4 {
		t.Errorf("decoded %+v", got)
	}
}

// Nothing published yet, or garbage in the key: the CI line just disappears.
func TestDecodeCIRejectsJunk(t *testing.T) {
	for _, raw := range []any{nil, "", "not json", `{"tests":5}`} {
		if _, ok := decodeCI(raw); ok {
			t.Errorf("decodeCI(%v) reported success", raw)
		}
	}
}

func TestMissingKeysCountAsZero(t *testing.T) {
	s := fakeRedis(t, func(commands [][]string) []map[string]any {
		return replyPerCommand(commands, map[string]any{"GET": nil})
	})

	snap, err := s.Read(context.Background(), time.Now())
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if snap.Views.Total != 0 || snap.HasCI {
		t.Errorf("empty redis gave %+v", snap)
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

	if _, err := s.Read(context.Background(), time.Now()); err == nil {
		t.Error("expected the redis error to surface")
	}
}

// Without credentials every call has to degrade quietly, since the service is
// meant to run with the store switched off.
func TestNilStoreIsSafe(t *testing.T) {
	var s *Store
	ctx := context.Background()

	if s.Enabled() {
		t.Error("a nil store should report itself disabled")
	}
	if _, err := s.Record(ctx, time.Now(), time.Millisecond); err != nil {
		t.Errorf("Record on nil store: %v", err)
	}
	if _, err := s.Read(ctx, time.Now()); err != nil {
		t.Errorf("Read on nil store: %v", err)
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
