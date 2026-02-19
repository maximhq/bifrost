package handlers

import (
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/transports/bifrost-http/integrations"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	"github.com/stretchr/testify/assert"
)

type mockHandlerStore struct {
	allowDirectKeys    bool
	headerFilterConfig *configstoreTables.GlobalHeaderFilterConfig
	availableProviders []schemas.ModelProvider
}

func (m *mockHandlerStore) ShouldAllowDirectKeys() bool {
	return m.allowDirectKeys
}

func (m *mockHandlerStore) GetHeaderFilterConfig() *configstoreTables.GlobalHeaderFilterConfig {
	return m.headerFilterConfig
}

func (m *mockHandlerStore) GetAvailableProviders() []schemas.ModelProvider {
	return m.availableProviders
}

func (m *mockHandlerStore) GetStreamChunkInterceptor() lib.StreamChunkInterceptor {
	return nil
}

var _ lib.HandlerStore = (*mockHandlerStore)(nil)

func TestNewIntegrationHandlerIncludesCohereRouter(t *testing.T) {
	store := &mockHandlerStore{allowDirectKeys: true}
	handler := NewIntegrationHandler(nil, store)

	assert.NotNil(t, handler)
	assert.NotEmpty(t, handler.extensions)

	foundCohere := false
	for _, extension := range handler.extensions {
		if _, ok := extension.(*integrations.CohereRouter); ok {
			foundCohere = true
			break
		}
	}

	assert.True(t, foundCohere, "cohere router should be registered in integration handler")
}
