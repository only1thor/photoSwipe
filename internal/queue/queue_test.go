package queue

import (
	"testing"
	"time"

	"photoSwipe/internal/store"
)

func mkPhoto(id string, state store.PhotoState, keep, unsure int, ago time.Duration) *store.Photo {
	p := &store.Photo{ID: id, Path: id + ".jpg", State: state, KeepCount: keep, UnsureCount: unsure}
	if ago > 0 {
		p.LastDecisionAt = time.Now().Add(-ago)
	}
	return p
}

func TestNext_OnlyUnsortedWhenAllNewMix(t *testing.T) {
	now := time.Now()
	sel := New()
	photos := []*store.Photo{
		mkPhoto("a", store.StateUnsorted, 0, 0, 0),
		mkPhoto("b", store.StateKept, 1, 0, 24*time.Hour),
		mkPhoto("c", store.StateKept, 3, 0, 24*time.Hour),
	}
	sess := &store.Session{Mix: store.MixAllNew}
	settings := store.DefaultSettings()
	for i := 0; i < 50; i++ {
		got, err := sel.Next(photos, sess, settings, now)
		if err != nil {
			t.Fatalf("Next: %v", err)
		}
		if got.ID != "a" {
			t.Fatalf("all_new mix returned non-unsorted photo: %s", got.ID)
		}
	}
}

func TestNext_OnlyKeptWhenReviewOnly(t *testing.T) {
	now := time.Now()
	sel := New()
	photos := []*store.Photo{
		mkPhoto("a", store.StateUnsorted, 0, 0, 0),
		mkPhoto("b", store.StateKept, 1, 0, 24*time.Hour),
	}
	sess := &store.Session{Mix: store.MixReviewOnly}
	settings := store.DefaultSettings()
	for i := 0; i < 50; i++ {
		got, err := sel.Next(photos, sess, settings, now)
		if err != nil {
			t.Fatalf("Next: %v", err)
		}
		if got.ID != "b" {
			t.Fatalf("review_only mix returned unsorted photo: %s", got.ID)
		}
	}
}

func TestNext_CooldownExcludesRecentlyDecided(t *testing.T) {
	now := time.Now()
	sel := New()
	settings := store.DefaultSettings()
	// CooldownHours default is 6h; 1h ago must be skipped.
	photos := []*store.Photo{
		mkPhoto("recent", store.StateKept, 1, 0, 1*time.Hour),
	}
	sess := &store.Session{Mix: store.MixReviewOnly}
	if _, err := sel.Next(photos, sess, settings, now); err != ErrNoCandidate {
		t.Fatalf("expected ErrNoCandidate, got %v", err)
	}
}

func TestNext_LockThresholdExcludes(t *testing.T) {
	now := time.Now()
	sel := New()
	settings := store.DefaultSettings()
	settings.LockThreshold = 3
	photos := []*store.Photo{
		mkPhoto("locked", store.StateKept, 3, 0, 24*time.Hour),
		mkPhoto("under", store.StateKept, 2, 0, 24*time.Hour),
	}
	sess := &store.Session{Mix: store.MixReviewOnly}
	for i := 0; i < 50; i++ {
		got, err := sel.Next(photos, sess, settings, now)
		if err != nil {
			t.Fatalf("Next: %v", err)
		}
		if got.ID == "locked" {
			t.Fatal("locked photo returned despite threshold")
		}
	}
}

func TestNext_HigherKeepCountLowersWeight(t *testing.T) {
	now := time.Now()
	sel := New()
	settings := store.DefaultSettings()
	a := mkPhoto("a", store.StateKept, 0, 0, 24*time.Hour)
	a.State = store.StateKept
	a.KeepCount = 1
	b := mkPhoto("b", store.StateKept, 5, 0, 24*time.Hour)
	photos := []*store.Photo{a, b}
	sess := &store.Session{Mix: store.MixReviewOnly}

	counts := map[string]int{}
	for i := 0; i < 5000; i++ {
		got, err := sel.Next(photos, sess, settings, now)
		if err != nil {
			t.Fatalf("Next: %v", err)
		}
		counts[got.ID]++
	}
	if counts["a"] <= counts["b"]*5 {
		t.Fatalf("expected a (keep=1) much more frequent than b (keep=5): a=%d b=%d", counts["a"], counts["b"])
	}
}

func TestNext_NoCandidate(t *testing.T) {
	sel := New()
	photos := []*store.Photo{
		mkPhoto("t", store.StateTrashed, 0, 0, 0),
	}
	sess := &store.Session{Mix: store.MixMixed}
	settings := store.DefaultSettings()
	if _, err := sel.Next(photos, sess, settings, time.Now()); err != ErrNoCandidate {
		t.Fatalf("expected ErrNoCandidate, got %v", err)
	}
}
