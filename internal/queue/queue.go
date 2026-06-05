package queue

import (
	"errors"
	"math"
	"math/rand"
	"sync"
	"time"

	"photoSwipe/internal/store"
)

// ErrNoCandidate is returned when no eligible photo can be selected.
var ErrNoCandidate = errors.New("no candidate photos")

type Selector struct {
	mu  sync.Mutex
	rnd *rand.Rand
}

func New() *Selector {
	return &Selector{rnd: rand.New(rand.NewSource(time.Now().UnixNano()))}
}

// Next selects the next photo according to the resurface algorithm.
//
// Each photo gets a weight based on its state:
//   - unsorted: 1.0
//   - kept:     base_rate * decay^keep_count
//   - unsure:   unsure_base_rate
//
// The weight is zeroed out if the photo is locked, has hit the lock threshold,
// or was decided on within the cooldown window. The composition mix scales
// new-vs-resurface weights to bias the session.
func (s *Selector) Next(photos []*store.Photo, sess *store.Session, settings store.Settings, now time.Time) (*store.Photo, error) {
	if sess == nil {
		return nil, errors.New("no active session")
	}

	newMult, resurfaceMult := mixWeights(sess.Mix)
	cooldown := time.Duration(settings.CooldownHours * float64(time.Hour))

	type cand struct {
		photo  *store.Photo
		weight float64
	}
	var pool []cand
	var total float64

	for _, p := range photos {
		w := weightFor(p, settings, newMult, resurfaceMult, cooldown, now)
		if w <= 0 {
			continue
		}
		pool = append(pool, cand{photo: p, weight: w})
		total += w
	}

	if len(pool) == 0 {
		return nil, ErrNoCandidate
	}

	s.mu.Lock()
	target := s.rnd.Float64() * total
	s.mu.Unlock()

	for _, c := range pool {
		target -= c.weight
		if target <= 0 {
			return c.photo, nil
		}
	}
	return pool[len(pool)-1].photo, nil
}

func weightFor(p *store.Photo, settings store.Settings, newMult, resurfaceMult float64, cooldown time.Duration, now time.Time) float64 {
	if p.State == store.StateTrashed || p.Locked {
		return 0
	}
	if settings.LockThreshold > 0 && p.KeepCount >= settings.LockThreshold {
		return 0
	}
	if !p.LastDecisionAt.IsZero() && now.Sub(p.LastDecisionAt) < cooldown {
		return 0
	}

	switch p.State {
	case store.StateUnsorted:
		return newMult * 1.0
	case store.StateKept:
		return resurfaceMult * settings.BaseRate * math.Pow(settings.Decay, float64(p.KeepCount))
	case store.StateUnsure:
		return resurfaceMult * settings.UnsureBaseRate * math.Pow(settings.Decay, float64(p.UnsureCount))
	}
	return 0
}

func mixWeights(mix store.CompositionMix) (newMult, resurfaceMult float64) {
	switch mix {
	case store.MixAllNew:
		return 1.0, 0
	case store.MixHeavyReview:
		return 0.3, 2.5
	case store.MixReviewOnly:
		return 0, 1.0
	case store.MixMixed:
		fallthrough
	default:
		return 1.0, 1.0
	}
}

// EstimateCounts is a debugging helper that counts how many photos are
// currently eligible by category.
func EstimateCounts(photos []*store.Photo, sess *store.Session, settings store.Settings, now time.Time) (newCount, keptCount, unsureCount int) {
	if sess == nil {
		return
	}
	newMult, resurfaceMult := mixWeights(sess.Mix)
	cooldown := time.Duration(settings.CooldownHours * float64(time.Hour))
	for _, p := range photos {
		if weightFor(p, settings, newMult, resurfaceMult, cooldown, now) <= 0 {
			continue
		}
		switch p.State {
		case store.StateUnsorted:
			newCount++
		case store.StateKept:
			keptCount++
		case store.StateUnsure:
			unsureCount++
		}
	}
	return
}
