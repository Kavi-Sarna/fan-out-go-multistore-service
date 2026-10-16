#!/usr/bin/env bash
# Keep the Dockerfile's golang image and go.mod on the same Go version, and
# on the latest patch of that minor.
#
# Dependabot updates one ecosystem at a time. A docker bump to golang:1.27.1
# leaves go.mod on 1.26.9, and CI fails the pair check. The same bump can land
# on an intermediate patch while 1.27.2 already exists — and the vulnerability
# scan then fails for CVEs the newer patch fixed. This script is what turns
# those pull requests green: take the newer of the two pins, advance to the
# latest patch of that minor, write both files, and stop.
#
# Safe to run locally. Prints what it did; exits 0 when the tree is already
# aligned or after rewriting it.
set -euo pipefail

cd "$(dirname "$0")/.."

DOCKERFILE="infra/docker/Dockerfile"
GOMOD="go.mod"

dockerfile_line="$(grep -E '^FROM golang:[0-9]+\.[0-9]+\.[0-9]+-' "${DOCKERFILE}" | head -1 || true)"
if [[ -z "${dockerfile_line}" ]]; then
  echo "no golang:x.y.z FROM line in ${DOCKERFILE}" >&2
  exit 1
fi

if [[ ! "${dockerfile_line}" =~ FROM\ golang:([0-9]+\.[0-9]+\.[0-9]+)-([a-z0-9]+)@sha256:([0-9a-f]+) ]]; then
  echo "could not parse golang image from: ${dockerfile_line}" >&2
  exit 1
fi
docker_ver="${BASH_REMATCH[1]}"
variant="${BASH_REMATCH[2]}"
docker_digest="${BASH_REMATCH[3]}"

go_line="$(grep -E '^go [0-9]+\.[0-9]+\.[0-9]+$' "${GOMOD}" || true)"
if [[ -z "${go_line}" ]]; then
  echo "${GOMOD} does not name a full Go version (go x.y.z)" >&2
  exit 1
fi
mod_ver="${go_line#go }"

version_ge() {
  # True when $1 >= $2 as dotted triples.
  local IFS=.
  # shellcheck disable=SC2206
  local a=($1) b=($2)
  for i in 0 1 2; do
    local ai="${a[i]:-0}" bi="${b[i]:-0}"
    if (( ai > bi )); then return 0; fi
    if (( ai < bi )); then return 1; fi
  done
  return 0
}

target_ver="${docker_ver}"
if version_ge "${mod_ver}" "${docker_ver}"; then
  target_ver="${mod_ver}"
fi

major_minor="${target_ver%.*}"

latest_patch="$(
  curl -fsSL "https://go.dev/dl/?mode=json&include=all" | python3 -c "
import json, re, sys
want = sys.argv[1]
pat = re.compile(r'^go(' + re.escape(want) + r'\.\d+)$')
found = []
for entry in json.load(sys.stdin):
    m = pat.match(entry['version'])
    if m:
        found.append(m.group(1))
if not found:
    sys.exit('no Go releases found for ' + want)
print(max(found, key=lambda v: tuple(int(p) for p in v.split('.'))))
" "${major_minor}"
)"

if [[ -z "${latest_patch}" ]]; then
  echo "failed to resolve latest patch for Go ${major_minor}" >&2
  exit 1
fi

image_ref="golang:${latest_patch}-${variant}"

registry_digest="$(
  token="$(curl -fsSL "https://auth.docker.io/token?service=registry.docker.io&scope=repository:library/golang:pull" \
    | python3 -c 'import json,sys; print(json.load(sys.stdin)["token"])')"
  # Prefer the multi-arch index digest — the same form Dependabot and the
  # existing Dockerfile pin.
  digest="$(
    curl -fsSI \
      -H "Authorization: Bearer ${token}" \
      -H "Accept: application/vnd.oci.image.index.v1+json, application/vnd.docker.distribution.manifest.list.v2+json, application/vnd.oci.image.manifest.v1+json, application/vnd.docker.distribution.manifest.v2+json" \
      "https://registry-1.docker.io/v2/library/golang/manifests/${latest_patch}-${variant}" \
      | tr -d '\r' \
      | awk -F': ' 'tolower($1)=="docker-content-digest"{print $2; exit}'
  )"
  if [[ -z "${digest}" ]]; then
    echo "failed to resolve digest for ${image_ref}" >&2
    exit 1
  fi
  printf '%s\n' "${digest#sha256:}"
)"

changed=0

desired_from="FROM golang:${latest_patch}-${variant}@sha256:${registry_digest} AS build"
if [[ "${dockerfile_line}" != "${desired_from}" ]]; then
  # Replace only the build-stage golang pin; leave the runtime FROM alone.
  python3 - "${DOCKERFILE}" "${desired_from}" <<'PY'
import pathlib, re, sys
path = pathlib.Path(sys.argv[1])
desired = sys.argv[2]
text = path.read_text()
new, n = re.subn(
    r'^FROM golang:\d+\.\d+\.\d+-[a-z0-9]+@sha256:[0-9a-f]+ AS build$',
    desired,
    text,
    count=1,
    flags=re.M,
)
if n != 1:
    sys.exit(f"expected one golang build FROM line to replace in {path}")
path.write_text(new)
PY
  echo "Dockerfile: ${docker_ver}-${variant}@${docker_digest:0:12}… → ${latest_patch}-${variant}@${registry_digest:0:12}…"
  changed=1
fi

if [[ "${mod_ver}" != "${latest_patch}" ]]; then
  python3 - "${GOMOD}" "${latest_patch}" <<'PY'
import pathlib, re, sys
path = pathlib.Path(sys.argv[1])
ver = sys.argv[2]
text = path.read_text()
new, n = re.subn(r'(?m)^go \d+\.\d+\.\d+$', f'go {ver}', text, count=1)
if n != 1:
    sys.exit(f"expected one go x.y.z line in {path}")
path.write_text(new)
PY
  echo "go.mod: ${mod_ver} → ${latest_patch}"
  changed=1
fi

if [[ "${changed}" -eq 0 ]]; then
  echo "already aligned on Go ${latest_patch} (${image_ref})"
else
  echo "synced to Go ${latest_patch}"
fi

if [[ -n "${GITHUB_OUTPUT:-}" ]]; then
  echo "changed=${changed}" >> "${GITHUB_OUTPUT}"
  echo "version=${latest_patch}" >> "${GITHUB_OUTPUT}"
fi
