package version

import (
	"strconv"
	"strings"
)

// semver is a minimal parser for the plain vMAJOR.MINOR.PATCH tags the
// release workflow produces. Pre-release and build metadata are deliberately
// unsupported: Parse reports ok=false for anything else (for example "dev"),
// so callers never act on a comparison they cannot trust.
type semver struct{ major, minor, patch int }

// Parse accepts "vX.Y.Z" or "X.Y.Z" with decimal parts.
func Parse(v string) (semver, bool) {
	v = strings.TrimSpace(v)
	v = strings.TrimPrefix(v, "v")
	parts := strings.Split(v, ".")
	if len(parts) != 3 {
		return semver{}, false
	}
	var out [3]int
	for i, p := range parts {
		if p == "" || (len(p) > 1 && p[0] == '0') {
			return semver{}, false
		}
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return semver{}, false
		}
		out[i] = n
	}
	return semver{out[0], out[1], out[2]}, true
}

// Compare orders two release versions. It returns -1, 0 or 1, and ok=false
// when either side is not a plain vX.Y.Z (so callers can refuse rather than
// guess).
func Compare(a, b string) (int, bool) {
	av, aok := Parse(a)
	bv, bok := Parse(b)
	if !aok || !bok {
		return 0, false
	}
	for _, d := range []int{av.major - bv.major, av.minor - bv.minor, av.patch - bv.patch} {
		switch {
		case d < 0:
			return -1, true
		case d > 0:
			return 1, true
		}
	}
	return 0, true
}
