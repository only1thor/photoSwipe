package store

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

const (
	stateFileName    = ".photosort-state.json"
	stateFileVersion = 1
	staleSessionAge  = 24 * time.Hour
)

// rootState is the on-disk JSON layout.
type rootState struct {
	Version    int               `json:"version"`
	Photos     map[string]*Photo `json:"photos"`
	Session    *Session          `json:"session,omitempty"`
	Settings   Settings          `json:"settings"`
	DailyStats map[string]int    `json:"daily_stats"`
}

type Store struct {
	mu       sync.RWMutex
	path     string
	photoDir string
	state    rootState
}

func Open(photoDir string) (*Store, error) {
	s := &Store{
		path:     filepath.Join(photoDir, stateFileName),
		photoDir: photoDir,
		state: rootState{
			Version:    stateFileVersion,
			Photos:     map[string]*Photo{},
			Settings:   DefaultSettings(),
			DailyStats: map[string]int{},
		},
	}
	if err := s.load(); err != nil {
		return nil, err
	}
	// Drop stale sessions on startup.
	if s.state.Session != nil && s.state.Session.Stale(time.Now(), staleSessionAge) {
		s.state.Session = nil
		if err := s.saveLocked(); err != nil {
			return nil, err
		}
	}
	return s, nil
}

func (s *Store) load() error {
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return s.saveLocked()
	}
	if err != nil {
		return err
	}
	var rs rootState
	if err := json.Unmarshal(data, &rs); err != nil {
		return fmt.Errorf("parse state: %w", err)
	}
	if rs.Photos == nil {
		rs.Photos = map[string]*Photo{}
	}
	if rs.DailyStats == nil {
		rs.DailyStats = map[string]int{}
	}
	if rs.Settings.BaseRate == 0 {
		rs.Settings = DefaultSettings()
	}
	defs := DefaultSettings()
	if rs.Settings.DupeThreshold == 0 {
		rs.Settings.DupeThreshold = defs.DupeThreshold
	}
	if rs.Settings.DefaultBatchSize == 0 {
		rs.Settings.DefaultBatchSize = defs.DefaultBatchSize
	}
	if !rs.Settings.DefaultMix.Valid() {
		rs.Settings.DefaultMix = defs.DefaultMix
	}
	// Normalize any pre-existing StateUnsure photos to StateUnsorted —
	// the "skip" model treats them as undecided. UnsureCount is preserved
	// so the resurface algorithm can still wind down their weight.
	for _, p := range rs.Photos {
		if p.State == StateUnsure {
			p.State = StateUnsorted
		}
	}
	s.state = rs
	return nil
}

