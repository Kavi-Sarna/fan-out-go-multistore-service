# PR 3 — walkthrough

**Goal:** a devcontainer that opens with the service already running, and that stays correct as the stack grows because tests and CI fail when it does not.

**Why now, at PR 3:** a devcontainer added late and never revisited drifts until the thing it exists to show is unreachable from it. Ports nobody forwarded, a machine sized for half the stack. Added while there is one service, it is nearly free, and it starts the habit that keeps it honest.

**Not in this PR:** Compose, which arrives with the Dockerfile in PR 4.


## Step 0 — what drifts, and how to stop it

The failure mode is specific. A devcontainer written once, against the stack as it was, goes quietly stale: a service is added, its port is not forwarded, and the Ports panel in a codespace simply never shows it. Nothing errors. It is just missing on the day someone tries to demonstrate it.

A rule in a checklist does not prevent that, because checklists are skimmed. So the rule is in two places that fail:

- **unit tests** that read `devcontainer.json` and compare it against the code, and
- **a CI job** that builds the container and runs the pipeline inside it.

The first proves the file says the right things. The second proves the container it describes actually starts.


## Step 1 — the image, the feature, and the machine

```json
"image": "mcr.microsoft.com/devcontainers/go:1.26-trixie@sha256:8b574a68...",
"features": {
  "ghcr.io/devcontainers/features/docker-in-docker:4": {
    "moby": false,
    "version": "29.9",
    "dockerDashComposeVersion": "v2"
  }
}
```

The Go devcontainer image rather than a base image plus a Go feature: the toolchain, `gopls` and the debugger arrive together and agree on versions. **Trixie** is Debian 13, the current stable. Pinned by digest, because a tag is a promise and a digest is a fact.

The digest is worth one check. A multi-arch tag resolves to an *index* that lists one manifest per platform; pin a platform-specific digest instead and the container works on one architecture and fails on the other. This one is an OCI index covering `linux/amd64` and `linux/arm64`, so it serves both a Codespaces VM and an ARM laptop.

Docker-in-docker is here now so that adding a Compose service later never means rebuilding the container just to get a daemon.

### The machine size, and a correction

The first draft asked for four cores and 16 GB, sized for the finished stack — three JVMs among Postgres, Cassandra, Kafka and the Schema Registry. The argument was that `hostRequirements` only applies at creation, so a two-core codespace made today could not grow later.

That argument was wrong. A codespace's machine type can be changed at any time after it is created, from the codespaces list or *Codespaces: Change Machine Type*. `hostRequirements` is a **floor on the picker**, not a fixed size. So there was no reason to pay for the finished stack while it is one Go binary:

```json
"hostRequirements": { "cpus": 2, "memory": "8gb", "storage": "32gb" }
```

On GitHub Free's 120 core-hours, two cores is 60 hours a month against 30 on four. The roadmap's Definition of Done now says the PR that adds something heavy raises the floor in the same change — the same discipline as forwarding a port, applied to the machine.


## Step 1b — the build that would have failed

The first draft of this file would not have produced a codespace at all.

Trixie is the current Debian, so it was the obvious image. The docker-in-docker feature installs *moby* — Microsoft's build of the Docker engine — by default. And Microsoft publishes no moby packages for trixie: `moby-engine`, `moby-cli` and `moby-compose` exist in its bookworm repository and nowhere in trixie's. The feature knows this, and guards against it:

```
The 'moby' option is not supported on debian 'trixie' because 'moby-cli' and
related system packages are not available in that distribution.
To continue, either set the feature option '"moby": false' or use a
different base image.
```

Exit 1, during codespace creation.

Nothing caught it. There is no Docker on the machine this was written on, so the container was never built; review read the file and could not build it either; and the CI job that builds the container has never run, because the repository is not on GitHub yet. Every check that existed was a check on what the file *said*.

It surfaced because of a question about something else — why the workflow had a cron — which led to reading what the feature installs, which led to the guard. `"moby": false` installs Docker CE from Docker's own repository, which does publish for trixie.

The lesson is narrower than "test more". The unit tests here are genuinely useful and would never have found this, because the failure lives in a script that runs inside an image, on a distribution, against a package repository. That is exactly what the CI job that builds the container is for, and the reason it exists is now concrete rather than theoretical.


## Step 1c — pinning what was not pinned, and dropping the cron

