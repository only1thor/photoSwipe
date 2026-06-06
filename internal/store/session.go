package store

import "time"

type CompositionMix string

const (
	MixAllNew      CompositionMix = "all_new"
	MixMixed       CompositionMix = "mixed"
	MixHeavyReview CompositionMix = "heavy_review"
	MixReviewOnly  CompositionMix = "review_only"
)

func (m CompositionMix) Valid() bool {
	switch m {
	case MixAllNew, MixMixed, MixHeavyReview, MixReviewOnly:
		return true
	}
	return false
}

// Decision is a single user choice, stored on the undo stack.
//
// Three shapes:
//   - Regular decision: PrevState/NewState/counters; Skipped=false, Cluster=nil.
//   - Skip: Skipped=true; PrevState == NewState == StateUnsorted; no counter changes.
//   - Cluster apply: Cluster != nil; PhotoID is the cluster ID; per-member
//     restore info lives in Cluster; single Session.Done bump for the lot.
type Decision struct {
	PhotoID         string             `json:"photo_id"`
	PrevState       PhotoState         `json:"prev_state"`
	NewState        PhotoState         `json:"new_state"`
	Timestamp       time.Time          `json:"timestamp"`
	PrevKeepCount   int                `json:"prev_keep_count,omitempty"`
	PrevUnsureCount int                `json:"prev_unsure_count,omitempty"`
	PrevLastSeenAt  time.Time          `json:"prev_last_seen_at,omitempty"`
	TrashFrom       string             `json:"trash_from,omitempty"`
	TrashTo         string             `json:"trash_to,omitempty"`
	Skipped         bool               `json:"skipped,omitempty"`
	AdvancedCounter bool               `json:"advanced_counter,omitempty"`
	Cluster         []ClusterMemberOp  `json:"cluster,omitempty"`
}

// ClusterMemberOp records one photo's transition inside a cluster apply,
// enough to undo it (state + counts + trash file move).
type ClusterMemberOp struct {
	PhotoID         string     `json:"photo_id"`
	PrevState       PhotoState `json:"prev_state"`
	NewState        PhotoState `json:"new_state"`
	PrevKeepCount   int        `json:"prev_keep_count,omitempty"`
	PrevUnsureCount int        `json:"prev_unsure_count,omitempty"`
	PrevLastSeenAt  time.Time  `json:"prev_last_seen_at,omitempty"`
	TrashFrom       string     `json:"trash_from,omitempty"`
	TrashTo         string     `json:"trash_to,omitempty"`
}

type Session struct {
	ID        string         `json:"id"`
	StartedAt time.Time      `json:"started_at"`
	Target    int            `json:"target"` // 0 = open-ended
	Done      int            `json:"done"`
	Mix       CompositionMix `json:"mix"`
	Stack     []Decision     `json:"stack"`
	// RecentlySkipped is a short FIFO of photo IDs the user just skipped.
	// The queue excludes these from candidate selection so a skip doesn't
	// immediately resurface the same photo. Capped at the session target
	// (or a small constant when open-ended).
	RecentlySkipped []string `json:"recently_skipped,omitempty"`
}

// Stale returns true if the session was started more than maxAge ago.
func (s *Session) Stale(now time.Time, maxAge time.Duration) bool {
	return now.Sub(s.StartedAt) > maxAge
}

// Complete returns true if a finite-target session has reached its target.
func (s *Session) Complete() bool {
	return s.Target > 0 && s.Done >= s.Target
}

type Settings struct {
	BaseRate         float64 `json:"base_rate"`
	Decay            float64 `json:"decay"`
	UnsureBaseRate   float64 `json:"unsure_base_rate"`
	CooldownHours    float64 `json:"cooldown_hours"`
	LockThreshold    int     `json:"lock_threshold"`
	FatigueNudge     bool    `json:"fatigue_nudge"`
	FatigueThreshold int     `json:"fatigue_threshold"`
	// DupeThreshold is the maximum Hamming distance (out of 128, summed
	// across both planes of the combined H+V dHash) between two photos
	// for them to be considered near-duplicates. Lower = stricter.
	DupeThreshold int `json:"dupe_threshold"`
	// DupeTimeWindowHours restricts near-duplicate comparison to photos
	// taken within this many hours of each other. 0 disables the window
	// (compare all photos against each other).
	DupeTimeWindowHours float64 `json:"dupe_time_window_hours"`

	// DefaultBatchSize is the target a freshly auto-created session is
	// given when the user lands on `/` with no active session.
	DefaultBatchSize int `json:"default_batch_size"`
	// DefaultMix is the composition mix applied to auto-created sessions.
	DefaultMix CompositionMix `json:"default_mix"`
	// SkipAdvancesCounter decides whether a skip eats one of the batch
	// slots (true, default — predictable count) or pulls in a replacement
	// (false — skip is truly free).
	SkipAdvancesCounter bool `json:"skip_advances_counter"`
}

func DefaultSettings() Settings {
	return Settings{
		BaseRate:            0.15,
		Decay:               0.4,
		UnsureBaseRate:      0.6,
		CooldownHours:       6,
		LockThreshold:       0,
		FatigueNudge:        false,
		FatigueThreshold:    100,
		DupeThreshold:       35,
		DefaultBatchSize:    10,
		DefaultMix:          MixMixed,
		SkipAdvancesCounter: true,
	}
}
