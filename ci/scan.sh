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

# Exits non-zero when a vulnerability is reachable, which is what fails the
# scheduled run and turns it into a notification.
"${GOVULNCHECK}" ./...
