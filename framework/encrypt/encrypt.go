// Package encrypt provides reversible AES-256-GCM encryption and decryption utilities
// for securing sensitive data like API keys and credentials.
//
// Key derivation and password hashing follow NIST guidance when the binary runs in
// FIPS 140-3 mode (crypto/fips140.Enabled): the data key comes from PBKDF2-HMAC-SHA-256
// (SP 800-132) and password hashes are PBKDF2-HMAC-SHA-256. Standard builds still write
// with the earlier Argon2id key and bcrypt hashes, so a rolling deploy never writes a
// value an older node cannot read. Both formats are always readable.
package encrypt

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/fips140"
	"crypto/hmac"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"golang.org/x/crypto/argon2"
	"golang.org/x/crypto/bcrypt"
)

const (
	// keyDerivationIterations is the PBKDF2 iteration count for the data key. It runs once per boot.
	keyDerivationIterations = 600_000
	// passwordHashIterations is the PBKDF2-HMAC-SHA-256 count for password hashes (OWASP 2023).
	passwordHashIterations = 600_000
	// maxPasswordHashIterations bounds the count parsed from a stored hash so a bad row cannot stall a request.
	maxPasswordHashIterations = 10_000_000
	// passwordSaltBytes is the random salt size for password hashes (SP 800-132 requires at least 16).
	passwordSaltBytes = 16
	// pbkdf2HashPrefix marks a PBKDF2-HMAC-SHA-256 password hash: $pbkdf2-sha256$i=<n>$<salt>$<hash>.
	pbkdf2HashPrefix = "$pbkdf2-sha256$"
	// verifiedPasswordTTL is how long a successful PBKDF2 check is remembered in memory.
	verifiedPasswordTTL = 5 * time.Minute
	// maxVerifiedPasswords bounds the success cache; it is cleared when full.
	maxVerifiedPasswords = 256
)

var (
	// legacyKeySalt is the fixed Argon2id salt of the original data key.
	legacyKeySalt = []byte("bifrost-encryption-v1-salt-2024")
	// approvedKeySalt is the fixed PBKDF2 salt of the data key. The key is system wide and every
	// node must derive the same one from the passphrase alone, so the salt cannot be random.
	approvedKeySalt = []byte("bifrost-encryption-v2-pbkdf2-sha256-salt")
)

// keySet holds the data keys derived from one passphrase.
type keySet struct {
	// write encrypts new values and is tried first on decrypt.
	write []byte
	// passphrase is kept so the other key is derived only if a value needs it.
	passphrase string
	// deriveOther derives the key that write is not.
	deriveOther func(string) []byte
	otherOnce   sync.Once
	other       []byte
}

// otherKey returns the non-write key, deriving it on first use.
func (k *keySet) otherKey() []byte {
	k.otherOnce.Do(func() { k.other = k.deriveOther(k.passphrase) })
	return k.other
}

var keys atomic.Pointer[keySet]
var logger schemas.Logger

var ErrEncryptionKeyNotInitialized = errors.New("encryption key is not initialized")

// ApprovedAlgorithmsOnly reports whether new keys and hashes must use NIST-approved
// algorithms. It is true when the binary runs in FIPS 140-3 mode.
func ApprovedAlgorithmsOnly() bool {
	return fips140.Enabled()
}

// deriveLegacyKey derives the original Argon2id data key (time=1, memory=64MB, threads=4).
func deriveLegacyKey(passphrase string) []byte {
	return argon2.IDKey([]byte(passphrase), legacyKeySalt, 1, 64*1024, 4, 32)
}

// deriveApprovedKey derives the PBKDF2-HMAC-SHA-256 data key (NIST SP 800-132).
// pbkdf2.Key fails only for an output length that is out of range, which 32 is not.
func deriveApprovedKey(passphrase string) []byte {
	key, err := pbkdf2.Key(sha256.New, passphrase, approvedKeySalt, keyDerivationIterations, 32)
	if err != nil {
		panic(fmt.Sprintf("encrypt: pbkdf2 key derivation failed: %v", err))
	}
	return key
}

