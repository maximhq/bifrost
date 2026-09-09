package lib

import (
	"io"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/maximhq/bifrost/framework/modelcatalog"
)

// Config tests exercise catalog initialization and persistence, not the size or
// changing contents of the public feeds. Intercept only their default URLs;
// custom URLs and local HTTP servers retain the normal transport behavior.
type catalogFixtureTransport struct {
	fallback http.RoundTripper
}

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
	http.DefaultTransport = catalogFixtureTransport{fallback: http.DefaultTransport}
	os.Exit(m.Run())
}
