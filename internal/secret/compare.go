package secret

import (
	"crypto/subtle"
	"regexp"
)

// legacyHashRe matches the pre-encryption password hash format: 64 hex chars.
// Used only to keep existing installs able to log in and upgrade in place.
var legacyHashRe = regexp.MustCompile(`^[0-9a-f]{64}$`)

// subtleEqualString compares two strings without leaking length-independent
// timing information (the old code used !=, which short-circuits on the first
// differing byte).
func subtleEqualString(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// subtleEqualBytes is subtleEqualString for byte slices.
func subtleEqualBytes(a, b []byte) bool {
	return subtle.ConstantTimeCompare(a, b) == 1
}
