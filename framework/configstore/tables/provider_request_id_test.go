package tables

import (
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/require"
)

func TestTableProviderProviderRequestIDRoundTrip(t *testing.T) {
	provider := &TableProvider{
		ProviderRequestID: &schemas.ProviderRequestIDConfig{Enabled: true, HeaderName: "x-request-id"},
	}
	require.NoError(t, provider.BeforeSave(nil))
	require.JSONEq(t, `{"enabled":true,"header_name":"x-request-id"}`, provider.ProviderRequestIDConfigJSON)

	loaded := &TableProvider{ProviderRequestIDConfigJSON: provider.ProviderRequestIDConfigJSON}
	require.NoError(t, loaded.AfterFind(nil))
	require.Equal(t, provider.ProviderRequestID, loaded.ProviderRequestID)

	loaded.ProviderRequestIDConfigJSON = ""
	require.NoError(t, loaded.AfterFind(nil))
	require.Nil(t, loaded.ProviderRequestID)
}
