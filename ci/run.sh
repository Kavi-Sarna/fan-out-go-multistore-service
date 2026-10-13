#!/usr/bin/env bash
# The build. This is what CI runs and what `make ci` runs.
#
# If the two ever differ, CI is a different program and its result is an
# opinion about that other program. Everything the pipeline checks is here,
# in order, and nothing is here that you cannot run on a laptop.
set -euo pipefail

cd "$(dirname "$0")/.."

# shellcheck source=ci/tools.env
source ci/tools.env

TOOLS="${PWD}/.tools"
MODULE="$(go list -m)"

# Keyed by version, so bumping a pin installs the new one rather than finding
# a file of the right name and running whatever version it happens to be.
GOLANGCI="${TOOLS}/golangci-lint-${GOLANGCI_VER}"
GOIMPORTS="${TOOLS}/goimports-${GOIMPORTS_VER}"

step() { printf '\n\033[1m==> %s\033[0m\n' "$1"; }

install_tool() {
  local binary="$1" module="$2" version="$3"
  [[ -x "${binary}" ]] && return 0
  echo "installing $(basename "${binary}")"
  local temp
  temp="$(mktemp -d)"
  GOBIN="${temp}" go install "${module}@${version}"
  mv "${temp}/$(basename "${module}")" "${binary}"
  rm -rf "${temp}"
}

step "tools"
mkdir -p "${TOOLS}"
install_tool "${GOLANGCI}" github.com/golangci/golangci-lint/v2/cmd/golangci-lint "${GOLANGCI_VER}"
install_tool "${GOIMPORTS}" golang.org/x/tools/cmd/goimports "${GOIMPORTS_VER}"
go version

step "tidy"
# -diff prints what tidy would change and exits non-zero if that is anything,
# without touching the files. It covers go.sum too, including the case where
# go.sum does not exist yet and should — which is the PR that adds the first
# dependency, and exactly when you want to be told.
if ! go mod tidy -diff; then
  echo "go.mod or go.sum is not tidy: run 'make tidy' and commit the result" >&2
  exit 1
fi

step "format"
# -l lists what would change. Checking rather than writing: CI must not
# produce a diff nobody asked for. Both tools report the same file when both
# would change it, hence sort -u.
unformatted="$( { gofmt -s -l . ; "${GOIMPORTS}" -l -local "${MODULE}" . ; } | sort -u)"
if [[ -n "${unformatted}" ]]; then
  echo "not formatted; run 'make fmt':" >&2
  echo "${unformatted}" >&2
  exit 1
fi

step "vet"
go vet ./...

step "lint"
"${GOLANGCI}" run

step "test"
# -race on every run rather than in a nightly job. A data race found a week
# later is a race nobody can attribute to a change.
# -shuffle makes an order dependency between tests fail here rather than
# intermittently for somebody else.
go test -race -shuffle=on -coverprofile=coverage.out ./...
go tool cover -func=coverage.out | tail -1

step "build"
go build -o bin/api ./cmd/api

step "image"
# Builds the image, runs it under Compose, and checks the three things that
# only a running container can show: it becomes healthy by its own
# HEALTHCHECK, it answers through the published port, and SIGTERM stops it
# cleanly. The last is the one worth having - a Go binary as PID 1 that did
# not handle SIGTERM would ignore it, sit out the grace period, and be killed
# with exit 137.
#
# Skipped without a Docker daemon, which a laptop may not have. CI always
# does, so there a missing daemon fails the run rather than skipping it.
if docker info >/dev/null 2>&1; then
  compose() { docker compose --file infra/docker/compose.yaml "$@"; }

  # On failure, show what the containers were doing before taking them down.
  # The first real failure of this step is likely to be the container never
  # becoming healthy, and "container is unhealthy" alone says nothing about
  # why.
  cleanup() {
    local status=$?
    if (( status != 0 )); then
      echo "--- compose ps ---" >&2
      compose ps --all >&2 || true
      echo "--- compose logs ---" >&2
      compose logs --no-color >&2 || true
    fi
    compose down --volumes --remove-orphans >/dev/null 2>&1 || true
  }
  trap cleanup EXIT

  compose build
  compose up --detach --wait --wait-timeout 60
  curl -fsS http://127.0.0.1:8081/healthz && echo

  container="$(compose ps --quiet api)"
  compose stop api
  exit_code="$(docker inspect --format '{{.State.ExitCode}}' "${container}")"
  if [[ "${exit_code}" != "0" ]]; then
    echo "the container exited ${exit_code} on SIGTERM; it should drain and exit 0" >&2
    compose logs api >&2
    exit 1
  fi
  echo "stopped cleanly on SIGTERM (exit 0)"

  trap - EXIT
  cleanup
elif [[ "${CI:-}" == "true" ]]; then
  echo "no Docker daemon in CI; the image step cannot be skipped here" >&2
  exit 1
else
  echo "skipped: no Docker daemon. CI runs this step, and so will 'make ci' on a machine with Docker." >&2
fi

printf '\n\033[1;32mok\033[0m\n'
