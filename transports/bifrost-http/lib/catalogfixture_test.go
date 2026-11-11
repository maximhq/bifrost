package lib

import (
	"context"
	"io"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/modelcatalog"
	"github.com/maximhq/bifrost/framework/modelcatalog/datasheet"
)

// Config tests exercise catalog initialization and persistence, not the size or
// changing contents of the public feeds. Intercept only their default URLs;
// custom URLs and local HTTP servers retain the normal transport behavior.
type catalogFixtureTransport struct {
	fallback http.RoundTripper
}

// catalogFixtureHits counts requests the fixture answered, so a test can prove
// the catalog loaders still route through it.
var catalogFixtureHits atomic.Int64

func (rt catalogFixtureTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	var body string
	switch req.URL.String() {
	case modelcatalog.DefaultPricingURL:
		body = `{"gpt-4o-mini":{"provider":"openai","mode":"chat","input_cost_per_token":0.00000015,"output_cost_per_token":0.0000006}}`
	case modelcatalog.DefaultModelParametersURL:
		body = `{"gpt-4o-mini":{"supports_reasoning":false,"supports_sampling_params":true}}`
	case modelcatalog.DefaultMCPLibraryURL:
		body = `{"servers":[]}`
	default:
		return rt.fallback.RoundTrip(req)
	}
	if err := req.Context().Err(); err != nil {
		return nil, err
	}
	catalogFixtureHits.Add(1)
	return &http.Response{
		StatusCode:    http.StatusOK,
		Header:        http.Header{"Content-Type": {"application/json"}},
		Body:          io.NopCloser(strings.NewReader(body)),
		ContentLength: int64(len(body)),
		Request:       req,
	}, nil
}

func TestMain(m *testing.M) {
	// Install once before any test or background worker starts. Leave it in
	// place until process exit so surviving workers cannot race a restoration.
	// The loaders build their own guarded client (see datasheet.NewHTTPClient),
	// so wrap that client's transport rather than http.DefaultTransport.
	newClient := datasheet.NewHTTPClient
	datasheet.NewHTTPClient = func(timeout time.Duration) *http.Client {
		client := newClient(timeout)
		client.Transport = catalogFixtureTransport{fallback: client.Transport}
		return client
	}
	os.Exit(m.Run())
}

// TestLoadConfigServesCatalogFeedsFromFixture pins that LoadConfig's catalog
// sync is answered by the fixture. When the loaders bypass it, every config
// test downloads the full public datasheets and the package takes ~35 minutes
// under -race in CI.
func TestLoadConfigServesCatalogFeedsFromFixture(t *testing.T) {
	initTestLogger()
	tempDir := createTempDir(t)
	providers := map[string]configstore.ProviderConfig{
		"openai": makeProviderConfigWithNetwork("openai-key-1", "sk-test-123", "https://api.openai.com"),
	}
	createConfigFile(t, tempDir, makeConfigDataWithProvidersAndDir(providers, tempDir))

	before := catalogFixtureHits.Load()
	ctx := context.Background()
	config, err := LoadConfig(ctx, tempDir)
	if err != nil {
		t.Fatalf("LoadConfig failed: %v", err)
	}
	defer config.Close(ctx)

	if got := catalogFixtureHits.Load() - before; got == 0 {
		t.Fatal("LoadConfig fetched the catalog feeds without going through the fixture; the loaders are reaching the public URLs")
	}
}
