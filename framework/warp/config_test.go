package warp

import (
	"context"
	"errors"
	"github.com/bytedance/sonic"
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
		"no provider": func(input *ConfigInput) { input.Provider = "" },
		"no model":    func(input *ConfigInput) { input.Model = "" },
		// Stored unchecked, this reported the deployment as configured and then
		// failed at provider construction on the first question.
		"unknown provider":       func(input *ConfigInput) { input.Provider = "not-a-provider" },
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
//
// Built from validWarpConfigInput so the only thing under test is the URL: a
// bare literal would now fail on the required embedding fields instead, and
// pass for the wrong reason.
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
		input := validWarpConfigInput()
		input.BaseURL = baseURL
		require.ErrorIs(t, ValidateConfigInput(input), ErrInvalidConfig, name)
	}

	// Empty stays valid: it means "use the provider's own default endpoint",
	// which is the common case for a hosted provider.
	for name, baseURL := range map[string]string{
		"empty":          "",
		"https host":     "https://models.internal.example.com",
		"http with port": "http://localhost:11434",
		"with path":      "https://gateway.example.com/openai/v1",
	} {
		input := validWarpConfigInput()
		input.BaseURL = baseURL
		require.NoError(t, ValidateConfigInput(input), name)
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

// History retention is Warp's own setting, deliberately not logs_store's. A
// deployment that keeps 7 days of request telemetry has not thereby decided to
// throw away somebody's saved chats after a week, and the reverse - keeping
// logs for a year because chats are worth keeping that long - is the expensive
// half of the same mistake.
func TestWarpConfigViewResolvesHistoryRetentionDefault(t *testing.T) {
	// Unconfigured: the form renders the default rather than a bare zero.
	view, err := newTestService(&recordingStore{}).ConfigView(context.Background())
	require.NoError(t, err)
	require.Equal(t, schemas.WarpDefaultHistoryRetentionDays, view.HistoryRetentionDays)

	// Stored zero means "never set", which resolves to the same default: a row
	// written before this setting existed must not read as "expire immediately".
	view, err = newTestService(&recordingStore{row: validWarpConfigRow()}).ConfigView(context.Background())
	require.NoError(t, err)
	require.Equal(t, schemas.WarpDefaultHistoryRetentionDays, view.HistoryRetentionDays)
}

func TestWarpSaveConfigRoundTripsHistoryRetention(t *testing.T) {
	store := &recordingStore{row: validWarpConfigRow()}
	input := validWarpConfigInput()
	input.HistoryRetentionDays = 7
	view, err := newTestService(store).SaveConfig(context.Background(), input)
	require.NoError(t, err)
	require.Len(t, store.upserted, 1)
	require.Equal(t, 7, store.upserted[0].HistoryRetentionDays)
	require.Equal(t, 7, view.HistoryRetentionDays)
}

// Negative is rejected rather than treated as "keep forever": a setting whose
// sign silently changes its meaning is how an operator ends up with a retention
// policy they did not choose.
func TestWarpValidateConfigInputRejectsNegativeHistoryRetention(t *testing.T) {
	input := validWarpConfigInput()
	input.HistoryRetentionDays = -1
	err := ValidateConfigInput(input)
	require.ErrorIs(t, err, ErrInvalidConfig)
	// Named explicitly: every validation failure wraps ErrInvalidConfig, so
	// asserting the sentinel alone would pass on somebody else's rejection.
	require.ErrorContains(t, err, "history_retention_days")
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

// A disabled save is a draft: validation deliberately accepts missing embedding
// fields so a form can be filled in over more than one sitting. Comparing those
// zeros against a complete stored embedding space then reads as "the space
// changed" - which either rejects the save for not renaming a namespace nobody
// touched, or retires the live namespace while writing zeros over the config.
func TestWarpEmbeddingSpaceUnchangedByIncompleteInput(t *testing.T) {
	stored := &tables.TableWarpConfig{
		ID: tables.WarpConfigRowID, Enabled: true, Provider: "openai", Model: "gpt-4o",
		EmbeddingProvider: "openai", EmbeddingModel: "text-embedding-3-small",
		EmbeddingDimension: 1536, LogVectorStoreNamespace: "BifrostWarpLogs",
	}

	for name, input := range map[string]*ConfigInput{
		"all embedding fields omitted": {Enabled: false, Provider: "openai", Model: "gpt-4o"},
		"provider only":                {Enabled: false, EmbeddingProvider: "openai"},
		"missing dimension":            {Enabled: false, EmbeddingProvider: "openai", EmbeddingModel: "text-embedding-3-small"},
		"zero dimension":               {Enabled: false, EmbeddingProvider: "openai", EmbeddingModel: "text-embedding-3-small", EmbeddingDimension: 0},
	} {
		require.False(t, embeddingSpaceChanged(stored, input), name)
	}

	// A complete input that genuinely names a different space still counts.
	changed := &ConfigInput{
		Enabled: true, EmbeddingProvider: "openai", EmbeddingModel: "text-embedding-3-large", EmbeddingDimension: 3072,
	}
	require.True(t, embeddingSpaceChanged(stored, changed))

	// And a complete input naming the same space does not.
	same := &ConfigInput{
		Enabled: true, EmbeddingProvider: "openai", EmbeddingModel: "text-embedding-3-small", EmbeddingDimension: 1536,
	}
	require.False(t, embeddingSpaceChanged(stored, same))
}

// A disabled draft that omits embedding fields must save without retiring the
// namespace the live config is still indexing under.
func TestWarpSaveDraftDoesNotRetireLiveNamespace(t *testing.T) {
	store := &recordingStore{row: &tables.TableWarpConfig{
		ID: tables.WarpConfigRowID, Enabled: true, Provider: "openai", Model: "gpt-4o",
		EmbeddingProvider: "openai", EmbeddingModel: "text-embedding-3-small",
		EmbeddingDimension: 1536, LogVectorStoreNamespace: "BifrostWarpLogs",
	}}
	_, err := newTestService(store).SaveConfig(context.Background(), &ConfigInput{
		Enabled: false, Provider: "openai", Model: "gpt-4o",
	})
	require.NoError(t, err, "a draft must not be rejected for a namespace it never changed")
	require.Len(t, store.upserted, 1)
	require.Empty(t, retiredNamespaces(&store.upserted[0]),
		"the live namespace must not be retired by a draft that named no embedding space")
}

// A disabled draft must not wipe the embedding settings it never mentioned.
//
// UpsertWarpConfig writes the whole row with UpdateAll, so copying an omitted
// field through as its zero value is a delete. Validation deliberately accepts
// an incomplete draft when enabled is false - that is the point of a draft - so
// the two together silently cleared the stored embedding space, and the next
// attempt to enable Warp failed validation for fields the operator had already
// supplied and never touched.
func TestWarpSaveDraftPreservesStoredEmbeddingSettings(t *testing.T) {
	store := &recordingStore{row: &tables.TableWarpConfig{
		ID: tables.WarpConfigRowID, Enabled: true, Provider: "openai", Model: "gpt-4o",
		EmbeddingProvider: "openai", EmbeddingModel: "text-embedding-3-small",
		EmbeddingAPIKeyID: "key-embed", EmbeddingDimension: 1536,
		LogVectorStoreNamespace: "BifrostWarpLogs",
		SemanticSearchThreshold: 0.8, SemanticSearchLimit: 10,
	}}

	_, err := newTestService(store).SaveConfig(context.Background(), &ConfigInput{
		Enabled: false, Provider: "openai", Model: "gpt-4o",
	})
	require.NoError(t, err)
	require.Len(t, store.upserted, 1)

	saved := store.upserted[0]
	require.Equal(t, "openai", saved.EmbeddingProvider, "the embedding provider must survive a draft that never named one")
	require.Equal(t, "text-embedding-3-small", saved.EmbeddingModel)
	require.Equal(t, "key-embed", saved.EmbeddingAPIKeyID)
	require.Equal(t, 1536, saved.EmbeddingDimension)
	require.Equal(t, "BifrostWarpLogs", saved.LogVectorStoreNamespace)
	require.False(t, saved.Enabled, "the one field the draft did set must still be written")
}

// Clearing has to stay possible: a complete input that names a different space
// replaces the stored one rather than merging into it.
func TestWarpSaveConfigStillReplacesANamedEmbeddingSpace(t *testing.T) {
	store := &recordingStore{row: validWarpConfigRow()}
	input := validWarpConfigInput()
	input.EmbeddingModel = "text-embedding-3-large"
	input.EmbeddingDimension = 3072
	input.LogVectorStoreNamespace = "BifrostWarpLogsV2"

	_, err := newTestService(store).SaveConfig(context.Background(), input)
	require.NoError(t, err)
	require.Equal(t, "text-embedding-3-large", store.upserted[0].EmbeddingModel)
	require.Equal(t, 3072, store.upserted[0].EmbeddingDimension)
}

// The namespace is created in the vector store before the save is validated, so
// a save that is about to be rejected had already provisioned one - leaving a
// namespace behind with no configuration naming it. It also meant the caller
// saw whatever Qdrant or Redis said about a dimension mismatch instead of the
// ErrInvalidConfig the request actually earned.
func TestWarpSaveConfigValidatesBeforeProvisioning(t *testing.T) {
	vectors := newFakeWarpVectorStore()
	store := &recordingStore{row: &tables.TableWarpConfig{
		ID: tables.WarpConfigRowID, Enabled: true, Provider: "openai", Model: "gpt-4o",
		EmbeddingProvider: "openai", EmbeddingModel: "text-embedding-3-small",
		EmbeddingDimension: 1536, LogVectorStoreNamespace: "BifrostWarpLogs",
	}}
	service := NewService(nil, WithConfigStore(store), WithVectorStore(vectors))

	// Changes the embedding space without renaming the namespace: rejected.
	_, err := service.SaveConfig(context.Background(), &ConfigInput{
		Enabled: true, Provider: "openai", Model: "gpt-4o",
		EmbeddingProvider: "openai", EmbeddingModel: "text-embedding-3-large", EmbeddingDimension: 3072,
		LogVectorStoreNamespace: "BifrostWarpLogs",
	})
	require.ErrorIs(t, err, ErrInvalidConfig)
	require.Empty(t, vectors.namespace, "no namespace may be provisioned for a save that is rejected")
	require.Empty(t, store.upserted)
}

// A stored custom namespace must survive a save that never mentions it.
//
// ValidateConfigInput replaces an omitted namespace with the default before the
// stored row is even loaded, so a merge afterwards cannot tell "not supplied"
// from "explicitly the default". A deployment on a custom namespace was
// therefore moved back to the default by any save that did not restate it - and
// because the namespace is part of the embedding space, that silently orphaned
// every vector already indexed under it.
func TestWarpSaveConfigKeepsStoredCustomEmbeddingSettings(t *testing.T) {
	store := &recordingStore{row: &tables.TableWarpConfig{
		ID: tables.WarpConfigRowID, Enabled: true, Provider: "openai", Model: "gpt-4o",
		EmbeddingProvider: "openai", EmbeddingModel: "text-embedding-3-small",
		EmbeddingDimension: 1536, LogVectorStoreNamespace: "CustomWarpLogs",
		SemanticSearchThreshold: 0.6, SemanticSearchLimit: 20,
	}}

	_, err := newTestService(store).SaveConfig(context.Background(), inputFromJSON(t, `{
		"enabled": true, "provider": "openai", "model": "gpt-4o-mini"
	}`))
	require.NoError(t, err)
	require.Len(t, store.upserted, 1)

	saved := store.upserted[0]
	require.Equal(t, "CustomWarpLogs", saved.LogVectorStoreNamespace, "a custom namespace must not be reset to the default")
	require.InDelta(t, 0.6, saved.SemanticSearchThreshold, 1e-9)
	require.Equal(t, 20, saved.SemanticSearchLimit)
	require.Equal(t, "gpt-4o-mini", saved.Model, "the field that was supplied still changes")
}

// A partial embedding change must still count as a change.
//
// embeddingSpaceChanged ran on the incomplete input and returned false, then the
// merge combined the new model with the stored provider and dimension - so a
// different embedding space was persisted without the namespace rule applying
// and without the old namespace being retired. Detection has to run on the
// effective configuration, after the stored values are folded in.
func TestWarpSaveConfigDetectsPartialEmbeddingSpaceChange(t *testing.T) {
	row := func() *tables.TableWarpConfig {
		return &tables.TableWarpConfig{
			ID: tables.WarpConfigRowID, Enabled: true, Provider: "openai", Model: "gpt-4o",
			EmbeddingProvider: "openai", EmbeddingModel: "text-embedding-3-small",
			EmbeddingDimension: 1536, LogVectorStoreNamespace: "BifrostWarpLogs",
		}
	}

	refused := &recordingStore{row: row()}
	_, err := newTestService(refused).SaveConfig(context.Background(), inputFromJSON(t, `{
		"enabled": false, "embedding_model": "text-embedding-3-large"
	}`))
	require.ErrorIs(t, err, ErrInvalidConfig)
	require.ErrorContains(t, err, "log_vector_store_namespace")
	require.Empty(t, refused.upserted, "a rejected save must not reach the store")

	accepted := &recordingStore{row: row()}
	_, err = newTestService(accepted).SaveConfig(context.Background(), inputFromJSON(t, `{
		"enabled": false, "embedding_model": "text-embedding-3-large",
		"log_vector_store_namespace": "BifrostWarpLogsV2"
	}`))
	require.NoError(t, err)
	require.Len(t, accepted.upserted, 1)
	require.Equal(t, []string{"BifrostWarpLogs"}, retiredNamespaces(&accepted.upserted[0]))
}

// An explicitly empty key reference must clear, not restore.
func TestWarpSaveConfigDistinguishesOmittedFromExplicitlyEmpty(t *testing.T) {
	row := func() *tables.TableWarpConfig {
		return &tables.TableWarpConfig{
			ID: tables.WarpConfigRowID, Enabled: true, Provider: "openai", Model: "gpt-4o",
			EmbeddingProvider: "openai", EmbeddingModel: "text-embedding-3-small",
			EmbeddingAPIKeyID: "key-embed", EmbeddingDimension: 1536,
			LogVectorStoreNamespace: "BifrostWarpLogs",
		}
	}

	omitted := &recordingStore{row: row()}
	_, err := newTestService(omitted).SaveConfig(context.Background(), inputFromJSON(t, `{"enabled": false}`))
	require.NoError(t, err)
	require.Equal(t, "key-embed", omitted.upserted[0].EmbeddingAPIKeyID, "an omitted field keeps the stored value")

	cleared := &recordingStore{row: row()}
	_, err = newTestService(cleared).SaveConfig(context.Background(), inputFromJSON(t, `{
		"enabled": false, "embedding_api_key_id": ""
	}`))
	require.NoError(t, err)
	require.Empty(t, cleared.upserted[0].EmbeddingAPIKeyID, "an explicit empty value clears the reference")
}

// inputFromJSON decodes a write the way the HTTP handler does, so field
// presence is recorded. A ConfigInput literal cannot express "absent".
func inputFromJSON(t *testing.T, body string) *ConfigInput {
	t.Helper()
	var input ConfigInput
	require.NoError(t, sonic.Unmarshal([]byte(body), &input))
	return &input
}

// A blank stored namespace means the default, not "no namespace". Comparing the
// raw values read "" and BifrostWarpLogs as two different places, so the model
// could change without a rename and new vectors of one shape landed in the same
// namespace as incompatible old ones.
func TestWarpSaveConfigResolvesNamespaceBeforeComparing(t *testing.T) {
	stored := validWarpConfigRow()
	stored.LogVectorStoreNamespace = ""
	input := validWarpConfigInput()
	// A different embedding model, and the namespace spelled as the default the
	// blank stored value already resolves to.
	input.EmbeddingModel = "text-embedding-3-large"
	input.LogVectorStoreNamespace = schemas.WarpDefaultLogVectorStoreNamespace

	store := &recordingStore{row: stored}
	_, err := newTestService(store).SaveConfig(context.Background(), input)
	require.ErrorIs(t, err, ErrInvalidConfig, "the namespace did not actually change")
	require.Empty(t, store.upserted)

	// A genuinely new namespace is accepted, and the retired list records what
	// was in use - the default, not the empty string, which names nothing.
	input.LogVectorStoreNamespace = "BifrostWarpLogsV2"
	store = &recordingStore{row: stored}
	_, err = newTestService(store).SaveConfig(context.Background(), input)
	require.NoError(t, err)
	require.Len(t, store.upserted, 1)
	require.NotNil(t, store.upserted[0].RetiredLogVectorStoreNamespaces)
	require.Contains(t, *store.upserted[0].RetiredLogVectorStoreNamespaces, schemas.WarpDefaultLogVectorStoreNamespace)
}

// failingStore persists nothing, so a save gets past namespace creation and
// then fails on the write - the window where an orphan is made.
type failingStore struct {
	recordingStore
	err error
}

func (s *failingStore) UpsertWarpConfig(context.Context, *tables.TableWarpConfig) error {
	return s.err
}

// A namespace created by a save that then failed to persist is referenced by no
// stored config, and the next save under a different name never reclaims it.
// One that already existed belongs to whatever configuration is still live.
func TestWarpSaveConfigCompensatesOnlyItsOwnNamespace(t *testing.T) {
	t.Run("removes the namespace it created", func(t *testing.T) {
		vectors := newFakeWarpVectorStore()
		store := &failingStore{err: errors.New("write failed")}
		service := NewService(nil, WithConfigStore(store), WithVectorStore(vectors))
		_, err := service.SaveConfig(context.Background(), validWarpConfigInput())
		require.Error(t, err)
		require.Equal(t, []string{validWarpConfigInput().LogVectorStoreNamespace}, vectors.deleted)
	})

	t.Run("leaves a namespace it did not create", func(t *testing.T) {
		input := validWarpConfigInput()
		vectors := newFakeWarpVectorStore()
		vectors.existing = []string{input.LogVectorStoreNamespace}
		store := &failingStore{err: errors.New("write failed")}
		service := NewService(nil, WithConfigStore(store), WithVectorStore(vectors))
		_, err := service.SaveConfig(context.Background(), input)
		require.Error(t, err)
		require.Empty(t, vectors.deleted, "a pre-existing namespace may hold vectors a live config still indexes")
	})
}

// A legacy row with no namespace stored still resolves to the default one.
//
// The rejection compared raw strings, so a row holding "" looked different from
// a request naming the default - and an embedding-space change was allowed to
// reuse the namespace it was already indexed under, mixing vectors from two
// configurations in one place. Comparing effective namespaces closes that.
func TestWarpSaveConfigRejectsReusingALegacyDefaultNamespace(t *testing.T) {
	for name, stored := range map[string]string{
		"empty":            "",
		"whitespace only":  "   ",
		"padded default":   "  " + schemas.WarpDefaultLogVectorStoreNamespace + "  ",
		"explicit default": schemas.WarpDefaultLogVectorStoreNamespace,
	} {
		store := &recordingStore{row: &tables.TableWarpConfig{
			ID: tables.WarpConfigRowID, Enabled: true, Provider: "openai", Model: "gpt-4o",
			EmbeddingProvider: "openai", EmbeddingModel: "text-embedding-3-small",
			EmbeddingDimension: 1536, LogVectorStoreNamespace: stored,
		}}
		_, err := newTestService(store).SaveConfig(context.Background(), inputFromJSON(t, `{
			"enabled": false, "embedding_model": "text-embedding-3-large",
			"log_vector_store_namespace": "`+schemas.WarpDefaultLogVectorStoreNamespace+`"
		}`))
		require.ErrorIs(t, err, ErrInvalidConfig, "%s: the effective namespace is unchanged, so the space may not move", name)
		require.ErrorContains(t, err, "log_vector_store_namespace", name)
	}
}

// Warp must still answer questions on a deployment with no vector store.
//
// Config is what NewTurn calls on every chat request, so returning
// ErrNoVectorStore there failed the whole feature - the handler maps it to 503
// - rather than just the semantic tool. Semantic search is one of several
// tools: buildToolsFor already adds semantic_search_logs only when a searcher
// exists, so the loop degrades to the other tools by construction.
func TestWarpConfigDoesNotRequireAVectorStoreToAnswer(t *testing.T) {
	service := NewService(nil, WithConfigStore(&recordingStore{row: validWarpConfigRow()}))
	require.Nil(t, service.vectorStore, "precondition: no vector store on this deployment")

	config, err := service.Config(context.Background())
	require.NoError(t, err, "chat must not be refused for want of a vector store")
	require.NotNil(t, config)
	require.Equal(t, "gpt-4o", config.Model)
}

// Enabling Warp without a vector store is still refused: that is a save the
// operator can fix, and accepting it would promise semantic search the
// deployment cannot provide.
func TestWarpSaveConfigStillRequiresAVectorStoreToEnable(t *testing.T) {
	service := NewService(nil, WithConfigStore(&recordingStore{}))
	_, err := service.SaveConfig(context.Background(), validWarpConfigInput())
	require.ErrorIs(t, err, ErrNoVectorStore)
}
