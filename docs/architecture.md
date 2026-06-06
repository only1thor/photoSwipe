# Architecture

A single Go binary serves a server-rendered HTML UI that uses htmx for
in-place fragment swaps. All state lives in a JSON sidecar file inside the
photos directory. There is no database, no message broker, no build step
on the client.

```
┌──────────────────────────────────────────────────────────────┐
│  Browser (htmx 2.0.9 + ~200 lines of vanilla JS)             │
│  ── touch / keyboard / button → POST /decision               │
│  ── HTML fragment in response → swap #photo-area             │
└─────────────────────────▲────────────────────────────────────┘
                          │  HTTP (single port, password gate)
┌─────────────────────────┴────────────────────────────────────┐
│  photoSwipe binary (Go)                                      │
│  internal/handlers ─┬─ internal/queue   (resurface algo)     │
│                     ├─ internal/store   (state, undo stack)  │
│                     ├─ internal/img     (scan, thumbs, trash)│
│                     ├─ internal/dhash   (perceptual hash)    │
│                     ├─ internal/dupes   (cluster algorithm)  │
│                     ├─ internal/indexer (background hasher)  │
│                     └─ internal/auth    (password, cookie)   │
└─────────────────────────▲────────────────────────────────────┘
                          │  filesystem only
┌─────────────────────────┴────────────────────────────────────┐
│  <photo-dir>/                                                │
│    .photosort-state.json     ← all decisions + settings      │
│    .trash/                   ← swiped-left files (immediate) │
│    .thumbs/                  ← cached JPEG thumbs (lazy)     │
│    holiday/IMG_0001.jpg      ← your photos, untouched        │
│    ...                                                       │
└──────────────────────────────────────────────────────────────┘
```

## Data model

All persistent state is in **one file**: `<photo-dir>/.photosort-state.json`.
It's written atomically (`*.tmp` + rename) on every mutation.

```jsonc
{
  "version": 1,
  "photos": {
    "<id>": {
      "id": "WHBOWFrJPKtJCpb8",     // base64url(sha256(path)[:12])
      "path": "holiday/IMG_0001.jpg", // slash-relative to photo-dir
      "state": "kept",               // unsorted | kept | unsure | trashed
      "keep_count": 2,
      "unsure_count": 0,
      "last_seen_at": "...",
      "last_decision_at": "...",
      "locked": false,               // never resurface if true
      "size_bytes": 4_182_733,
      "added_at": "...",
      "trashed_path": "",            // set when state=trashed
      "dhash": 16028324..,           // 64-bit perceptual fingerprint
      "dhashed_at": "...",           // zero = not yet hashed
      "time": "..."                  // file mtime; used as capture-time proxy
    }
  },
  "session": {                       // null between sessions
    "id": "...",
    "started_at": "...",
    "target": 50,                    // 0 = open-ended
    "done": 12,
    "mix": "mixed",                  // all_new | mixed | heavy_review | review_only
    "stack": [ /* Decision[] — see below */ ]
  },
  "settings": {
    "base_rate": 0.15,               // weight floor for kept photos
    "decay": 0.4,                    // multiplier per keep_count
    "unsure_base_rate": 0.6,
    "cooldown_hours": 6,
    "lock_threshold": 0,             // 0 = no auto-lock
    "fatigue_nudge": false,
    "fatigue_threshold": 100,
    "dupe_threshold": 10,             // max Hamming distance for near-dups
    "dupe_time_window_hours": 0       // 0 = no window; else hours of slack
  },
  "daily_stats": { "2026-06-05": 47 } // for the fatigue nudge
}
```

### Decision (undo stack entry)

Every user choice pushes a `Decision` onto `session.stack`. The full prior
state of the photo is captured so undo is a pure data revert — no replay
needed.

```jsonc
{
  "photo_id": "WHBOWFrJPKtJCpb8",
  "prev_state": "unsorted",
  "new_state": "kept",
  "timestamp": "...",
  "prev_keep_count": 1,
  "prev_unsure_count": 0,
  "prev_last_seen_at": "...",
  "trash_from": "/photos/IMG_0001.jpg", // only when new_state=trashed
  "trash_to":   "/photos/.trash/IMG_0001.jpg"
}
```

Undo also runs the matching filesystem rollback (`RestoreFromTrash`) when
the decision was a trash.

## The resurface algorithm

For each "next photo" request we score every non-trashed, non-locked photo:

```
weight =
    new_mult        if state == unsorted
    resurface_mult * base_rate         * decay^keep_count    if state == kept
    resurface_mult * unsure_base_rate  * decay^unsure_count  if state == unsure
```

Then **zero out** the weight when:

- `locked == true`
- `lock_threshold > 0 && keep_count >= lock_threshold`
- `last_decision_at` is within `cooldown_hours`