The first draft had a weekly schedule on the devcontainer workflow, justified as "the image is pinned by digest, but the feature is pinned only to its major version and can move".

Reading the feature's defaults showed something stronger:

```
version                   default='latest'   Docker engine
dockerDashComposeVersion  default='latest'   Docker Compose
```

The feature installs the newest Docker engine and the newest Compose every time the container is built. Two builds a month apart got different versions of exactly the tools every later Compose file depends on.

A schedule that rebuilds to see whether anything moved is a way of finding out about unpinned inputs. Pinning them is the better fix, and it is what the rest of this project already does.

| Input             | How it is pinned                              |
| ----------------- | --------------------------------------------- |
| Go image          | by digest                                     |
| The feature       | by lockfile, `devcontainer-lock.json`         |
| Docker engine     | `"version": "29.9"`                           |
| Docker Compose    | by Debian package version, in `on-create.sh`  |
| Go tools          | `ci/tools.env`                                |

**The lockfile** is the devcontainer spec's own mechanism, and it divides the work the way `go.mod` and `go.sum` do: `devcontainer.json` names the feature by major version, and the lockfile records the exact version, a digest, and an integrity hash:

```json
"ghcr.io/devcontainers/features/docker-in-docker:4": {
  "version": "4.1.3",
  "resolved": "ghcr.io/devcontainers/features/docker-in-docker@sha256:fae40f3c...",
  "integrity": "sha256:fae40f3c..."
}
```

Pinning `:4.1.3` in `devcontainer.json` instead would fight Dependabot, which keeps that line on the major and rewrites the lockfile. The devcontainer CLI generated it without Docker — `npx @devcontainers/cli upgrade` resolves features against the registry — and a test now fails if a feature is added without regenerating it.

**The engine version** is matched by the feature as `^(.+:)?29\.9([\.\+ ~:-]|$)`, so `29.9` takes the newest 29.9.x and cannot accidentally match `29.90`. 29.9.0 exists for trixie on both architectures; pinning a version that did not exist would have been the moby problem again.

**Compose cannot be pinned through the feature at all.** Its only options are `none`, `latest`, `v1` and `v2`, and with moby off it runs `apt-get install docker-compose-plugin` with no version. So `on-create.sh` installs a specific one and holds it:

```bash
readonly compose_version="5.6.0-1~debian.13~trixie"
sudo apt-get install -y --allow-downgrades "docker-compose-plugin=${compose_version}"
sudo apt-mark hold docker-compose-plugin
```

The plugin is at 5.6.0 — Compose's numbering has moved well past v2 — which is itself the argument for pinning it.

**Dependabot** proposes updates as pull requests, which the devcontainer workflow then builds. Drift becomes something reviewed instead of something discovered. It covers the feature and the GitHub Actions; it does *not* cover the image digest, the engine version or the Compose version, and the config says so rather than implying otherwise.

With every input pinned, a weekly rebuild would build the same thing every week. The schedule is gone.


## Step 1d — the schedule that does make sense

There is one job whose answer changes when nobody commits anything: a vulnerability scan. The database is updated daily, so a build that was clean yesterday can have a reachable vulnerability today. Nothing in the repository moved; the world did. That is what a schedule is for.

The companion Spark project scanned its container image. This service has no image until PR 4, so the scanner is `govulncheck`, Go's own. It checks the code, its dependencies and **the standard library** against the Go vulnerability database, and it reports only vulnerabilities in functions this code actually calls — a finding is a path to vulnerable code, not a version number to argue about.

It runs nightly, on pull requests that change `go.mod` — adding a dependency can introduce a vulnerability, and that is a change somebody made — and via `make scan`. It is deliberately not in `ci/run.sh`: a CVE published overnight would turn every open pull request red for a cause none of them introduced.

**Its first run found fourteen.** All in the standard library of go1.26.2, all reachable, with traces through this code:

```
Vulnerability #13: GO-2026-5037
    Inefficient candidate hostname parsing in crypto/x509
    Found in: crypto/x509@go1.26.2
    Fixed in: crypto/x509@go1.26.4
    Example traces found:
      #1: cmd/api/main.go:105:25: api.serve calls http.Server.Serve,
          which eventually calls x509.Certificate.Verify
```

Go 1.26.9 was current — seven patch releases ahead. Patch releases carry only security and bug fixes, so the move was to the latest rather than to the minimum that fixed these:

