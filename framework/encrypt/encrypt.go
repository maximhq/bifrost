// Package encrypt provides reversible AES-256-GCM encryption and decryption utilities
// for securing sensitive data like API keys and credentials.
// We are not using it anywhere yet - we will introduce encryption for all the sensitive data in one go to avoid breaking changes
package encrypt

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"

	"github.com/maximhq/bifrost/core/schemas"
	"golang.org/x/crypto/argon2"
	"golang.org/x/crypto/bcrypt"
)

var (
	encryptionKey []byte
	logger        schemas.Logger
)

var ErrEncryptionKeyNotInitialized = errors.New("encryption key is not initialized")

// Init initializes the encryption key using Argon2id KDF to derive a secure 32-byte key
// from the provided passphrase. This ensures strong entropy regardless of passphrase length.
// The function accepts any passphrase but warns if it's too short (< 16 bytes).
func Init(key string, _logger schemas.Logger) {
	logger = _logger
	if key == "" {
		// TODO uncomment this warning when we have full coverage for encryption
		// In this case encryption will be disabled
		// logger.Warn("encryption key is not set, encryption will be disabled. To set encryption key: use the encryption_key field in the configuration file or set the BIFROST_ENCRYPTION_KEY environment variable. Note that - once encryption key is set, it cannot be changed later unless you clean up the database.")
		return
	}

	// Warn if passphrase is too short
	if len(key) < 16 {
		logger.Warn("encryption passphrase is shorter than 16 bytes, consider using a longer passphrase for better security")
	}

	// Derive a secure 32-byte key using Argon2id KDF
	// We use a fixed salt since this is a system-wide encryption key (not per-user passwords)
	// Argon2id parameters: time=1, memory=64MB, threads=4, keyLen=32
	// This provides strong security while maintaining reasonable performance for initialization
	salt := []byte("bifrost-encryption-v1-salt-2024")
	encryptionKey = argon2.IDKey([]byte(key), salt, 1, 64*1024, 4, 32)
}

// CompareHash compares a hash and a password
func CompareHash(hash string, password string) (bool, error) {
	err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(password))
	if err != nil {
		if errors.Is(err, bcrypt.ErrMismatchedHashAndPassword) {
			return false, nil
		}
		return false, fmt.Errorf("failed to compare hash: %w", err)
	}
	return true, nil
}

// Hash hashes a password using bcrypt
func Hash(password string) (string, error) {
	hashedPassword, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return "", fmt.Errorf("failed to hash password: %w", err)
	}
	return string(hashedPassword), nil
}

// Encrypt encrypts a plaintext string using AES-256-GCM and returns a base64-encoded ciphertext
func Encrypt(plaintext string) (string, error) {
	if encryptionKey == nil {
		return plaintext, nil
	}
	if plaintext == "" {
		return "", nil
	}

	block, err := aes.NewCipher(encryptionKey)
	if err != nil {
		return plaintext, fmt.Errorf("failed to create cipher: %w", err)
	}

	aesGCM, err := cipher.NewGCM(block)
	if err != nil {
		return plaintext, fmt.Errorf("failed to create GCM: %w", err)
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

// Decrypt decrypts a base64-encoded ciphertext using AES-256-GCM and returns the plaintext
func Decrypt(ciphertext string) (string, error) {
	if encryptionKey == nil {
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

	block, err := aes.NewCipher(encryptionKey)
	if err != nil {
		return "", fmt.Errorf("failed to create cipher: %w", err)
	}

	aesGCM, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("failed to create GCM: %w", err)
	}

	// Extract nonce
	nonceSize := aesGCM.NonceSize()
	if len(data) < nonceSize {
		return "", fmt.Errorf("ciphertext too short")
	}

	nonce, ciphertextBytes := data[:nonceSize], data[nonceSize:]

	// Decrypt the data
	plaintext, err := aesGCM.Open(nil, nonce, ciphertextBytes, nil)
	if err != nil {
		return "", fmt.Errorf("failed to decrypt: %w", err)
	}

	return string(plaintext), nil
}
