#!/usr/bin/env bash
# Known vulnerabilities in the code, its dependencies, and the Go standard
# library it is built with.
#
# Not part of ci/run.sh, deliberately. Its result depends on a vulnerability
# database that changes every day, not on the commit: a CVE published
# overnight would turn every open pull request red for a reason none of them
# caused and none of them can fix. So it runs on a schedule against main, on
# pull requests that change dependencies - which can introduce a
# vulnerability, and should be told - and on demand via `make scan`.
#
# govulncheck reports only vulnerabilities in functions this code actually
# calls, not everything that appears somewhere in the dependency graph. A
# finding here is a path to vulnerable code, not a version number to argue
# about.
set -euo pipefail

cd "$(dirname "$0")/.."

# shellcheck source=ci/tools.env
source ci/tools.env

TOOLS="${PWD}/.tools"
GOVULNCHECK="${TOOLS}/govulncheck-${GOVULNCHECK_VER}"

if [[ ! -x "${GOVULNCHECK}" ]]; then
  echo "installing govulncheck ${GOVULNCHECK_VER}"
  mkdir -p "${TOOLS}"
  temp="$(mktemp -d)"
  GOBIN="${temp}" go install "golang.org/x/vuln/cmd/govulncheck@${GOVULNCHECK_VER}"
  mv "${temp}/govulncheck" "${GOVULNCHECK}"
  rm -rf "${temp}"
fi

go version
"${GOVULNCHECK}" -version | head -3

echo
echo "==> govulncheck: the code, its dependencies, the standard library"
# Exits non-zero when a vulnerability is reachable, which is what fails the
# scheduled run and turns it into a notification.
"${GOVULNCHECK}" ./...

# The image, which govulncheck cannot see. A base image carries packages the
# Go code never calls - a libc, certificates, timezone data - and those have
# vulnerabilities of their own. The runtime here is distroless, so this should
# say nothing; it is here to keep saying nothing as the base image ages.
TRIVY="${TOOLS}/trivy-${TRIVY_VER}"
if [[ ! -x "${TRIVY}" ]]; then
  echo "installing trivy ${TRIVY_VER}"
  temp="$(mktemp -d)"
  GOBIN="${temp}" go install "github.com/aquasecurity/trivy/cmd/trivy@${TRIVY_VER}"
  mv "${temp}/trivy" "${TRIVY}"
  rm -rf "${temp}"
fi

# Trivy reads only public artifacts here - its database, the base images -
# but it consults the Docker credential configuration first, and a credential
# helper left behind by an uninstalled Docker Desktop makes it fail before it
# starts. An empty configuration sidesteps that without touching the real one.
# It does not affect access to the local daemon, which is a socket, not a
# credential.
trivy_config="$(mktemp -d)"
trap 'rm -rf "${trivy_config}"' EXIT

echo
echo "==> trivy config: the Dockerfile"
DOCKER_CONFIG="${trivy_config}" "${TRIVY}" config --quiet --exit-code 1 \
  --severity HIGH,CRITICAL .

if docker info >/dev/null 2>&1; then
  echo
  echo "==> trivy image: what ships"
  docker build --quiet --file infra/docker/Dockerfile --tag fan-out-api:scan . >/dev/null
  # --ignore-unfixed: a vulnerability with no fixed version is one nobody can
  # act on from this repository, and reporting it every night trains people to
  # ignore the job.
  DOCKER_CONFIG="${trivy_config}" "${TRIVY}" image --quiet --exit-code 1 \
    --severity HIGH,CRITICAL --ignore-unfixed fan-out-api:scan
elif [[ "${CI:-}" == "true" ]]; then
  echo "no Docker daemon in CI; the image scan cannot be skipped here" >&2
  exit 1
else
  echo
  echo "skipped the image scan: no Docker daemon. The scheduled job runs it." >&2
fi
