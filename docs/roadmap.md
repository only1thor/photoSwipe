# Roadmap

Things deliberately left out of v1, grouped by area. Each item lists *why*
it was deferred and a sketch of what would change to implement it — so
future-you (or a contributor) can pick one up without re-deriving the
trade-offs.

Nothing here is a commitment. Items above the line are likely to land
sooner because they unblock real workflows; items below are nice-to-haves
or substantial undertakings.

---

## ★ Likely to land first

### EXIF: orientation-aware thumbnails

**Status:** v1 ships raw images on `/photo/{id}` (orientation preserved
by browser) but the `/thumb/{id}` endpoint re-encodes after resize and
loses the orientation tag. Cluster grids therefore show portrait
phone shots rotated.

**Cost:** ~30 lines of pure-Go JPEG marker walking to read the EXIF
Orientation tag (tag 0x0112) from the APP1 segment, plus a rotate/flip
step before encoding. No new dependency.

**Where:** `internal/img/thumb.go` (`scaleTo` → `scaleAndOrient`). Pull
orientation from `image.DecodeConfig`'s metadata if we vendor a small
EXIF reader, or hand-roll the marker walk in `internal/img/exif.go`.

**Knock-on:** the `/meta/{id}` endpoint could surface the EXIF capture
time, camera, lens, and f-stop in the same panel.

---

### EXIF DateTimeOriginal for the clustering time window

**Status:** `Photo.Time` is set from file mtime, which is correct for
unmodified imports (rsync `-t`, photo backup apps) but wrong for files
that have been re-saved by an editor or downloaded fresh.

**Cost:** if we land the orientation reader above, capture time is a
sibling tag (0x9003) — same parser, ~5 extra lines.

**Where:** `internal/img/scan.go` — prefer EXIF DateTimeOriginal,
fall back to mtime. Backfill via a one-shot migration on rescan.

**Knock-on:** more accurate clustering windows; same-day grouping
becomes meaningful even after a re-export.

---

### Trashed-photo browse / restore / empty UI

**Status:** files in `.trash/` are recoverable from the filesystem and
in-session via Undo, but there is no UI for browsing them after the
session ends.

**Cost:** small. One new page `/trash` lists trashed photos as a grid
(reuse the thumb endpoint), with per-photo *Restore* and a top-level
*Empty trash* button.

**Where:** new `handleTrash` + `handleTrashRestore` + `handleTrashEmpty`;
new template `trash.html`. `Store.SetPhotoState(id, StateUnsorted, "")`
already exists for the restore case; for hard-delete we'd add
`Store.ForgetPhoto(id)` and an `os.Remove` call.

**Safety:** *Empty trash* should require typing the word "empty" or a
confirm dialog — it's the one truly irreversible action.

---

### Background thumbnail prewarm

**Status:** thumbnails generate on first request, which is fine for
the swipe flow (raw images) but noticeable on `/duplicates` when first
opening a big cluster.

**Cost:** modest. The indexer goroutine already walks every photo for
dHash; extending it to also write a `<id>-600.jpg` thumbnail is a few
extra lines.

**Where:** `internal/indexer/indexer.go`. Reuse `img.ServeThumb`'s
generation path, write directly to the cache without serving.

**Trade-off:** disk usage. For 10k photos × ~80 KB per 600 px thumb
that's ~800 MB. Make it opt-in via a setting.

---

### In-session duplicate hint

**Status:** the swipe queue and the duplicates view are independent
flows. If you're swiping a photo that's part of a cluster you've
already kept members of, you don't know.

**Cost:** small. The card-render handler can check whether the
current photo's dHash has known cluster-mates in `kept`/`unsure`
state, and surface a badge like *"3 similar already kept · review →"*.

**Where:** `internal/handlers/handlers.go` `handleHome` /
`handleDecision` — call `dupes.Find` filtered to the current photo's
cluster. Cache the cluster-of-current-photo per request.

**UX consideration:** the hint must not block the swipe — it's a
nudge, not an interrupt.

---

## More substantial / lower priority

### HEIC / HEIF support (iPhone photos)

**Why deferred:** HEIC decode requires either `libheif` (C dependency,
breaks `FROM scratch`) or a pure-Go decoder (none mature). Either path
adds significant attack surface, contradicting the project's hardening
posture.

**Two viable paths:**

1. **Distroless instead of scratch.** Switch the final container stage
   to `gcr.io/distroless/cc-debian12` and install `libheif`. Loses a
   small amount of hardening (libc is now present) for broad iPhone
   compatibility.
2. **Sidecar pre-conversion.** A separate, optional binary watches the
   photos dir and converts HEIC → JPEG before the main app sees them.
   Keeps the web app pure-Go and `FROM scratch`. Most operationally
   complex; gives the user explicit control over what gets converted.

**Recommendation:** option 1 behind a build flag (`-tags heic`) so the
default image stays minimal and HEIC users opt in with their own build.

### RAW (CR2, NEF, ARW, DNG…) support

**Why deferred:** same reason as HEIC plus much larger format zoo.
Realistic path: shell out to `dcraw` / `libraw` in a sidecar (option 2
above). Out of scope for v1's "single binary" promise.

