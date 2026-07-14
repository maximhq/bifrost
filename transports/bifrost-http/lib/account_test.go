package lib

import (
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBaseAccountGetConfigForProviderPreservesPassthroughExtraParams(t *testing.T) {
	account := NewBaseAccount(&Config{Providers: map[schemas.ModelProvider]configstore.ProviderConfig{
		"omitted":  {},
		"disabled": {PassthroughExtraParams: schemas.Ptr(false)},
		"enabled":  {PassthroughExtraParams: schemas.Ptr(true)},
	}})

	for _, tt := range []struct {
		provider schemas.ModelProvider
		want     *bool
	}{
		{provider: "omitted"},
		{provider: "disabled", want: schemas.Ptr(false)},
		{provider: "enabled", want: schemas.Ptr(true)},
	} {
		t.Run(string(tt.provider), func(t *testing.T) {
			config, err := account.GetConfigForProvider(tt.provider)
			require.NoError(t, err)
			if tt.want == nil {
				assert.Nil(t, config.PassthroughExtraParams)
				return
			}
			require.NotNil(t, config.PassthroughExtraParams)
			assert.Equal(t, *tt.want, *config.PassthroughExtraParams)
		})
	}
}
