package store

import (
	"crypto/sha256"
	"encoding/base64"
	"time"
)

type PhotoState string

const (
	StateUnsorted PhotoState = "unsorted"
	StateKept     PhotoState = "kept"
	StateUnsure   PhotoState = "unsure"
	StateTrashed  PhotoState = "trashed"
)

type Photo struct {
	ID             string     `json:"id"`
	Path           string     `json:"path"`
	State          PhotoState `json:"state"`
	KeepCount      int        `json:"keep_count"`
	UnsureCount    int        `json:"unsure_count"`
	LastSeenAt     time.Time  `json:"last_seen_at,omitempty"`
	LastDecisionAt time.Time  `json:"last_decision_at,omitempty"`
	Locked         bool       `json:"locked,omitempty"`
	SizeBytes      int64      `json:"size_bytes"`
	AddedAt        time.Time  `json:"added_at"`
	TrashedPath    string     `json:"trashed_path,omitempty"`

	// DHash is a perceptual fingerprint used for near-duplicate detection.
	// 0 paired with a zero DHashedAt means "not yet computed"; a non-zero
	// DHashedAt with DHash==0 means "computation failed, do not retry".
	DHash     uint64    `json:"dhash,omitempty"`
	DHashedAt time.Time `json:"dhashed_at,omitempty"`

	// Time is the photo's capture time, used to restrict near-duplicate
	// clustering to photos taken close together. Populated from file mtime
	// at scan; EXIF DateTimeOriginal is not yet read.
	Time time.Time `json:"time,omitempty"`
}

// PhotoID derives a stable URL-safe id from the relative path.
func PhotoID(relPath string) string {
	h := sha256.Sum256([]byte(relPath))
	return base64.RawURLEncoding.EncodeToString(h[:12])
}
