# Operations

How to run, expose, back up, and troubleshoot a deployed instance.

## Deployment options

### Docker Compose (recommended)

```sh
export PHOTOSWIPE_PASSWORD=changeme
export PHOTOSWIPE_PHOTOS_PATH=/srv/photos
docker compose up -d --build
```

`compose.yaml` pins:

- bind to `127.0.0.1:8080` only — put it behind your reverse proxy
- `read_only: true` on the container root filesystem
- `cap_drop: [ALL]` and `no-new-privileges:true`
- `user: ${UID}:${GID}` so the container writes the photos with your host UID

### Plain Docker

```sh
docker build -t photoswipe:latest .
docker run -d \
  --name photoswipe \
  --read-only \
  --cap-drop ALL \
  --security-opt no-new-privileges:true \
  -p 127.0.0.1:8080:8080 \
  -e PHOTOSWIPE_PASSWORD=changeme \
  -v /srv/photos:/photos:rw \
  --user "$(id -u):$(id -g)" \
  photoswipe:latest
```

### Bare Nix (no container)

```sh
nix develop --command \
  bash -c 'PHOTOSWIPE_PASSWORD=changeme go run . -photo-dir /srv/photos'
```

For a long-running deployment, build a static binary and put it under
systemd:

```sh
nix develop --command \
  bash -c 'CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /usr/local/bin/photoswipe .'
```

A minimal unit:

```ini
# /etc/systemd/system/photoswipe.service
[Service]
ExecStart=/usr/local/bin/photoswipe
Environment=PHOTOSWIPE_PASSWORD=changeme
Environment=PHOTOSWIPE_PHOTO_DIR=/srv/photos
Environment=PHOTOSWIPE_ADDR=127.0.0.1:8080
DynamicUser=yes
ProtectSystem=strict
ReadWritePaths=/srv/photos
NoNewPrivileges=true
ProtectHome=true
PrivateTmp=true
Restart=on-failure

[Install]
WantedBy=multi-user.target
```

## Exposing to your phone

The app binds to `127.0.0.1:8080` by default — invisible outside the host.
Pick one of:

- **Reverse proxy + LAN-only DNS** (Caddy/Nginx/Traefik on the same host,
  TLS terminated upstream). The cookie is automatically marked `Secure`
  when `r.TLS != nil` is true, which it will be behind a proxy that
  terminates TLS *to the app* — but most reverse proxies don't proxy TLS to
  the upstream. The cookie still works as `HttpOnly` + `SameSite=Lax`; it
  just won't carry the `Secure` flag. That's acceptable on a LAN.
- **Tailscale / WireGuard** with the app bound to the VPN interface.
- **Cloudflare Tunnel / Tailscale Funnel** if you really want it on the
  open internet (and you trust your password — consider rotating it; see
  [updating.md](updating.md)).

The viewport meta tag disables pinch-zoom and pull-to-refresh, so the
swipe gestures feel native on mobile Safari and Chrome.

## What lives where on disk

```
<photo-dir>/
├── .photosort-state.json   ← single source of truth for decisions + hashes
├── .trash/                 ← files moved here on swipe-left or trash_all
│   ├── holiday/IMG_0001.jpg
│   └── ...
├── .thumbs/                ← cached JPEG thumbs (regenerable, safe to nuke)
│   ├── <id>-600.jpg
│   └── ...
├── holiday/                ← your folder structure is preserved
│   ├── IMG_0001.jpg
│   └── ...
└── ...
```

The `.thumbs/` directory is safe to delete at any time — thumbnails
regenerate on next request. Sizing: roughly the count of distinct
`(photo, width)` pairs you've viewed in the duplicates UI, each ~30-150 KB.

The scan ignores any directory whose name starts with `.` (so
`.git/`, `.cache/`, `.thumbnails/`, etc. are silently skipped).

## Backups

The **only** file you need to back up to preserve decision history is:

```
<photo-dir>/.photosort-state.json
```

The photo files themselves are separate from the state. If you back up the
photos with rsync/restic/Borg/etc., include the `.trash/` subfolder if you
want trashed photos to survive a restore.

A reasonable rotation:

```sh
# daily snapshot of the state file
cp /srv/photos/.photosort-state.json \
   /srv/backups/photosort-state.$(date +%Y%m%d).json
```

The state file is a self-contained JSON document; you can hand-edit it if
needed (stop the app first — writes aren't coordinated through any IPC
beyond the in-process mutex).

## Restoring trashed files

Two ways:

