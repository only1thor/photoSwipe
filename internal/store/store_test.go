package store

import (
	"testing"
	"time"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	st, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return st
}

func mustUpsert(t *testing.T, st *Store, rel string) *Photo {
	t.Helper()
	p, _, err := st.UpsertPhoto(rel, 1234, time.Now())
	if err != nil {
		t.Fatalf("UpsertPhoto %s: %v", rel, err)
	}
	return p
}

func TestRecordSkip_NoOpButAdvancesCounter(t *testing.T) {
	st := openTestStore(t)
	p := mustUpsert(t, st, "a.jpg")
	if _, err := st.StartSession(5, MixMixed); err != nil {
		t.Fatalf("StartSession: %v", err)
	}

	if _, err := st.RecordSkip(p.ID); err != nil {
		t.Fatalf("RecordSkip: %v", err)
	}

	got, _ := st.GetPhoto(p.ID)
	if got.State != StateUnsorted {
		t.Fatalf("skip changed state to %q", got.State)
	}
	if got.KeepCount != 0 || got.UnsureCount != 0 {
		t.Fatalf("skip bumped counters: keep=%d unsure=%d", got.KeepCount, got.UnsureCount)
	}
	sess := st.Session()
	if sess.Done != 1 {
		t.Fatalf("Done=%d, want 1 (SkipAdvancesCounter default true)", sess.Done)
	}
	if len(sess.RecentlySkipped) != 1 || sess.RecentlySkipped[0] != p.ID {
		t.Fatalf("RecentlySkipped=%v, want [%s]", sess.RecentlySkipped, p.ID)
	}
}

func TestRecordSkip_DoesNotAdvanceWhenSettingDisabled(t *testing.T) {
	st := openTestStore(t)
	p := mustUpsert(t, st, "a.jpg")
	s := st.Settings()
	s.SkipAdvancesCounter = false
	if err := st.UpdateSettings(s); err != nil {
		t.Fatalf("UpdateSettings: %v", err)
	}
	if _, err := st.StartSession(5, MixMixed); err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	if _, err := st.RecordSkip(p.ID); err != nil {
		t.Fatalf("RecordSkip: %v", err)
	}
	if d := st.Session().Done; d != 0 {
		t.Fatalf("Done=%d, want 0", d)
	}
}

func TestRecordSkip_UndoRestores(t *testing.T) {
	st := openTestStore(t)
	p := mustUpsert(t, st, "a.jpg")
	if _, err := st.StartSession(5, MixMixed); err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	if _, err := st.RecordSkip(p.ID); err != nil {
		t.Fatalf("RecordSkip: %v", err)
	}
	if _, err := st.Undo(); err != nil {
		t.Fatalf("Undo: %v", err)
	}
	sess := st.Session()
	if sess.Done != 0 {
		t.Fatalf("after undo Done=%d, want 0", sess.Done)
	}
	if len(sess.RecentlySkipped) != 0 {
		t.Fatalf("after undo RecentlySkipped=%v, want empty", sess.RecentlySkipped)
	}
}

func TestRecordClusterDecision_UndoRoundTrip(t *testing.T) {
	st := openTestStore(t)
	a := mustUpsert(t, st, "a.jpg")
	b := mustUpsert(t, st, "b.jpg")
	c := mustUpsert(t, st, "c.jpg")
	if _, err := st.StartSession(5, MixMixed); err != nil {
		t.Fatalf("StartSession: %v", err)
	}

	ops := []ClusterMemberOp{
		{PhotoID: a.ID, NewState: StateKept},
		{PhotoID: b.ID, NewState: StateTrashed, TrashFrom: "x", TrashTo: "y"},
		{PhotoID: c.ID, NewState: StateTrashed, TrashFrom: "p", TrashTo: "q"},
	}
	if _, err := st.RecordClusterDecision("cluster-1", ops); err != nil {
		t.Fatalf("RecordClusterDecision: %v", err)
	}

	pa, _ := st.GetPhoto(a.ID)
	pb, _ := st.GetPhoto(b.ID)
	pc, _ := st.GetPhoto(c.ID)
	if pa.State != StateKept || pa.KeepCount != 1 {
		t.Fatalf("a: state=%q keep=%d", pa.State, pa.KeepCount)
	}
	if pb.State != StateTrashed || pb.TrashedPath != "y" {
		t.Fatalf("b: state=%q trashed=%q", pb.State, pb.TrashedPath)
	}
	if pc.State != StateTrashed {
		t.Fatalf("c: state=%q", pc.State)
	}
	if st.Session().Done != 1 {
		t.Fatalf("Done=%d, want 1 (cluster counts once)", st.Session().Done)
	}

	d, err := st.Undo()
	if err != nil {
		t.Fatalf("Undo: %v", err)
	}
	if len(d.Cluster) != 3 {
		t.Fatalf("undo Decision.Cluster len=%d, want 3", len(d.Cluster))
	}

	pa, _ = st.GetPhoto(a.ID)
	pb, _ = st.GetPhoto(b.ID)
	if pa.State != StateUnsorted || pa.KeepCount != 0 {
		t.Fatalf("a not restored: state=%q keep=%d", pa.State, pa.KeepCount)
	}
	if pb.State != StateUnsorted || pb.TrashedPath != "" {
		t.Fatalf("b not restored: state=%q trashed=%q", pb.State, pb.TrashedPath)
	}
	if st.Session().Done != 0 {
		t.Fatalf("Done=%d after undo, want 0", st.Session().Done)
	}
}

func TestLoad_NormalizesStateUnsureToUnsorted(t *testing.T) {
	dir := t.TempDir()
	st, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	p, _, _ := st.UpsertPhoto("legacy.jpg", 100, time.Now())
	// Simulate an old state file with StateUnsure.
	st.mu.Lock()
	st.state.Photos[p.ID].State = StateUnsure
	st.state.Photos[p.ID].UnsureCount = 3
	_ = st.saveLocked()
	st.mu.Unlock()

	// Reopen.
	st2, err := Open(dir)
	if err != nil {
		t.Fatalf("Open reload: %v", err)
	}
	got, _ := st2.GetPhoto(p.ID)
	if got.State != StateUnsorted {
		t.Fatalf("state=%q, want StateUnsorted", got.State)
	}
	if got.UnsureCount != 3 {
		t.Fatalf("UnsureCount=%d, want 3 (preserved)", got.UnsureCount)
	}
}

func TestAutoStartSession_UsesDefaults(t *testing.T) {
	st := openTestStore(t)
	sess, err := st.AutoStartSession()
	if err != nil {
		t.Fatalf("AutoStartSession: %v", err)
	}
	if sess.Target != 10 {
		t.Fatalf("Target=%d, want 10 (default)", sess.Target)
	}
	if sess.Mix != MixMixed {
		t.Fatalf("Mix=%q, want mixed", sess.Mix)
	}
}