…and **weighted-sample one photo** from the remaining pool.

### Composition mix

The `new_mult` and `resurface_mult` scalars vary by `session.mix`:

| Mix             | new_mult | resurface_mult |
|-----------------|----------|----------------|
| `all_new`       | 1.0      | 0              |
| `mixed`         | 1.0      | 1.0            |
| `heavy_review`  | 0.3      | 2.5            |
| `review_only`   | 0        | 1.0            |

### What this means in practice (defaults)

With `base_rate=0.15`, `decay=0.4`, `mix=mixed`:

| keep_count | weight (vs. unsorted = 1.0) | rough probability when 10 unsorted + 1 kept |
|------------|------------------------------|---------------------------------------------|
| 1          | 0.060                        | ~0.6 %                                       |
| 2          | 0.024                        | ~0.24 %                                      |
| 3          | 0.010                        | ~0.10 %                                      |
| 4          | 0.004                        | ~0.04 %                                      |

So a photo confirmed 4 times needs ~250 picks before it's likely to
resurface — effectively locked unless you also lower `cooldown_hours`.

As the unsorted pool shrinks, the relative resurface probability rises
automatically (the total weight goes down, kept weights stay the same).

## Near-duplicate detection

### dHash (`internal/dhash`)

A perceptual fingerprint that's robust to compression, scale changes, and
small edits. Per image:

1. Resize to **9×8 grayscale** via `golang.org/x/image/draw` (bilinear).
2. For each of the 8 rows, compare each pixel to its right neighbour.
3. Emit one bit per comparison (`1` if left brighter than right).
4. Pack the 64 bits into a `uint64`.

Result: `Distance(a, b) := popcount(a XOR b)` → Hamming distance in 0…64.

### Clustering (`internal/dupes`)

`dupes.Find(photos, distanceThreshold, timeWindow) → []Cluster`:

1. **Eligibility filter**: skip trashed photos and photos that haven't
   been hashed yet (zero `DHashedAt`). At least 2 photos required.
2. **Sort by `Photo.Time`** so a time window can be enforced via early
   break-out.
3. **Pairwise union-find**:

   ```go
   for i := 0; i < N; i++ {
     for j := i + 1; j < N; j++ {
       if timeWindow > 0 && time[j] - time[i] > timeWindow { break }
       if dhash.Distance(h[i], h[j]) <= distanceThreshold {
         union(id[i], id[j])
       }
     }
   }
   ```

   The break on time is what turns this from O(N²) into O(N · W) when a
   window is set.
4. **Cluster ID** = lex-smallest member ID, so identity is stable across
   reruns even though union-find iteration order isn't.
5. **Members sorted** by `SizeBytes` desc — the first entry is the
   default "best" pick.
6. Singletons are dropped.

### Background indexer (`internal/indexer`)

A single goroutine started from `main.go` after the initial scan. Loop:

1. Ask the store for the next photo with `DHashedAt.IsZero() &&
   State != trashed`. Snapshot returned (cheap copy).
2. Decode the file header just enough to get an `image.Image`.
3. Compute the dHash; write back via `Store.SetHash(id, hash)`.
4. On any error, mark the photo as `DHash = 0, DHashedAt = now` so we
   don't retry forever — `MarkHashFailed`.
5. When there's nothing to hash, sleep 30 s and try again (picks up new
   photos added by future rescans).
6. On shutdown, `Stop()` closes the channel and waits for the loop to
   exit between iterations.

This makes hashing **eventually consistent**: the duplicates view shows
results for whatever has been indexed so far, with a progress indicator.

### Time window — what about EXIF?

`Photo.Time` is populated from the file's mtime at scan, not from EXIF
DateTimeOriginal. Reasoning: EXIF parsing would add ~80 lines of pure-Go
JPEG marker walking (acceptable) but introduces format-specific quirks
that aren't worth it for v1. mtime tracks well for unmodified imports
(rsync `-t`, photo backup apps); it can be wrong if the file has been
re-saved by an editor. For users with editor-touched archives, set
`dupe_time_window_hours` to 0 to disable the window.

EXIF orientation is also intentionally not handled — see the v1 caveats
in the README. The browser preserves orientation when serving raw
images via `/photo/{id}`; thumbnails via `/thumb/{id}` lose orientation
because we re-encode after resize.

### Thumbnails (`internal/img.ServeThumb`)

Lazy, on-demand JPEG thumbnails for the cluster grid. Sized at most
`ThumbnailMaxSide = 1600` pixels on the longest side. Cached to
`<photo-dir>/.thumbs/<id>-<width>.jpg`. The cache directory starts with
a dot so the scanner skips it. Atomic writes via `*.tmp` + rename, same
pattern as the state file.

The full swipe flow does **not** use thumbnails — it serves the raw
image, so EXIF orientation is preserved.

