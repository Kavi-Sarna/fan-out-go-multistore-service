# PR 1 — walkthrough

**Goal:** a service that builds, answers one request, and stops without dropping work that was already in progress.

**Why this first:** shutdown is the part everyone adds later, and later is after the first deploy has already cut somebody's request in half. It is also the only behavior in PR 1 that can be got *wrong* in a way the compiler will not mention.

**Not in this PR:** any store, any container, any pipeline, and no devcontainer yet — that is PR 3, early and on purpose.


## Step 0 — what "runnable" has to mean here

The discipline is that every PR leaves `main` runnable. For a service whose eventual job is writing to three datastores, that forces a decision in PR 1: **the absence of configuration is a switch, not an error.**

No `ADDR` means `:8080`. Later, no `DATABASE_URL` will mean the in-memory store. The alternative — fail fast when a dependency is unconfigured — sounds more rigorous and means `make run` stops working in PR 6 and never starts again until PR 8.


## Step 1 — three functions, because one of them needs testing

```go
func main()                                                    // signals, exit code
func run(ctx, logger, addr) error                              // wiring
func serve(ctx, logger, server, listener, grace) error         // start and drain
```

`main` is four lines and does nothing worth a test. `run` opens a listener and builds a server. `serve` is the only one with behavior that can be wrong, so it is the only one shaped for a test: give it a handler that sleeps, cancel its context, and see whether the request finishes.

That split is the whole reason the tests in this PR are worth anything. A `main()` that inlines all three is testable only by starting a subprocess and sending it signals, which is slow, flaky, and skipped.


## Step 2 — the listener is opened before the server runs

```go
var config net.ListenConfig
listener, err := config.Listen(ctx, "tcp", addr)
if err != nil {
    return fmt.Errorf("listening on %s: %w", addr, err)
}
```

`http.ListenAndServe` would do both at once, and a port already in use would surface from inside a goroutine *after* the process looks healthy. Opening the listener first turns that into a startup error with the address in it.

`net.ListenConfig` rather than `net.Listen` so the bind itself is cancellable: a `SIGTERM` arriving during startup stops the process instead of being ignored until the listener is up.

There is a test for it, because "it fails clearly" is a claim:

```go
func TestRunReportsAPortAlreadyInUse(t *testing.T) {
    taken := listen(t)
    defer taken.Close()
    err := run(context.Background(), taken.Addr().String())
    // ... errors.As(err, &net.OpError)
}
```


## Step 3 — draining, and the context that must not be the one you have

```go
case <-ctx.Done():
}

drain, cancel := context.WithTimeout(context.WithoutCancel(ctx), grace)
defer cancel()

if err := server.Shutdown(drain); err != nil {
    return fmt.Errorf("draining: %w", err)
}
```

The subtle line is `context.WithoutCancel`.

We arrive here *because* `ctx` was canceled. Passing that same `ctx` to `Shutdown` tells it to give up immediately — the deadline it is handed is already in the past. The result looks like graceful shutdown, compiles, runs, and drops every in-flight request. It is the exact bug this PR exists to not have.

`WithoutCancel` keeps the context's values, such as a trace, while dropping its cancellation, and `WithTimeout` then imposes a fresh deadline. Before Go 1.21 this had to be `context.Background()`, losing the values with it.

What `Shutdown` actually does is worth knowing precisely:

1. closes the listeners, so nothing new is accepted
2. closes idle connections
3. waits for active connections to go idle, or for the deadline

And what it does **not** do: wait for hijacked connections. A WebSocket or anything that took over the connection needs its own shutdown path. There isn't one here, because there is nothing hijacking — but that is a fact about today, not a property of the code.


## Step 4 — the test that fails if you delete the feature

A test that passes whether or not the code is correct is worse than no test, because it is also a claim.

```go
func TestServeDrainsRequestsAlreadyInFlight(t *testing.T) {
    // handler closes `started`, sleeps 300ms, writes "finished"
    <-started     // shut down only once the handler is provably running
    cancel()
    // assert the client read "finished"
}
```

