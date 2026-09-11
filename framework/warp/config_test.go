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
	return NewService(nil, WithConfigStore(store), WithVectorStore(newFakeWarpVectorStore()))
}

func validWarpConfigRow() *tables.TableWarpConfig {
	return &tables.TableWarpConfig{
		ID: tables.WarpConfigRowID, Enabled: true, Provider: "openai", Model: "gpt-4o",
		EmbeddingProvider: "openai", EmbeddingModel: "text-embedding-3-small",
		EmbeddingDimension: 1536, LogVectorStoreNamespace: schemas.WarpDefaultLogVectorStoreNamespace,
	}
}

func validWarpConfigInput() *ConfigInput {
	return &ConfigInput{
		Enabled: true, Provider: "openai", Model: "gpt-4o",
		EmbeddingProvider: "openai", EmbeddingModel: "text-embedding-3-small",
		EmbeddingDimension: 1536, LogVectorStoreNamespace: schemas.WarpDefaultLogVectorStoreNamespace,
	}
}

// A key reference round-trips like any other field: there is no secret here, so
// no redaction step and no presence flag.
func TestWarpConfigViewReturnsKeyReference(t *testing.T) {
	row := validWarpConfigRow()
	row.APIKeyID = "key-abc"
	service := newTestService(&recordingStore{row: row})
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
	require.True(t, view.VectorStoreConnected)
}

func TestWarpSaveConfigRequiresConnectedVectorStore(t *testing.T) {
	service := NewService(nil, WithConfigStore(&recordingStore{}))
	_, err := service.SaveConfig(context.Background(), validWarpConfigInput())
	require.ErrorIs(t, err, ErrNoVectorStore)
}

func TestWarpConfigViewWithoutStoreIsUnavailable(t *testing.T) {
	_, err := NewService(nil).ConfigView(context.Background())
	require.ErrorIs(t, err, ErrUnavailable)
}

// The reference is a plain field, so clearing it is just sending an empty
// value - none of the omitted-versus-empty ambiguity a write-only secret forces.
func TestWarpSaveConfigRoundTripsKeyReference(t *testing.T) {
	row := validWarpConfigRow()
	row.APIKeyID = "key-abc"
	store := &recordingStore{row: row}
	input := validWarpConfigInput()
	input.Model = "gpt-4o-mini"
	input.APIKeyID = "key-xyz"
	view, err := newTestService(store).SaveConfig(context.Background(), input)
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
	_, err := newTestService(store).SaveConfig(context.Background(), validWarpConfigInput())
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
	for name, mutate := range map[string]func(*ConfigInput){
		"no provider":            func(input *ConfigInput) { input.Provider = "" },
		"no model":               func(input *ConfigInput) { input.Model = "" },
		"no embedding provider":  func(input *ConfigInput) { input.EmbeddingProvider = "" },
		"no embedding model":     func(input *ConfigInput) { input.EmbeddingModel = "" },
		"no embedding dimension": func(input *ConfigInput) { input.EmbeddingDimension = 0 },
	} {
		input := validWarpConfigInput()
		mutate(input)
		store := &recordingStore{}
		_, err := newTestService(store).SaveConfig(context.Background(), input)
		require.ErrorIs(t, err, ErrInvalidConfig, name)
		require.Empty(t, store.upserted, name)
	}
}

func TestWarpValidateConfigInputRejectsIterationsAboveCeiling(t *testing.T) {
	input := validWarpConfigInput()
	input.MaxIterations = 50
	err := ValidateConfigInput(input)
	require.ErrorIs(t, err, ErrInvalidConfig)
}

// nil is the unset state and must never be rejected - it is the default every
// deployment already had before Temperature existed. 0 is a real value (fully
// deterministic) and has to be accepted too, not treated as "not set".
func TestWarpValidateConfigInputAcceptsNilAndZeroTemperature(t *testing.T) {
	input := validWarpConfigInput()
	input.Temperature = nil
	require.NoError(t, ValidateConfigInput(input))

	zero := 0.0
	input.Temperature = &zero
	require.NoError(t, ValidateConfigInput(input))
}

