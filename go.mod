module github.com/go-fleettools/fleetfind

// ⛔ The PATCH, because `go 1.27` names none and govulncheck judges the
// standard library by the version the module claims. GO-2026-6809 —
// crypto/x509, where compromised intermediate certificates are no longer
// refused by Certificate.Verify — has its fix RELEASED in 1.27.3, and vulngate
// accepts no waiver for an advisory whose fix is released. The lane runs this
// very line, through `go-version-file: go.mod`.
//
// Within the same minor, so nothing that could build this before cannot now.
//
// Raised to 1.27.4 on 2026-11-15 for GO-2026-6859 — net/http, fix RELEASED in
// 1.27.4. THIRD raise in three weeks, found by `modfloor --floor 1.27.4`
// reading the directive this module PUBLISHED, from the module proxy: eight of
// the eleven published modules of this organisation read BELOW at that moment.
//
// ⛔ RENOVATE WILL NEVER OFFER THIS UPDATE — measured on 2026-11-15, not
// assumed. Its github-actions manager maintains the CI pin (`with: go-version`,
// dependency `go`, type uses-with: go-fleettools/docs#29 is exactly that), and
// its gomod manager SEES this directive — the dependency dashboard lists it
// under "Detected Dependencies" — yet offers nothing for it. Of the three Go
// pins, the only one a consumer honours is the unwatched one. The gate for that
// belongs on a clock, as modfloor beside the nightly vulnscan sweep, and not in
// one more paragraph here.
go 1.27.4