```
go 1.26.9
```

`go.mod` names the version, so everything follows it: the local toolchain downloaded 1.26.9 on the next `go` command, CI reads it through `go-version-file`, and the devcontainer will do the same. The rescan:

```
Go: go1.26.9
Scanner: govulncheck@v1.9.0
No vulnerabilities found.
```

That is the case for a scan made in one paragraph: the service had been building with fourteen known, reachable vulnerabilities since PR 1, and no other check here could have said so.


## Step 2 — the two hooks, split by how often they run

```json
"onCreateCommand": ".devcontainer/on-create.sh",
"postStartCommand": ".devcontainer/post-start.sh",
"waitFor": "onCreateCommand"
```

**`on-create.sh`** runs once and does the slow things: `go mod download`, `make tools`, `make build`. It is `set -euo pipefail`, and failing creation is the right severity — a codespace that cannot build is one you want to know about immediately, and Codespaces drops you into a recovery container with the log.

**`post-start.sh`** runs on *every* start, including a resume after the 30-minute idle stop. That split matters: a service started only on create would be gone the second time the codespace opened.


## Step 3 — the start hook must never fail the start

`post-start.sh` is deliberately **not** `set -e`. A service that will not start should leave you with a working editor to fix it in, not a codespace that refuses to open. Every failure path warns and exits 0, and that is verified rather than intended: put a line that is not Go into `main.go` and the hook prints

```
WARNING: the build failed; run 'make build' to see why.
```

and the codespace opens anyway.

### Surviving the hook

The subtle line is how the service is launched:

```bash
ADDR="${addr}" ${launcher[@]+"${launcher[@]}"} nohup ./bin/api >"${log}" 2>&1 </dev/null &
```

where `launcher` is `setsid` when it exists.

`nohup` alone only ignores `SIGHUP`. The process still belongs to the hook's session and process group, and whether the lifecycle runner signals that group when the hook exits depends on the runner's version. `setsid` puts the service in a session of its own, and `</dev/null` closes the last stream tying it to the hook. Without both, the likeliest real failure is the quiet one: the hook reports success, exits, and takes the service with it.

`setsid` is util-linux, so it is in the Debian image and absent on macOS. The fallback keeps the script runnable anywhere — which is how it was tested — and `${x[@]+"${x[@]}"}` is there because macOS ships bash 3.2, which treats an empty array as unset under `-u` and aborts.

### Knowing the service is ours

The first version decided "already running" from whether `/healthz` answered 200. Anything answering 200 on that port would have been mistaken for the service. Now the hook records the PID it launched and checks both:

```bash
ours() { [[ -f "${pidfile}" ]] && kill -0 "$(cat "${pidfile}")" 2>/dev/null; }

if ours && healthy; then
  echo "the service is already running on ${addr}"
elif healthy; then
  echo "WARNING: something else is serving ${addr}; not starting a second copy."
```

Tested with a Python server holding the port: the hook names the problem instead of claiming success.


## Step 4 — the rules, as tests

`cmd/api/devcontainer_test.go`, five tests. The interesting part is what each compares against.

**The service port is read from `defaultAddr`**, not written out again. Change the port the service listens on without changing the devcontainer and the build fails:

```
port 8080 is what the service listens on but is not in forwardPorts
```

**Every forwarded port has a label, and every label a forwarded port.** An unlabelled port is a bare number in the Ports panel; a label without a port is a leftover from a service that was removed.

**The start hook checks the same port.** Review spotted that `post-start.sh` health-checks a port of its own, which is a second copy of the number. Change `defaultAddr` and the JSON but not the script, and the hook probes a port nothing listens on, forever and silently. The test reads `readonly port=8080` out of the script and compares:

```
post-start.sh checks port 9090, but the service listens on 8080
```

**The hooks are executable.** A hook without its execute bit fails with "permission denied" at creation — the one moment nobody is reading the log:

```
on-create.sh is not executable (mode -rw-r--r--)
```

### Parsing JSONC without a parser

`devcontainer.json` allows comments. Rather than add a dependency for one test, the file keeps to the subset the standard library reads once full-line comments are stripped: no trailing commas, no comment after a value.

