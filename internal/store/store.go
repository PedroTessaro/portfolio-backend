// Package store keeps the state that has to outlive a single invocation: the
// README view counter, a rolling window of response times, and whatever the CI
// last published about itself.
//
// Serverless functions have no disk and no shared memory, so this talks to a
// Redis over its HTTP API — no driver, no connection pool, nothing to keep warm.
// Every read the SVG needs travels in one pipelined round trip, because that
// round trip is on the request path and shows up in the latency the SVG prints.
//
// The store is optional. With no credentials configured the service still
// serves the SVG; it just drops the lines it can no longer fill in.
package store

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strconv"
	"time"
)

// How many response times to keep. Enough for a stable p95, small enough that
// pulling the whole window back costs little.
const sampleWindow = 500

type Store struct {
	baseURL string
	token   string
	http    *http.Client
}

type Views struct {
	Total int
	Today int
}

// Latency is the percentile view of the rolling window.
type Latency struct {
	P50     time.Duration
	P95     time.Duration
	Samples int
}

// CIStatus is written by the GitHub Actions workflow, never by this service.
type CIStatus struct {
	Status     string    `json:"status"`
	SHA        string    `json:"sha"`
	Tests      int       `json:"tests"`
	Coverage   float64   `json:"coverage"`
	FinishedAt time.Time `json:"finished_at"`
}

// Snapshot is everything the SVG needs from Redis, fetched together.
type Snapshot struct {
	Views   Views
	Latency Latency
	CI      CIStatus
	HasCI   bool
}

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

// Record counts a visit and returns everything the render needs.
//
// sample is the duration of the *previous* request this instance served, not
// the current one — the current one isn't over yet, and measuring it would mean
// a second round trip after the response was already written, which a frozen
// serverless instance may never get to make. The cost of doing it this way is
// that the last request before an instance goes idle never lands in the window.
func (s *Store) Record(ctx context.Context, now time.Time, sample time.Duration) (Snapshot, error) {
	if !s.Enabled() {
		return Snapshot{}, nil
	}

	commands := [][]string{
		{"INCR", keyTotal},
		{"INCR", keyDay(now)},
	}
	if sample > 0 {
		commands = append(commands,
			[]string{"LPUSH", keyLatency, strconv.FormatInt(sample.Microseconds(), 10)},
			[]string{"LTRIM", keyLatency, "0", strconv.Itoa(sampleWindow - 1)},
		)
	}
	commands = append(commands,
		[]string{"LRANGE", keyLatency, "0", strconv.Itoa(sampleWindow - 1)},
		[]string{"GET", keyCI},
	)

	results, err := s.pipeline(ctx, commands)
	if err != nil {
		return Snapshot{}, err
	}

	snap := Snapshot{Views: Views{Total: asInt(results[0]), Today: asInt(results[1])}}
	snap.Latency = percentiles(results[len(results)-2])
	snap.CI, snap.HasCI = decodeCI(results[len(results)-1])
	return snap, nil
}

// Read is Record without the write, for endpoints that must not inflate the
// counter.
func (s *Store) Read(ctx context.Context, now time.Time) (Snapshot, error) {
	if !s.Enabled() {
		return Snapshot{}, nil
	}

	results, err := s.pipeline(ctx, [][]string{
		{"GET", keyTotal},
		{"GET", keyDay(now)},
		{"LRANGE", keyLatency, "0", strconv.Itoa(sampleWindow - 1)},
		{"GET", keyCI},
	})
	if err != nil {
		return Snapshot{}, err
	}

	snap := Snapshot{Views: Views{Total: asInt(results[0]), Today: asInt(results[1])}}
	snap.Latency = percentiles(results[2])
	snap.CI, snap.HasCI = decodeCI(results[3])
	return snap, nil
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

const (
	keyTotal   = "views:total"
	keyLatency = "latency:samples"
	keyCI      = "ci:status"
)

func keyDay(now time.Time) string { return "views:day:" + now.UTC().Format("2006-01-02") }

// percentiles turns the raw LRANGE reply into p50 and p95. Nearest-rank, which
// is the honest method for a few hundred samples.
func percentiles(raw any) Latency {
	values, ok := raw.([]any)
	if !ok || len(values) == 0 {
		return Latency{}
	}

	micros := make([]int, 0, len(values))
	for _, v := range values {
		if n := asInt(v); n > 0 {
			micros = append(micros, n)
		}
	}
	if len(micros) == 0 {
		return Latency{}
	}
	sort.Ints(micros)

	at := func(q float64) time.Duration {
		i := int(q * float64(len(micros)))
		if i >= len(micros) {
			i = len(micros) - 1
		}
		return time.Duration(micros[i]) * time.Microsecond
	}
	return Latency{P50: at(0.50), P95: at(0.95), Samples: len(micros)}
}

func decodeCI(raw any) (CIStatus, bool) {
	encoded, ok := raw.(string)
	if !ok || encoded == "" {
		return CIStatus{}, false
	}

	var status CIStatus
	if err := json.Unmarshal([]byte(encoded), &status); err != nil {
		return CIStatus{}, false
	}
	return status, status.Status != ""
}

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
