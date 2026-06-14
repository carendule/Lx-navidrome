# Lx-Navidrome Docker build & push

This directory contains the build & push scripts for shipping
Lx-Navidrome as a Docker image to Docker Hub (or any other
OCI registry).

## Files

| File | Purpose |
|------|---------|
| `Dockerfile.lx` | Multi-stage Dockerfile (Go build → Debian runtime, includes Node.js v24) |
| `build-docker.sh` | One-shot builder with preflight checks |
| `push-docker.sh` | One-shot pusher with auth handling |
| `setup-worktree.sh` | (pre-existing) Worktree helper, unrelated to Docker |

## What's bundled in the runtime image

| Package | Why |
|---------|-----|
| `ffmpeg` | Embed pipeline (cover resize, USLT post-pass, transcoding) |
| `libwebp7` + `libwebpdemux2` + `libwebpmux3` | Native WebP encoding via dlopen (binary is dynamically linked) |
| `nodejs` (v24) | The `ScriptExecutor` in `script_executor.go` prefers Node.js for running user-supplied lx-music scripts. When node is missing it falls back to a goja (pure-Go JS) sandbox, but a number of common scripts depend on Node-specific globals and fail under goja. |
| `ca-certificates` | HTTPS for online-source fetchers |
| `sqlite3` | CLI for ad-hoc inspection of the data dir |
| `tini` | Signal forwarding for `docker stop` |

## Quick start

```bash
# 1. Make sure Docker is installed and the daemon is running.
docker info

# 2. Make sure the UI is pre-built (mandatory; the Go
#    compile fails otherwise because ui/embed.go uses
#    //go:embed build/*).
(cd ui && npm ci && npm run build)

# 3. Build the image.
./scripts/build-docker.sh

# 4. Push to Docker Hub.
DOCKERHUB_USER=yourname ./scripts/push-docker.sh
```

## One-shot build + push

```bash
PUSH=1 DOCKERHUB_USER=yourname ./scripts/build-docker.sh
```

The script will prompt for the DockerHub token on the
first push. To script it in CI, set `DOCKERHUB_TOKEN` as
an env var too.

## Multi-arch builds

By default the script builds for the host architecture.
To build for a different platform, pass `PLATFORM`:

```bash
PLATFORM=linux/arm64 ./scripts/build-docker.sh
```

Supported platforms (via the upstream `xx`-style cross
compile in `Dockerfile.lx`): `linux/amd64`, `linux/arm64`,
`linux/arm/v7`. The upstream multi-arch `Dockerfile`
covers more targets if you need them — just `docker build
-f Dockerfile .` instead.

## Why a separate Dockerfile.lx

The upstream `Dockerfile` at the repo root is the
navidrome.org release pipeline's multi-arch builder:

- It builds the UI in a `node:lts-alpine` stage
- It cross-compiles to 11 platforms with `xx` + `osxcross`
- It uses `gcr.io/distroless/static` for the final image

All of that is overkill for a self-hosted deployment.
`Dockerfile.lx` is the leaner, single-arch, Debian-slim
variant that produces a ~150MB image with the same
runtime surface.

## CI

The CI workflow (`.github/workflows/pipeline.yml`)
already builds navidrome on push to master and on tags;
for Docker Hub release builds, add a `release.yml` that
calls `./scripts/build-docker.sh` + `./scripts/push-docker.sh`
on `v*` tags. A minimal example:

```yaml
name: Docker Release
on:
  push:
    tags: ['v*']
jobs:
  release:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v6
      - name: Set up BuildKit
        run: echo "DOCKER_BUILDKIT=1" >> $GITHUB_ENV
      - name: Login to Docker Hub
        uses: docker/login-action@v4
        with:
          username: ${{ secrets.DOCKERHUB_USER }}
          password: ${{ secrets.DOCKERHUB_TOKEN }}
      - name: Build and push
        run: PUSH=1 DOCKERHUB_USER=${{ secrets.DOCKERHUB_USER }} ./scripts/build-docker.sh
```

## Troubleshooting

### "ui/build/index.html is missing"
Run `cd ui && npm ci && npm run build` first. The
Go compile uses `//go:embed build/*` so an empty
`build/` directory is a hard build failure.

### "permission denied" on the Docker socket
Add your user to the `docker` group:
```bash
sudo usermod -aG docker $USER
# then log out and back in
```
Or run the script with `sudo`.

### "denied: requested access to the resource is denied"
You're not logged in to the registry. Either:
- Run `docker login` first
- Or set `DOCKERHUB_USER` + `DOCKERHUB_TOKEN` before
  invoking the script
- Make sure the token has `Read, Write, Delete` scope
  (not just `Read`)

### Multi-arch build fails on a non-x86 host
The script uses `docker build` (not `buildx`) for the
single-arch case. If your host is arm64 and you want
to cross-build an amd64 image, install buildx and
set `PLATFORM=linux/amd64` — the script auto-detects
this and switches to buildx.
