# syntax=docker/dockerfile:1

# Two stages, and nothing from the first reaches the second except one file.
# The build stage has a compiler, a module cache and a shell. The runtime has
# none of them, because none of them are needed to run a static binary and
# each is something a vulnerability scanner would rightly report.

# Both base images are written out in full on their FROM lines rather than
# held in ARGs. Dependabot reads only a literal image after FROM; behind a
# variable, neither image would ever be offered an update, and both pins would
# age without anything saying so. A test holds that.

# The Go version here must match go.mod. GOTOOLCHAIN=local below makes a
# mismatch fail the build instead of quietly downloading a different
# toolchain, and cmd/api/container_test.go fails before the build gets that
# far.
FROM golang:1.27.3-trixie@sha256:1896654cfc5c2b5ad74c07f29cb36f5d2d1cbed42e3054070fbfe444e521e83c AS build

WORKDIR /src

# CGO off for a fully static binary, which is what lets the runtime image
# have no libc. GOTOOLCHAIN=local so the image's Go is the Go that builds.
ENV CGO_ENABLED=0 \
    GOTOOLCHAIN=local

# Modules before source, so a change to the code does not re-run this step.
# The modules themselves live in the cache mount rather than in this layer,
# and the build step below mounts the same cache - on a builder that has run
# before, nothing is fetched twice. On a fresh builder, such as CI, they are
# fetched once per build.
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

COPY . .

# -trimpath removes the build machine's paths from the binary, so two builds
# of the same source are byte-identical regardless of where they ran.
# -s -w drops the symbol table and DWARF; the runtime's own pclntab stays, so
# stack traces and profiles still resolve function names.
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    go build -trimpath -ldflags="-s -w" -o /out/api ./cmd/api

# distroless/static: certificates, a passwd entry for an unprivileged user,
# timezone data, and nothing else - no shell, no package manager. Debian 13,
# the same release the devcontainer runs.
FROM gcr.io/distroless/static-debian13:nonroot@sha256:516757106de35205efb70b91ba03f6f1298424a0678c603255cf2852a7000087

COPY --from=build /out/api /api

# The distroless nonroot user, stated numerically. Kubernetes can only verify
# runAsNonRoot against a number; a name would have to be resolved inside the
# image, which it will not do.
USER 65532:65532

EXPOSE 8080

# The service probes its own /healthz, because there is nothing else in this
# image that could. The interval matches what an orchestrator would use; the
# start period covers the time to bind.
HEALTHCHECK --interval=10s --timeout=3s --start-period=5s --retries=3 \
    CMD ["/api", "healthcheck"]

# Exec form, so the service is PID 1 and receives SIGTERM directly. A Go
# program as PID 1 is fine on both counts that usually matter: it installs
# its own SIGTERM handler, so the signal is not ignored, and it starts no
# child processes, so there are no zombies to reap.
ENTRYPOINT ["/api"]