// Init derives the 32-byte data key from the passphrase. In FIPS mode the write key is
// PBKDF2-HMAC-SHA-256; otherwise it is the earlier Argon2id key. The other key is derived
// only when a stored value fails to decrypt under the write key.
// The function accepts any passphrase but warns if it's too short (< 16 bytes).
func Init(key string, _logger schemas.Logger) {
	logger = _logger
	if key == "" {
		keys.Store(nil)
		logger.Warn("encryption key is not set, encryption will be disabled. To set encryption key: use the encryption_key field in the configuration file or set the BIFROST_ENCRYPTION_KEY environment variable. Note that - once encryption key is set, it cannot be changed later unless you clean up the database.")
		return
	}

	// Warn if passphrase is too short
	if len(key) < 16 {
		logger.Warn("encryption passphrase is shorter than 16 bytes, consider using a longer passphrase for better security")
	}

	set := &keySet{passphrase: key}
	if ApprovedAlgorithmsOnly() {
		set.write, set.deriveOther = deriveApprovedKey(key), deriveLegacyKey
	} else {
		set.write, set.deriveOther = deriveLegacyKey(key), deriveApprovedKey
	}
	keys.Store(set)
}

// IsPasswordHash reports whether s is a password hash this package writes or verifies
// (PBKDF2-HMAC-SHA-256 or bcrypt), as opposed to a plaintext password.
func IsPasswordHash(s string) bool {
	return strings.HasPrefix(s, pbkdf2HashPrefix) ||
		strings.HasPrefix(s, "$2a$") ||
		strings.HasPrefix(s, "$2b$") ||
		strings.HasPrefix(s, "$2y$")
}

// CompareHash compares a stored password hash (PBKDF2 or bcrypt) with a password.
func CompareHash(hash string, password string) (bool, error) {
	if strings.HasPrefix(hash, pbkdf2HashPrefix) {
		return comparePBKDF2(hash, password)
	}
	err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(password))
	if err != nil {
		if errors.Is(err, bcrypt.ErrMismatchedHashAndPassword) {
			return false, nil
		}
		return false, fmt.Errorf("failed to compare hash: %w", err)
	}
	return true, nil
}

// Hash hashes a password: PBKDF2-HMAC-SHA-256 in FIPS mode, bcrypt otherwise.
func Hash(password string) (string, error) {
	if ApprovedAlgorithmsOnly() {
		return hashPBKDF2(password)
	}
	hashedPassword, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return "", fmt.Errorf("failed to hash password: %w", err)
	}
	return string(hashedPassword), nil
}

// hashPBKDF2 hashes a password with PBKDF2-HMAC-SHA-256 and a random 16-byte salt.
func hashPBKDF2(password string) (string, error) {
	salt := make([]byte, passwordSaltBytes)
	if _, err := io.ReadFull(rand.Reader, salt); err != nil {
		return "", fmt.Errorf("failed to read salt: %w", err)
	}
	derived, err := pbkdf2.Key(sha256.New, password, salt, passwordHashIterations, sha256.Size)
	if err != nil {
		return "", fmt.Errorf("failed to hash password: %w", err)
	}
	return fmt.Sprintf("%si=%d$%s$%s", pbkdf2HashPrefix, passwordHashIterations,
		base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(derived)), nil
}

// comparePBKDF2 checks a password against a $pbkdf2-sha256$ hash in constant time.
func comparePBKDF2(hash, password string) (bool, error) {
	if verifiedPasswords.contains(hash, password) {
		return true, nil
	}
	parts := strings.Split(strings.TrimPrefix(hash, pbkdf2HashPrefix), "$")
	if len(parts) != 3 || !strings.HasPrefix(parts[0], "i=") {
		return false, errors.New("failed to compare hash: malformed pbkdf2 hash")
	}
	iterations, err := strconv.Atoi(strings.TrimPrefix(parts[0], "i="))
	if err != nil || iterations < 1 || iterations > maxPasswordHashIterations {
		return false, errors.New("failed to compare hash: invalid pbkdf2 iteration count")
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[1])
	if err != nil {
		return false, fmt.Errorf("failed to compare hash: %w", err)
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[2])
	if err != nil || len(want) == 0 {
		return false, errors.New("failed to compare hash: invalid pbkdf2 digest")
	}
	got, err := pbkdf2.Key(sha256.New, password, salt, iterations, len(want))
	if err != nil {
		return false, fmt.Errorf("failed to compare hash: %w", err)
	}
	if subtle.ConstantTimeCompare(got, want) != 1 {
		return false, nil
	}
	verifiedPasswords.add(hash, password)
	return true, nil
}

// passwordCache remembers recent successful PBKDF2 checks so per-request Basic auth does not
// pay 600,000 iterations every time. Entries are HMACs under a per-process random key, so the
// cache holds no password or fast password hash.
type passwordCache struct {
	mu      sync.Mutex
	secret  []byte
	entries map[string]time.Time
}

var verifiedPasswords = newPasswordCache()

