# fleetfind

**Which repository already does this?**

```
fleetfind -refresh             # ~377 gh calls: one dated inventory, then the file/function index
fleetfind -index               # only the index, from the module proxy: 0 GitHub calls
fleetfind odf rtf markdown
```

```
go-odf/odf                         ODT (OpenDocument Text) ⇄ richdoc converter, pure Go (CGO-free)
go-rtf/rtf                         RTF <-> richdoc converter, pure Go (CGO-free)
go-richdoc/markdown                Markdown (CommonMark + GFM) <-> richdoc converter

3 of 2023 repositories, inventory of 2026-10-08
```

## Files and functions, not only domains

A repository's name and description name its *domain* ("descriptive
statistics"), never its functions, so `fleetfind quantile` used to print
`nothing in 2253 repositories` while `go-statistics/stats` has `quantile.go`
(go-fleettools/fleettools#306). The index adds the root and nested **file
names** and the **exported top-level identifiers** (functions, methods, types,
constants, variables; `go/parser`, no type checking, test files and `testdata`
excluded) of every repository that is a Go module:

```
$ fleetfind quantile
go-statistics/stats   Probability distributions, descriptive statistics and tests in pure Go …
go-statistics/stats   file:quantile.go
go-statistics/stats   ident:Quantile
go-statistics/stats   ident:QuantileAveragedInvertedCDF
```

- **No GitHub budget.** The source is the Go module proxy (`@latest`, then the
  module zip at github.com/org/repo), so `fleetfind -index` makes **0 GitHub
  calls**. `-refresh` still lists organisations (~377 `gh` calls, ~1000 REST
  requests) and then runs the index; `-no-index` skips it.
- **Incremental.** A repository is read again only when its `pushed_at` changed
  or it could not be read last time.
- **Bounded.** A zip over `-max-zip` MiB (default 32) is refused, an archive over
  20000 entries or declaring over 128 MiB is refused, bytes are counted as read
  rather than trusted from the header, and zip entry names are only ever read
  (never written to disk) and refused if they could escape (`..`, absolute, `\`,
  NUL, control characters). Fuzz tests cover the name and archive readers; bomb
  tests assert the refusal.
- **Honest about what it could not read.** Every run prints how many repositories
  were indexed, how many are not in the proxy (not a module, **private**, or
  unpublished: the proxy cannot see a private repository, so those are a blind
  spot, not a NO), how many are **UNREAD** with the reason, and how many were
  skipped as not Go. A zero result is never printed as `nothing in N
  repositories` when part of N could not be read.

## Install

```sh
go install github.com/go-fleettools/fleetfind@latest
```

## Why

The fleet keeps a capability map by hand, and it says of itself, in bold, that
it drifts — *"a stale index is worse than none: it gives the false assurance of
having looked."* It was right. In three days a mutation runner was rebuilt here
five hours after the fleet published one, and an icon pack was very nearly
rebuilt on top of one that already existed.

`go-fleettools` has eleven sweepers — red branches, open pull requests, modules
ahead of their tag, tests that never run — and none of them answers *"who does
X?"*, which is the question whose wrong answer costs the most.

So this keeps no opinions. It asks GitHub.

## What it will not let you believe

⛔ **Every answer carries the date the inventory was taken**, and one older than
a day says so **on stderr before it answers**:

```
fleetfind: this inventory was taken 192h0m0s ago (2026-09-30) — refresh before believing a NO
```

A "nothing matches" is only as good as when it was measured. That line is the
whole tool.

⛔ **Zero results are said out loud** — `nothing in 2023 repositories (taken
2026-10-08) matches "epub"` — because a sweep that prints nothing is
indistinguishable from a sweep that could not read.

⛔ **An empty fleet is refused, twice and separately.** No organisations at all
is *"I could not list them"*; organisations that listed and brought back no
repository is *"the query shape changed"*. Neither writes an empty inventory
over a good one. One organisation that refuses does not lose the other 334 —
the organisation count in the output is what shows somebody it happened.

⛔ **Archived repositories are found but never returned by default.** A retired
org still answering to the standard name is how a clone went to a frozen copy
once; `-all` asks for them, because knowing something *moved* is the useful
answer.

## Ranking

A name beats a description. `go-odf/ods` answers "spreadsheet" better than a
sentence that happens to use the word, so a path match scores 10 and a
description or topic match 3. **Every term must match**: two words narrow, they
do not widen.

## Then confirm

A hit is a lead, not an authority — the same rule the hand-kept map asks for:

```sh
gh api repos/<org>/<repo> --jq '{full_name, id, pushed_at, archived}'
```

## Licence

BSD-3-Clause.
