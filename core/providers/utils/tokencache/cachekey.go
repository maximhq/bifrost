package tokencache

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"strings"
)

// CacheKey derives the cache key for a credential from its identifying fields, so no secret is
// used as a map key in a form that could be logged. version namespaces the key so a change
// in which fields a consumer hashes can never alias an entry minted under the old scheme.
//
// Fields are length-framed rather than joined with a separator. With a plain "|",
// {"a|b", "c"} and {"a", "b|c"} collide, and a collision here means one tenant's token
// served on another tenant's request.
//
// This runs once per request on a minted-token path, so it frames the fields into a stack
// buffer and hashes with sha256.Sum256 rather than a heap-allocated hasher: the hex string is
// the only allocation unless the fields exceed 512 bytes (a PEM private key does, and then
// the buffer grows once).
func CacheKey(version string, fields ...string) string {
	var stack [512]byte
	buf := stack[:0]
	buf = binary.BigEndian.AppendUint64(buf, uint64(len(version)))
	buf = append(buf, version...)
	for _, f := range fields {
		buf = binary.BigEndian.AppendUint64(buf, uint64(len(f)))
		buf = append(buf, f...)
	}
	sum := sha256.Sum256(buf)
	return hex.EncodeToString(sum[:])
}

// NormalizePEM repairs the most common deployment mistake: a PEM pasted into an environment
// variable or a JSON config where the newlines survived as the two characters backslash and
// n. pem.Decode returns nil for that input, and the operator gets a parse failure with
// nothing to act on.
func NormalizePEM(raw string) string {
	s := strings.TrimSpace(raw)
	if !strings.Contains(s, "\n") && strings.Contains(s, `\n`) {
		s = strings.ReplaceAll(s, `\n`, "\n")
	}
	return strings.ReplaceAll(s, "\r\n", "\n")
}