The `started` channel matters. Without it the test would race the request against the shutdown, and would sometimes pass by the request never having arrived. Waiting for the handler to *begin* makes this a test about draining rather than about timing.

Checked by removing the feature. Replace `Shutdown(drain)` with `Close()`:

```
--- FAIL: TestServeDrainsRequestsAlreadyInFlight
    in-flight request got "request failed: Get \"http://127.0.0.1:64486/slow\": EOF"
```

`EOF` is exactly what a client sees when a deploy drops its request. That is the failure this PR buys you out of, and now it is a red test rather than a paragraph.

Two more hold the edges: the listener really is closed afterwards (dialling the address is refused), and a handler that outlasts the grace period surfaces the deadline instead of hanging the process forever.


## Step 4b — the race that turns a crash into a clean exit

Review found this, and it is the kind of bug that only appears in the incident review.

```go
select {
case err := <-failed:   // Serve failed for a real reason
    ...
case <-ctx.Done():      // we were asked to stop
}
```

Both cases can become ready at the same instant — a genuine `Serve` failure landing at the same moment as the `SIGTERM` that cancels `ctx` — and Go's `select` picks between ready cases **at random**. Take the `ctx.Done()` branch and the error sitting in `failed` is never read: `Shutdown` returns promptly because the server has already stopped, `serve` returns `nil`, and the process exits **0** with the crash unlogged.

The fix is to read the channel again after draining rather than to make the select cleverer:

```go
shutdownErr := server.Shutdown(drain)

if err := <-failed; err != nil {
    return fmt.Errorf("serving: %w", err)
}
if shutdownErr != nil {
    return fmt.Errorf("draining: %w", shutdownErr)
}
```

It cannot block. `Serve` has returned by the time `Shutdown` does, and the goroutine either sent an error or closed the channel — a receive on a closed channel yields the zero value immediately.

The ordering matters too: a real serve failure is the more interesting of the two, so it is reported even when the drain also timed out.


## Step 4c — structured logging, from the first line

The roadmap commits to structured logging in PR 1, with the reasoning that converting a codebase's log calls in bulk later is a PR nobody wants to review. The first draft of this PR used `log.Printf` anyway, which review caught.

The fix is not a dependency. `log/slog` has been in the standard library since Go 1.21:

```go
logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
```

```json
{"time":"...","level":"INFO","msg":"listening","addr":"127.0.0.1:18080"}
{"time":"...","level":"INFO","msg":"draining","grace":"15s"}
```

The logger is passed into `run` and `serve` rather than reached for globally, which costs one parameter and means the tests can hand them a logger that writes to `io.Discard`. Whether `zap` is worth a dependency for its speed is a question for PR 16, when there is something in the hot path to measure.


## Step 5 — `/healthz` checks nothing, on purpose

```go
func healthz(w http.ResponseWriter, r *http.Request) {
    w.Header().Set("Content-Type", "application/json")
    w.WriteHeader(http.StatusOK)
    _ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}
```

Three deliberate things.

**It touches no dependency.** A liveness probe that checks a database turns a slow database into an outage: the probe fails, the orchestrator restarts the pod, and the new pod meets the same slow database. Liveness answers "is this process wedged". Readiness is where dependencies belong, and it arrives with the stores.

**The header is set before `WriteHeader`.** After it, the map has already been serialized into the response and the `Set` is silently discarded. This is a rule rather than a judgement call: headers first, status, then body.

**The encode error is ignored, with a comment.** The only way it fails is a broken connection, and there is nobody left to tell. `errcheck` would flag a bare `json.NewEncoder(w).Encode(...)`; `_ =` says the omission was a decision.

Routing is `"GET /healthz"` rather than a method check inside the handler, which Go 1.22's `ServeMux` made possible — and the tests assert that `POST /healthz` is **405**, not 404. A client that gets 404 concludes the endpoint does not exist and stops looking.


## Step 6 — the one server setting that is not optional

