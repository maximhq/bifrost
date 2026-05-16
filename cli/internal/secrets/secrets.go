package secrets

import (
	"errors"
	"fmt"
	"strings"

	"github.com/zalando/go-keyring"
)

const service = "bifrost-cli"

func keyForProfile(profileID string) string {
	return "profile:" + profileID + ":virtual-key"
}

// SetVirtualKey stores a virtual key in the system keyring for the given profile.
// If value is empty, the existing key is deleted.
func SetVirtualKey(profileID, value string) error {
	if strings.TrimSpace(value) == "" {
		return DeleteVirtualKey(profileID)
	}
	if err := keyring.Set(service, keyForProfile(profileID), value); err != nil {
		return fmt.Errorf("store virtual key: %w", err)
	}
	return nil
}

// GetVirtualKey retrieves the virtual key for the given profile from the system keyring.
// Returns an empty string if no key is stored.
func GetVirtualKey(profileID string) (string, error) {
	v, err := keyring.Get(service, keyForProfile(profileID))
	if err != nil {
		if errors.Is(err, keyring.ErrNotFound) {
			return "", nil
		}
		return "", fmt.Errorf("read virtual key: %w", err)
	}
	return strings.TrimSpace(v), nil
}

// DeleteVirtualKey removes the virtual key for the given profile from the system keyring.
func DeleteVirtualKey(profileID string) error {
	err := keyring.Delete(service, keyForProfile(profileID))
	if err != nil && !errors.Is(err, keyring.ErrNotFound) {
		return fmt.Errorf("delete virtual key: %w", err)
	}
	return nil
}
