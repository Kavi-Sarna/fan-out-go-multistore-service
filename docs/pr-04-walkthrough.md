# PR 4 — walkthrough

**Goal:** the service as it ships — a static binary in a minimal image, under Compose — with the image built, started, health-checked and stopped by CI on every pull request.

**Why now:** every later PR adds a container to Compose. The pattern for the service's own image, and for checking that a container really does what its file claims, is worth settling while there is exactly one.

**Not in this PR:** any store. Postgres arrives in PR 7, as the first service Compose pulls rather than builds.


## Step 0 — what can only be checked by running it

PR 3 shipped a devcontainer that would not have built, and nothing caught it, because every check was a check on what a file said. This PR is almost all container, on a machine with no Docker, so it starts by separating what can be verified statically from what cannot:

| Verified here, without Docker                                         | Only a running container can show         |
| --------------------------------------------------------------------- | ----------------------------------------- |
| both base image digests exist and are multi-arch                      | the image builds                          |
| the runtime base has no HIGH or CRITICAL vulnerabilities              | it becomes healthy by its own check       |
| the Dockerfile has no misconfigurations                               | it answers through the published port     |
| the binary compiles static for linux                                  | `SIGTERM` stops it cleanly, as PID 1      |
| the build context, as `Dockerfile.dockerignore` leaves it, compiles   |                                           |

The right-hand column is what `ci/run.sh` now does, and why it fails in CI rather than skipping when there is no Docker daemon.


## Step 1 — two stages, and what crosses between them

```dockerfile
FROM golang:1.26.9-trixie@sha256:f06b7122... AS build
...
RUN go build -trimpath -ldflags="-s -w" -o /out/api ./cmd/api

FROM gcr.io/distroless/static-debian13:nonroot@sha256:e2e927ec...
COPY --from=build /out/api /api
```

One file crosses. The build stage has a compiler, a module cache and a shell; none of them reach the image, and the difference is measured rather than asserted:

| Image                                     | HIGH + CRITICAL   |
| ----------------------------------------- | ----------------- |
| distroless static — what ships            | **0**             |
| `golang:1.26.9-trixie` — the build stage  | **159**           |

`CGO_ENABLED=0` is what makes this possible: a fully static binary needs no libc, so the runtime image can have none. **distroless/static** carries certificates (needed once the service talks TLS to a database), a passwd entry for an unprivileged user, and timezone data. No shell, no package manager. Debian 13, the same release as the devcontainer.

`-trimpath` removes the build machine's paths, so two builds of the same source are byte-identical wherever they ran. `-s -w` drop the symbol table and DWARF — the runtime's own `pclntab` stays, so stack traces and profiles still resolve function names.

`GOTOOLCHAIN=local` means the image's Go is the Go that builds. Without it, a `go.mod` that asked for a newer Go would quietly download one inside the build. With it, the build fails — and a unit test fails first, comparing the image tag against `go.mod`, without needing Docker.


## Step 2 — base images on their FROM lines, not behind ARGs

The first draft held both images in `ARG`s at the top of the file, with `FROM ${GO_IMAGE}`. Tidy, and invisible to Dependabot.

Dependabot's Docker parser matches lines that begin `FROM` followed by a literal image — its pattern has no room for `$` or `{`. Run against both versions:

|                                       | build image           | runtime image         |
| ------------------------------------- | --------------------- | --------------------- |
| `FROM ${GO_IMAGE}`                    | **not seen**          | **not seen**          |
| `FROM golang:1.26.9-trixie@sha256:…`  | seen, tag and digest  | seen, tag and digest  |

Behind a variable, neither image would ever be offered an update, and both pins would age without anything saying so — exactly the failure pinning plus Dependabot exists to prevent, silently undone by a refactor. The images are now written out in full, and a test fails if a `FROM` takes its image from a variable or is not pinned by digest.


## Step 3 — a health check with nothing to run it

A container health check runs a command inside the container. distroless has no shell, no curl, no wget — there is nothing to run. So the service checks itself:

```dockerfile
HEALTHCHECK --interval=10s --timeout=3s --start-period=5s --retries=3 \
    CMD ["/api", "healthcheck"]
```

`api healthcheck` probes `/healthz` and reports through its exit code. Exec form, so no shell is involved.

### The probe that could be pointed anywhere

gosec flagged the first version as a request forgery, and it was right. The probe built its URL from `ADDR`:

```
ADDR=example.com:80 /api healthcheck   →   GET http://example.com:80/healthz
```

