#!/usr/bin/env bash
# Runs once, when the codespace is created: everything slow that does not
# change between sessions.
set -euo pipefail

cd "$(dirname "$0")/.."

# Compose, pinned. The docker-in-docker feature installs the newest
# docker-compose-plugin and offers no option to choose otherwise, so the
# version that every later Compose file is written against is fixed here.
# The string is the Debian package version, which is the same on amd64 and
# arm64.
readonly compose_version="5.6.0-1~debian.13~trixie"
if [[ "$(dpkg-query -W -f='${Version}' docker-compose-plugin 2>/dev/null)" != "${compose_version}" ]]; then
  sudo apt-get update -qq
  sudo apt-get install -y -qq --allow-downgrades "docker-compose-plugin=${compose_version}"
  # Held, so an unrelated apt upgrade inside the container does not move it.
  sudo apt-mark hold docker-compose-plugin >/dev/null
fi
docker compose version

go mod download

# The pinned linter and formatter, the same versions CI installs. An editor
# running a different golangci-lint from the pipeline is how a file that
# lints clean locally fails in review.
make tools

# Built once here so that every later start is a process launch rather than
# a compile.
make build

echo
echo "ready. The service starts on every codespace start; see post-start.sh."
