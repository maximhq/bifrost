package handlers

import (
	"context"
	"errors"

	"github.com/maximhq/bifrost/framework/configstore/tables"
)

type virtualKeyByIDStore interface {
	GetVirtualKey(ctx context.Context, id string) (*tables.TableVirtualKey, error)
}

// errVirtualKeyNotFound is returned when a virtual key ID resolves to nothing
// in either the cache or the store.
var errVirtualKeyNotFound = errors.New("virtual key not found or inactive")

// virtualKeyByID resolves a virtual key by its row ID, preferring the governance
// in-memory cache and falling back to the store on a miss (e.g. a key created
// since the cache last refreshed) or when no cache is wired. The active-state
// check is left to the caller, matching both sources (neither filters inactive
// keys by ID).
func virtualKeyByID(ctx context.Context, cache VirtualKeyCache, store virtualKeyByIDStore, vkID string) (*tables.TableVirtualKey, error) {
	if cache != nil {
		if vk, ok := cache.GetVirtualKeyByID(ctx, vkID); ok && vk != nil {
			return vk, nil
		}
	}
	if store == nil {
		return nil, errVirtualKeyNotFound
	}
	vk, err := store.GetVirtualKey(ctx, vkID)
	if err != nil || vk == nil {
		return nil, errVirtualKeyNotFound
	}
	return vk, nil
}
