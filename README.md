# fan-out-go-multistore-service

[![Open in GitHub Codespaces](https://github.com/codespaces/badge.svg)](https://codespaces.new/kavi-sarna/fan-out-go-multistore-service)

One write, three stores, all or none: distributed-transaction semantics in Go over Postgres, Cassandra and Kafka.

That requirement is the whole point. No transaction primitive spans those three — Postgres is ACID on its own, Cassandra offers Paxos per partition and no more, and Kafka's transactions stop at Kafka's own edge. There is no coordinator to enlist all three, so all-or-nothing is not something you configure. It is something you build.

The worked example is device sessions. A session carries an IP and a user agent: fields that are useful, identifying, and one careless line away from a log nobody can un-write.

> **Status: PR 4 — runs the way it ships.** `GET /healthz`, a graceful shutdown, one script as the build, a devcontainer, and now an image: a static binary on distroless, under Compose, that CI builds, starts, and stops on every pull request. No database yet; that is PR 7.


## Quick start

```bash
make run        # listens on :8080
curl localhost:8080/healthz
```

```bash
make ci         # everything the pipeline does, in the same order
make scan       # known vulnerabilities in the code, its deps and the stdlib
make test       # just the tests
make race       # the same, under the race detector
make lint       # golangci-lint
make build      # bin/api
make up         # the image, under Compose, on :8081
make down       # stop it
make help       # everything else
```

Needs Go 1.26 and nothing else. `golangci-lint` and `goimports` are pinned in `ci/tools.env` and installed into `.tools` on first use.


## Running it in a browser

Click the badge. GitHub builds a container with Go 1.26, Docker and the pinned linters, compiles the service, and starts it. When the terminal says the service is running, port 8080 is already forwarded:

```bash
curl localhost:8080/healthz
```

The first create takes a few minutes; a resume takes seconds and restarts the service on its own, so the forwarded port answers whenever the codespace is open. A codespace stops after 30 idle minutes and costs nothing while stopped.

To show someone, set port 8080 to **Public** in the Ports panel. Nothing in this service has authentication, so that URL is open to anyone who has it — make it public only for the length of the demo.

**Two cores is enough for now.** The devcontainer asks for what the stack needs today, and the PR that adds something heavier raises the floor in the same change. A codespace's machine type can be changed at any time — from the codespaces list, or *Codespaces: Change Machine Type* in the command palette — so it grows with the stack rather than being recreated. On GitHub Free two cores is 60 hours a month, against 30 on four.


## Running it in a container

```bash
make up                         # builds the image, starts it, waits until healthy
curl localhost:8081/healthz
make down
```

Port 8081, not 8080, because the devcontainer already runs the service natively on 8080 and two processes cannot hold one port. `make run` and `make up` work side by side, which is also a quick way to compare the binary with the image.

The image is two stages. The first compiles a static binary; the second is distroless — certificates, an unprivileged user and timezone data, with no shell and no package manager — and receives one file from the first:

| Image                                                 | HIGH + CRITICAL vulnerabilities   |
| ----------------------------------------------------- | --------------------------------- |
| Runtime base, distroless static — what ships          | 0                                 |
| Build base, `golang:1.26.9-trixie` — never shipped    | 159                               |

With no shell or curl in the image, the container health check runs the service's own `healthcheck` subcommand, which probes `/healthz` on loopback. Only loopback: the host in `ADDR` says which interface to bind, not somewhere to send a request.

CI builds the image on every pull request, starts it under Compose, waits for it to become healthy, and sends it `SIGTERM` — failing unless it exits 0. A Go binary that is PID 1 and ignored the signal would sit out the grace period and be killed with exit 137.


## The build is one script

`ci/run.sh` is what CI runs and what `make ci` runs. If those two ever differed, CI would be a different program and its result an opinion about that other program.

```
==> tools     ==> tidy      ==> format
==> vet       ==> lint      ==> test      ==> build
```

Tests run with `-race` and `-shuffle=on` on every invocation rather than in a nightly job. A data race found a week later is a race nobody can attribute to a change, and shuffling makes an order dependency between tests fail here rather than intermittently for whoever runs them next.


## Vulnerabilities, on a schedule

`make scan` runs `govulncheck` against the code, its dependencies, and the Go standard library. It reports only vulnerabilities in functions this code actually calls, so a finding is a path to vulnerable code rather than a version number to argue about.

It runs nightly, and it is the only job here that does. Its answer changes without anyone committing anything: the vulnerability database is updated every day, so a build that was clean yesterday can have a reachable vulnerability today. That is also why it is not part of `make ci` — a CVE published overnight would turn every open pull request red for a cause none of them introduced. It runs on pull requests that change `go.mod`, because adding a dependency is a change somebody made and should hear about.

Its first run found fourteen reachable vulnerabilities in the standard library of go1.26.2. The fix was moving to go1.26.9; `go.mod` names the version, so CI and the devcontainer follow it.

Everything else is pinned — the devcontainer image by digest, its feature by lockfile, Docker and Compose by version — and Dependabot proposes updates as pull requests. A scheduled rebuild of pinned inputs would build the same thing every week.


## The layering rules are tests

`cmd/api` is the one package allowed to name a concrete store; everything below it works against an interface it declares itself. That is enforced in `cmd/api/layout_test.go`, because an invariant nobody checks is a preference:

```
internal/leak imports github.com/google/uuid;
only [internal/store/ internal/kafka/ internal/obs/] may reach outside
the standard library
```

Two further rules — that only the composition root may name an implementation, and that the domain imports nothing of ours — skip with a printed reason until the packages they govern exist. A rule that cannot yet be broken is not a rule that is being kept, and saying so beats passing quietly.


## Graceful shutdown

Send `SIGTERM` while a request is in flight and the request finishes:

```
$ ADDR=127.0.0.1:18080 ./bin/api
listening on 127.0.0.1:18080
^C
draining, up to 15s
```

`Shutdown` closes the listeners first, so nothing new is accepted, then waits for connections to go idle. Requests already running get up to 15 seconds. Past that the process exits anyway — a deploy that waits forever on one stuck handler is an outage dressed as politeness.

This is tested rather than asserted: replace `Shutdown` with `Close` and `TestServeDrainsRequestsAlreadyInFlight` fails with an `EOF`, which is what the client sees when a deploy drops a request mid-flight.


## Layout

```
cmd/api/main.go             wiring: configuration, listener, server, shutdown
cmd/api/layout_test.go      the layering rules, beside the root they protect
internal/httpapi/           routes and handlers, no knowledge of how it is started
infra/docker/               the image and the service as it ships
  Dockerfile                two stages: compile, then distroless with one binary
  compose.yaml              the service in a container; stores join it a PR at a time
  Dockerfile.dockerignore   what the build context leaves out
ci/run.sh                   the build. CI calls this and so do you
.devcontainer/              the container a codespace runs, and its start hooks
ci/tools.env                tool versions, read by both make and the script
ci/scan.sh                  govulncheck, nightly and on dependency changes
.github/dependabot.yml      pinned versions move by pull request, not by surprise
docs/roadmap.md             the twenty-two PRs, and why each is in that order
```

Everything the service will eventually talk to arrives behind an interface, so the domain never imports a driver. Today that is one route and no stores; the shape is there to hold.


## Why `/healthz` checks nothing

A liveness endpoint that checks a database turns a slow dependency into an outage: the probe fails, the orchestrator restarts the pod, the new pod finds the same slow database. Liveness answers "is this process wedged". Readiness, which arrives with the stores, is where dependencies belong.


## Docs

- [`docs/roadmap.md`](https://gist.github.com/Kavi-Sarna/e924cf2b8e8d32f45c8456622d4ec848) — the full plan
- [`docs/pr-01-walkthrough.md`](docs/pr-01-walkthrough.md) — this PR, in detail
- [`docs/pr-02-walkthrough.md`](docs/pr-02-walkthrough.md) — one script as the build, and rules that are tests
- [`docs/pr-03-walkthrough.md`](docs/pr-03-walkthrough.md) — the devcontainer, early, and kept honest by tests
- [`docs/pr-04-walkthrough.md`](docs/pr-04-walkthrough.md) — the image, distroless, and the checks only a running container can pass
