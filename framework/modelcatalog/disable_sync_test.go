package modelcatalog

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestInit_DisableSync_NoHTTPCall verifies that when DisableSync is true and
// the URLs are unreachable, Init still succeeds without making any HTTP call —
// the airgapped-cluster contract.
func TestInit_DisableSync_NoHTTPCall(t *testing.T) {
	var hits int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&hits, 1)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("{}"))
	}))
	defer srv.Close()

	url := srv.URL
	syncSecs := int64(3600)
	disable := true
	cfg := &Config{
		PricingURL:          &url,
		ModelParametersURL:  &url,
		PricingSyncInterval: &syncSecs,
		DisableSync:         &disable,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	mc, err := Init(ctx, cfg, nil, bifrost.NewDefaultLogger(schemas.LogLevelError))
	require.NoError(t, err, "Init must not fail when sync is disabled")
	require.NotNil(t, mc)
	defer mc.Cleanup()

	assert.Equal(t, int64(0), atomic.LoadInt64(&hits), "no HTTP call should be made when DisableSync is true")
	assert.Equal(t, 0, len(mc.pricingData))
}

// TestSync_RuntimeToggleViaAtomic verifies the atomic flag is honored at
// runtime: flipping it on suppresses HTTP fetches, flipping it back off
// resumes them. Uses loadPricingIntoMemoryFromURL / loadModelParametersIntoMemoryFromURL
// (the no-configstore paths) so the test does not need a database.
func TestSync_RuntimeToggleViaAtomic(t *testing.T) {
	var hits int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&hits, 1)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("{}"))
	}))
	defer srv.Close()

	mc := NewTestCatalog(nil)
	mc.logger = bifrost.NewDefaultLogger(schemas.LogLevelError)
	mc.pricingURL = srv.URL
	mc.modelParametersURL = srv.URL

	// Disabled → no HTTP, no work.
	mc.disableSync.Store(true)
	require.NoError(t, mc.loadPricingIntoMemoryFromURL(context.Background()))
	require.NoError(t, mc.loadModelParametersIntoMemoryFromURL(context.Background()))
	require.Equal(t, int64(0), atomic.LoadInt64(&hits), "disabled must skip HTTP")

	// Re-enabled → HTTP fires for both fetchers.
	mc.disableSync.Store(false)
	require.NoError(t, mc.loadPricingIntoMemoryFromURL(context.Background()))
	require.NoError(t, mc.loadModelParametersIntoMemoryFromURL(context.Background()))
	assert.Equal(t, int64(2), atomic.LoadInt64(&hits), "re-enable must resume HTTP fetches")
}

