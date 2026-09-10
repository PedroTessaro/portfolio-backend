// Package githubapi fetches the public profile numbers.
//
// The GitHub API allows 60 requests an hour without a token, and the README is
// served on every visit, so the numbers are cached in two layers: in the
// instance's memory while it stays warm, and in Redis so a cold start inherits
// what a previous invocation already fetched.
//
// Only the request that finds both layers empty pays for the API call. If that
// call fails, whatever was cached keeps serving — a README with numbers from
// half an hour ago beats a broken one.
package githubapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

// Versioned: adding a field to Stats would otherwise keep decoding against the
// old shape until the TTL expired, quietly dropping whatever is new.
const cacheKey = "github:stats:v2"

type Stats struct {
	Repos      int       `json:"repos"`
	Stars      int       `json:"stars"`
	Commits    int       `json:"commits_this_year"`
	HasCommits bool      `json:"has_commits"` // contributions need a token
	Projects   []Project `json:"projects"`
	Last       Commit    `json:"last_commit"`
	FetchedAt  time.Time `json:"fetched_at"`

	Stale      bool   `json:"-"`
	LastAPIErr string `json:"-"`
}

// Project is one featured repository, named in the config but described by the
// API so the listing can't drift out of date.
type Project struct {
	Name     string    `json:"name"`
	Language string    `json:"language"`
	Stars    int       `json:"stars"`
	PushedAt time.Time `json:"pushed_at"`
}

// Commit is the most recent push, shown so the profile proves it is alive
// rather than claiming it.
type Commit struct {
	Message string    `json:"message"`
	Repo    string    `json:"repo"`
	At      time.Time `json:"at"`
}

func (c Commit) Empty() bool { return c.Message == "" }

func (s Stats) Age() time.Duration { return time.Since(s.FetchedAt) }

// Cache is the slice of the store this package needs, kept narrow so tests can
// run without one.
type Cache interface {
	GetCached(ctx context.Context, key string, dst any) (bool, error)
	SetCached(ctx context.Context, key string, value any, ttl time.Duration) error
}

type Client struct {
	user     string
	token    string
	featured []string
	ttl      time.Duration
	cache    Cache
	log      *slog.Logger
	http     *http.Client
	baseURL  string // pointed at a local server in tests

	mu   sync.RWMutex
	memo Stats
}

func New(user, token string, featured []string, ttl time.Duration, cache Cache, log *slog.Logger) *Client {
	return &Client{
		user:     user,
		token:    token,
		featured: featured,
		ttl:      ttl,
		cache:    cache,
		log:      log,
		http:     &http.Client{Timeout: 8 * time.Second},
		baseURL:  "https://api.github.com",
	}
}

func (c *Client) Stats(ctx context.Context) Stats {
	if s, ok := c.fromMemo(); ok {
		return s
	}

	if c.cache != nil {
		var cached Stats
		if found, err := c.cache.GetCached(ctx, cacheKey, &cached); err != nil {
			c.log.Warn("reading stats cache", "err", err)
		} else if found && time.Since(cached.FetchedAt) < c.ttl {
			c.remember(cached)
			return cached
		}
	}

	fresh, err := c.fetch(ctx)
	if err != nil {
		c.log.Warn("github fetch failed", "err", err)
		return c.lastResort(err)
	}

	c.remember(fresh)
	if c.cache != nil {
		if err := c.cache.SetCached(ctx, cacheKey, fresh, c.ttl); err != nil {
			c.log.Warn("writing stats cache", "err", err)
		}
	}
	return fresh
}

func (c *Client) fromMemo() (Stats, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if c.memo.FetchedAt.IsZero() || time.Since(c.memo.FetchedAt) >= c.ttl {
		return Stats{}, false
	}
	return c.memo, true
}

func (c *Client) remember(s Stats) {
	c.mu.Lock()
	c.memo = s
	c.mu.Unlock()
}

// lastResort is reached when GitHub is unreachable: serve anything we still
// hold, flagged so the SVG can say so.
func (c *Client) lastResort(cause error) Stats {
	c.mu.RLock()
	defer c.mu.RUnlock()

	stats := c.memo
	stats.Stale = true
	stats.LastAPIErr = cause.Error()
	return stats
}

