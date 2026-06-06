// Package dupes groups photos into near-duplicate clusters based on their
// dHash fingerprints and an optional capture-time window.
package dupes

import (
	"sort"
	"time"

	"photoSwipe/internal/dhash"
	"photoSwipe/internal/store"
)

// Cluster is a set of mutually-similar photos. Photos is sorted by SizeBytes
// descending so the first entry is the largest (a reasonable "best" default).
// ID is the lexicographically smallest photo ID in the cluster, giving each
// cluster a stable identity across runs.
type Cluster struct {
	ID     string
	Photos []*store.Photo
}

// Find scans photos and returns clusters of near-duplicates whose pairwise
// Hamming distance is ≤ distanceThreshold. timeWindow (if non-zero)
// restricts comparison to pairs whose Photo.Time differs by less than the
// window — photos with a zero Time fall back to "no window" for that pair.
//
// Only photos that have been successfully hashed and are not in StateTrashed
// participate. Singleton groups are dropped.
func Find(photos []*store.Photo, distanceThreshold int, timeWindow time.Duration) []Cluster {
	// Snapshot eligible photos.
	var hashed []*store.Photo
	for _, p := range photos {
		if p.State == store.StateTrashed {
			continue
		}
		if p.DHashedAt.IsZero() || p.HashVersion < store.CurrentHashVersion {
			continue
		}
		if p.DHash == 0 && p.DHashV == 0 {
			continue
		}
		hashed = append(hashed, p)
	}
	if len(hashed) < 2 {
		return nil
	}

	// Sort by Time (zero values sort first). This enables the sliding-window
	// break-out below when a window is configured.
	sort.SliceStable(hashed, func(i, j int) bool {
		return hashed[i].Time.Before(hashed[j].Time)
	})

	// Union-Find.
	parent := make(map[string]string, len(hashed))
	for _, p := range hashed {
		parent[p.ID] = p.ID
	}
	var find func(string) string
	find = func(x string) string {
		if parent[x] != x {
			parent[x] = find(parent[x])
		}
		return parent[x]
	}
	union := func(a, b string) {
		ra, rb := find(a), find(b)
		if ra == rb {
			return
		}
		// Make the smaller ID the root so the cluster ID is stable.
		if ra < rb {
			parent[rb] = ra
		} else {
			parent[ra] = rb
		}
	}

	for i := 0; i < len(hashed); i++ {
		for j := i + 1; j < len(hashed); j++ {
			if timeWindow > 0 && !hashed[i].Time.IsZero() && !hashed[j].Time.IsZero() {
				if hashed[j].Time.Sub(hashed[i].Time) > timeWindow {
					break // sorted by Time: further j's are even farther
				}
			}
			ha := dhash.Hash{H: hashed[i].DHash, V: hashed[i].DHashV}
			hb := dhash.Hash{H: hashed[j].DHash, V: hashed[j].DHashV}
			if dhash.Distance(ha, hb) <= distanceThreshold {
				union(hashed[i].ID, hashed[j].ID)
			}
		}
	}

	// Group by root.
	groups := make(map[string][]*store.Photo)
	for _, p := range hashed {
		root := find(p.ID)
		groups[root] = append(groups[root], p)
	}

	var out []Cluster
	for root, members := range groups {
		if len(members) < 2 {
			continue
		}
		sort.SliceStable(members, func(i, j int) bool {
			if members[i].SizeBytes != members[j].SizeBytes {
				return members[i].SizeBytes > members[j].SizeBytes
			}
			return members[i].ID < members[j].ID
		})
		out = append(out, Cluster{ID: root, Photos: members})
	}

	// Largest clusters first; ties broken by ID for stable ordering.
	sort.SliceStable(out, func(i, j int) bool {
		if len(out[i].Photos) != len(out[j].Photos) {
			return len(out[i].Photos) > len(out[j].Photos)
		}
		return out[i].ID < out[j].ID
	})
	return out
}
