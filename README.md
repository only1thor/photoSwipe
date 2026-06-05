# photoSwipe

Self-hosted, single-binary webapp for sorting a photo collection one image at
a time — swipe right to keep, left to trash, up if unsure. Kept photos can
re-emerge for review with a confidence-decay algorithm.

Designed for minimal supply-chain attack surface: pure Go, one direct
dependency (`golang.org/x/image`), vendored htmx pinned with SRI, and a
`FROM scratch` container image.

## Features

- **Swipe / keyboard / button** gestures, responsive on mobile + desktop.
- **Resurface algorithm.** Kept photos re-appear with weight
  `base_rate · decay^keep_count`, gated by a cooldown. The more confirms,
  the less often they come back.
- **Unsure pile** behaves the same way with a higher base rate.
- **Sessions** with photo-count targets (10 / 25 / 50 / 100 / custom / ∞)
  and a composition slider (new / mixed / heavy review / review only).
- **Undo stack** scoped to the session (unlimited depth), including
  trash restoration from the `.trash/` folder.
- **Tap-and-hold / spacebar** to zoom.
- **EXIF-less metadata panel** — dimensions, file size, mtime (HEIC and
  full EXIF intentionally deferred; both would break `FROM scratch`).
- **Single-password auth** with constant-time compare and HTTP-only cookies.

## Quick start

```sh
# 1. populate htmx (committed file; this verifies the hash)
./scripts/vendor-htmx.sh

# 2. dev shell (Nix flake)
nix develop

# 3. run against your photo directory
PHOTOSWIPE_PASSWORD=changeme go run . -photo-dir /path/to/photos
```

Then open <http://localhost:8080>.

## Docker

```sh
PHOTOSWIPE_PASSWORD=changeme \
PHOTOSWIPE_PHOTOS_PATH=/path/to/photos \
  docker compose up --build
```

The container runs `FROM scratch` with a read-only root filesystem, no
capabilities, and `no-new-privileges`. The only writable mount is `/photos`,
where deletions are moved into a `.trash/` subfolder.

## Configuration

| Flag / Env | Default | Notes |
|---|---|---|
| `-photo-dir` / `PHOTOSWIPE_PHOTO_DIR` | `/photos` | host folder mounted into the container |
| `-trash-dir` / `PHOTOSWIPE_TRASH_DIR` | `<photo-dir>/.trash` | where swipe-left moves files |
| `-addr` / `PHOTOSWIPE_ADDR` | `:8080` | listen address |
| `-password` / `PHOTOSWIPE_PASSWORD` | — | required, min 6 chars |

State is stored in `<photo-dir>/.photosort-state.json` — back this up if you
want decision history to survive a wipe.

## Hardening recap

- Single direct dependency: `golang.org/x/image` (maintained by the Go team).
- htmx vendored as one file (`web/static/htmx.min.js`), pinned with SHA-256
  in `scripts/vendor-htmx.sh` and SHA-384 SRI in `web/templates/layout.html`.
- Build: `CGO_ENABLED=0`, stripped, trimpath.
- Runtime image: `FROM scratch`, non-root (UID 65532), read-only rootfs in
  Compose, no Linux capabilities, no shell.
- HTTP: password gate on every route except `/login` and `/static/`; cookies
  are `HttpOnly`, `SameSite=Lax`, `Secure` when TLS is detected; 32-byte
  random session tokens with constant-time password compare.
- Filesystem: photo paths are joined under `photo-dir` and rejected if they
  escape via `..`.

## Roadmap

Intentionally out of scope for v1 (each would add either a C dependency or
a substantial pure-Go surface — open an issue if you want them):

- HEIC/HEIF (needs libheif)
- Full EXIF parsing & orientation-aware thumbnails
- Near-duplicate clustering (perceptual hashing)
- Multi-user accounts
- Trashed-photo grid review before the move (we trash immediately by design)

## Docs

- [`docs/architecture.md`](docs/architecture.md) — data model, resurface
  algorithm with worked examples, request flow, security model.
- [`docs/operations.md`](docs/operations.md) — deployment recipes (Compose,
  Docker, systemd), backups, troubleshooting.
- [`docs/updating.md`](docs/updating.md) — how to bump htmx, Go, the one
  dep, the container, and the on-disk state schema.

## Layout

```
photoSwipe/
├── main.go                  entrypoint, config, embed web/
├── internal/
│   ├── store/               JSON sidecar, Photo/Session/Decision
│   ├── queue/               resurface algorithm + tests
│   ├── img/                 scan, metadata, trash move/restore
│   ├── auth/                password gate + cookie session
│   └── handlers/            HTTP routes, template loading
├── web/
│   ├── templates/           html/template files (layout + pages + fragments)
│   └── static/              htmx.min.js (vendored), app.css, app.js
├── scripts/vendor-htmx.sh   re-vendor with hash verification
├── flake.nix                Nix devshell (go, gopls, gotools, git, curl, jq)
├── Dockerfile               multi-stage → FROM scratch
└── compose.yaml             example deployment with hardening flags
```
