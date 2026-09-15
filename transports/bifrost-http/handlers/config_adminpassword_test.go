package handlers

import (
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/encrypt"
	"github.com/valyala/fasthttp"
)

// TestValidateAndHashAdminPassword covers the shared validate+hash helper both
// auth branches (enable and disable) now go through, including the #6413
// follow-up: a mask-shaped password submitted while disabling auth must be
// hashed so CompareHash accepts it once auth is re-enabled.
func TestValidateAndHashAdminPassword(t *testing.T) {
	t.Run("policy failure returns 400 and no hash", func(t *testing.T) {
		hashed, status, errMsg := validateAndHashAdminPassword(&schemas.SecretVar{Val: "short"})
		if hashed != nil {
			t.Fatalf("expected nil SecretVar on policy failure, got %+v", hashed)
		}
		if status != fasthttp.StatusBadRequest {
			t.Errorf("status = %d, want %d", status, fasthttp.StatusBadRequest)
		}
		if !strings.Contains(errMsg, "auth password must include") {
			t.Errorf("errMsg = %q, want policy failure message", errMsg)
		}
	})

	t.Run("valid password is bcrypt-hashed and comparable", func(t *testing.T) {
		const plain = "StrongPass1!x"
		hashed, status, errMsg := validateAndHashAdminPassword(&schemas.SecretVar{Val: plain})
		if errMsg != "" || status != 0 {
			t.Fatalf("unexpected error: status=%d msg=%q", status, errMsg)
		}
		if hashed.GetValue() == plain {
			t.Fatal("password was stored raw, not hashed")
		}
		ok, err := encrypt.CompareHash(hashed.GetValue(), plain)
		if err != nil || !ok {
			t.Errorf("CompareHash(hashed, plain) = %v, %v; want true, nil", ok, err)
		}
	})

	t.Run("mask-shaped password survives hash+compare round trip (#6413)", func(t *testing.T) {
		// 4 chars + 24 asterisks + 4 chars: shaped like the redaction mask but a
		// real credential. It passes the policy (length, upper, lower, digit,
		// special) and must round-trip through hashing like any other password.
		const maskShaped = "Aa1b************************cdef"
		sv := &schemas.SecretVar{Val: maskShaped}
		if sv.ShouldPreserveStoredSentinelOnly() {
			t.Fatal("sanity: mask-shaped value must be treated as a new password")
		}
		hashed, status, errMsg := validateAndHashAdminPassword(sv)
		if errMsg != "" || status != 0 {
			t.Fatalf("unexpected error: status=%d msg=%q", status, errMsg)
		}
		if hashed.GetValue() == maskShaped {
			t.Fatal("mask-shaped password was persisted raw; re-enabling auth would lock the admin out")
		}
		ok, err := encrypt.CompareHash(hashed.GetValue(), maskShaped)
		if err != nil || !ok {
			t.Errorf("CompareHash after re-enable = %v, %v; want true, nil", ok, err)
		}
	})
}
