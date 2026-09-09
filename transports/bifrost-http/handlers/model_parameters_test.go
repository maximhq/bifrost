package handlers

import (
	"context"
	"fmt"
	"net"
	"testing"

	"github.com/maximhq/bifrost/framework/configstore"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/modelcatalog"
	"github.com/maximhq/bifrost/framework/modelcatalog/datasheet"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

func TestGetModelParameters_ResolvesQualifiedAndBareIDs(t *testing.T) {
	SetLogger(&mockLogger{})
	ctx := context.Background()

	dbPath := t.TempDir() + "/config.db"
	store, err := configstore.NewConfigStore(ctx, &configstore.Config{
		Enabled: true,
		Type:    configstore.ConfigStoreTypeSQLite,
		Config:  &configstore.SQLiteConfig{Path: dbPath},
	}, &mockLogger{})
	require.NoError(t, err)

	require.NoError(t, store.UpsertModelParametersBatch(ctx, []configstoreTables.TableModelParameters{
		{Model: "gpt-5.5", Data: `{"model_parameters":[{"id":"reasoning_effort"}]}`},
		{Model: "openrouter/moonshotai/kimi-k2.5", Data: `{"model_parameters":[{"id":"temperature"}]}`},
	}))

	ds := datasheet.New(store, &mockLogger{}, datasheet.Config{})
	rows, err := ds.LoadModelParamsFromDB(ctx)
	require.NoError(t, err)
	require.Equal(t, 2, rows)

	h := &ProviderHandler{
		dbStore: store,
		inMemoryStore: &lib.Config{
			ModelCatalog: modelcatalog.NewTestCatalogWithDatasheet(ds),
		},
	}

	tests := []struct {
		name       string
		model      string
		wantStatus int
		wantBody   string
	}{
		{
			name:       "exact bare key",
			model:      "gpt-5.5",
			wantStatus: fasthttp.StatusOK,
			wantBody:   `{"model_parameters":[{"id":"reasoning_effort"}]}`,
		},
		{
			name:       "provider-qualified resolves to bare key",
			model:      "openai/gpt-5.5",
			wantStatus: fasthttp.StatusOK,
			wantBody:   `{"model_parameters":[{"id":"reasoning_effort"}]}`,
		},
		{
			name:       "openrouter double-qualified resolves to bare key",
			model:      "openrouter/openai/gpt-5.5",
			wantStatus: fasthttp.StatusOK,
			wantBody:   `{"model_parameters":[{"id":"reasoning_effort"}]}`,
		},
		{
			name:       "bare alias resolves to openrouter-qualified key",
			model:      "kimi-k2.5",
			wantStatus: fasthttp.StatusOK,
			wantBody:   `{"model_parameters":[{"id":"temperature"}]}`,
		},
		{
			name:       "unknown model still 404s",
			model:      "definitely-not-a-model",
			wantStatus: fasthttp.StatusNotFound,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var req fasthttp.Request
			req.SetRequestURI(fmt.Sprintf("/api/models/parameters?model=%s", tt.model))
			reqCtx := &fasthttp.RequestCtx{}
			reqCtx.Init(&req, &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 12345}, nil)

			h.getModelParameters(reqCtx)

			require.Equal(t, tt.wantStatus, reqCtx.Response.StatusCode())
			if tt.wantBody != "" {
				require.Equal(t, tt.wantBody, string(reqCtx.Response.Body()))
			}
		})
	}

	t.Run("nil inMemoryStore falls back to exact DB lookup", func(t *testing.T) {
		bare := &ProviderHandler{dbStore: store}

		var req fasthttp.Request
		req.SetRequestURI("/api/models/parameters?model=gpt-5.5")
		reqCtx := &fasthttp.RequestCtx{}
		reqCtx.Init(&req, &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 12345}, nil)

		bare.getModelParameters(reqCtx)

		require.Equal(t, fasthttp.StatusOK, reqCtx.Response.StatusCode())
		require.Equal(t, `{"model_parameters":[{"id":"reasoning_effort"}]}`, string(reqCtx.Response.Body()))
	})
}