func TestWarpValidateConfigInputRejectsOutOfRangeTemperature(t *testing.T) {
	for name, value := range map[string]float64{"negative": -0.1, "above ceiling": 2.1} {
		input := validWarpConfigInput()
		input.Temperature = &value
		err := ValidateConfigInput(input)
		require.ErrorIs(t, err, ErrInvalidConfig, name)
	}
}

func TestWarpValidateConfigInputRejectsUnknownReasoningEffort(t *testing.T) {
	input := validWarpConfigInput()
	input.ReasoningEffort = "extreme"
	err := ValidateConfigInput(input)
	require.ErrorIs(t, err, ErrInvalidConfig)
}

func TestWarpValidateConfigInputAcceptsEveryKnownReasoningEffort(t *testing.T) {
	for _, effort := range schemas.WarpReasoningEfforts {
		input := validWarpConfigInput()
		input.ReasoningEffort = effort
		require.NoError(t, ValidateConfigInput(input), effort)
	}
}

// Temperature is a pointer specifically so 0 round-trips distinctly from
// "never configured" - this proves the whole path (input -> row -> view)
// actually preserves that distinction rather than collapsing it somewhere.
func TestWarpSaveConfigRoundTripsExplicitZeroTemperature(t *testing.T) {
	store := &recordingStore{}
	input := validWarpConfigInput()
	zero := 0.0
	input.Temperature = &zero
	input.ReasoningEffort = "low"

	view, err := newTestService(store).SaveConfig(context.Background(), input)
	require.NoError(t, err)
	require.NotNil(t, store.upserted[0].Temperature)
	require.Equal(t, 0.0, *store.upserted[0].Temperature)
	require.Equal(t, "low", store.upserted[0].ReasoningEffort)
	require.NotNil(t, view.Temperature)
	require.Equal(t, 0.0, *view.Temperature)
	require.Equal(t, "low", view.ReasoningEffort)
}

func TestWarpSaveConfigLeavesTemperatureAndReasoningEffortUnsetByDefault(t *testing.T) {
	store := &recordingStore{}
	view, err := newTestService(store).SaveConfig(context.Background(), validWarpConfigInput())
	require.NoError(t, err)
	require.Nil(t, store.upserted[0].Temperature)
	require.Empty(t, store.upserted[0].ReasoningEffort)
	require.Nil(t, view.Temperature)
	require.Empty(t, view.ReasoningEffort)
}

// Config is what the chat path calls. It must refuse a disabled or incomplete
// config rather than handing back something half-usable.
func TestWarpConfigRejectsUnusableConfigs(t *testing.T) {
	for name, row := range map[string]*tables.TableWarpConfig{
		"missing":  nil,
		"disabled": {Enabled: false, Provider: "openai", Model: "gpt-4o", EmbeddingProvider: "openai", EmbeddingModel: "embed", EmbeddingDimension: 3},
		"no model": {Enabled: true, Provider: "openai"},
	} {
		_, err := newTestService(&recordingStore{row: row}).Config(context.Background())
		require.ErrorIs(t, err, ErrUnavailable, name)
	}
}

func TestWarpSaveConfigRequiresNewNamespaceForEmbeddingSpace(t *testing.T) {
	store := &recordingStore{row: validWarpConfigRow()}
	input := validWarpConfigInput()
	input.EmbeddingModel = "text-embedding-3-large"
	_, err := newTestService(store).SaveConfig(context.Background(), input)
	require.ErrorIs(t, err, ErrInvalidConfig)

	input.LogVectorStoreNamespace = "BifrostWarpLogsV2"
	view, err := newTestService(store).SaveConfig(context.Background(), input)
	require.NoError(t, err)
	require.Equal(t, "BifrostWarpLogsV2", view.LogVectorStoreNamespace)
	require.Equal(t, []string{schemas.WarpDefaultLogVectorStoreNamespace}, retiredNamespaces(store.row))
}