func (s *Store) saveLocked() error {
	data, err := json.MarshalIndent(&s.state, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saveLocked()
}

// UpsertPhoto registers or updates a photo discovered by scan. Returns the
// canonical photo and a boolean indicating whether it was newly added.
// The mtime argument is the file's modification time, used as a capture-time
// proxy for near-duplicate clustering windows.
func (s *Store) UpsertPhoto(relPath string, sizeBytes int64, mtime time.Time) (*Photo, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := PhotoID(relPath)
	if p, ok := s.state.Photos[id]; ok {
		changed := false
		if p.SizeBytes != sizeBytes {
			p.SizeBytes = sizeBytes
			changed = true
		}
		if p.Time.IsZero() && !mtime.IsZero() {
			p.Time = mtime
			changed = true
		}
		if changed {
			_ = s.saveLocked()
		}
		return p, false, nil
	}
	p := &Photo{
		ID:        id,
		Path:      relPath,
		State:     StateUnsorted,
		SizeBytes: sizeBytes,
		AddedAt:   time.Now(),
		Time:      mtime,
	}
	s.state.Photos[id] = p
	return p, true, s.saveLocked()
}

// NextUnhashed returns one photo that hasn't been hashed yet (DHashedAt
// is zero), was hashed under an older algorithm version, or isn't trashed.
// Returns nil if there's nothing to hash.
func (s *Store) NextUnhashed() *Photo {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, p := range s.state.Photos {
		if p.State == StateTrashed {
			continue
		}
		if p.DHashedAt.IsZero() || p.HashVersion < CurrentHashVersion {
			clone := *p
			return &clone
		}
	}
	return nil
}

// CurrentHashVersion is the algorithm version of dhash.Compute. Photos
// stored with a lower HashVersion are treated as unhashed so the indexer
// will redo them — see NextUnhashed.
const CurrentHashVersion = 2

// SetCaptureTime updates a photo's capture time, preferring EXIF
// DateTimeOriginal over the mtime previously set during scan. Idempotent;
// only writes if t differs from the existing value.
func (s *Store) SetCaptureTime(id string, t time.Time) error {
	if t.IsZero() {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.state.Photos[id]
	if !ok {
		return errors.New("photo not found")
	}
	if p.Time.Equal(t) {
		return nil
	}
	p.Time = t
	return s.saveLocked()
}

// SetHash records a successful hash computation under CurrentHashVersion.
func (s *Store) SetHash(id string, h, v uint64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.state.Photos[id]
	if !ok {
		return errors.New("photo not found")
	}
	p.DHash = h
	p.DHashV = v
	p.HashVersion = CurrentHashVersion
	p.DHashedAt = time.Now()
	return s.saveLocked()
}

// MarkHashFailed records a failed hash attempt so the indexer won't retry.
func (s *Store) MarkHashFailed(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.state.Photos[id]
	if !ok {
		return errors.New("photo not found")
	}
	p.DHash = 0
	p.DHashV = 0
	p.HashVersion = CurrentHashVersion
	p.DHashedAt = time.Now()
	return s.saveLocked()
}

// HashProgress returns (hashed, total) counts over the non-trashed pool.
func (s *Store) HashProgress() (int, int) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var hashed, total int
	for _, p := range s.state.Photos {
		if p.State == StateTrashed {
			continue
		}
		total++
		if !p.DHashedAt.IsZero() && p.HashVersion >= CurrentHashVersion {
			hashed++
		}
	}
	return hashed, total
}

// ForgetMissing removes photos whose IDs are not in the keep set. Returns
// the count removed.
func (s *Store) ForgetMissing(keep map[string]struct{}) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var removed int
	for id, p := range s.state.Photos {
		if _, ok := keep[id]; ok {
			continue
		}
		// Keep already-trashed entries even if file moved; otherwise drop.
		if p.State == StateTrashed {
			continue
		}
		delete(s.state.Photos, id)
		removed++
	}
	if removed > 0 {
		return removed, s.saveLocked()
	}
	return 0, nil
}

func (s *Store) GetPhoto(id string) (*Photo, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	p, ok := s.state.Photos[id]
	if !ok {
		return nil, false
	}
	clone := *p
	return &clone, true
}

// AllPhotos returns a snapshot slice of all photos.
func (s *Store) AllPhotos() []*Photo {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*Photo, 0, len(s.state.Photos))
	for _, p := range s.state.Photos {
		clone := *p
		out = append(out, &clone)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

// MarkSeen updates LastSeenAt for a photo without recording a decision.
func (s *Store) MarkSeen(id string, now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if p, ok := s.state.Photos[id]; ok {
		p.LastSeenAt = now
		_ = s.saveLocked()
	}
}

// Session returns a snapshot of the current session, or nil.
func (s *Store) Session() *Session {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.state.Session == nil {
		return nil
	}
	clone := *s.state.Session
	clone.Stack = append([]Decision(nil), s.state.Session.Stack...)
	return &clone
}

func (s *Store) StartSession(target int, mix CompositionMix) (*Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !mix.Valid() {
		return nil, fmt.Errorf("invalid mix %q", mix)
	}
	if target < 0 {
		return nil, errors.New("target must be >= 0")
	}
	id, err := randID()
	if err != nil {
		return nil, err
	}
	s.state.Session = &Session{
		ID:        id,
		StartedAt: time.Now(),
		Target:    target,
		Mix:       mix,
	}
	if err := s.saveLocked(); err != nil {
		return nil, err
	}
	clone := *s.state.Session
	return &clone, nil
}

// AutoStartSession creates a session using saved defaults
// (Settings.DefaultBatchSize + DefaultMix). Used by `/` when no session
// is active so the user lands directly in the swipe view.
func (s *Store) AutoStartSession() (*Session, error) {
	s.mu.RLock()
	target := s.state.Settings.DefaultBatchSize
	mix := s.state.Settings.DefaultMix
	s.mu.RUnlock()
	if target < 0 {
		target = 0
	}
	if !mix.Valid() {
		mix = MixMixed
	}
	return s.StartSession(target, mix)
}

func (s *Store) EndSession() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state.Session = nil
	return s.saveLocked()
}

// SessionExtend bumps the session target by delta. delta=0 leaves it
// open-ended. The per-batch skip-cooldown FIFO (RecentlySkipped) is
// cleared so the next batch can resurface anything the user skipped in
// the previous one.
func (s *Store) SessionExtend(delta int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state.Session == nil {
		return errors.New("no active session")
	}
	if delta <= 0 {
		s.state.Session.Target = 0
	} else if s.state.Session.Target > 0 {
		s.state.Session.Target += delta
	}
	s.state.Session.RecentlySkipped = nil
	return s.saveLocked()
}

