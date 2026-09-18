package tables

import (
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/require"
)

func TestTableProviderMiniMaxConfigRoundTrip(t *testing.T) {
	provider := &TableProvider{
		MiniMaxConfig: &schemas.MiniMaxConfig{AuthType: schemas.MiniMaxAuthTypeXKey},
	}
	require.NoError(t, provider.BeforeSave(nil))
	require.JSONEq(t, `{"auth_type":"x-key"}`, provider.MiniMaxConfigJSON)

	loaded := &TableProvider{MiniMaxConfigJSON: provider.MiniMaxConfigJSON}
	require.NoError(t, loaded.AfterFind(nil))
	require.NotNil(t, loaded.MiniMaxConfig)
	require.Equal(t, schemas.MiniMaxAuthTypeXKey, loaded.MiniMaxConfig.AuthType)
}
