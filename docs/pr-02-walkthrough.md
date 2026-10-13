# PR 2 — walkthrough

**Goal:** one script that is the build, called by CI and by a laptop, plus the layering rules written as tests rather than as a paragraph.

**Why now:** before there is much code. A pipeline added after twenty files exist is a pipeline that spends its first week failing on things nobody chose; added at two, it is a gate that only ever fails on new work.

**Not in this PR:** the devcontainer (PR 3) and the Dockerfile (PR 4).


## Step 0 — the thesis, and the trap inside it

`ci/run.sh` is what CI runs and what `make ci` runs.

If those two ever differ, CI is a *different program* and its verdict is an opinion about that other program. Every "works on my machine" argument is really a disagreement about which program was run.

The trap is that saying this is easy and the drift happens one level down. The first version of this PR declared `GOLANGCI_VER := v2.11.2` in the Makefile **and** `GOLANGCI_VER="${GOLANGCI_VER:-v2.11.2}"` in the script — two copies of the same pin, which is the identical failure mode wearing a different hat. They now share one file:

```bash
# ci/tools.env
GOLANGCI_VER=v2.11.2
GOIMPORTS_VER=v0.39.0
```

`NAME=value` is the intersection of what `make include` and bash `source` both understand, which is the whole reason the file looks like that.

The module path got the same treatment: `go list -m` in both, rather than the literal written out three times.


## Step 1 — the steps, and the two flags worth defending

```
==> tools    ==> tidy    ==> format    ==> vet
==> lint     ==> test    ==> build
```

**`-race` on every run, not nightly.** A data race found a week later is a race nobody can attribute to a change; found on the PR, it belongs to whoever is already looking at the code. The cost is a couple of seconds at this size.

**`-shuffle=on`.** Tests that pass in declaration order and fail in any other order are a bug you have already written and have not met yet. Shuffling means it fails here rather than intermittently for whoever runs them next.

**The format step checks rather than writes.** `gofmt -s -l` and `goimports -l` list what *would* change; CI that reformats produces a diff nobody asked for. Both tools report the same file when both would change it, hence `sort -u` — a cosmetic detail worth one line because a file listed twice reads like two problems.


## Step 2 — the tidy step, which took three attempts

The requirement: a `go.mod` that disagrees with the imports is a build that works until somebody runs it somewhere else.

The first version did this by hand — copy `go.mod` and `go.sum` aside, run `go mod tidy`, diff against the copies, restore via a trap. It worked, it passed review's reading of the shell semantics, and it had a hole that only opens on one specific PR.

**`go.sum` does not exist yet**, because there are no dependencies. So the backup was conditional:

```bash
[[ -f go.sum ]] && cp go.sum go.sum.ci-backup
...
if [[ -f go.sum.ci-backup ]]; then      # never true on that run
  diff -u go.sum.ci-backup go.sum || exit 1
fi
```

On the PR that adds the project's first dependency and forgets to commit `go.sum`, `go mod tidy` *creates* it, the guard skips the diff because there was never a backup, and the pipeline goes green on a file that exists only on that runner. Reviewed, reproduced, confirmed.

The fix is not a better backup. It is a flag that already exists:

```bash
if ! go mod tidy -diff; then
  echo "go.mod or go.sum is not tidy: run 'make tidy' and commit the result" >&2
  exit 1
fi
```

`-diff` prints what tidy would change and exits non-zero if that is anything, without touching either file. It covers the never-existed case because it compares against what *should* be there rather than against a snapshot of what was. Reproducing the same scenario now gives:

```
==> tidy
diff current/go.sum tidy/go.sum
--- current/go.sum
+++ tidy/go.sum
@@ -0,0 +1,2 @@
+github.com/google/uuid v1.6.0 h1:NIvaJDMOsjHA8n1jAhLSgzrAzy1Hgr+hNrb57e+94F0=
+github.com/google/uuid v1.6.0/go.mod h1:TIyPZe4MgqvfeYDBFedMoGGpEw/LqOeaOT+nhxU+yHo=
```

Thirty lines of backup, trap and restore deleted along with the bug they were carrying. The `Makefile`'s `tidy` target lost its git-diff dance for the same reason.


## Step 3 — a pinned tool that is not actually pinned

The install guard was existence-based:

```bash
[[ -x "${TOOLS}/golangci-lint" ]] || go install ...@${GOLANGCI_VER}
```

