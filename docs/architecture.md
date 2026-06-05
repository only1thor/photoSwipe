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
│                     ├─ internal/img     (scan, trash move)   │
│                     └─ internal/auth    (password, cookie)   │
└─────────────────────────▲────────────────────────────────────┘
                          │  filesystem only
┌─────────────────────────┴────────────────────────────────────┐
│  <photo-dir>/                                                │
│    .photosort-state.json     ← all decisions + settings      │
│    .trash/                   ← swiped-left files (immediate) │
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
      "trashed_path": ""             // set when state=trashed
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
    "fatigue_threshold": 100
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
| `/meta/{id}`        | GET    | **fragment** `meta` (size, dims, mtime)           |
| `/settings`         | GET    | full page                                         |
| `/settings`         | POST   | 303 → `/settings`                                 |
| `/rescan`           | POST   | tiny HTML status fragment                         |
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

See [`security.md`](#) — TODO — for the full threat model. The headline
non-mitigation: this is a single-user, password-gated webapp intended for
LAN use. It is not hardened against a logged-in attacker abusing decision
endpoints, nor against an OS-level adversary with access to the photos
directory.

## Build & deployment shape

- **Binary**: `~13 MB`, stripped, `CGO_ENABLED=0`, static.
- **Container**: multi-stage `golang:1.23-alpine` → `FROM scratch`,
  user `65532:65532`, `/photos` is the only volume.
- **Runtime memory**: tens of MB plus whatever Go decodes for image
  metadata (only the header is read; pixel data is streamed to the
  client by `http.ServeFile`).
- **Disk writes**: every decision writes `.photosort-state.json` (~few
  KB to low MB depending on photo count).
