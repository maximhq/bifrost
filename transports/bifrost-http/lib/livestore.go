package lib

import (
	"context"
	"fmt"
	"sync"

	"github.com/maximhq/bifrost/framework/livestore"
)

var (
	presetStoreMu sync.Mutex
	presetStore   *livestore.Store
)

// SetLiveStore hands LoadConfig the live store to load instead of a new one. A build that
// registers collections of its own creates the store with livestore.New, registers its own on it,
// and calls SetLiveStore before LoadConfig. Passing nil clears it. LoadConfig takes the preset
// once, so a later LoadConfig in the same process creates its own unless SetLiveStore is called
// again.
func SetLiveStore(store *livestore.Store) {
	presetStoreMu.Lock()
	presetStore = store
	presetStoreMu.Unlock()
}

// takePresetStore returns the store SetLiveStore handed over, clearing it, or nil when there is
// none.
func takePresetStore() *livestore.Store {
	presetStoreMu.Lock()
	defer presetStoreMu.Unlock()
	store := presetStore
	presetStore = nil
	return store
}

// loadLiveStore fills the live store once everything LoadConfig writes to the config store has
// been written: from the config store when there is one, otherwise from config.json alone.
func loadLiveStore(ctx context.Context, config *Config) error {
	live := takePresetStore()
	if live == nil {
		var err error
		if live, err = livestore.New(); err != nil {
			return fmt.Errorf("failed to create live store: %w", err)
		}
	}
	config.LiveStore = live
	if config.ConfigStore != nil {
		if err := live.LoadAll(ctx, config.ConfigStore.DB()); err != nil {
			return fmt.Errorf("failed to load live store: %w", err)
		}
		return nil
	}
	if err := live.LoadRecords(ctx, nil); err != nil {
		return fmt.Errorf("failed to load live store: %w", err)
	}
	return nil
}