### Pinch-zoom and pan on the swipe card

**Why deferred:** the v1 zoom is binary — tap-and-hold toggles a
fullscreen view at the image's native size. No pan.

**Cost:** moderate. Either add a small dependency (e.g. a tiny
pan-zoom JS library, vendored single file with SRI) or implement
two-finger pinch + drag manually in `app.js`. ~150 lines of careful
event handling for the manual route.

### Favorite/star gesture

**Why deferred:** v1 has three positive outcomes (keep, unsure) and one
negative (trash). A "favorite" 4-state would crowd the gesture model
and need a new visual badge.

**If added:** swipe-down or double-tap for favorite. New `StateStarred`
distinct from `StateKept`. New mix option `favorites_only`. Card badge.

### Side-by-side cluster comparison

**Why deferred:** the v1 grid is fine for 2-4 photos but compressing
6+ photos into a phone screen means tiny thumbs. A dedicated
side-by-side comparator (swap the active pair with arrows, A/B testing
style) would handle this better.

**Cost:** new page `/duplicates/compare?cluster=…`, JS to swap pairs.

### Multi-user accounts

**Why deferred:** v1 is single-user-by-design — one password, one
photos directory, one decision history. Multi-user adds account
management, per-user state files, isolation testing, and password
reset flows.

**If added:** put user identity into `auth` package, namespace the
state file (`.photosort-state.<user>.json` or per-user directories),
add account CRUD pages. Probably 500+ LOC and shifts the project's
shape considerably.

### Trust-the-reverse-proxy auth mode

**Why deferred:** v1 always enforces its own password. Users behind
Tailscale/Cloudflare-Tunnel/Authelia don't need it.

**Cost:** small. Add `-trust-proxy-header` flag/env that, when set,
treats requests bearing the specified header (e.g.
`X-Authenticated-User`) as authenticated and skips the password gate.
Documented separately because mis-configuring it would expose the
app — the header must only be set by the proxy, never trusted from
arbitrary clients.

### Pre-trash grid review

**Why deferred:** user explicitly chose immediate-trash semantics for
the swipe flow during the design discussion (Undo + filesystem-level
recovery were judged sufficient).

**If added:** new state `StateTrashPending`, batched apply at session
end, separate review screen before the move. Substantial behavioral
change — don't do this silently.

### Stats / charts page

**Why deferred:** v1 surfaces `today_count` and `done` only. No
trends, no per-folder breakdown, no progress chart.

**Cost:** moderate. `daily_stats` already exists in the state file —
just needs a render. Pure-CSS sparkline or a vendored tiny chart lib.

### Watch-folder ingest

**Why deferred:** v1 picks up new photos on `/rescan` (manual) or app
restart. A long-running deployment should ideally detect new files
without intervention.

**Cost:** small. `fsnotify` is a maintained pure-Go inotify wrapper
with a small dep tree. Trigger `img.Scan` debounced.

### LSH / BK-tree for very large libraries

**Why deferred:** the current clustering is O(N²) or O(N·W) with a
time window. For 100k+ photos without a time window, this hits ~10s
of CPU per `/duplicates` page render.

**Cost:** moderate. A BK-tree built once per page load brings
near-duplicate lookup to O(log N) average. Pure Go, no deps. Worth
doing if benchmarks show it matters; meaningless for libraries under
~20k photos.

### Server-sent events for indexer progress

**Why deferred:** v1 shows indexer progress as a counter in the page
header that updates on refresh. No live updates.

**Cost:** small. An `/events` SSE endpoint that emits `hash_progress`
and `cluster_added` events. Frontend uses `EventSource` (built into
browsers, no JS deps).

**Trade-off:** adds an idle long-lived connection per open tab. Mild.

### Decision-history CSV export

**Why deferred:** users who want analytics on their sorting can
already read the JSON state file.

**Cost:** tiny. New endpoint that emits CSV from the photos map.

### Metrics endpoint

**Why deferred:** the project explicitly disclaims metrics in
[`operations.md`](operations.md) — for request-level observability,
put it in the reverse proxy.

**If added:** Prometheus-compatible `/metrics` exposing `hash_total`,
`decisions_total{action=...}`, `clusters_open`. Pure-Go via
`expvar` would avoid a dependency.

### REST/JSON API for external tools

**Why deferred:** v1 is server-rendered HTML for a single user. An
API surface is a different shape of project.

**If added:** version the API path (`/api/v1/...`), gate with the
same cookie auth (or add bearer token support), document the schema
separately. Significant effort and an ongoing maintenance burden.

---

## Not on the roadmap

These came up during design and were deliberately decided against, not
just deferred. Reopening them needs a fresh design discussion.

- **AI-based similarity** (CLIP embeddings, cloud APIs). dHash is
  deterministic, free, and fast. Embeddings are a different project.
- **Photo editing in-app** (crop, rotate, color). Out of scope —
  use a real editor; this is a sorter.
- **Cloud storage backends** (S3, Google Photos). v1 is local-files.
  Adding storage abstractions would inflate the codebase substantially
  for a feature most self-hosters don't want.
- **Browser-side decoding for HEIC**. Would shift complexity into JS
  with a heavy WASM module, contradicting the supply-chain posture.
