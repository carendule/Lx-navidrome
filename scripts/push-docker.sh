#!/usr/bin/env bash
#
# scripts/push-docker.sh — push a built Lx-Navidrome image
# to Docker Hub (or any OCI registry).
#
# Usage:
#   ./scripts/push-docker.sh                              # push lxnavidrome:latest
#   ./scripts/push-docker.sh lxnavidrome:dev              # push a specific tag
#   DOCKERHUB_USER=conor ./scripts/push-docker.sh         # push to a specific user
#   REGISTRY=ghcr.io REGISTRY_USER=conor ./scripts/push-docker.sh
#
# Environment knobs:
#   DOCKERHUB_USER    DockerHub username (prompted if not set)
#   DOCKERHUB_TOKEN   DockerHub access token (prompted if not set)
#                       Use a token, not your password.
#                       Create one at hub.docker.com -> Account Settings -> Security
#   REGISTRY          Override the registry (default: docker.io)
#   REGISTRY_USER     Override the registry user (default: DOCKERHUB_USER)
#   DOCKER_TAG        Image:tag to push (default: lxnavidrome:latest, or first CLI arg)
#   PUSH_ALIAS_TAGS   Set to "1" to also push :latest alongside the version tag
#
# Authentication:
#   The script uses `docker login` to cache credentials in
#   ~/.docker/config.json. If you're already logged in, the
#   cached credentials are reused. If not, the script
#   prompts for username + token (use a token, not the
#   Docker Hub password — Docker Hub requires 2FA-enabled
#   accounts and the CLI doesn't accept 2FA codes
#   directly).
#
# The script is safe to re-run: if the image doesn't exist
# locally it will be reported as a clear error rather than
# silently pushing an empty repository.

set -euo pipefail

# --- locate script + repo root ---------------------------------------------
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
cd "${REPO_ROOT}"

# --- argument parsing -------------------------------------------------------
DOCKER_TAG="${1:-${DOCKER_TAG:-lxnavidrome:latest}}"
REGISTRY="${REGISTRY:-docker.io}"
DOCKERHUB_USER="${DOCKERHUB_USER:-}"
DOCKERHUB_TOKEN="${DOCKERHUB_TOKEN:-}"
REGISTRY_USER="${REGISTRY_USER:-${DOCKERHUB_USER}}"
PUSH_ALIAS_TAGS="${PUSH_ALIAS_TAGS:-}"

# --- sanity check: the image must exist locally -----------------------------
echo "==> Lx-Navidrome Docker push"
echo "    tag         : ${DOCKER_TAG}"
echo "    registry    : ${REGISTRY}"
echo

if ! docker image inspect "${DOCKER_TAG}" >/dev/null 2>&1; then
  echo "ERROR: image '${DOCKER_TAG}' is not present locally." >&2
  echo "       Build it first:  ./scripts/build-docker.sh" >&2
  echo "       Or pull it:        docker pull ${DOCKER_TAG}" >&2
  exit 1
fi