// newPasswordCache returns an empty cache with a fresh random HMAC key.
func newPasswordCache() *passwordCache {
	secret := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, secret); err != nil {
		panic(fmt.Sprintf("encrypt: failed to seed password cache: %v", err))
	}
	return &passwordCache{secret: secret, entries: make(map[string]time.Time)}
}

// entryKey binds the stored hash and the password into one cache key.
func (c *passwordCache) entryKey(hash, password string) string {
	mac := hmac.New(sha256.New, c.secret)
	mac.Write([]byte(hash))
	mac.Write([]byte{0})
	mac.Write([]byte(password))
	return string(mac.Sum(nil))
}

// contains reports whether this hash and password were verified within the TTL.
func (c *passwordCache) contains(hash, password string) bool {
	key := c.entryKey(hash, password)
	c.mu.Lock()
	defer c.mu.Unlock()
	expires, ok := c.entries[key]
	if !ok {
		return false
	}
	if time.Now().After(expires) {
		delete(c.entries, key)
		return false
	}
	return true
}

// add records a successful check, clearing the cache first when it is full.
func (c *passwordCache) add(hash, password string) {
	key := c.entryKey(hash, password)
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.entries) >= maxVerifiedPasswords {
		clear(c.entries)
	}
	c.entries[key] = time.Now().Add(verifiedPasswordTTL)
}

// Encrypt encrypts a plaintext string using AES-256-GCM and returns a base64-encoded ciphertext
func Encrypt(plaintext string) (string, error) {
	set := keys.Load()
	if set == nil {
		return plaintext, nil
	}
	if plaintext == "" {
		return "", nil
	}

	aesGCM, err := newGCM(set.write)
	if err != nil {
		return plaintext, err
	}

	// Create a nonce (number used once)
	nonce := make([]byte, aesGCM.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return plaintext, fmt.Errorf("failed to read nonce: %w", err)
	}

	// Encrypt the data
	ciphertext := aesGCM.Seal(nonce, nonce, []byte(plaintext), nil)

	// Encode to base64 for storage
	return base64.StdEncoding.EncodeToString(ciphertext), nil
}

// newGCM returns an AES-256-GCM AEAD for key.
func newGCM(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("failed to create cipher: %w", err)
	}
	aesGCM, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("failed to create GCM: %w", err)
	}
	return aesGCM, nil
}

// IsEnabled returns true if the encryption key has been initialized
func IsEnabled() bool {
	return keys.Load() != nil
}

// Key returns a copy of the derived 32-byte write key, or nil if the
// encryption key has not been initialized. The returned slice is a copy so
// callers may not mutate the underlying key. Used by subsystems that need to
// derive their own domain-separated subkeys (e.g. WebSocket ticket signing).
func Key() []byte {
	set := keys.Load()
	if set == nil {
		return nil
	}
	out := make([]byte, len(set.write))
	copy(out, set.write)
	return out
}

// HashSHA256 returns a deterministic hex-encoded SHA-256 hash of the input.
// Used for hash-based lookups on encrypted columns (e.g., virtual key value, session token).
func HashSHA256(value string) string {
	h := sha256.Sum256([]byte(value))
	return hex.EncodeToString(h[:])
}

// Decrypt decrypts a base64-encoded AES-256-GCM ciphertext. It tries the write key first
// and then the other data key, so values written under either KDF stay readable.
func Decrypt(ciphertext string) (string, error) {
	set := keys.Load()
	if set == nil {
		return ciphertext, ErrEncryptionKeyNotInitialized
	}
	if ciphertext == "" {
		return ciphertext, nil
	}

	// Decode from base64
	data, err := base64.StdEncoding.DecodeString(ciphertext)
	if err != nil {
		return "", fmt.Errorf("failed to decode base64: %w", err)
	}

	plaintext, err := open(set.write, data)
	if err == nil {
		return string(plaintext), nil
	}
	if other, otherErr := open(set.otherKey(), data); otherErr == nil {
		return string(other), nil
	}
	return "", err
}

// open authenticates and decrypts nonce||ciphertext under key.
func open(key, data []byte) ([]byte, error) {
	aesGCM, err := newGCM(key)
	if err != nil {
		return nil, err
	}

	// Extract nonce
	nonceSize := aesGCM.NonceSize()
	if len(data) < nonceSize {
		return nil, fmt.Errorf("ciphertext too short")
	}

	nonce, ciphertextBytes := data[:nonceSize], data[nonceSize:]

	// Decrypt the data
	plaintext, err := aesGCM.Open(nil, nonce, ciphertextBytes, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to decrypt: %w", err)
	}
	return plaintext, nil
}