Exploiting that needs control of the container's environment, which is a high bar. But the design was wrong regardless: a health check probes *this process*, and the host in `ADDR` says which interface to bind, not somewhere to send a request. So it now only ever dials loopback:

```go
switch ip := net.ParseIP(host); {
case host == "" || host == "localhost" || (ip != nil && ip.IsUnspecified()):
case ip != nil && ip.IsLoopback():
    if ip.To4() == nil { loopback = "::1" }
default:
    return fmt.Errorf("ADDR host %q is not loopback or unspecified; ...", host)
}
```

`:8080` — what a container is given, so its port mapping can reach it — is probed on 127.0.0.1. Anything else is refused before a request is made, and a test puts a server behind four non-loopback addresses and fails if any of them is reached. Review confirmed the edges: IPv4-mapped loopback is allowed, an IPv4-mapped private address is refused, and `HTTP_PROXY` cannot divert the probe because Go never proxies loopback.

gosec's taint analysis still follows the port from the environment into the URL and cannot see that it was validated as an integer, so two lines carry a narrow `//nolint:gosec // G704` with the reason. Suppressing a finding is right once the design no longer has the flaw; suppressing it instead of fixing the design would not have been.


## Step 4 — Compose, and a default that would have dropped requests

```yaml
ports:
  - "8081:8080"
stop_grace_period: 20s
```

**8081 on the host** because the devcontainer already runs the service natively on 8080, and two processes cannot hold one port. `make run` and `make up` work side by side.

**`stop_grace_period`** is the line worth reading twice. Compose sends `SIGTERM`, waits, then `SIGKILL`. Its default wait is ten seconds. The service drains for up to fifteen. Left at the default, Compose would kill the process five seconds into finishing exactly the requests graceful shutdown exists to finish — PR 1's whole subject, undone by a default nobody set. A test compares `stop_grace_period` against `defaultGrace` from the code.

The rest is the posture Kubernetes will enforce, applied here first so a dependency on any of it shows up on a laptop:

```yaml
read_only: true
cap_drop: [ALL]
security_opt:
  - no-new-privileges:true
```

The binary writes nothing to disk and binds above 1024, so it needs neither a writable filesystem nor any capability.

### The rule from PR 3, enforced on its first chance

`TestEveryComposePortIsForwardedByTheDevcontainer` failed the first time it ran: Compose published 8081 and the devcontainer did not forward it. That is the drift PR 3's rule exists to prevent, caught the moment a second port existed. 8081 is now forwarded, labeled *API (container)*, with 8080 relabeled *API (native)*.

### Where the container files live

Everything container-related sits under `infra/docker/`, so the stores, the load test and whatever deploys this later have an `infra/` to join rather than a crowded root. The cost is that the Dockerfile and the build context are no longer the same directory:

```yaml
build:
  context: ../..
  dockerfile: infra/docker/Dockerfile
```

The context stays the repository root, because the build needs `go.mod`, `cmd` and `internal`; the Dockerfile path is relative to it. The ignore file follows the Dockerfile, not the context: BuildKit reads `infra/docker/Dockerfile.dockerignore` ahead of any `.dockerignore` at the root, so it is named for its Dockerfile and kept beside it. The Makefile and CI pass `--file infra/docker/compose.yaml` to every Compose command, so a bare `docker compose` at the root finds nothing; `make up`, `make logs` and `make down` are the way in.

`TestComposeBuildsTheDockerfileTheseTestsRead` holds the three together: the context resolves to the root, the Dockerfile Compose builds is the one the other tests read, and the ignore file is where the builder looks. Move one without the others and the image is built from a file nobody tested, or `.git` and `.tools` ride into the build context.

A second test reads the ignore file itself, because one that exists but is empty would pass the first. The build stage runs `COPY . .`, so anything not excluded lands in a layer: `.git`, `.tools`, `bin`, and any `.env`, `.pem` or `.key` file must stay out. None of the credential files exist yet; the stores bring the first ones, and the exclusion is cheaper to have before then than to remember after.

Compose's project directory follows `--file`, so relative paths in `compose.yaml`, and a `.env` beside it, resolve against `infra/docker`. That is deliberate: the stores' configuration will live there too.


## Step 5 — what CI does with the image

```bash
compose() { docker compose --file infra/docker/compose.yaml "$@"; }

compose build
compose up --detach --wait --wait-timeout 60
curl -fsS http://127.0.0.1:8081/healthz
compose stop api
# ...and the exit code must be 0
```

