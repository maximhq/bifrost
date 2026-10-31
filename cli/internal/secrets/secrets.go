package secrets

import (
	"errors"
	"fmt"
	"strings"

	"github.com/zalando/go-keyring"
)

const service = "bifrost-cli"

var (
	keyringGet    = keyring.Get
	keyringSet    = keyring.Set
	keyringDelete = keyring.Delete
)

// AgentAccessTokenPrefix identifies refreshable Bifrost Enterprise agent
// access tokens without exposing or decoding their opaque contents.
const AgentAccessTokenPrefix = "ck-bf-agent-"

// Kind identifies a secret independently from the gateway profile metadata.
type Kind string

// Keyring adapts the operating-system credential store to the small store
// interfaces used by command and session packages.
type Keyring struct{}

func (Keyring) Get(profileID string, kind Kind) (string, error) {
	return Get(profileID, kind)
}

func (Keyring) Set(profileID string, kind Kind, value string) error {
	return Set(profileID, kind, value)
}

func (Keyring) Delete(profileID string, kind Kind) error {
	return Delete(profileID, kind)
}

const (
	// VirtualKey authorizes inference and virtual-key self-service operations.
	VirtualKey Kind = "virtual-key"
	// ManagementKey authorizes Enterprise management API operations.
	ManagementKey Kind = "management-key"
	// SessionToken authorizes management operations for a logged-in human.
	SessionToken Kind = "session-token"
	// AgentToken authorizes user-scoped Enterprise inference after browser SSO.
	AgentToken Kind = "agent-token"
	// AgentRefreshToken rotates an Enterprise browser SSO agent session.
	AgentRefreshToken Kind = "agent-refresh-token"
	// AgentUser stores the non-secret identity summary returned during browser SSO.
	AgentUser Kind = "agent-user"
	// AgentVirtualKeyID stores the non-secret assigned-key selection for SSO inference.
	AgentVirtualKeyID Kind = "agent-virtual-key-id"
)

// keyForProfile returns the keyring account name for a profile secret.
func keyForProfile(profileID string, kind Kind) string {
	return "profile:" + profileID + ":" + string(kind)
}

// Set stores a profile secret or removes it when value is empty.
func Set(profileID string, kind Kind, value string) error {
	if strings.TrimSpace(value) == "" {
		return Delete(profileID, kind)
	}
	if err := keyringSet(service, keyForProfile(profileID, kind), strings.TrimSpace(value)); err != nil {
		return fmt.Errorf("store %s: %w", kind, err)
	}
	return nil
}

// Get retrieves a profile secret and returns an empty value when none exists.
func Get(profileID string, kind Kind) (string, error) {
	value, err := keyringGet(service, keyForProfile(profileID, kind))
	if err != nil {
		if errors.Is(err, keyring.ErrNotFound) {
			return "", nil
		}
		return "", fmt.Errorf("read %s: %w", kind, err)
	}
	return strings.TrimSpace(value), nil
}

// Delete removes a profile secret if one exists.
func Delete(profileID string, kind Kind) error {
	err := keyringDelete(service, keyForProfile(profileID, kind))
	if err != nil && !errors.Is(err, keyring.ErrNotFound) {
		return fmt.Errorf("delete %s: %w", kind, err)
	}
	return nil
}

// SetVirtualKey stores a virtual key in the system keyring for the given profile.
// If value is empty, the existing key is deleted.
func SetVirtualKey(profileID, value string) error {
	return Set(profileID, VirtualKey, value)
}

// GetVirtualKey retrieves the virtual key for the given profile from the system keyring.
// Returns an empty string if no key is stored.
func GetVirtualKey(profileID string) (string, error) {
	return Get(profileID, VirtualKey)
}

// DeleteVirtualKey removes the virtual key for the given profile from the system keyring.
func DeleteVirtualKey(profileID string) error {
	return Delete(profileID, VirtualKey)
}
