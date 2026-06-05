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
type Decision struct {
	PhotoID         string     `json:"photo_id"`
	PrevState       PhotoState `json:"prev_state"`
	NewState        PhotoState `json:"new_state"`
	Timestamp       time.Time  `json:"timestamp"`
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
	// DupeThreshold is the maximum Hamming distance (out of 64) between two
	// dHashes for the photos to be considered near-duplicates.
	DupeThreshold int `json:"dupe_threshold"`
	// DupeTimeWindowHours restricts near-duplicate comparison to photos
	// taken within this many hours of each other. 0 disables the window
	// (compare all photos against each other).
	DupeTimeWindowHours float64 `json:"dupe_time_window_hours"`
}

func DefaultSettings() Settings {
	return Settings{
		BaseRate:         0.15,
		Decay:            0.4,
		UnsureBaseRate:   0.6,
		CooldownHours:    6,
		LockThreshold:    0,
		FatigueNudge:     false,
		FatigueThreshold: 100,
		DupeThreshold:    10,
	}
}