## Session lifecycle

```
┌──────────────────────────┐
│  No session              │  GET / shows session_start
└─────────┬────────────────┘
          │ POST /session/start
          ▼
┌──────────────────────────┐
│  Active                  │  GET / shows next photo
│  done < target           │  POST /decision → next card
└──────┬────────────┬──────┘
       │            │
done == target     ErrNoCandidate (pool empty)
       │            │
       ▼            ▼
┌──────────────────────────┐
│  Complete                │  GET / shows summary
│  (sess.Complete()==true) │  POST /session/extend → back to Active
└─────────┬────────────────┘  POST /session/end   → No session
          │
          ▼
   (terminal)
```

Sessions older than 24 h are auto-dropped on startup (see
`staleSessionAge` in `internal/store/store.go`). The undo stack lives only
inside the session — ending a session clears it.

## Request flow

The UI is server-rendered HTML; htmx swaps fragments in place rather than
exchanging JSON. There is no SPA router and no client-side state beyond a
transient swipe-gesture buffer.

| Path                | Method | Returns                                           |
|---------------------|--------|---------------------------------------------------|
| `/`                 | GET    | full page: session_start / photo / summary        |
| `/login`            | GET    | login form                                        |
| `/login`            | POST   | sets cookie, 303 → `/`                            |
| `/logout`           | GET    | clears cookie, 303 → `/login`                     |
| `/session/start`    | POST   | 303 → `/`                                         |
| `/session/end`      | POST   | 303 → `/`                                         |
| `/session/extend`   | POST   | 303 → `/`                                         |
| `/next`             | GET    | **fragment** `card` (or `HX-Redirect: /` if done) |
| `/decision`         | POST   | **fragment** `card` (or `HX-Redirect: /` if done) |
| `/undo`             | POST   | **fragment** `card`                               |
| `/photo/{id}`       | GET    | raw image bytes                                   |
| `/thumb/{id}?w=N`   | GET    | JPEG thumbnail (≤1600 px); cached to `.thumbs/`   |
| `/meta/{id}`        | GET    | **fragment** `meta` (size, dims, mtime)           |
| `/settings`         | GET    | full page                                         |
| `/settings`         | POST   | 303 → `/settings`                                 |
| `/rescan`           | POST   | tiny HTML status fragment                         |
| `/duplicates`       | GET    | full page: cluster review or "all clear"          |
| `/duplicates/resolve` | POST | applies `keep_best` / `keep_all` / `trash_all` / `skip`; 303 → `/duplicates` |
| `/static/*`         | GET    | embedded htmx, app.css, app.js                    |

When a decision completes the session, `/decision` returns `204 No Content`
with `HX-Redirect: /`, and htmx navigates to `/` for the full summary page.

## Template loading

`html/template` requires that the `"content"` block be unique per template
set. We load five "page" sets — each is `layout.html` + the page's own
`content` definition + the card fragment — and two standalone "fragment"
templates. See `loadTemplates()` in `internal/handlers/handlers.go`. The
templates are embedded into the binary via `//go:embed all:web` in
`main.go`.

## Security model

| Surface         | Mitigation                                                                 |
|-----------------|----------------------------------------------------------------------------|
| Password        | Constant-time compare (`subtle.ConstantTimeCompare`)                       |
| Session token   | 32 random bytes, base64url, in an in-memory map; cleared on restart        |
| Cookie          | `HttpOnly`, `SameSite=Lax`, `Secure` if `r.TLS != nil`, 30-day expiry      |
| Path traversal  | Photo paths joined under `photo-dir`; prefix-check rejects escapes         |
| Supply chain    | One Go dep + vendored htmx (SRI-pinned); `FROM scratch` image              |
| Trash safety    | Files are moved (not deleted); undo restores within the session            |
| State writes    | `*.tmp` + atomic rename                                                    |
| Concurrency     | `sync.RWMutex` on the store; queue's PRNG protected by its own mutex       |

**Headline non-mitigation**: this is a single-user, password-gated
webapp intended for LAN use. It is not hardened against a logged-in
attacker abusing decision endpoints, nor against an OS-level adversary
with access to the photos directory. Multi-user accounts and a
reverse-proxy trust mode are listed in [`roadmap.md`](roadmap.md).

## Build & deployment shape

- **Binary**: `~13 MB`, stripped, `CGO_ENABLED=0`, static.
- **Container**: multi-stage `golang:1.23-alpine` → `FROM scratch`,
  user `65532:65532`, `/photos` is the only volume.
- **Runtime memory**: tens of MB plus whatever Go decodes for image
  metadata (only the header is read; pixel data is streamed to the
  client by `http.ServeFile`).
- **Disk writes**: every decision writes `.photosort-state.json` (~few
  KB to low MB depending on photo count).
