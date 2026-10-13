#!/usr/bin/env bash
# Runs on every start, including a resume after the 30-minute idle stop, and
# leaves the service running so the forwarded port answers immediately.
#
# Deliberately not `set -e`. A service that fails to start should leave you
# with a working editor to fix it in, not a codespace that will not open.
set -uo pipefail

cd "$(dirname "$0")/.."

readonly port=8080
readonly addr="127.0.0.1:${port}"
readonly log="/tmp/api.log"
readonly pidfile="/tmp/api.pid"

if ! command -v curl >/dev/null 2>&1; then
  echo "WARNING: curl is missing, so the service cannot be health-checked." >&2
  exit 0
fi

healthy() { curl -fsS "http://${addr}/healthz" >/dev/null 2>&1; }
ours() { [[ -f "${pidfile}" ]] && kill -0 "$(cat "${pidfile}")" 2>/dev/null; }

# Keeps .tools/golangci-lint pointing at the pinned version, so a pin bumped
# while the codespace was stopped reaches the editor on the next start.
make tools >/dev/null 2>&1 || echo "WARNING: 'make tools' failed; the editor's linter may be stale." >&2

if ours && healthy; then
  echo "the service is already running on ${addr}"
elif healthy; then
  # Something answers on the port and it is not the process this script
  # started. Starting another would fail to bind, so say what is happening.
  echo "WARNING: something else is serving ${addr}; not starting a second copy." >&2
  exit 0
else
  # Rebuilt rather than reused: code may have changed while it was stopped.
  if ! make build >/dev/null 2>&1; then
    echo "WARNING: the build failed; run 'make build' to see why." >&2
    exit 0
  fi

  # setsid and a closed stdin, so the service belongs to no session the
  # lifecycle runner tears down when this hook exits. nohup alone only
  # ignores SIGHUP, and leaves the process in the hook's session. setsid is
  # util-linux, so it is in the container; the fallback is for running this
  # script anywhere else. The ${x[@]+...} form is because bash 3.2 treats an
  # empty array as unset under -u.
  launcher=()
  command -v setsid >/dev/null 2>&1 && launcher=(setsid)
  ADDR="${addr}" ${launcher[@]+"${launcher[@]}"} nohup ./bin/api >"${log}" 2>&1 </dev/null &
  echo $! >"${pidfile}"

  for _ in $(seq 1 20); do
    healthy && break
    sleep 0.5
  done
  if ! healthy; then
    echo "WARNING: the service did not come up; the log is ${log}." >&2
    exit 0
  fi
  echo "the service is running on ${addr}; its log is ${log}"
fi

cat <<'BANNER'

  curl localhost:8080/healthz    the service
  make ci                        everything the pipeline does
  make help                      the rest

  To show someone, set port 8080 to Public in the Ports panel. Nothing here
  has authentication, so make it public only for as long as you need it.
BANNER
