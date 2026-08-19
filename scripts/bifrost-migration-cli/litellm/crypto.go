package litellm

import (
	"crypto/sha256"
	"encoding/base64"
	"fmt"

	"golang.org/x/crypto/nacl/secretbox"
)

// secretBoxNonceLen is the 24-byte NaCl SecretBox nonce that LiteLLM prepends to
// the ciphertext (PyNaCl's box.encrypt returns nonce||ciphertext).
const secretBoxNonceLen = 24

// DecryptValue reverses LiteLLM's encrypt_value_helper. The stored string is
// base64 (url-safe, with a standard-base64 fallback for the old format) over a
// NaCl SecretBox sealed message whose 32-byte key is SHA-256(saltKey); the first
// 24 bytes are the nonce. saltKey is LITELLM_SALT_KEY, falling back to the proxy
// master_key. An empty input decrypts to an empty string.
func DecryptValue(encoded, saltKey string) (string, error) {
	if encoded == "" {
		return "", nil
	}
	raw, err := base64.URLEncoding.DecodeString(encoded)
	if err != nil {
		raw, err = base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			return "", fmt.Errorf("base64 decode: %w", err)
		}
	}
	if len(raw) < secretBoxNonceLen+secretbox.Overhead {
		return "", fmt.Errorf("ciphertext too short: %d bytes", len(raw))
	}

	key := sha256.Sum256([]byte(saltKey))
	var nonce [secretBoxNonceLen]byte
	copy(nonce[:], raw[:secretBoxNonceLen])

	out, ok := secretbox.Open(nil, raw[secretBoxNonceLen:], &nonce, &key)
	if !ok {
		return "", fmt.Errorf("decrypt failed (wrong salt key?)")
	}
	return string(out), nil
}
