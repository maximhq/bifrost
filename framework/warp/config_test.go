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
		// Stored unchecked, this reported the deployment as configured and then
		// failed at provider construction on the first question.
		"unknown provider": {Enabled: true, Provider: "not-a-provider", Model: "gpt-4o"},
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

// SaveConfig forwards its input straight into ValidateConfigInput, which
// normalizes in place. An in-process caller holding a nil *ConfigInput must get
// ErrInvalidConfig back rather than panicking inside the framework.
func TestWarpValidateConfigInputRejectsNilInput(t *testing.T) {
	require.ErrorIs(t, ValidateConfigInput(nil), ErrInvalidConfig)

	store := &recordingStore{}
	_, err := newTestService(store).SaveConfig(context.Background(), nil)
	require.ErrorIs(t, err, ErrInvalidConfig)
	require.Empty(t, store.upserted, "a rejected write must not reach the store")
}

// BaseURL overrides the provider's default endpoint and is handed to the Warp
// client's ProviderConfig verbatim, so a value that is not an absolute http(s)
// URL is otherwise only discovered on the first outbound call - long after the
// operator has left the settings page.
func TestWarpValidateConfigInputRejectsMalformedBaseURL(t *testing.T) {
	for name, baseURL := range map[string]string{
		"not a url":     "notaurl",
		"wrong scheme":  "ftp://models.example.com",
		"no host":       "http://",
		"scheme only":   "https://",
		"relative path": "/v1/chat/completions",
		// Userinfo is a credential, and this field is stored unencrypted and
		// returned unredacted - the whole point of api_key_id is that Warp keeps
		// no secret of its own, which a password in the URL would undo.
		"password in url": "https://user:hunter2@models.example.com",
		"user in url":     "https://token@models.example.com",
	} {
		err := ValidateConfigInput(&ConfigInput{
			Enabled: true, Provider: "openai", Model: "gpt-4o", BaseURL: baseURL,
		})
		require.ErrorIs(t, err, ErrInvalidConfig, name)
	}

	// Empty stays valid: it means "use the provider's own default endpoint",
	// which is the common case for a hosted provider.
	for name, baseURL := range map[string]string{
		"empty":          "",
		"https host":     "https://models.internal.example.com",
		"http with port": "http://localhost:11434",
		"with path":      "https://gateway.example.com/openai/v1",
	} {
		err := ValidateConfigInput(&ConfigInput{
			Enabled: true, Provider: "openai", Model: "gpt-4o", BaseURL: baseURL,
		})
		require.NoError(t, err, name)
	}
}

func TestWarpValidateConfigInputAcceptsRegisteredCustomProvider(t *testing.T) {
	// Custom providers reach the same registry when Bifrost prepares them at
	// startup, so validating against it must not be a standard-providers-only
	// check.
	const custom = schemas.ModelProvider("warp-test-custom")
	schemas.RegisterKnownProvider(custom)
	defer schemas.UnregisterKnownProvider(custom)
	// Asserted on the provider check alone rather than on NoError: later
	// branches add required embedding fields, and a bare input failing on those
	// would make this read as a passing provider test that never ran one.
	err := ValidateConfigInput(&ConfigInput{Enabled: true, Provider: custom, Model: "some-model"})
	if err != nil {
		require.NotContains(t, err.Error(), "unknown provider")
	}
}