```go
server := &http.Server{
    Handler:           httpapi.Routes(),
    ReadHeaderTimeout: readHeaderTimeout,
}
```

Go's zero value for every server timeout is "no timeout". A client that opens a connection and sends headers one byte at a time holds a goroutine for as long as it likes — Slowloris, and the reason `gosec` has a rule for this one specifically.

The others — `ReadTimeout`, `WriteTimeout`, `IdleTimeout` — are prudent and not yet decidable: their right values depend on what the handlers do, and the handlers do nothing yet. They arrive with the stores.


## Step 6b — three smaller things review caught

**`make tidy` could never fail.** It ended `git diff --exit-code go.mod go.sum 2>/dev/null || true`, and `|| true` zeroes the exit code unconditionally. The target's own help text says "fail if it changed". It also looked fine because there is no `.git` yet, so it was going to be wired into CI in PR 2 as a check that cannot check anything.

**A second Ctrl-C did nothing during the drain.** `signal.NotifyContext` keeps intercepting until `stop()` runs, which was deferred to the end of `main` — so for fifteen seconds the only way out was `SIGKILL`. Restoring default handling as soon as the first signal lands fixes it:

```go
go func() {
    <-ctx.Done()
    stop()
}()
```

Irrelevant in Kubernetes, which sends `SIGKILL` after `terminationGracePeriodSeconds` regardless. Entirely relevant at a terminal.

**`gosec` was switched off for every test file.** Convenient, and wrong in a specific foreseeable way: PRs 9 and 12 put real connection strings into `_test.go` fixtures, which is what G101 exists to catch. The exclusion now lists only `errcheck` and `revive`, and the two unchecked `Close()` calls that G104 then flagged are written as `_ = conn.Close()` — which is what they always meant.


## Step 7 — the linters, and why `noctx` was obeyed

The config starts from golangci-lint's `standard` set and adds twelve. Each is there for a specific way *this* service can go wrong: `bodyclose` and `sqlclosecheck` for leaked connections, `contextcheck` for a `context.Background()` where a request context belongs, `errorlint` for `==` on a wrapped error, `govet`'s `shadow` for the shadowed `err` that silently drops a failure.

`noctx` initially failed the tests rather than the code — `http.Get`, `net.Listen`, `httptest.NewRequest`. The tempting fix is to exclude tests from it. Obeying it instead produced `net.ListenConfig.Listen`, which is how the bind became cancellable in step 2. The linter was making a better point than the one it was asked about.


## Definition of done

- [ ] `make run` serves, and `curl /healthz` returns 200 with JSON
- [ ] `make lint`, `make test` and `make race` all pass
- [ ] `SIGTERM` lets an in-flight request finish, then refuses new connections
- [ ] Replacing `Shutdown` with `Close` turns a test red
- [ ] `POST /healthz` is 405 and `/nope` is 404
- [ ] A port already in use fails at startup, naming the address


## Files in this PR

| File                                  |                                                                                   |
| ------------------------------------- | --------------------------------------------------------------------------------- |
| `go.mod`                              | the module, pinned to Go 1.26                                                     |
| `.gitignore`                          | `bin/` and `coverage.out`, before the first `git add .` picks up an 8 MB binary   |
| `cmd/api/main.go`                     | signals, wiring, the drain, and `log/slog` from the first line                    |
| `cmd/api/main_test.go`                | four tests; one fails if the drain is removed                                     |
| `internal/httpapi/httpapi.go`         | routes and `/healthz`                                                             |
| `internal/httpapi/httpapi_test.go`    | 200, 405 and 404                                                                  |
| `Makefile`                            | `fmt`, `vet`, `lint`, `test`, `race`, `cover`, `build`, `run`, `tidy`             |
| `.golangci.yml`                       | the standard set plus twelve, each for a specific failure                         |
| `README.md`                           | what it is and how to see it work                                                 |

Next: **PR 2** — `ci/run.sh` and a workflow that calls it, so the gate and the local command are the same program.
