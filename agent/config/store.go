package config

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
)

const configFileName = "agent-config.enc"

// Store handles local persistence of the agent configuration.
// The config is encrypted at rest using a machine-specific key derived from
// platform identifiers, providing basic protection against casual inspection.
type Store struct {
	dir string
}

// NewStore creates a config store at the given directory.
func NewStore(dir string) *Store {
	return &Store{dir: dir}
}

// Save encrypts and writes the configuration to disk.
func (s *Store) Save(cfg *AgentConfig) error {
	data, err := json.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}

	encrypted, err := encrypt(data, s.deriveKey())
	if err != nil {
		return fmt.Errorf("encrypt config: %w", err)
	}

	path := filepath.Join(s.dir, configFileName)
	if err := os.MkdirAll(s.dir, 0700); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}
	if err := os.WriteFile(path, encrypted, 0600); err != nil {
		return fmt.Errorf("write config: %w", err)
	}
	return nil
}

// Load reads and decrypts the configuration from disk.
// Returns nil, nil if no saved config exists.
func (s *Store) Load() (*AgentConfig, error) {
	path := filepath.Join(s.dir, configFileName)

	encrypted, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read config: %w", err)
	}

	data, err := decrypt(encrypted, s.deriveKey())
	if err != nil {
		return nil, fmt.Errorf("decrypt config: %w", err)
	}

	var cfg AgentConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("unmarshal config: %w", err)
	}
	return &cfg, nil
}

// Delete removes the saved configuration file.
func (s *Store) Delete() error {
	path := filepath.Join(s.dir, configFileName)
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// deriveKey generates a machine-specific encryption key.
// This is basic protection — the goal is to prevent casual reading of the
// config file, not to defend against a determined attacker with machine access.
func (s *Store) deriveKey() []byte {
	// Combine machine-specific identifiers
	hostname, _ := os.Hostname()
	homeDir, _ := os.UserHomeDir()

	material := fmt.Sprintf("bifrost-agent:%s:%s:%s:%s",
		hostname, homeDir, runtime.GOOS, runtime.GOARCH)

	key := sha256.Sum256([]byte(material))
	return key[:]
}

// encrypt uses AES-256-GCM to encrypt data.
func encrypt(plaintext, key []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}

	aesGCM, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}

	nonce := make([]byte, aesGCM.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}

	return aesGCM.Seal(nonce, nonce, plaintext, nil), nil
}

// decrypt uses AES-256-GCM to decrypt data.
func decrypt(ciphertext, key []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}

	aesGCM, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}

	nonceSize := aesGCM.NonceSize()
	if len(ciphertext) < nonceSize {
		return nil, fmt.Errorf("ciphertext too short")
	}

	nonce, ciphertext := ciphertext[:nonceSize], ciphertext[nonceSize:]
	return aesGCM.Open(nil, nonce, ciphertext, nil)
}
