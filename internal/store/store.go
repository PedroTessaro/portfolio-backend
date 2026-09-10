// Package store keeps the state that has to outlive a single invocation: the
// README view counter and the cached GitHub numbers.
//
// Serverless functions have no disk and no shared memory, so this talks to a
// Redis over its HTTP API — no driver, no connection pool, and nothing to keep
// warm between invocations.
//
// The store is optional. With no credentials configured the service still
// serves the SVG; it just drops the view counter, the same way the commits
// column disappears without a GitHub token.
package store

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"time"
)

type Store struct {
	baseURL string
	token   string
	http    *http.Client
}

type Views struct {
	Total int
	Today int
}

// FromEnv builds a store from the variables Vercel's Upstash integration
// injects. It returns nil when they are absent, which callers treat as
// "counter disabled" rather than an error.
func FromEnv() *Store {
	url, token := os.Getenv("KV_REST_API_URL"), os.Getenv("KV_REST_API_TOKEN")
	if url == "" || token == "" {
		return nil
	}
	return New(url, token)
}

// New points the store at a specific Redis endpoint. FromEnv is the normal way
// in; this exists so tests can aim it at a stub.
func New(baseURL, token string) *Store {
	return &Store{
		baseURL: baseURL,
		token:   token,
		http:    &http.Client{Timeout: 5 * time.Second},
	}
}

func (s *Store) Enabled() bool { return s != nil }

// Hit records a visit and returns the counters including it. INCR replies with
// the new value, so one round trip both writes and reads.
func (s *Store) Hit(ctx context.Context, now time.Time) (Views, error) {
	if !s.Enabled() {
		return Views{}, nil
	}

	results, err := s.pipeline(ctx, [][]string{
		{"INCR", keyTotal},
		{"INCR", keyDay(now)},
	})
	if err != nil {
		return Views{}, err
	}
	return Views{Total: asInt(results[0]), Today: asInt(results[1])}, nil
}

// Snapshot reads without incrementing, so /whoami and /metrics don't inflate
// the README view count.
func (s *Store) Snapshot(ctx context.Context, now time.Time) (Views, error) {
	if !s.Enabled() {
		return Views{}, nil
	}

	results, err := s.pipeline(ctx, [][]string{
		{"GET", keyTotal},
		{"GET", keyDay(now)},
	})
	if err != nil {
		return Views{}, err
	}
	return Views{Total: asInt(results[0]), Today: asInt(results[1])}, nil
}

// GetCached reads a JSON value written by SetCached. A miss is (false, nil):
// an expired cache is an ordinary outcome, not a failure.
func (s *Store) GetCached(ctx context.Context, key string, dst any) (bool, error) {
	if !s.Enabled() {
		return false, nil
	}

	results, err := s.pipeline(ctx, [][]string{{"GET", key}})
	if err != nil {
		return false, err
	}
	raw, ok := results[0].(string)
	if !ok || raw == "" {
		return false, nil
	}
	if err := json.Unmarshal([]byte(raw), dst); err != nil {
		return false, fmt.Errorf("decode cached %s: %w", key, err)
	}
	return true, nil
}

func (s *Store) SetCached(ctx context.Context, key string, value any, ttl time.Duration) error {
	if !s.Enabled() {
		return nil
	}

	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	_, err = s.pipeline(ctx, [][]string{
		{"SET", key, string(encoded), "EX", strconv.Itoa(int(ttl.Seconds()))},
	})
	return err
}

const keyTotal = "views:total"

func keyDay(now time.Time) string { return "views:day:" + now.UTC().Format("2006-01-02") }

// pipeline sends several commands in one request and returns their results in
// order. Batching matters here: every round trip is paid on the request path.
func (s *Store) pipeline(ctx context.Context, commands [][]string) ([]any, error) {
	body, err := json.Marshal(commands)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.baseURL+"/pipeline", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+s.token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := s.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("redis pipeline returned %s", resp.Status)
	}

	var replies []struct {
		Result any    `json:"result"`
		Error  string `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&replies); err != nil {
		return nil, err
	}
	if len(replies) != len(commands) {
		return nil, fmt.Errorf("sent %d commands, got %d replies", len(commands), len(replies))
	}

	out := make([]any, len(replies))
	for i, r := range replies {
		if r.Error != "" {
			return nil, fmt.Errorf("%s: %s", commands[i][0], r.Error)
		}
		out[i] = r.Result
	}
	return out, nil
}

// asInt copes with Redis returning counters as numbers from INCR and as strings
// from GET, and with nil for a key that does not exist yet.
func asInt(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case string:
		parsed, err := strconv.Atoi(n)
		if err != nil {
			return 0
		}
		return parsed
	default:
		return 0
	}
}
