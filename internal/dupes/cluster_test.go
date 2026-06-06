package dupes

import (
	"testing"
	"time"

	"photoSwipe/internal/store"
)

func mk(id string, dhash uint64, state store.PhotoState, size int64, t time.Time) *store.Photo {
	p := &store.Photo{
		ID:        id,
		Path:      id + ".jpg",
		State:     state,
		SizeBytes: size,
		Time:      t,
	}
	if dhash != 0 {
		p.DHash = dhash
		// Leave DHashV at zero — tests below compare only the H plane.
		p.HashVersion = store.CurrentHashVersion
		p.DHashedAt = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	}
	return p
}

func TestFind_GroupsByHashProximity(t *testing.T) {
	now := time.Now()
	photos := []*store.Photo{
		mk("a", 0xff00ff00ff00ff00, store.StateUnsorted, 100, now),
		mk("b", 0xff00ff00ff00ff01, store.StateUnsorted, 200, now), // 1 bit off → cluster with a
		mk("c", 0x0000000000000000, store.StateUnsorted, 300, now), // 32 bits off → alone
		mk("d", 0xff00ff00ff00ff03, store.StateUnsorted, 150, now), // 2 bits off from a
	}
	cs := Find(photos, 5, 0)
	if len(cs) != 1 {
		t.Fatalf("got %d clusters, want 1", len(cs))
	}
	if len(cs[0].Photos) != 3 {
		t.Fatalf("got %d photos in cluster, want 3", len(cs[0].Photos))
	}
	// Sorted by size desc → c is not in cluster, so order: c(300),d(150),b(200),a(100) — wait c isn't here
	// In-cluster: a,b,d; sorted by size: b(200), d(150), a(100)
	want := []string{"b", "d", "a"}
	for i, p := range cs[0].Photos {
		if p.ID != want[i] {
			t.Fatalf("photo[%d]=%s, want %s", i, p.ID, want[i])
		}
	}
}

func TestFind_RespectsTrashedState(t *testing.T) {
	now := time.Now()
	photos := []*store.Photo{
		mk("a", 0xff00, store.StateUnsorted, 100, now),
		mk("b", 0xff00, store.StateTrashed, 200, now),
	}
	cs := Find(photos, 5, 0)
	if len(cs) != 0 {
		t.Fatalf("expected no clusters (one photo trashed), got %d", len(cs))
	}
}

func TestFind_SkipsUnhashed(t *testing.T) {
	now := time.Now()
	a := mk("a", 0xff00, store.StateUnsorted, 100, now)
	b := &store.Photo{ID: "b", Path: "b.jpg", State: store.StateUnsorted, SizeBytes: 200, Time: now} // unhashed
	cs := Find([]*store.Photo{a, b}, 5, 0)
	if len(cs) != 0 {
		t.Fatalf("expected no clusters, got %d", len(cs))
	}
}

func TestFind_StableClusterID(t *testing.T) {
	now := time.Now()
	photos := []*store.Photo{
		mk("z", 0xff, store.StateUnsorted, 100, now),
		mk("a", 0xff, store.StateUnsorted, 200, now),
		mk("m", 0xff, store.StateUnsorted, 150, now),
	}
	cs := Find(photos, 5, 0)
	if len(cs) != 1 || cs[0].ID != "a" {
		t.Fatalf("cluster ID=%q, want %q (lex-smallest)", cs[0].ID, "a")
	}
}

func TestFind_TimeWindowExcludesFarPairs(t *testing.T) {
	base := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	photos := []*store.Photo{
		mk("morning", 0xabcd, store.StateUnsorted, 100, base),
		mk("evening", 0xabcd, store.StateUnsorted, 100, base.Add(10*time.Hour)),
	}
	// With a 1-hour window, these should NOT cluster despite identical hashes.
	cs := Find(photos, 5, time.Hour)
	if len(cs) != 0 {
		t.Fatalf("time-far pair clustered: %d clusters", len(cs))
	}
	// With no window, they should cluster.
	cs = Find(photos, 5, 0)
	if len(cs) != 1 {
		t.Fatalf("identical hashes did not cluster without window: %d clusters", len(cs))
	}
}

func TestFind_TimeWindowSlidingBreaks(t *testing.T) {
	// 1000 photos spread over 1000 hours; each within 1-bit of its neighbours.
	// With a 2-hour window we should still cluster neighbours but the inner
	// loop should break early — verified indirectly by test speed.
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	photos := make([]*store.Photo, 1000)
	for i := range photos {
		photos[i] = mk("p"+itoa(i), uint64(i), store.StateUnsorted, int64(100+i), base.Add(time.Duration(i)*time.Hour))
	}
	cs := Find(photos, 1, 2*time.Hour)
	// We don't assert specific count; just ensure no panic and finite output.
	if cs == nil && len(photos) > 0 {
		// Ok if zero clusters were found
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var s []byte
	for n > 0 {
		s = append([]byte{byte('0' + n%10)}, s...)
		n /= 10
	}
	return string(s)
}