- **In the session**: hit Undo (`Z` or the Undo button). Works as long as
  the session that made the decision is still active and the stack still
  contains it.
- **After the session ended**: files in `<photo-dir>/.trash/` keep their
  original relative paths. Move them back manually, then click
  *Rescan photos directory* on the session-start screen — they'll
  re-appear as unsorted. The state file still has them marked `trashed`,
  but the rescan upserts based on path so they'll re-enter the pool.

If you want to make this less manual, an empty-trash script could simply
delete everything under `<photo-dir>/.trash/` after a grace period.

## Logs and observability

The app writes to stdout/stderr in Go's default `log` format. Useful lines:

| Log                                            | Meaning                                          |
|------------------------------------------------|--------------------------------------------------|
| `photoSwipe starting — photos=... addr=...`   | Boot                                              |
| `scan: N new, M total`                         | Initial or rescan: new photos found / total seen  |
| `indexer: started`                             | Background dHash indexer is running               |
| `indexer: <path>: <error>`                     | One photo couldn't be hashed (will not retry)     |
| `indexer: stopped (hashed N this run)`         | Indexer exited cleanly at shutdown                |
| `restore failed: ...`                          | Undo could not move a trashed file back           |
| `shutting down`                                | Received SIGINT/SIGTERM, draining HTTP            |

There are no metrics endpoints by design. If you need request-level
observability, put it in your reverse proxy.

## Troubleshooting

### "PHOTOSWIPE_PASSWORD must be set and at least 6 characters"

Set it, longer than 5 chars. The compose file requires it via
`${PHOTOSWIPE_PASSWORD:?...}` so the container won't start without it.

### "photo not found" on `/decision`

The state in memory doesn't match what the browser sent — usually because
the state file was rewritten by hand or the container restarted mid-session.
Reload the page; the home handler picks a fresh photo.

### Photos show but rotate the wrong way

There is no EXIF-orientation handling in v1 (intentional — see
[architecture.md](architecture.md) "Roadmap"). Modern browsers honor
the orientation tag *if the original JPEG is served unmodified*, which is
exactly what `/photo/{id}` does. If you still see wrong rotations, it
means your browser ignores EXIF orientation (rare on modern mobile). The
workaround is to re-encode the offending files with the orientation
baked into the pixels — e.g. `exiftool -all= -tagsfromfile @ -Orientation`
externally.

### "move to trash: rename ...: invalid cross-device link"

The trash directory must live on the same filesystem as the photos for
`os.Rename` to work atomically. By default it's `<photo-dir>/.trash`,
which is always same-fs. If you set `-trash-dir` to a different mount,
move it back.

### The container can't write to `/photos`

Compose passes `user: "${UID}:${GID}"` — make sure that UID actually owns
the host directory. If you run with the default container user (UID 65532)
you'll need to `chown -R 65532:65532 /srv/photos`. This is one of the
ways the container's read-only rootfs surfaces: the only writable place
is the photos volume, and it has to accept the runtime UID.

### htmx isn't loading

Check the browser console for an integrity-mismatch error. If the file in
`web/static/htmx.min.js` doesn't match the SHA-384 SRI in
`web/templates/layout.html`, browsers refuse to execute it. See
[updating.md](updating.md) — "Updating htmx".

### Duplicate clusters keep coming back after I resolved them

Refresh the page once. `/duplicates` is stateless on the server — the
"skipped" set is round-tripped in the URL, so a back-button can
re-show a cluster you skipped. The only way to permanently make a
cluster disappear is to apply one of the non-skip actions
(`keep_best`, `keep_all`, `trash_all`), which change underlying photo
states.

### The duplicates page says "Indexing in background" for hours

Each photo costs roughly *decode + 9×8 resize* — fast for normal JPEGs
but minutes per huge RAW or for misbehaving files. Check the log for
`indexer:` lines. Photos that fail to decode are skipped and won't be
retried. To see exact counts, the page shows `<hashed> / <total>`.

### Wrong files are getting clustered

Two knobs in **settings**:

- Lower `dupe_threshold` (down to e.g. 6) — only nearly-identical
  photos cluster. Burst shots usually differ by ≤ 4 bits.
- Set `dupe_time_window_hours` to a non-zero value (e.g. 24 for
  "same day", 0.1 for "burst-only"). Photos taken further apart
  than the window will not be compared even if their hashes match.

### Session got stuck on the summary page

If `/` keeps showing the summary even after the unsorted pool refills,
your active session is set to a `target` already met. Click *Continue +N*
or *Done* — the latter clears the session and `/` will offer to start a
new one.
