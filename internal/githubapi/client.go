// Package githubapi fetches the public profile numbers.
//
// The README is served on every visit, but the GitHub API allows 60 requests
// per hour without a token. So the numbers live in a cache refreshed by a
// background goroutine: no HTTP request ever waits on GitHub, and an outage
// there just leaves the last good values in place.
package githubapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"
)

type Stats struct {
	Repos      int       `json:"repos"`
	Stars      int       `json:"stars"`
	Commits    int       `json:"commits_this_year"`
	HasCommits bool      `json:"-"` // contributions need a token; without one the column is dropped
	FetchedAt  time.Time `json:"fetched_at"`
	Stale      bool      `json:"stale"`
	LastAPIErr string    `json:"last_error,omitempty"`
}

type Client struct {
	user     string
	token    string
	interval time.Duration
	log      *slog.Logger
	http     *http.Client
	baseURL  string // pointed at a local server in tests

	mu     sync.RWMutex
	cached Stats
	valid  bool
}

func New(user, token string, interval time.Duration, log *slog.Logger) *Client {
	return &Client{
		user:     user,
		token:    token,
		interval: interval,
		log:      log,
		http:     &http.Client{Timeout: 8 * time.Second},
		baseURL:  "https://api.github.com",
	}
}

// Stats returns the cached snapshot without touching the network, so the
// response time printed in the SVG reflects only this service's own work.
func (c *Client) Stats() Stats {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if !c.valid {
		return Stats{Stale: true, LastAPIErr: "not refreshed yet"}
	}
	stats := c.cached
	// Well past the interval means refreshes have been failing quietly.
	stats.Stale = time.Since(stats.FetchedAt) > 3*c.interval
	return stats
}

// Start refreshes once and then every interval until ctx is cancelled. Errors
// are logged and dropped: the previous values keep serving.
func (c *Client) Start(ctx context.Context) {
	refresh := func() {
		reqCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()

		fresh, err := c.fetch(reqCtx)
		if err != nil {
			c.log.Warn("github refresh failed", "err", err)
			return
		}

		c.mu.Lock()
		c.cached, c.valid = fresh, true
		c.mu.Unlock()
		c.log.Info("github stats refreshed", "repos", fresh.Repos, "stars", fresh.Stars)
	}

	go func() {
		refresh()
		ticker := time.NewTicker(c.interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				refresh()
			}
		}
	}()
}

func (c *Client) fetch(ctx context.Context) (Stats, error) {
	repos, stars, err := c.fetchRepos(ctx)
	if err != nil {
		return Stats{}, err
	}

	stats := Stats{
		Repos:     repos,
		Stars:     stars,
		FetchedAt: time.Now(),
	}

	// Contributions only exist on the GraphQL API, which requires auth. Without
	// a token the rest of the numbers are still fine.
	if c.token != "" {
		if commits, err := c.fetchContributions(ctx); err == nil {
			stats.Commits = commits
			stats.HasCommits = true
		} else {
			stats.LastAPIErr = err.Error()
		}
	}
	return stats, nil
}

// fetchRepos pages through the public repos, skipping forks since those aren't
// my work.
func (c *Client) fetchRepos(ctx context.Context) (repos, stars int, err error) {
	type repo struct {
		Fork  bool `json:"fork"`
		Stars int  `json:"stargazers_count"`
	}

	for page := 1; page <= 10; page++ {
		url := fmt.Sprintf("%s/users/%s/repos?per_page=100&page=%d&type=owner", c.baseURL, c.user, page)
		var batch []repo
		if err := c.getJSON(ctx, url, &batch); err != nil {
			return 0, 0, err
		}
		for _, r := range batch {
			if r.Fork {
				continue
			}
			repos++
			stars += r.Stars
		}
		if len(batch) < 100 {
			break
		}
	}
	return repos, stars, nil
}

func (c *Client) fetchContributions(ctx context.Context) (int, error) {
	const query = `query($login:String!){
        user(login:$login){
            contributionsCollection{ contributionCalendar{ totalContributions } }
        }
    }`

	body, err := json.Marshal(map[string]any{
		"query":     query,
		"variables": map[string]string{"login": c.user},
	})
	if err != nil {
		return 0, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/graphql", bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("graphql returned %s", resp.Status)
	}

	var payload struct {
		Data struct {
			User struct {
				ContributionsCollection struct {
					ContributionCalendar struct {
						TotalContributions int `json:"totalContributions"`
					} `json:"contributionCalendar"`
				} `json:"contributionsCollection"`
			} `json:"user"`
		} `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return 0, err
	}
	if len(payload.Errors) > 0 {
		return 0, fmt.Errorf("graphql: %s", payload.Errors[0].Message)
	}
	return payload.Data.User.ContributionsCollection.ContributionCalendar.TotalContributions, nil
}

func (c *Client) getJSON(ctx context.Context, url string, dst any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		// Rate limiting is the expected failure here, worth naming in the log.
		if resp.StatusCode == http.StatusForbidden && resp.Header.Get("X-RateLimit-Remaining") == "0" {
			return fmt.Errorf("github rate limit exhausted (resets at %s)", resp.Header.Get("X-RateLimit-Reset"))
		}
		return fmt.Errorf("GET %s returned %s", url, resp.Status)
	}
	return json.NewDecoder(resp.Body).Decode(dst)
}
