package warp

import (
	"context"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/stretchr/testify/require"
)

type recordingStore struct {
	row      *tables.TableWarpConfig
	upserted []tables.TableWarpConfig
}

func (s *recordingStore) GetWarpConfig(context.Context) (*tables.TableWarpConfig, error) {
	return s.row, nil
}

func (s *recordingStore) UpsertWarpConfig(_ context.Context, config *tables.TableWarpConfig) error {
	s.upserted = append(s.upserted, *config)
	s.row = config
	return nil
}

func newTestService(store *recordingStore) *Service {
	return NewService(nil, WithConfigStore(store))
}

// A key reference round-trips like any other field: there is no secret here, so
// no redaction step and no presence flag.
func TestWarpConfigViewReturnsKeyReference(t *testing.T) {
	service := newTestService(&recordingStore{row: &tables.TableWarpConfig{
		ID: tables.WarpConfigRowID, Enabled: true, Provider: "openai", Model: "gpt-4o", APIKeyID: "key-abc",
	}})
	view, err := service.ConfigView(context.Background())
	require.NoError(t, err)
	require.Equal(t, "key-abc", view.APIKeyID)
	require.True(t, view.Configured)
	require.Equal(t, "gpt-4o", view.Model)
}

// An unconfigured deployment must render its empty settings form, so this is a
// defaults view rather than an error.
func TestWarpConfigViewUnconfiguredReturnsDefaults(t *testing.T) {
	service := newTestService(&recordingStore{})
	view, err := service.ConfigView(context.Background())
	require.NoError(t, err)
	require.False(t, view.Configured)
	require.Empty(t, view.APIKeyID)
	require.Equal(t, schemas.WarpDefaultMaxIterations, view.MaxIterations)
}

func TestWarpConfigViewWithoutStoreIsUnavailable(t *testing.T) {
	_, err := NewService(nil).ConfigView(context.Background())
	require.ErrorIs(t, err, ErrUnavailable)
}

// The reference is a plain field, so clearing it is just sending an empty
// value - none of the omitted-versus-empty ambiguity a write-only secret forces.
func TestWarpSaveConfigRoundTripsKeyReference(t *testing.T) {
	store := &recordingStore{row: &tables.TableWarpConfig{
		ID: tables.WarpConfigRowID, Enabled: true, Provider: "openai", Model: "gpt-4o", APIKeyID: "key-abc",
	}}
	view, err := newTestService(store).SaveConfig(context.Background(), &ConfigInput{
		Enabled: true, Provider: "openai", Model: "gpt-4o-mini", APIKeyID: "key-xyz",
	})
	require.NoError(t, err)
	require.Len(t, store.upserted, 1)
	require.Equal(t, "key-xyz", store.upserted[0].APIKeyID)
	require.Equal(t, "gpt-4o-mini", store.upserted[0].Model)
	require.Equal(t, "key-xyz", view.APIKeyID)
}

// A provider on a trusted network, or one using ambient credentials, needs no
// key at all - so an empty reference must be accepted, not rejected.
func TestWarpSaveConfigAcceptsEmptyKeyReference(t *testing.T) {
	store := &recordingStore{}
	_, err := newTestService(store).SaveConfig(context.Background(), &ConfigInput{
		Enabled: true, Provider: "openai", Model: "gpt-4o",
	})
	require.NoError(t, err)
	require.Len(t, store.upserted, 1)
	require.Empty(t, store.upserted[0].APIKeyID)
}

// A half-filled draft with the toggle off is legitimate: an operator must be
// able to fill the form in over more than one sitting.
func TestWarpSaveConfigAllowsIncompleteDraftWhenDisabled(t *testing.T) {
	store := &recordingStore{}
	_, err := newTestService(store).SaveConfig(context.Background(), &ConfigInput{Enabled: false})
	require.NoError(t, err)
	require.Len(t, store.upserted, 1)
}

func TestWarpValidateConfigInputRejectsIncompleteWhenEnabled(t *testing.T) {
	for name, input := range map[string]*ConfigInput{
		"no provider": {Enabled: true, Model: "gpt-4o"},
		"no model":    {Enabled: true, Provider: "openai"},
	} {
		store := &recordingStore{}
		_, err := newTestService(store).SaveConfig(context.Background(), input)
		require.ErrorIs(t, err, ErrInvalidConfig, name)
		require.Empty(t, store.upserted, name)
	}
}

func TestWarpValidateConfigInputRejectsIterationsAboveCeiling(t *testing.T) {
	err := ValidateConfigInput(&ConfigInput{Enabled: true, Provider: "openai", Model: "gpt-4o", MaxIterations: 50})
	require.ErrorIs(t, err, ErrInvalidConfig)
}

// Config is what the chat path calls. It must refuse a disabled or incomplete
// config rather than handing back something half-usable.
func TestWarpConfigRejectsUnusableConfigs(t *testing.T) {
	for name, row := range map[string]*tables.TableWarpConfig{
		"missing":  nil,
		"disabled": {Enabled: false, Provider: "openai", Model: "gpt-4o"},
		"no model": {Enabled: true, Provider: "openai"},
	} {
		_, err := newTestService(&recordingStore{row: row}).Config(context.Background())
		require.ErrorIs(t, err, ErrUnavailable, name)
	}
}
