#!/usr/bin/env bash
#
# scripts/build-docker.sh — one-shot Docker image builder for
# the Lx-Navidrome fork.
#
# Usage:
#   ./scripts/build-docker.sh                          # build local/latest
#   ./scripts/build-docker.sh v0.5.0                   # build with a tag
#   IMAGE=ghcr.io/me/lxnav DOCKER_TAG=me/lxnav:dev \
#     ./scripts/build-docker.sh                        # custom image+tag
#
# Environment knobs:
#   DOCKER_TAG     Target image:tag (default: lxnavidrome:VERSION)
#   IMAGE          DockerHub repo (default: extracted from DOCKER_TAG)
#   PUSH           Set to "1" to also push after build
#   PLATFORM       Multi-arch target (default: host arch via buildx)
#   GIT_SHA        Override the embedded git SHA
#   GIT_TAG        Override the embedded git tag
#   SKIP_UI_CHECK  Set to "1" to skip the ui/build/ sanity check
#   NO_CACHE       Set to "1" to disable Docker build cache
#   CUSTOM_CA_CERT_FILE  Optional path to a PEM/CRT root CA file to trust
#                        inside Docker build stages (for corporate MITM/proxy)
#
# The script does NOT need sudo if your user is in the
# `docker` group. If you get "permission denied" on the
# Docker socket, either:
#   1. Add yourself to the docker group: `sudo usermod -aG docker $USER`
#   2. Or invoke the script with sudo: `sudo ./scripts/build-docker.sh`
#
# After the build, run `./scripts/push-docker.sh` to push
# the image to Docker Hub.

set -euo pipefail

# --- locate repo root --------------------------------------------------------
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
cd "${REPO_ROOT}"

# --- argument parsing -------------------------------------------------------
#
# VERSION semantics:
#  - First CLI arg OR $VERSION env var: explicit version
#    string the user wants this image to be tagged with
#    (e.g. "v1.0.0", "1.0.0", "1.0.0-rc1"). The script
#    uses this verbatim, so you can produce a clean
#    release image even when your working tree is dirty.
#  - Empty (default): the script auto-detects from git.
#    - When the working tree is CLEAN, the result is the
#      latest tag (e.g. "v1.0.0") — a release-quality tag.
#    - When the working tree is DIRTY, git describe
#      appends "-dirty" to the tag, so the result is
#      e.g. "v1.0.0-dirty". The "-dirty" suffix is a
#      strong signal that this image is a dev snapshot
#      built from a non-clean checkout and should NOT
#      be pushed to a release tag.
#
# We do NOT strip "-dirty" — it would let users
# accidentally push a dev build to a release tag by
# forgetting the dirty state. The suffix is preserved
# in the docker tag so "docker images" surfaces the
# dev/release distinction at a glance.
#
# When the working tree is clean and you want a release
# build, just `git commit` first; no need to pass a
# version explicitly.
VERSION="${1:-${VERSION:-}}"
if [[ -z "${VERSION}" ]]; then
  if command -v git >/dev/null 2>&1 && git rev-parse --git-dir >/dev/null 2>&1; then
    VERSION="$(git describe --tags --always --dirty 2>/dev/null || echo "dev")"
  else
    VERSION="dev"
  fi
fi

# Strip leading "v" from the tag if present. Docker tags
# conventionally don't have it. We keep the original
# version (with the v) in the GIT_TAG ldflag so the
# binary's own --version output reads "v1.0.0".
VERSION_CLEAN="${VERSION#v}"

# --- image / tag derivation -------------------------------------------------
DOCKER_TAG="${DOCKER_TAG:-lxnavidrome:${VERSION_CLEAN}}"
IMAGE="${IMAGE:-${DOCKER_TAG%%:*}}"
GIT_SHA="${GIT_SHA:-$(git rev-parse --short HEAD 2>/dev/null || echo unknown)}"
GIT_TAG="${GIT_TAG:-${VERSION}}"
PLATFORM="${PLATFORM:-}"
# Optional flags. These must default to empty / unset so
# the conditional checks (`[[ "${SKIP_UI_CHECK}" == "1" ]]`,
# `[[ "${NO_CACHE}" == "1" ]]`, `[[ "${PUSH}" == "1" ]]`)
# work without tripping `set -u` on a never-set variable.
SKIP_UI_CHECK="${SKIP_UI_CHECK:-}"
NO_CACHE="${NO_CACHE:-}"
PUSH="${PUSH:-}"
CUSTOM_CA_CERT_FILE="${CUSTOM_CA_CERT_FILE:-}"
CUSTOM_CA_CERT_B64=""

