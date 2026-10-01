package handlers

import (
	"context"
	"testing"

	"github.com/fasthttp/router"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/maximhq/bifrost/framework/queryscope"
	"github.com/maximhq/bifrost/plugins/logging"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
	"gorm.io/gorm"
)

type visibilityLogManager struct {
	logging.LogManager
	t            *testing.T
	hidden       []string
	searchCalled bool
	deleteCalled bool
}

func (m *visibilityLogManager) GetAvailableModels(ctx context.Context, _ int, _ string) ([]string, error) {
	if len(logstore.HiddenRequestTypesFromContext(ctx)) > 0 {
		return []string{"visible-model"}, nil
	}
	return []string{"visible-model", "hidden-model"}, nil
}

func (m *visibilityLogManager) Search(ctx context.Context, _ *logstore.SearchFilters, _ *logstore.PaginationOptions) (*logstore.SearchResult, error) {
	m.searchCalled = true
	require.Equal(m.t, m.hidden, logstore.HiddenRequestTypesFromContext(ctx))
	require.NotNil(m.t, queryscope.FromContext(ctx), "access control must survive visibility filtering")
	return &logstore.SearchResult{}, nil
}

func (m *visibilityLogManager) DeleteLogs(ctx context.Context, _ []string) error {
	m.deleteCalled = true
	require.Empty(m.t, logstore.HiddenRequestTypesFromContext(ctx), "write routes must stay unfiltered")
	return nil
}

func TestHiddenRequestTypesRoutes(t *testing.T) {
	for _, hidden := range [][]string{nil, {}, {"count_tokens", "embedding"}} {
		mgr := &visibilityLogManager{t: t, hidden: hidden}
		// Empty config is a no-op, so it need not attach an empty slice.
		if len(hidden) == 0 {
			mgr.hidden = nil
		}
		h := &LoggingHandler{
			logManager: mgr, redactedKeysManager: noRedactedKeys{},
			config: &lib.Config{ClientConfig: &configstore.ClientConfig{HiddenRequestTypes: hidden}},
		}
		r := router.New()
		h.RegisterRoutes(r)
		ctx := &fasthttp.RequestCtx{}
		ctx.Request.SetRequestURI("/api/logs")
		ctx.Request.Header.SetMethod("GET")
		ctx.SetUserValue(schemas.BifrostContextKeyQueryScope, queryscope.QueryScope(func(db *gorm.DB) *gorm.DB { return db }))
		r.Handler(ctx)
		require.Equal(t, fasthttp.StatusOK, ctx.Response.StatusCode())
		require.True(t, mgr.searchCalled)
		require.Empty(t, logstore.HiddenRequestTypesFromContext(ctx), "request context is restored after reading")

		ctx.Request.Header.SetMethod("DELETE")
		ctx.Request.SetBodyString(`{"ids":["hidden-id"]}`)
		r.Handler(ctx)
		require.Equal(t, fasthttp.StatusOK, ctx.Response.StatusCode())
		require.True(t, mgr.deleteCalled)
	}
}

func TestHiddenRequestTypesFilterCache(t *testing.T) {
	h := &LoggingHandler{
		logManager: &visibilityLogManager{t: t}, redactedKeysManager: noRedactedKeys{},
		config: &lib.Config{ClientConfig: &configstore.ClientConfig{}, LogsStoreConfig: &logstore.Config{Type: logstore.LogStoreTypeSQLite}},
	}
	r := router.New()
	h.RegisterRoutes(r)
	// Populate the unrestricted cache first, then ensure hiding a type does not
	// serve that cached result. Removing the setting restores all options.
	for _, hidden := range [][]string{nil, {"embedding"}, nil} {
		h.config.ClientConfig.HiddenRequestTypes = hidden
		var req fasthttp.Request
		req.SetRequestURI("/api/logs/filterdata?dimensions=models")
		req.Header.SetMethod("GET")
		ctx := &fasthttp.RequestCtx{}
		ctx.Init(&req, nil, nil)
		r.Handler(ctx)
		require.Equal(t, fasthttp.StatusOK, ctx.Response.StatusCode())
		require.Contains(t, string(ctx.Response.Body()), "visible-model")
		if len(hidden) > 0 {
			require.NotContains(t, string(ctx.Response.Body()), "hidden-model")
		} else {
			require.Contains(t, string(ctx.Response.Body()), "hidden-model")
		}
	}
}