func (c *Client) fetch(ctx context.Context) (Stats, error) {
	repos, err := c.fetchRepos(ctx)
	if err != nil {
		return Stats{}, err
	}

	stats := Stats{
		Repos:     len(repos),
		Projects:  c.featuredFrom(repos),
		FetchedAt: time.Now(),
	}
	for _, r := range repos {
		stats.Stars += r.Stars
	}

	// Both of the following are extras: a failure costs one line of the SVG,
	// never the whole render.
	if last, err := c.fetchLastCommit(ctx, repos); err == nil {
		stats.Last = last
	} else {
		c.log.Warn("last commit unavailable", "err", err)
	}

	// Contributions only exist on the GraphQL API, which requires auth. Without
	// a token the rest of the numbers are still fine.
	if c.token != "" {
		if commits, err := c.fetchContributions(ctx); err == nil {
			stats.Commits = commits
			stats.HasCommits = true
		} else {
			c.log.Warn("contributions unavailable", "err", err)
		}
	}
	return stats, nil
}

// repo is the slice of the API payload we care about. One pass over it feeds
// both the totals and the featured listing.
type repo struct {
	Name     string    `json:"name"`
	Fork     bool      `json:"fork"`
	Stars    int       `json:"stargazers_count"`
	Language string    `json:"language"`
	PushedAt time.Time `json:"pushed_at"`
}

// fetchRepos pages through the public repos, skipping forks since those aren't
// my work.
func (c *Client) fetchRepos(ctx context.Context) ([]repo, error) {
	var all []repo

	for page := 1; page <= 10; page++ {
		url := fmt.Sprintf("%s/users/%s/repos?per_page=100&page=%d&type=owner", c.baseURL, c.user, page)
		var batch []repo
		if err := c.getJSON(ctx, url, &batch); err != nil {
			return nil, err
		}
		for _, r := range batch {
			if !r.Fork {
				all = append(all, r)
			}
		}
		if len(batch) < 100 {
			break
		}
	}
	return all, nil
}

// featuredFrom resolves the configured names against what the API returned,
// newest push first so the listing reads like ls -t.
func (c *Client) featuredFrom(repos []repo) []Project {
	byName := make(map[string]repo, len(repos))
	for _, r := range repos {
		byName[strings.ToLower(r.Name)] = r
	}

	projects := make([]Project, 0, len(c.featured))
	for _, name := range c.featured {
		r, ok := byName[strings.ToLower(name)]
		if !ok {
			// Renamed, deleted or made private: worth a log line, not a failure.
			c.log.Warn("featured repo not found", "name", name)
			continue
		}
		language := r.Language
		if language == "" {
			language = "-"
		}
		projects = append(projects, Project{
			Name:     r.Name,
			Language: language,
			Stars:    r.Stars,
			PushedAt: r.PushedAt,
		})
	}

	sort.Slice(projects, func(i, j int) bool {
		return projects[i].PushedAt.After(projects[j].PushedAt)
	})
	return projects
}

// fetchLastCommit asks the most recently pushed repo for its head commit.
//
// The obvious route, /users/{u}/events/public, turns out to be useless for
// this: its PushEvent payload carries before/head/ref and no commit message at
// all. Going through the repo list we already have costs the same one extra
// call and returns the real subject line.
func (c *Client) fetchLastCommit(ctx context.Context, repos []repo) (Commit, error) {
	var newest repo
	for _, r := range repos {
		if r.PushedAt.After(newest.PushedAt) {
			newest = r
		}
	}
	if newest.Name == "" {
		return Commit{}, fmt.Errorf("no repositories to read")
	}

	var commits []struct {
		Commit struct {
			Message   string `json:"message"`
			Committer struct {
				Date time.Time `json:"date"`
			} `json:"committer"`
		} `json:"commit"`
	}

	url := fmt.Sprintf("%s/repos/%s/%s/commits?per_page=1", c.baseURL, c.user, newest.Name)
	if err := c.getJSON(ctx, url, &commits); err != nil {
		return Commit{}, err
	}
	if len(commits) == 0 {
		return Commit{}, fmt.Errorf("%s has no commits", newest.Name)
	}

	message := commits[0].Commit.Message
	if i := strings.IndexByte(message, '\n'); i >= 0 {
		message = message[:i] // subject line only
	}

	return Commit{
		Message: strings.TrimSpace(message),
		Repo:    newest.Name,
		At:      commits[0].Commit.Committer.Date,
	}, nil
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
