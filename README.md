# fleetfind

**Which repository already does this?**

```
fleetfind -refresh             # ~335 calls, one dated inventory
fleetfind odf rtf markdown
```

```
go-odf/odf                         ODT (OpenDocument Text) ⇄ richdoc converter, pure Go (CGO-free)
go-rtf/rtf                         RTF <-> richdoc converter, pure Go (CGO-free)
go-richdoc/markdown                Markdown (CommonMark + GFM) <-> richdoc converter

3 of 2023 repositories, inventory of 2026-10-08
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