# --- pre-flight checks ------------------------------------------------------
echo "==> Lx-Navidrome Docker build"
echo "    version     : ${VERSION}"
echo "    image       : ${DOCKER_TAG}"
echo "    git sha     : ${GIT_SHA}"
echo "    git tag     : ${GIT_TAG}"
echo "    platform    : ${PLATFORM:-<host default>}"
# Surface the dev-vs-release distinction in big letters
# so a careless push to a release tag is hard to miss.
if [[ "${VERSION}" == *-dirty || "${VERSION}" == *-SNAPSHOT ]]; then
  echo
  echo "    ┌──────────────────────────────────────────────────────────────┐"
  echo "    │  DEV BUILD — the working tree has uncommitted changes.    │"
  echo "    │  Do NOT push this image to a release tag.                  │"
  echo "    │  To make a release build: commit your changes first, then  │"
  echo "    │  re-run without the -dirty suffix appearing in the tag.    │"
  echo "    └──────────────────────────────────────────────────────────────┘"
  echo
fi
echo

# 1. ui/build/ must contain an index.html, otherwise the
#    `//go:embed build/*` directive in ui/embed.go will
#    fail to compile. The directory might be a stale
#    `.gitkeep`-only checkout; in that case we'd build
#    a binary that has no UI assets at all and the
#    browser shows a blank page.
if [[ "${SKIP_UI_CHECK}" != "1" ]]; then
  if [[ ! -f "${REPO_ROOT}/ui/build/index.html" ]]; then
    echo "ERROR: ui/build/index.html is missing." >&2
    echo "       The UI must be pre-built before the Go compile step." >&2
    echo "       Run:  cd ui && npm ci && npm run build" >&2
    echo "       Then re-run this script." >&2
    exit 1
  fi

  # 1b. ui/build/ can exist but still be stale. If any source
  #     file in ui/src or ui/public is newer than build/index.html,
  #     the embedded UI in the image will miss recent frontend changes.
  UI_BUILD_INDEX="${REPO_ROOT}/ui/build/index.html"
  UI_BUILD_MTIME="$(stat -c %Y "${UI_BUILD_INDEX}" 2>/dev/null || echo 0)"
  UI_SRC_MTIME="$(
    {
      find "${REPO_ROOT}/ui/src" -type f -printf '%T@\n' 2>/dev/null || true
      find "${REPO_ROOT}/ui/public" -type f -printf '%T@\n' 2>/dev/null || true
      stat -c %Y "${REPO_ROOT}/ui/package.json" 2>/dev/null || true
      stat -c %Y "${REPO_ROOT}/ui/package-lock.json" 2>/dev/null || true
      stat -c %Y "${REPO_ROOT}/ui/pnpm-lock.yaml" 2>/dev/null || true
      stat -c %Y "${REPO_ROOT}/ui/yarn.lock" 2>/dev/null || true
    } | awk 'BEGIN { max=0 } { t=int($1); if (t>max) max=t } END { print max }'
  )"

  if [[ -z "${UI_SRC_MTIME}" ]]; then
    UI_SRC_MTIME=0
  fi

  if (( UI_SRC_MTIME > UI_BUILD_MTIME )); then
    echo "ERROR: ui/build/ is stale compared to ui/src or ui/public." >&2
    echo "       Your recent frontend changes are not in the embedded assets." >&2
    echo "       Run:  cd ui && npm ci && npm run build" >&2
    echo "       Then re-run this script." >&2
    exit 1
  fi

  echo "    ui/build/    : OK ($(du -sh ui/build 2>/dev/null | cut -f1))"
fi

# The upstream .dockerignore excludes ui/build/, which
# would break our build (the Go //go:embed directive
# requires ui/build/ in the build context). We swap
# .dockerignore with our override file for the duration
# of the build (see the "Swapping .dockerignore" block
# below) and restore the upstream file on exit.
# Make sure the override file exists.
if [[ ! -f "${REPO_ROOT}/.dockerignore.lx" ]]; then
  echo "ERROR: .dockerignore.lx is missing from the repo root." >&2
  echo "       This file is required so the build context includes" >&2
  echo "       ui/build/ (needed for the //go:embed directive)." >&2
  exit 1
