package lib

import (
	"context"
	"errors"
	"fmt"

	"github.com/maximhq/bifrost/framework/configstore"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/livestore"
	"github.com/maximhq/bifrost/framework/vectorstore"
)

// ErrVectorStoreNotConfigured is a vector store name config.json does not configure.
var ErrVectorStoreNotConfigured = errors.New("vector store is not configured")

// SharedVectorStoreError is a settings write to a store that shares another's connection, whose
// settings are that other store's.
type SharedVectorStoreError struct {
	Name, Owner string
}

func (e *SharedVectorStoreError) Error() string {
	return fmt.Sprintf("vector store %q shares its connection with %q; edit %q instead", e.Name, e.Owner, e.Owner)
}

// InvalidVectorStoreSettingsError is a retention_days or max_size_mb out of range.
type InvalidVectorStoreSettingsError struct{ Err error }

func (e *InvalidVectorStoreSettingsError) Error() string { return e.Err.Error() }
func (e *InvalidVectorStoreSettingsError) Unwrap() error { return e.Err }

// VectorStoreView is one configured vector store as the dashboard shows it: what config.json
// configures, what became of it at boot, and its settings. Endpoint is where it keeps its data,
// never a credential.
type VectorStoreView struct {
	Name          string `json:"name"`
	Type          string `json:"type"`
	Primary       bool   `json:"primary"`
	Status        string `json:"status"`
	Error         string `json:"error"`
	SharedWith    string `json:"shared_with"`
	Endpoint      string `json:"endpoint"`
	RetentionDays int    `json:"retention_days"`
	MaxSizeMB     int    `json:"max_size_mb"`
}

// VectorStoreViews returns every configured vector store, the primary first.
func (c *Config) VectorStoreViews() []VectorStoreView {
	stores := c.VectorStores.All()
	views := make([]VectorStoreView, 0, len(stores))
	for _, entry := range stores {
		views = append(views, c.vectorStoreView(entry))
	}
	return views
}

// GetVectorStoreView returns one configured vector store, or ErrVectorStoreNotConfigured.
func (c *Config) GetVectorStoreView(name string) (VectorStoreView, error) {
	entry, ok := c.VectorStores.Get(name)
	if !ok {
		return VectorStoreView{}, fmt.Errorf("%q: %w", name, ErrVectorStoreNotConfigured)
	}
	return c.vectorStoreView(entry), nil
}

func (c *Config) vectorStoreView(entry *vectorstore.NamedStore) VectorStoreView {
	view := VectorStoreView{
		Name: entry.Name, Type: string(entry.Type), Primary: entry.Primary, Status: string(entry.Status),
		SharedWith: entry.SharedWith, Endpoint: entry.Location,
	}
	if entry.Err != nil {
		view.Error = entry.Err.Error()
	}
	// A store that shares another's connection is that store, under its settings: shown as such.
	settingsOf := entry.Name
	if entry.SharedWith != "" {
		settingsOf = entry.SharedWith
	}
	if c.LiveStore != nil {
		if settings, ok := c.LiveStore.Collections.VectorStoreSettings.Get(settingsOf); ok {
			view.RetentionDays, view.MaxSizeMB = settings.RetentionDays, settings.MaxSizeMB
		}
	}
	return view
}

// SaveVectorStoreSettings writes a configured store's retention_days and max_size_mb to the
// config store: only the values that are not nil are written, so an overlapping save of the
// other setting is not undone, and the hash of what config.json gave stays, so the edit lasts
// until config.json changes the value. It returns the row before the write (nil when there was
// none) and what the write made of it, both read in the write's transaction, so an overlapping
// save of the other setting shows in them. The live store is not updated:
// ReloadVectorStoreSettings does that, after the caller has recorded the change.
func (c *Config) SaveVectorStoreSettings(ctx context.Context, name string, retentionDays, maxSizeMB *int) (before, after *configstoreTables.TableVectorStoreSettings, err error) {
	if c.ConfigStore == nil {
		return nil, nil, errors.New("vector store settings need a config store")
	}
	entry, ok := c.VectorStores.Get(name)
	if !ok {
		return nil, nil, fmt.Errorf("%q: %w", name, ErrVectorStoreNotConfigured)
	}
	if entry.SharedWith != "" {
		return nil, nil, &SharedVectorStoreError{Name: name, Owner: entry.SharedWith}
	}
	// Each value sent is checked on its own; one not sent is not written, whatever it holds now.
	checkRetention, checkSize := 0, 0
	if retentionDays != nil {
		checkRetention = *retentionDays
	}
	if maxSizeMB != nil {
		checkSize = *maxSizeMB
	}
	if err := configstore.ValidateVectorStoreSettings(checkRetention, checkSize); err != nil {
		return nil, nil, &InvalidVectorStoreSettingsError{Err: err}
	}
	before, after, err = c.ConfigStore.UpdateVectorStoreSettings(ctx, name, retentionDays, maxSizeMB)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to save the settings of vector store %q: %w", name, err)
	}
	return before, after, nil
}

// ReloadVectorStoreSettings re-reads a store's settings into the live store, retrying a failed
// read within the request's budget. A read that still fails leaves the previous settings in memory.
func (c *Config) ReloadVectorStoreSettings(ctx context.Context, name string) error {
	return c.refreshRef(ctx, "vector store settings", livestore.Ref{Kind: livestore.KindVectorStoreSettings, ID: name})
}
