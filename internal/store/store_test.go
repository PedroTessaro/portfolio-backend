package store

import (
	"path/filepath"
	"testing"
	"time"
)

func openTemp(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestHitIncrementsTotalAndToday(t *testing.T) {
	s := openTemp(t)
	now := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)

	for i := 1; i <= 3; i++ {
		v, err := s.Hit(now)
		if err != nil {
			t.Fatalf("Hit: %v", err)
		}
		if v.Total != i || v.Today != i {
			t.Fatalf("after %d hits: total=%d today=%d, want both %d", i, v.Total, v.Today, i)
		}
	}
}

func TestTodayResetsAcrossDays(t *testing.T) {
	s := openTemp(t)
	yesterday := time.Date(2026, 9, 9, 23, 0, 0, 0, time.UTC)
	today := time.Date(2026, 9, 10, 1, 0, 0, 0, time.UTC)

	for i := 0; i < 5; i++ {
		if _, err := s.Hit(yesterday); err != nil {
			t.Fatalf("Hit: %v", err)
		}
	}

	v, err := s.Hit(today)
	if err != nil {
		t.Fatalf("Hit: %v", err)
	}
	if v.Total != 6 {
		t.Errorf("total = %d, want 6", v.Total)
	}
	if v.Today != 1 {
		t.Errorf("today = %d, want 1", v.Today)
	}
}

func TestSnapshotDoesNotIncrement(t *testing.T) {
	s := openTemp(t)
	now := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)

	if _, err := s.Hit(now); err != nil {
		t.Fatalf("Hit: %v", err)
	}
	for i := 0; i < 3; i++ {
		v, err := s.Snapshot(now)
		if err != nil {
			t.Fatalf("Snapshot: %v", err)
		}
		if v.Total != 1 {
			t.Fatalf("Snapshot changed the total: %d", v.Total)
		}
	}
}

func TestSnapshotOnEmptyDatabase(t *testing.T) {
	v, err := openTemp(t).Snapshot(time.Now())
	if err != nil {
		t.Fatalf("Snapshot on empty db: %v", err)
	}
	if v.Total != 0 || v.Today != 0 {
		t.Errorf("empty db returned total=%d today=%d, want zeros", v.Total, v.Today)
	}
}

// Fly recycles machines; the numbers have to survive a restart.
func TestCountersSurviveReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "persist.db")
	now := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)

	first, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	for i := 0; i < 7; i++ {
		if _, err := first.Hit(now); err != nil {
			t.Fatalf("Hit: %v", err)
		}
	}
	if err := first.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	second, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer second.Close()

	v, err := second.Snapshot(now)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if v.Total != 7 {
		t.Errorf("total after reopen = %d, want 7", v.Total)
	}
}