fi
echo "    dockerignore : .dockerignore.lx (overrides upstream .dockerignore during build)"

# 2. Docker must be installed and the daemon must be reachable.
if ! command -v docker >/dev/null 2>&1; then
  echo "ERROR: docker is not installed." >&2
  echo "       On Debian/Ubuntu:  sudo apt install docker.io" >&2
  echo "       On macOS:          brew install --cask docker" >&2
  echo "       On Windows:        install Docker Desktop" >&2
  exit 1
fi
if ! docker info >/dev/null 2>&1; then
  echo "ERROR: cannot reach the Docker daemon." >&2
  echo "       Start Docker Desktop, or:  sudo systemctl start docker" >&2
  echo "       Verify your user is in the 'docker' group, or run as root." >&2
  exit 1
fi

# 3. buildx is required for --ignorefile (used below)
#    and the Dockerfile's BuildKit-only --mount=type=cache
#    / --mount=type=bind syntax. Docker 23+ ships buildx
#    by default; on older daemons install it via
#    `docker buildx install`. We don't need DOCKER_BUILDKIT=1
#    explicitly — buildx always uses BuildKit.
if ! docker buildx version >/dev/null 2>&1; then
  echo "ERROR: docker buildx is not installed." >&2
  echo "       Install with:  docker buildx install" >&2
  echo "       Or upgrade Docker Desktop / docker-ce to >= 23.0" >&2
  exit 1
fi
echo "    buildx       : $(docker buildx version 2>&1 | head -1)"

if [[ -n "${CUSTOM_CA_CERT_FILE}" ]]; then
  if [[ ! -f "${CUSTOM_CA_CERT_FILE}" ]]; then
    echo "ERROR: CUSTOM_CA_CERT_FILE does not exist: ${CUSTOM_CA_CERT_FILE}" >&2
    exit 1
  fi
  CUSTOM_CA_CERT_B64="$(base64 -w 0 "${CUSTOM_CA_CERT_FILE}")"
  echo "    custom CA    : ${CUSTOM_CA_CERT_FILE}"
fi

# 4. Detect host architecture for the buildx target. The
#    default buildx builder uses the host arch, so we
#    just need to communicate the right TARGETARCH
#    arg to the Dockerfile.
HOST_ARCH="$(uname -m)"

# --- build -------------------------------------------------------------------
#
# We always invoke `docker buildx build`, not the legacy
# `docker build`. Two reasons:
#
# 1. The Dockerfile uses BuildKit-only features (`--mount=type=cache`,
#    `--mount=type=bind`) that the legacy builder rejects.
# 2. The `--ignorefile` flag is buildx-only; the legacy builder
#    only consults `.dockerignore` at the build context root.
#
# On Docker >= 23, `docker build` is an alias for `docker buildx build`
# when BuildKit is enabled, so the legacy invocation usually "works"
# but silently ignores BuildKit-only flags. We invoke buildx
# explicitly to make the flag behavior deterministic across versions.
#
# When the user does NOT pass PLATFORM, we let buildx auto-detect
# the host arch (the same as `docker build` would have done).
# When PLATFORM is set explicitly, we use it for cross-compile.

# Build a "host arch" string in buildx's linux/<arch> format
# for the auto-detect case. We can't trust the
# `uname -m | sed` trick (x86_64 != amd64) so map explicitly.
case "${HOST_ARCH}" in
  x86_64)  HOST_ARCH_BUILDX="linux/amd64" ;;
  aarch64) HOST_ARCH_BUILDX="linux/arm64" ;;
  armv7l)  HOST_ARCH_BUILDX="linux/arm/v7" ;;
  armv6l)  HOST_ARCH_BUILDX="linux/arm/v6" ;;
  *)       HOST_ARCH_BUILDX="linux/${HOST_ARCH}" ;;
esac

# If the user didn't pass PLATFORM, default to the host arch
# (buildx auto-detect also does this, but being explicit avoids
# the "what platform did we just build for?" surprise).
if [[ -z "${PLATFORM}" ]]; then
  PLATFORM="${HOST_ARCH_BUILDX}"
