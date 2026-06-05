# Update procedures

How to bump each component without breaking the security posture or losing
state.

The general rule: every "update" should leave the binary still buildable
with `go build` from a clean checkout, still passing `go test ./...`, and
still producing a `FROM scratch` container.

## Update htmx

htmx is vendored as a single file with both a SHA-256 verifier in the
script and a SHA-384 SRI inlined in `layout.html`. To upgrade:

1. **Pick the target version** — keep it on the 2.x major line until you
   are ready to audit a 4.x beta. Latest releases:
   <https://github.com/bigskysoftware/htmx/releases>

2. **Edit `scripts/vendor-htmx.sh`** — bump `HTMX_VERSION`, leave the
   `EXPECTED_SHA*` placeholders for now:

   ```sh
   HTMX_VERSION="2.0.10"
   EXPECTED_SHA256=""
   EXPECTED_SHA384_B64=""
   ```

3. **First run will fail with a hash mismatch.** That's by design — it
   prints the actual hashes so you can transcribe them after manually
   verifying against an independent source (the release notes, a second
   download from a different mirror, your package mirror, …):

   ```sh
   ./scripts/vendor-htmx.sh
   # ERROR: SHA-256 mismatch
   #   expected:
   #   actual:   <sha256>
   ```

4. **Cross-check the hash.** Compare against the GitHub release page's
   "Assets" view or download the same file from
   `https://unpkg.com/htmx.org@<version>/dist/htmx.min.js` and confirm
   both hashes match. If they don't, do not proceed.

5. **Paste the hashes back** into `scripts/vendor-htmx.sh`:

   ```sh
   EXPECTED_SHA256="<the actual sha256>"
   EXPECTED_SHA384_B64="<the actual sha384 base64>"
   ```

6. **Re-run the script.** It should now succeed and write
   `web/static/htmx.min.js`.

7. **Update the SRI in `web/templates/layout.html`** — paste the new
   `EXPECTED_SHA384_B64` after `sha384-`:

   ```html
   <script src="/static/htmx.min.js"
           integrity="sha384-<the actual sha384 base64>"
           defer></script>
   ```

8. **Verify in a browser.** Boot the app and confirm there is no
   `Subresource Integrity` error in the console. A mismatch is silent on
   the network tab but loud in the console.

If you ever bump across a major (2.x → 4.x), also read the htmx migration
notes — the `hx-vals`, `HX-Redirect`, and `htmx.ajax` APIs we use are all
v2-stable, but check before assuming compatibility.

## Update Go (toolchain)

The toolchain lives in `flake.nix` — the field is just `go`, which
resolves to whatever the pinned nixpkgs ships. To pin a specific version:

```nix
packages = with pkgs; [ go_1_23 gopls gotools go-tools git curl jq ];
```

To pull a newer nixpkgs revision, update `flake.lock`:

```sh
nix flake update
```

Then bump the minimum in `go.mod` if you want to use newer language
features:

```
go 1.24
```

Verify nothing broke:

```sh
nix develop --command bash -c 'go vet ./... && go test ./...'
```

The `Dockerfile` pins `golang:1.23-alpine` for reproducible container
builds. Bump it in lockstep — keeping the dev shell and the container
image on the same minor avoids "works on my machine" surprises.

## Update `golang.org/x/image`

We have exactly one direct dependency. Bump it deliberately:

```sh
nix develop --command bash -c '
  go get golang.org/x/image@latest
  go mod tidy
  go vet ./... && go test ./...
'
```

Then inspect `go.sum` — the new module version is hash-pinned. Commit
`go.mod` + `go.sum` together. If `go.sum` shows other modules changing,
something has crept into the dep tree; investigate before merging.

You can also enforce hash verification in CI:

```sh
go mod verify
```

## Rebuild the container

```sh
docker compose build --no-cache
docker compose up -d
```

The build is multi-stage and uses BuildKit cache mounts, so a normal
rebuild (`docker compose up --build`) only re-runs steps after the first
file change. `--no-cache` is only useful when you've upgraded the base
image or want to flush stale layers.

To verify the image is still minimal:

```sh
docker history photoswipe:latest
docker image inspect photoswipe:latest --format '{{.Size}}' | numfmt --to=iec
```

A `FROM scratch` image with the stripped Go binary should land around
13–15 MB. If it suddenly jumps to hundreds of MB, the final stage is
probably no longer `FROM scratch` — re-check the `Dockerfile`.

## State schema changes

`.photosort-state.json` carries a `"version": 1` field. When you add or
rename a field in `internal/store/`:

1. **Backwards-compatible field added.** No version bump needed. The
   `encoding/json` Unmarshal will ignore unknown fields on read; old
   files load with the new field at its zero value.

2. **Field renamed or removed.** Bump `stateFileVersion` in `store.go`
   and add a migration branch in `load()`:

   ```go
   switch rs.Version {
   case 0, 1:
       migrateV1ToV2(&rs)
       fallthrough
   case 2:
       // current
   }
   ```

   Migrations should be idempotent — re-running them on an already-v2
   file must be safe. Write the new version back on the next save.

3. **Field semantics changed but name is the same.** Bump the version and
   migrate; do not silently reinterpret old data.

Before deploying a migration, **back up the state file** of any running
instance (just `cp`). The atomic-rename writer means the worst case from
a crash mid-migration is the old file plus a half-written `.tmp`, but a
deliberate copy is cheaper than worrying.

## Settings defaults

The defaults are in `store.DefaultSettings()`. They are applied:

- on first run (no state file exists yet)
- on load when `BaseRate == 0` (treated as uninitialized)

If you change a default after users already have a state file, **their
existing setting wins** — that's by design. To force a settings reset for
a user, delete the `"settings"` key from their state file (and `BaseRate`
will be 0, triggering the defaults branch).

Document the change in the [architecture.md](architecture.md) "Defaults"
table so future-you remembers why a kept photo's weight suddenly halved.

## Rotate the password

The password is checked in constant time against the env var on every
login. To rotate:

1. Set the new `PHOTOSWIPE_PASSWORD` in your `.env` / compose / systemd
   unit.
2. `docker compose up -d --force-recreate` (or `systemctl restart photoswipe`).
3. All in-memory session tokens are dropped on restart — every browser
   gets bounced back to `/login`. There is no "session invalidation"
   endpoint by design.

If you suspect the old password leaked, also delete any reverse-proxy
access logs that recorded the form POST body (most don't log bodies, but
verify).

## Roll back

There is no migration history table. Roll back by:

1. Stopping the app.
2. Restoring `.photosort-state.json` from backup.
3. Restoring the previous binary or container image tag.
4. Restarting.

Because state lives in the photos directory (not in the container), the
state survives downgrade/upgrade cycles trivially.

## Sanity checks before any release

```sh
nix develop --command bash -c '
  set -e
  gofmt -l . | tee /tmp/fmtout
  test ! -s /tmp/fmtout                  # no unformatted files
  go vet ./...
  go test ./...
  go mod verify
'
docker compose build
```

Run a 30-second smoke test against a throwaway photo dir — the README
"Quick start" sequence is enough.
