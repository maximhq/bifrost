package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/encrypt"
	"gorm.io/gorm/clause"
)

// tracePartIdentityConfigKey names the trace part identity key in the config store.
const tracePartIdentityConfigKey = "trace_part_identity_key"

// tracePartIdentityKeySize is the length of the trace part identity key in bytes.
const tracePartIdentityKeySize = 32

// loadTracePartIdentityKey returns the key trace ingest uses for part ids, creating it on first use.
// Every node shares the config store, so every node reads the same key. A new key is inserted only
// when none exists, so nodes that start together still agree on the one that won. The key is stored
// encrypted when an encryption key is configured.
func loadTracePartIdentityKey(ctx context.Context, store configstore.ConfigStore) ([]byte, error) {
	fresh := make([]byte, tracePartIdentityKeySize)
	if _, err := rand.Read(fresh); err != nil {
		return nil, fmt.Errorf("failed to generate the trace part identity key: %w", err)
	}
	value, err := encodeTracePartIdentityKey(fresh)
	if err != nil {
		return nil, err
	}
	row := &tables.TableGovernanceConfig{Key: tracePartIdentityConfigKey, Value: value}
	if err := store.DB().WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(row).Error; err != nil {
		return nil, fmt.Errorf("failed to store the trace part identity key: %w", err)
	}
	stored, err := store.GetConfig(ctx, tracePartIdentityConfigKey)
	if err != nil {
		return nil, fmt.Errorf("failed to read the trace part identity key: %w", err)
	}
	return decodeTracePartIdentityKey(stored.Value)
}

// encodeTracePartIdentityKey formats a key for the config store: "enc:" and its ciphertext when an
// encryption key is configured, "hex:" and its hex form otherwise.
func encodeTracePartIdentityKey(key []byte) (string, error) {
	text := hex.EncodeToString(key)
	if !encrypt.IsEnabled() {
		return "hex:" + text, nil
	}
	ciphertext, err := encrypt.Encrypt(text)
	if err != nil {
		return "", fmt.Errorf("failed to encrypt the trace part identity key: %w", err)
	}
	return "enc:" + ciphertext, nil
}

// decodeTracePartIdentityKey reads a key encodeTracePartIdentityKey formatted. An encrypted key needs
// the encryption key it was written with.
func decodeTracePartIdentityKey(value string) ([]byte, error) {
	text, found := strings.CutPrefix(value, "hex:")
	if !found {
		ciphertext, encrypted := strings.CutPrefix(value, "enc:")
		if !encrypted {
			return nil, fmt.Errorf("the stored trace part identity key has an unknown format")
		}
		plain, err := encrypt.Decrypt(ciphertext)
		if err != nil {
			return nil, fmt.Errorf("failed to decrypt the trace part identity key: %w", err)
		}
		text = plain
	}
	key, err := hex.DecodeString(text)
	if err != nil || len(key) != tracePartIdentityKeySize {
		return nil, fmt.Errorf("the stored trace part identity key is not a %d-byte key", tracePartIdentityKeySize)
	}
	return key, nil
}
