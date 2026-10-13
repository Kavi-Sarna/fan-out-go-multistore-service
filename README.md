# fan-out-go-multistore-service
One write, three stores, all or none: distributed-transaction semantics in Go over Postgres, Cassandra and Kafka.

That requirement is the whole point. No transaction primitive spans those three — Postgres is ACID on its own, Cassandra offers Paxos per partition and no more, and Kafka's transactions stop at Kafka's own edge. There is no coordinator to enlist all three, so all-or-nothing is not something you configure. It is something you build.

The worked example is device sessions. A session carries an IP and a user agent: fields that are useful, identifying, and one careless line away from a log nobody can un-write.

> **Status: PR 2 — one script is the build.** `GET /healthz`, a shutdown that lets in-flight requests finish, and a pipeline that CI and a laptop run by invoking the same file. No database yet; that is PR 7.


## Quick start

```bash
make run        # listens on :8080
curl localhost:8080/healthz
```

```bash
make ci         # everything the pipeline does, in the same order
make test       # just the tests
make race       # the same, under the race detector
make lint       # golangci-lint
make build      # bin/api
make help       # everything else
```

Needs Go 1.26 and nothing else. `golangci-lint` and `goimports` are pinned in `ci/tools.env` and installed into `.tools` on first use.


## The build is one script

`ci/run.sh` is what CI runs and what `make ci` runs. If those two ever differed, CI would be a different program and its result an opinion about that other program.

```
==> tools     ==> tidy      ==> format
==> vet       ==> lint      ==> test      ==> build
```

Tests run with `-race` and `-shuffle=on` on every invocation rather than in a nightly job. A data race found a week later is a race nobody can attribute to a change, and shuffling makes an order dependency between tests fail here rather than intermittently for whoever runs them next.


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
cmd/api/main.go       wiring: configuration, listener, server, shutdown
cmd/api/layout_test.go the layering rules, beside the root they protect
internal/httpapi/     routes and handlers, no knowledge of how it is started
ci/run.sh             the build. CI calls this and so do you
ci/tools.env          tool versions, read by both make and the script
docs/roadmap.md       the twenty-two PRs, and why each is in that order
```

Everything the service will eventually talk to arrives behind an interface, so the domain never imports a driver. Today that is one route and no stores; the shape is there to hold.


## Why `/healthz` checks nothing

A liveness endpoint that checks a database turns a slow dependency into an outage: the probe fails, the orchestrator restarts the pod, the new pod finds the same slow database. Liveness answers "is this process wedged". Readiness, which arrives with the stores, is where dependencies belong.


## Docs

- [`docs/roadmap.md`](https://gist.github.com/Kavi-Sarna/e924cf2b8e8d32f45c8456622d4ec848) — the full plan
- [`docs/pr-01-walkthrough.md`](docs/pr-01-walkthrough.md) — this PR, in detail
- [`docs/pr-02-walkthrough.md`](docs/pr-02-walkthrough.md) — one script as the build, and rules that are tests
