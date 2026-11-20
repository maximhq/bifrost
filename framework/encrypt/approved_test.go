package encrypt

import (
	"strings"
	"testing"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"golang.org/x/crypto/bcrypt"
)

// useKeySet installs a key set whose write key is chosen explicitly, so tests can play both
// the FIPS and standard sides without rebuilding with GODEBUG=fips140=on.
func useKeySet(t *testing.T, passphrase string, approvedWrite bool) {
	t.Helper()
	logger = bifrost.NewDefaultLogger(schemas.LogLevelError)
	set := &keySet{passphrase: passphrase}
	if approvedWrite {
		set.write, set.deriveOther = deriveApprovedKey(passphrase), deriveLegacyKey
	} else {
		set.write, set.deriveOther = deriveLegacyKey(passphrase), deriveApprovedKey
	}
	keys.Store(set)
	t.Cleanup(func() { keys.Store(nil) })
}

// TestDecryptAcrossKDFs checks that values written under either data key stay readable
// after the write key switches, in both directions.
func TestDecryptAcrossKDFs(t *testing.T) {
	const passphrase = "cross-kdf-passphrase-for-tests"
	for _, firstApproved := range []bool{false, true} {
		useKeySet(t, passphrase, firstApproved)
		ciphertext, err := Encrypt("provider-secret")
		if err != nil {
			t.Fatalf("Encrypt() error = %v", err)
		}
		useKeySet(t, passphrase, !firstApproved)
		got, err := Decrypt(ciphertext)
		if err != nil {
			t.Fatalf("Decrypt() after switching write key (firstApproved=%v) error = %v", firstApproved, err)
		}
		if got != "provider-secret" {
			t.Fatalf("Decrypt() = %q, want %q", got, "provider-secret")
		}
	}
}

// TestDecryptRejectsOtherPassphrase checks the fallback key does not accept a different passphrase.
func TestDecryptRejectsOtherPassphrase(t *testing.T) {
	useKeySet(t, "first-passphrase-for-tests", true)
	ciphertext, err := Encrypt("provider-secret")
	if err != nil {
		t.Fatalf("Encrypt() error = %v", err)
	}
	useKeySet(t, "second-passphrase-for-tests", false)
	if _, err := Decrypt(ciphertext); err == nil {
		t.Fatal("Decrypt() with another passphrase succeeded, want error")
	}
}

// TestApprovedKeyDiffersFromLegacy guards against the two derivations collapsing into one.
func TestApprovedKeyDiffersFromLegacy(t *testing.T) {
	if string(deriveApprovedKey("same-passphrase")) == string(deriveLegacyKey("same-passphrase")) {
		t.Fatal("approved and legacy data keys are equal")
	}
}

// TestPBKDF2PasswordHash covers the PBKDF2 hash format, round trip and rejection.
func TestPBKDF2PasswordHash(t *testing.T) {
	hash, err := hashPBKDF2("correct horse battery staple")
	if err != nil {
		t.Fatalf("hashPBKDF2() error = %v", err)
	}
	if !strings.HasPrefix(hash, pbkdf2HashPrefix+"i=600000$") {
		t.Fatalf("hash = %q, want prefix %q", hash, pbkdf2HashPrefix+"i=600000$")
	}
	other, err := hashPBKDF2("correct horse battery staple")
	if err != nil {
		t.Fatalf("hashPBKDF2() error = %v", err)
	}
	if hash == other {
		t.Fatal("two hashes of one password are equal, want random salts")
	}
	if ok, err := CompareHash(hash, "correct horse battery staple"); err != nil || !ok {
		t.Fatalf("CompareHash(correct) = %v, %v; want true, nil", ok, err)
	}
	if ok, err := CompareHash(hash, "wrong password"); err != nil || ok {
		t.Fatalf("CompareHash(wrong) = %v, %v; want false, nil", ok, err)
	}
}

// TestPBKDF2CacheOnlyRemembersSuccess checks a cached success never lets another password in.
func TestPBKDF2CacheOnlyRemembersSuccess(t *testing.T) {
	hash, err := hashPBKDF2("cached-password")
	if err != nil {
		t.Fatalf("hashPBKDF2() error = %v", err)
	}
	if ok, _ := CompareHash(hash, "cached-password"); !ok {
		t.Fatal("first CompareHash() = false, want true")
	}
	if !verifiedPasswords.contains(hash, "cached-password") {
		t.Fatal("successful check was not cached")
	}
	if ok, _ := CompareHash(hash, "cached-passwor"); ok {
		t.Fatal("CompareHash() accepted a different password after a cached success")
	}
	if verifiedPasswords.contains(hash, "cached-passwor") {
		t.Fatal("failed check was cached")
	}
}

// TestCompareHashStillVerifiesBcrypt keeps hashes written by earlier versions valid.
func TestCompareHashStillVerifiesBcrypt(t *testing.T) {
	legacy, err := bcrypt.GenerateFromPassword([]byte("old-admin-password"), bcrypt.MinCost)
	if err != nil {
		t.Fatalf("bcrypt error = %v", err)
	}
	if ok, err := CompareHash(string(legacy), "old-admin-password"); err != nil || !ok {
		t.Fatalf("CompareHash(bcrypt, correct) = %v, %v; want true, nil", ok, err)
	}
	if ok, err := CompareHash(string(legacy), "nope"); err != nil || ok {
		t.Fatalf("CompareHash(bcrypt, wrong) = %v, %v; want false, nil", ok, err)
	}
}

// TestCompareHashRejectsMalformedPBKDF2 checks bad stored hashes fail with an error, not a match.
func TestCompareHashRejectsMalformedPBKDF2(t *testing.T) {
	for _, hash := range []string{
		pbkdf2HashPrefix + "i=600000$onlysalt",
		pbkdf2HashPrefix + "x=600000$c2FsdA$ZGlnZXN0",
		pbkdf2HashPrefix + "i=0$c2FsdA$ZGlnZXN0",
		pbkdf2HashPrefix + "i=999999999$c2FsdA$ZGlnZXN0",
		pbkdf2HashPrefix + "i=1000$!!!$ZGlnZXN0",
		pbkdf2HashPrefix + "i=1000$c2FsdA$",
	} {
		if ok, err := CompareHash(hash, "anything"); err == nil || ok {
			t.Errorf("CompareHash(%q) = %v, %v; want false, error", hash, ok, err)
		}
	}
}
