// Package store keeps the README view counter.
package store

import (
	"database/sql"
	"fmt"
	"time"

	_ "modernc.org/sqlite" // pure-Go driver, so the binary stays static
)

type Store struct {
	db *sql.DB
}

type Views struct {
	Total int
	Today int
}

// Rolling up per day keeps the table around 365 rows a year instead of one row
// per visit, and still gives an exact total.
const schema = `
CREATE TABLE IF NOT EXISTS daily_views (
    day   TEXT PRIMARY KEY,
    count INTEGER NOT NULL DEFAULT 0
);`

func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite at %s: %w", path, err)
	}

	// SQLite serialises writes anyway; a bigger pool only buys contention.
	db.SetMaxOpenConns(1)

	for _, pragma := range []string{
		"PRAGMA journal_mode = WAL",
		"PRAGMA busy_timeout = 5000",
		"PRAGMA synchronous = NORMAL",
	} {
		if _, err := db.Exec(pragma); err != nil {
			db.Close()
			return nil, fmt.Errorf("%s: %w", pragma, err)
		}
	}

	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("create schema: %w", err)
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

// Hit records a visit and returns the counter with that visit included.
func (s *Store) Hit(now time.Time) (Views, error) {
	day := now.UTC().Format("2006-01-02")

	const upsert = `
        INSERT INTO daily_views (day, count) VALUES (?, 1)
        ON CONFLICT(day) DO UPDATE SET count = count + 1;`
	if _, err := s.db.Exec(upsert, day); err != nil {
		return Views{}, fmt.Errorf("increment %s: %w", day, err)
	}
	return s.snapshot(day)
}

// Snapshot reads without incrementing, so /whoami and /metrics don't inflate
// the README view count.
func (s *Store) Snapshot(now time.Time) (Views, error) {
	return s.snapshot(now.UTC().Format("2006-01-02"))
}

func (s *Store) snapshot(day string) (Views, error) {
	const query = `
        SELECT COALESCE(SUM(count), 0),
               COALESCE(SUM(CASE WHEN day = ? THEN count ELSE 0 END), 0)
        FROM daily_views;`

	var v Views
	if err := s.db.QueryRow(query, day).Scan(&v.Total, &v.Today); err != nil {
		return Views{}, fmt.Errorf("read views: %w", err)
	}
	return v, nil
}
