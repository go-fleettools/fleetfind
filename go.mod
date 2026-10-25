module github.com/go-fleettools/fleetfind

// ⛔ The PATCH, because `go 1.27` names none and govulncheck judges the
// standard library by the version the module claims. GO-2026-6809 —
// crypto/x509, where compromised intermediate certificates are no longer
// refused by Certificate.Verify — has its fix RELEASED in 1.27.3, and vulngate
// accepts no waiver for an advisory whose fix is released. The lane runs this
// very line, through `go-version-file: go.mod`.
//
// Within the same minor, so nothing that could build this before cannot now.
go 1.27.3