fi
TARGETARCH="${PLATFORM##*/}"  # e.g. amd64 from linux/amd64
echo "    target arch  : ${TARGETARCH} (from ${PLATFORM})"
echo

BUILD_ARGS=(
  --build-arg "GIT_SHA=${GIT_SHA}"
  --build-arg "GIT_TAG=${GIT_TAG}"
  --build-arg "TARGETARCH=${TARGETARCH}"
  --build-arg "CUSTOM_CA_CERT_B64=${CUSTOM_CA_CERT_B64}"
  --tag "${DOCKER_TAG}"
  --file "Dockerfile.lx"
  --platform "${PLATFORM}"
  --label "org.opencontainers.image.revision=${GIT_SHA}"
  --label "org.opencontainers.image.version=${VERSION_CLEAN}"
  --label "build.timestamp=$(date -u +%Y-%m-%dT%H:%M:%SZ)"
)

# Cross-platform builds produce a manifest-list image rather
# than a runnable single-arch image. The --load flag forces
# buildx to materialize the requested platform's image into
# the local docker daemon (instead of leaving it as a
# manifest in the buildx cache). For native host builds this
# is a no-op; for cross builds it's required for `docker run`
# to work.
if [[ "${PLATFORM}" != "${HOST_ARCH_BUILDX}" ]]; then
  echo "==> Cross-platform build: ${PLATFORM} (host is ${HOST_ARCH_BUILDX})"
  BUILD_ARGS+=(--load)
fi

if [[ "${NO_CACHE}" == "1" ]]; then
  BUILD_ARGS+=(--no-cache)
  echo "==> Build cache disabled (NO_CACHE=1)"
fi

# Buildx doesn't accept --ignorefile (a buildkit feature
# that was never ported to buildx). The standard buildx
# build reads `.dockerignore` from the build context root.
# Our `.dockerignore.lx` overrides the upstream
# `.dockerignore` so ui/build/ is allowed in the context
# (mandatory for the `//go:embed build/*` directive in
# ui/embed.go). We swap them for the duration of the build
# and restore the upstream file on the way out so a
# subsequent `docker build -f Dockerfile` (the multi-arch
# release pipeline) still works correctly.
echo
echo "==> Swapping .dockerignore for the duration of the build"
if [[ -f ".dockerignore" && ! -f ".dockerignore.upstream" ]]; then
  cp .dockerignore .dockerignore.upstream
fi
cp .dockerignore.lx .dockerignore

# We always restore on exit (success or failure) so the
# repo is left in a clean state. The trap fires on the
# normal exit path AND on Ctrl-C / build failure.
ORIG_CWD="${PWD}"
restore_dockerignore() {
  cd "${ORIG_CWD}" || true
  if [[ -f .dockerignore.upstream ]]; then
    mv .dockerignore.upstream .dockerignore
    echo "==> Restored .dockerignore"
  else
    # No upstream was stashed (e.g. the user removed it
    # before our run); just delete the .dockerignore we
    # wrote so the working tree is clean.
    rm -f .dockerignore
  fi
}
trap restore_dockerignore EXIT

echo "==> Running: docker buildx build ${BUILD_ARGS[*]} ."
echo

# We stream the build output to stdout. The user can
# interrupt with Ctrl-C and the partial layers will be
# cached for the next attempt.
docker buildx build "${BUILD_ARGS[@]}" .

# --- post-build verification -------------------------------------------------
echo
echo "==> Build complete: ${DOCKER_TAG}"
echo "    image id    : $(docker images --no-trunc --quiet "${DOCKER_TAG}" | head -1)"

# Quick sanity check: the image should be runnable and
# report a version. We don't start the server (that would
# block), just invoke --version which exits immediately.
echo "    version     : $(docker run --rm "${DOCKER_TAG}" --version 2>&1 | head -1)"

# --- optional: push ----------------------------------------------------------
if [[ "${PUSH}" == "1" ]]; then
  echo
  echo "==> PUSH=1 detected, pushing ${DOCKER_TAG}"
  "${SCRIPT_DIR}/push-docker.sh" "${DOCKER_TAG}"
fi

echo
echo "Done. To run:"
echo "    docker run -d --name lxnavidrome -p 4533:4533 \\"
echo "        -v /path/to/music:/music \\"
echo "        -v /path/to/data:/data \\"
echo "        ${DOCKER_TAG}"