The first version also used strict types — `[]int` for ports and `map[string]map[string]string` for attributes. Both reject valid configuration: `forwardPorts` may hold `"db:5432"`, and `requireLocalPort` is a boolean. Either would have failed with a message blaming the comment format. The types are now `[]any` and `map[string]any`, and the error names both possible causes rather than guessing.


## Step 5 — the editor runs the same linter as CI

```json
"remoteEnv": {
  "PATH": "${containerWorkspaceFolder}/.tools:${containerEnv:PATH}"
},
"settings": {
  "go.lintTool": "golangci-lint-v2"
}
```

Three corrections are folded into those lines.

**`--fast` does not exist in golangci-lint v2.** The first draft passed it in `go.lintFlags`. Against the pinned binary it is `Error: unknown flag: --fast`, and nothing would have said so except a broken lint in the editor. The flag is gone, and so is the idea: a faster subset in the editor is a *different set of rules*, and therefore a file that is clean locally and red in CI.

**`golangci-lint-v2`** is a separate value the Go extension supports, because v2 removed flags its v1 integration passes.

**The binary is found by PATH, not by a hardcoded path.** The first draft named `golangci-lint-v2.11.2` in an `alternateTools` setting — a second copy of the pin PR 2 consolidated into `ci/tools.env`. Now `.tools` is on the container's PATH, and `.tools/golangci-lint` is a symlink `make tools` re-points at the pinned version. `post-start.sh` runs `make tools` on every start, so a pin bumped while the codespace was stopped reaches the editor on the next open.


## Step 6 — building the container in CI

```yaml
- uses: devcontainers/ci@v0.3
  with:
    runCmd: make ci
    push: never
```

This builds the container from `devcontainer.json`, runs the lifecycle hooks, then runs the pipeline inside it. It is the difference between a devcontainer that is correct and one that merely parses.

It runs on pull requests that touch the container's inputs — `.devcontainer/`, `ci/`, the `Makefile`, `go.mod` — and that is all. With every input pinned there is nothing for a schedule to catch, and a Dependabot pull request that moves a pin is one of those changes, so the update is built before it lands.


## Definition of done

- [ ] A codespace opens with the service running and port 8080 forwarded
- [ ] A broken build still opens the codespace, with a warning
- [ ] A second start finds the running service rather than starting another
- [ ] Changing the service port without the devcontainer fails a test
- [ ] A hook without its execute bit fails a test
- [ ] The editor lints with the same binary and rules as CI
- [ ] CI builds the container and runs `make ci` inside it
- [ ] Every input to the container is pinned, and a feature without a lock entry fails a test
- [ ] `make scan` reports no reachable vulnerabilities, and runs nightly


## Files in this PR

| File                                      |                                                                                                                   |
| ----------------------------------------- | ----------------------------------------------------------------------------------------------------------------- |
| `.devcontainer/devcontainer.json`         | the image, docker-in-docker with moby off and the engine pinned, two cores, the forwarded port, `.tools` on PATH  |
| `.devcontainer/devcontainer-lock.json`    | the feature pinned to an exact version and digest                                                                 |
| `.devcontainer/on-create.sh`              | once, at creation: Compose pinned and held, dependencies, tools, a build                                          |
| `.devcontainer/post-start.sh`             | every start: re-point tools, rebuild, launch the service so it survives the hook                                  |
| `.github/workflows/devcontainer.yml`      | builds the container and runs `make ci` inside it; no schedule                                                    |
| `.github/workflows/scan.yml`              | govulncheck nightly, and on pull requests that change dependencies                                                |
| `.github/dependabot.yml`                  | pinned versions move by pull request                                                                              |
| `ci/scan.sh`                              | the scan, pinned and installed like every other tool                                                              |
| `ci/tools.env`                            | `govulncheck` joins the pins                                                                                      |
| `cmd/api/devcontainer_test.go`            | six tests tying the devcontainer to the code, including the lockfile                                              |
| `go.mod`                                  | go1.26.9, which clears fourteen reachable standard-library vulnerabilities                                        |
| `Makefile`                                | `make scan`, and stable symlinks to the pinned tools                                                              |
| `README.md`                               | the Codespaces badge, sizing, and the scan                                                                        |
| `docs/roadmap.md`                         | the Definition of Done covers `hostRequirements` too                                                              |

Next: **PR 4** — a multi-stage Dockerfile and a Compose file with the service in it, an image scan alongside `govulncheck`, and the devcontainer test extended to read Compose's ports.
