// Package version carries the build identity stamped in by the release
// pipeline with -ldflags "-X", plus the small comparison helpers the
// self-update command needs.
package version

// Build identity. The release workflow overrides these with
//
//	-X github.com/albertoruiz/space-elevator/internal/version.Version=vX.Y.Z
//	-X github.com/albertoruiz/space-elevator/internal/version.Commit=<sha>
//	-X github.com/albertoruiz/space-elevator/internal/version.Date=<rfc3339>
//
// A plain `go build` leaves Version at "dev".
//
// Careful: Go silently ignores -X for a symbol that does not exist, so a
// package move without the matching ldflags change produces a "dev" binary
// with no error. The release workflow asserts `--version` against the tag.
var (
	Version = "dev"
	Commit  = ""
	Date    = ""
)

// String is the one-line identity the deploy scripts parse; keep the shape
// (see RUNBOOK-actualizar-space-elevator.md §5.1 and §6).
func String() string {
	return "space-elevator version " + Version
}

// IsRelease reports whether this binary carries a comparable release tag.
func IsRelease() bool {
	_, ok := Parse(Version)
	return ok
}