// RecordDecision mutates the photo state, increments session counters,
// and pushes the prior state onto the undo stack.
func (s *Store) RecordDecision(photoID string, newState PhotoState, trashFrom, trashTo string) (*Decision, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.state.Photos[photoID]
	if !ok {
		return nil, fmt.Errorf("photo %s not found", photoID)
	}
	if s.state.Session == nil {
		return nil, errors.New("no active session")
	}
	now := time.Now()
	d := Decision{
		PhotoID:         photoID,
		PrevState:       p.State,
		NewState:        newState,
		Timestamp:       now,
		PrevKeepCount:   p.KeepCount,
		PrevUnsureCount: p.UnsureCount,
		PrevLastSeenAt:  p.LastSeenAt,
		TrashFrom:       trashFrom,
		TrashTo:         trashTo,
	}
	switch newState {
	case StateKept:
		p.KeepCount++
	case StateUnsure:
		p.UnsureCount++
	case StateTrashed:
		p.TrashedPath = trashTo
	}
	p.State = newState
	p.LastDecisionAt = now
	p.LastSeenAt = now

	s.state.Session.Done++
	s.state.Session.Stack = append(s.state.Session.Stack, d)

	day := now.Format("2006-01-02")
	s.state.DailyStats[day]++

	if err := s.saveLocked(); err != nil {
		return nil, err
	}
	return &d, nil
}

// RecordSkip marks a photo as skipped for the current session. The photo's
// State and counters are untouched — semantically, the photo behaves as if
// it was never shown. The ID is pushed onto Session.RecentlySkipped so the
// queue won't immediately re-pick it; the cap is the session target (or 5
// when open-ended). Whether Session.Done advances is governed by
// Settings.SkipAdvancesCounter.
func (s *Store) RecordSkip(photoID string) (*Decision, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.state.Photos[photoID]
	if !ok {
		return nil, fmt.Errorf("photo %s not found", photoID)
	}
	if s.state.Session == nil {
		return nil, errors.New("no active session")
	}
	now := time.Now()
	advance := s.state.Settings.SkipAdvancesCounter
	d := Decision{
		PhotoID:         photoID,
		PrevState:       p.State,
		NewState:        p.State,
		Timestamp:       now,
		PrevLastSeenAt:  p.LastSeenAt,
		Skipped:         true,
		AdvancedCounter: advance,
	}
	p.LastSeenAt = now

	cap := s.state.Session.Target
	if cap <= 0 {
		cap = 5
	}
	rs := append(s.state.Session.RecentlySkipped, photoID)
	if len(rs) > cap {
		rs = rs[len(rs)-cap:]
	}
	s.state.Session.RecentlySkipped = rs
	s.state.Session.Stack = append(s.state.Session.Stack, d)
	if advance {
		s.state.Session.Done++
		s.state.DailyStats[now.Format("2006-01-02")]++
	}
	if err := s.saveLocked(); err != nil {
		return nil, err
	}
	return &d, nil
}

// RecordClusterDecision applies a set of per-member transitions atomically:
// for each member, the caller supplies the desired NewState and (for trash
// targets) the TrashFrom/TrashTo paths. The whole apply counts as one
// session decision; undo restores every member in reverse order.
func (s *Store) RecordClusterDecision(clusterID string, ops []ClusterMemberOp) (*Decision, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state.Session == nil {
		return nil, errors.New("no active session")
	}
	now := time.Now()
	// Snapshot prev state before mutating so undo is sound.
	for i := range ops {
		p, ok := s.state.Photos[ops[i].PhotoID]
		if !ok {
			return nil, fmt.Errorf("photo %s not found", ops[i].PhotoID)
		}
		ops[i].PrevState = p.State
		ops[i].PrevKeepCount = p.KeepCount
		ops[i].PrevUnsureCount = p.UnsureCount
		ops[i].PrevLastSeenAt = p.LastSeenAt
	}
	for _, op := range ops {
		p := s.state.Photos[op.PhotoID]
		switch op.NewState {
		case StateKept:
			p.KeepCount++
		case StateTrashed:
			p.TrashedPath = op.TrashTo
		}
		p.State = op.NewState
		p.LastDecisionAt = now
		p.LastSeenAt = now
	}
	d := Decision{
		PhotoID:   clusterID,
		Timestamp: now,
		Cluster:   ops,
	}
	s.state.Session.Done++
	s.state.Session.Stack = append(s.state.Session.Stack, d)
	s.state.DailyStats[now.Format("2006-01-02")]++
	if err := s.saveLocked(); err != nil {
		return nil, err
	}
	return &d, nil
}