Each line answers one of the questions only a running container can. `--wait` waits for the image's own `HEALTHCHECK`; the curl goes through the published port; and the last checks the claim in the Dockerfile's final comment:

```dockerfile
# Exec form, so the service is PID 1 and receives SIGTERM directly. A Go
# program as PID 1 is fine on both counts that usually matter: it installs
# its own SIGTERM handler, so the signal is not ignored, and it starts no
# child processes, so there are no zombies to reap.
```

A process that is PID 1 gets no default signal dispositions — a program that did not handle `SIGTERM` would ignore it, sit out the grace period, and be killed with exit **137**. Requiring exit **0** turns that comment into a check.

Review found that the first version discarded everything on an early failure: if the container never became healthy, `set -e` fired, the cleanup took the containers down silently, and CI said only "container is unhealthy". Since the image has never been built, that is the likeliest first failure, so cleanup now prints `compose ps` and the logs before tearing down.

On a laptop with no Docker daemon the step skips with a message. In CI, where `CI=true`, the same absence fails the run: a skipped image check in CI is a check that silently stopped happening.


## Step 6 — scanning the image, and the first dependency

`ci/scan.sh` grows a second half. `govulncheck` sees the Go code; it cannot see the image, and a base image carries packages the code never calls. So trivy scans both the Dockerfile's configuration and the built image, nightly alongside `govulncheck`. On distroless it should say nothing — it is there to keep saying nothing as the base ages.

Trivy is installed the same way as every other tool: `go install` at a pinned version into `.tools`. That makes it a source build checked against module checksums, at the cost of a slow first run — so the scan job caches `.tools`, keyed on the pins and on `go.mod`.

The container tests need to read `compose.yaml`, and the standard library has no YAML parser, so this PR adds the project's first dependency. The obvious choice, `gopkg.in/yaml.v3`, was **archived in April 2025**; the maintained fork is `go.yaml.in/yaml/v3`. It is used only from tests, so it is never linked into the binary.


## Definition of done

- [ ] `make up` builds the image and waits until it is healthy on :8081
- [ ] The runtime image has no HIGH or CRITICAL vulnerabilities
- [ ] CI builds and runs the image, and fails unless SIGTERM exits 0
- [ ] Compose's grace period is longer than the service's drain
- [ ] Every port Compose publishes is forwarded by the devcontainer
- [ ] Compose builds the Dockerfile the tests read, with its ignore file beside it
- [ ] The health check cannot be pointed anywhere but loopback
- [ ] Dependabot can see both base images


## Files in this PR

| File                                      |                                                                               |
| ----------------------------------------- | ----------------------------------------------------------------------------- |
| `infra/docker/Dockerfile`                 | two stages, both images literal on their FROM lines and pinned by digest      |
| `infra/docker/Dockerfile.dockerignore`    | what the build context leaves out, named for the Dockerfile beside it         |
| `infra/docker/compose.yaml`               | the service on :8081, a grace period longer than the drain, no privileges     |
| `cmd/api/main.go`                         | a `healthcheck` subcommand that only dials loopback                           |
| `cmd/api/healthcheck_test.go`             | eight tests, including that nothing but loopback is ever dialed               |
| `cmd/api/container_test.go`               | nine tests holding the container files to the code and to each other          |
| `cmd/api/devcontainer_test.go`            | a spelling fix in a comment                                                   |
| `go.mod`, `go.sum`                        | the first dependency, `go.yaml.in/yaml/v3`, used only by tests                |
| `.devcontainer/devcontainer.json`         | 8081 forwarded and labeled beside 8080                                        |
| `ci/run.sh`                               | builds, starts, health-checks and stops the image; logs on failure            |
| `ci/scan.sh`                              | trivy for the image and the Dockerfile, beside govulncheck                    |
| `ci/tools.env`                            | trivy joins the pins                                                          |
| `.github/workflows/ci.yml`                | caches the pinned tools                                                       |
| `.github/workflows/scan.yml`              | runs on changes under `infra/docker`, caches the tools                        |
| `.github/workflows/devcontainer.yml`      | runs on changes under `infra/docker`                                          |
| `.github/dependabot.yml`                  | watches the Dockerfile in `infra/docker`                                      |
| `Makefile`                                | `image`, `up`, `down`, `logs`, all through one `COMPOSE`                      |
| `README.md`                               | running it in a container, and the new layout                                 |
| `docs/roadmap.md`                         | links this walkthrough                                                        |

Next: **PR 5** — the domain: the session type, its validation, and its redaction, in a package that imports nothing.
