package handlers

import (
	"errors"

	"github.com/maximhq/bifrost/framework/livestore"
)

// errVirtualKeyNotFound is returned when a virtual key ID resolves to nothing.
var errVirtualKeyNotFound = errors.New("virtual key not found or inactive")

// virtualKeyValueByID resolves a virtual key's value from its row ID through the live store, which
// every write refreshes, so a key it does not hold does not exist. A key whose value could not be
// resolved is not found either. Whether the key may be used is not decided here: governance refuses
// an inactive or expired key from the grant it resolves.
func virtualKeyValueByID(live *livestore.Store, vkID string) (string, error) {
	vk, ok := live.Collections.VirtualKeys.Get(vkID)
	if !ok || vk.Value == "" {
		return "", errVirtualKeyNotFound
	}
	return vk.Value, nil
}