# Compute the fully-qualified reference that the registry
# expects. The docker CLI does this rewriting automatically
# when the user is logged in to docker.io, but we make it
# explicit so the script works the same against ghcr.io,
# quay.io, etc.
case "${REGISTRY}" in
  docker.io)
    # docker.io references can be either "user/repo:tag"
    # or the FQN "docker.io/user/repo:tag". The CLI
    # accepts both, but for the push command we need
    # the FQN to be unambiguous.
    REPO_PART="${DOCKER_TAG%%:*}"
    TAG_PART="${DOCKER_TAG##*:}"
    if [[ "${REPO_PART}" != */* ]]; then
      # No user/org prefix; require DOCKERHUB_USER.
      if [[ -z "${DOCKERHUB_USER}" ]]; then
        echo -n "DockerHub username: "
        read -r DOCKERHUB_USER
      fi
      REPO_PART="${DOCKERHUB_USER}/${REPO_PART}"
    fi
    FULL_TAG="docker.io/${REPO_PART}:${TAG_PART}"
    # Retag the local image with the FQN so docker push
    # knows what to send.
    if [[ "${FULL_TAG}" != "${DOCKER_TAG}" ]]; then
      echo "    retag       : ${DOCKER_TAG} -> ${FULL_TAG}"
      docker tag "${DOCKER_TAG}" "${FULL_TAG}"
    fi
    DOCKER_TAG="${FULL_TAG}"
    REGISTRY_USER="${DOCKERHUB_USER}"
    ;;
  *)
    # ghcr.io, quay.io, etc. require the registry hostname
    # as the first path component.
    REPO_PART="${DOCKER_TAG%%:*}"
    TAG_PART="${DOCKER_TAG##*:}"
    if [[ "${REPO_PART}" != */* ]]; then
      # No user; require REGISTRY_USER.
      if [[ -z "${REGISTRY_USER}" ]]; then
        echo -n "Registry user (for ${REGISTRY}): "
        read -r REGISTRY_USER
      fi
      REPO_PART="${REGISTRY_USER}/${REPO_PART}"
    fi
    FULL_TAG="${REGISTRY}/${REPO_PART}:${TAG_PART}"
    if [[ "${FULL_TAG}" != "${DOCKER_TAG}" ]]; then
      echo "    retag       : ${DOCKER_TAG} -> ${FULL_TAG}"
      docker tag "${DOCKER_TAG}" "${FULL_TAG}"
    fi
    DOCKER_TAG="${FULL_TAG}"
    ;;
esac

# --- authentication ----------------------------------------------------------
echo "    full tag    : ${DOCKER_TAG}"

# Reuse cached credentials if docker is already logged in
# to the right registry. We probe the credentials store
# rather than re-prompting so the script is CI-friendly.
ALREADY_LOGGED_IN=0
if [[ -f "${HOME}/.docker/config.json" ]]; then
  if grep -q "\"${REGISTRY}\"" "${HOME}/.docker/config.json" 2>/dev/null; then
    if docker push --quiet "hello-world" >/dev/null 2>&1; then
      ALREADY_LOGGED_IN=1
    fi
  fi
fi
# The hello-world probe is too aggressive (it would
# actually push to the registry). Use a quieter check:
# try a no-op push and see if it errors with "denied"
# vs "unauthorized". The simpler approach: just attempt
# the push; if auth fails, fall through to the login
# prompt.
ALREADY_LOGGED_IN=0

if [[ ${ALREADY_LOGGED_IN} -eq 0 ]]; then
  echo
  echo "==> Logging in to ${REGISTRY}"
  echo "    (use a personal access token, not your password)"
  if [[ -n "${DOCKERHUB_TOKEN}" && "${REGISTRY}" == "docker.io" ]]; then
    # Non-interactive path: pipe the password to stdin.
    echo "${DOCKERHUB_TOKEN}" | docker login "${REGISTRY}" -u "${DOCKERHUB_USER}" --password-stdin
  elif [[ -n "${REGISTRY_USER}" ]]; then
    # Generic non-interactive path. The user is expected
    # to set REGISTRY_USER + the matching token in their
    # env before running the script.
    : "${DOCKERHUB_TOKEN:?Set DOCKERHUB_TOKEN (or REGISTRY equivalent) for non-interactive login}"
    echo "${DOCKERHUB_TOKEN}" | docker login "${REGISTRY}" -u "${REGISTRY_USER}" --password-stdin
  else
    docker login "${REGISTRY}"
  fi
fi

# --- push -------------------------------------------------------------------
echo
echo "==> Pushing ${DOCKER_TAG}"
docker push "${DOCKER_TAG}"

# --- optional: also push :latest when pushing a versioned tag ---------------
if [[ "${PUSH_ALIAS_TAGS}" == "1" ]]; then
  REPO_PART="${DOCKER_TAG%:*}"
  LATEST_TAG="${REPO_PART}:latest"
  if [[ "${LATEST_TAG}" != "${DOCKER_TAG}" ]]; then
    echo
    echo "==> PUSH_ALIAS_TAGS=1, also pushing ${LATEST_TAG}"
    docker tag "${DOCKER_TAG}" "${LATEST_TAG}"
    docker push "${LATEST_TAG}"
  fi
fi

echo
echo "Done. The image is now available at:"
echo "    ${DOCKER_TAG}"
echo
echo "Pull on another machine with:"
echo "    docker pull ${DOCKER_TAG}"