// SetPhotoState changes a photo's state outside of any session — used by
// the duplicates resolution flow. Does NOT touch the session undo stack
// or session.Done; bumps daily decision count.
func (s *Store) SetPhotoState(id string, newState PhotoState, trashTo string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.state.Photos[id]
	if !ok {
		return fmt.Errorf("photo %s not found", id)
	}
	now := time.Now()
	switch newState {
	case StateKept:
		p.KeepCount++
	case StateUnsure:
		p.UnsureCount++
	case StateTrashed:
		p.TrashedPath = trashTo
	}
	p.State = newState
	p.LastDecisionAt = now
	p.LastSeenAt = now
	s.state.DailyStats[now.Format("2006-01-02")]++
	return s.saveLocked()
}

// Undo pops the most recent decision and reverts the photo (or whole
// cluster). The caller is responsible for any filesystem rollback (moving
// files back from trash) — Undo returns the popped Decision so the handler
// can iterate Cluster / replay TrashFrom-TrashTo restores.
func (s *Store) Undo() (*Decision, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state.Session == nil || len(s.state.Session.Stack) == 0 {
		return nil, errors.New("nothing to undo")
	}
	last := s.state.Session.Stack[len(s.state.Session.Stack)-1]
	s.state.Session.Stack = s.state.Session.Stack[:len(s.state.Session.Stack)-1]
	day := last.Timestamp.Format("2006-01-02")

	switch {
	case last.Skipped:
		// Restore the LastSeenAt; counters and state weren't touched.
		if p, ok := s.state.Photos[last.PhotoID]; ok {
			p.LastSeenAt = last.PrevLastSeenAt
		}
		// Also remove from RecentlySkipped (last occurrence).
		rs := s.state.Session.RecentlySkipped
		for i := len(rs) - 1; i >= 0; i-- {
			if rs[i] == last.PhotoID {
				s.state.Session.RecentlySkipped = append(rs[:i], rs[i+1:]...)
				break
			}
		}
		if last.AdvancedCounter {
			if s.state.Session.Done > 0 {
				s.state.Session.Done--
			}
			if s.state.DailyStats[day] > 0 {
				s.state.DailyStats[day]--
			}
		}
	case last.Cluster != nil:
		// Reverse-iterate; restore each member.
		for i := len(last.Cluster) - 1; i >= 0; i-- {
			op := last.Cluster[i]
			p, ok := s.state.Photos[op.PhotoID]
			if !ok {
				continue
			}
			p.State = op.PrevState
			p.KeepCount = op.PrevKeepCount
			p.UnsureCount = op.PrevUnsureCount
			p.LastSeenAt = op.PrevLastSeenAt
			if op.NewState == StateTrashed {
				p.TrashedPath = ""
			}
		}
		if s.state.Session.Done > 0 {
			s.state.Session.Done--
		}
		if s.state.DailyStats[day] > 0 {
			s.state.DailyStats[day]--
		}
	default:
		p, ok := s.state.Photos[last.PhotoID]
		if !ok {
			return nil, fmt.Errorf("photo %s not found", last.PhotoID)
		}
		p.State = last.PrevState
		p.KeepCount = last.PrevKeepCount
		p.UnsureCount = last.PrevUnsureCount
		p.LastSeenAt = last.PrevLastSeenAt
		if last.NewState == StateTrashed {
			p.TrashedPath = ""
		}
		if s.state.Session.Done > 0 {
			s.state.Session.Done--
		}
		if s.state.DailyStats[day] > 0 {
			s.state.DailyStats[day]--
		}
	}

	if err := s.saveLocked(); err != nil {
		return nil, err
	}
	return &last, nil
}

func (s *Store) Settings() Settings {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.state.Settings
}

func (s *Store) UpdateSettings(ns Settings) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state.Settings = ns
	return s.saveLocked()
}

func (s *Store) DailyCount(day string) int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.state.DailyStats[day]
}

// Counts returns (unsorted, kept, unsure, trashed).
func (s *Store) Counts() (int, int, int, int) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var u, k, m, t int
	for _, p := range s.state.Photos {
		switch p.State {
		case StateUnsorted:
			u++
		case StateKept:
			k++
		case StateUnsure:
			m++
		case StateTrashed:
			t++
		}
	}
	return u, k, m, t
}

func randID() (string, error) {
	b := make([]byte, 9)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