Bump the version and nothing happens: the file exists, the check short-circuits, and the laptop keeps running whatever it installed months ago while CI — always a fresh checkout — runs the new one. The exact laptop-versus-CI divergence this PR exists to prevent, moved one level down and made invisible.

Keying the path on the version fixes it, because a bump changes the filename and therefore the target:

```
.tools/golangci-lint-v2.11.2
.tools/goimports-v0.39.0
```

`go install` names the binary after the package and cannot be told to add a suffix, so both the script and the Makefile install into a scratch `GOBIN` and rename. Three lines, and the pin now means what it says.


## Step 4 — the layering rules, as tests

You asked whether the implementations would stay out of the calling code. The answer is interfaces declared by their consumers — and a test, because an invariant nobody checks is a preference, and preferences lose arguments.

`cmd/api/layout_test.go` holds three rules. It lives beside the composition root because that is what they are about: `cmd/api` is the one package allowed to name a concrete store.

**Rule 1 — only adapters may reach outside the standard library.**

```go
func thirdParty(path string) bool {
    if strings.HasPrefix(path, module) {
        return false
    }
    first, _, _ := strings.Cut(path, "/")
    return strings.Contains(first, ".")
}
```

A dot in the first path element is how the go command itself distinguishes a module path from the standard library. Stated as "nothing outside stdlib" rather than as a list of driver names, this has teeth **today**:

```
internal/leak imports github.com/google/uuid;
only [internal/store/ internal/kafka/ internal/obs/] may reach outside
the standard library
```

The version I first proposed — fail if anything imports `pgx` — would have passed trivially until PR 8, because `pgx` is not in `go.mod` yet. A test that cannot fail is a claim, not a check.

Note `strings.HasPrefix(relative+"/", prefix)` with the trailing slash on both sides: without it, `internal/storage` would match the prefix `internal/store/` and quietly inherit permission it was never granted.

**Rules 2 and 3 skip, and say so.**

```
layout_test.go:125: no implementation package exists yet; this begins enforcing when one does
layout_test.go:153: internal/session does not exist yet; this begins enforcing when it does
--- SKIP: TestOnlyTheCompositionRootNamesAnImplementation
--- SKIP: TestTheDomainImportsNothingOfOurs
```

Review caught that rule 2 was worse than vacuous: the only way to trip it today is to import a package that does not exist, which fails the build for a reason that has nothing to do with layering. So it announces itself instead. A green line that checked nothing is the one kind of test that is worse than no test, because it is also a claim.

One more thing review found: the self-import check used `strings.HasSuffix(implementation, relative)`, which is correct for these three names and only by coincidence. It is now a direct comparison against the path with the module prefix trimmed.


## Step 5 — the workflow is four lines

```yaml
- uses: actions/checkout@v7
- uses: actions/setup-go@v7
  with:
    go-version-file: go.mod
    cache: true
- run: ./ci/run.sh
```

`go-version-file: go.mod` so the toolchain is pinned in one place rather than in two that drift — the theme of this PR, again.

`concurrency` with `cancel-in-progress` because a second push makes the first run's answer irrelevant. `permissions: contents: read`, since this workflow reads code and nothing else. And `timeout-minutes: 15`, because without it a hung module download sits on a runner for GitHub's six-hour default before anyone finds out.


## Definition of done

- [ ] `make ci` and `./ci/run.sh` are the same program
- [ ] An unformatted file, a failing test, and an untidy `go.mod` each fail it
- [ ] A first dependency with no committed `go.sum` fails it
- [ ] Bumping a tool version in `ci/tools.env` installs that version
- [ ] A third-party import outside an adapter fails the tests
- [ ] Rules that cannot yet be broken say so rather than passing


## Files in this PR

| File                          |                                                           |
| ----------------------------- | --------------------------------------------------------- |
| `ci/run.sh`                   | the build: tools, tidy, format, vet, lint, test, build    |
| `ci/tools.env`                | tool versions, read by both make and the script           |
| `.github/workflows/ci.yml`    | four lines that call the script                           |
| `cmd/api/layout_test.go`      | three layering rules; one has teeth today, two say so     |
| `Makefile`                    | a `ci` target, version-keyed tools, a simpler `tidy`      |
| `README.md`                   | status, `make ci`, and what the layering rules enforce    |

Next: **PR 3** — the devcontainer, deliberately early, with the rule that every later PR adding a service also forwards its port.
