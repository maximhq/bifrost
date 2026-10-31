package sagemaker

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/require"
)

// fakeControlPlane serves ListEndpoints in two pages and counts the calls.
func fakeControlPlane(t *testing.T) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var calls atomic.Int32
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Host != "api.sagemaker.us-east-1.amazonaws.com" {
			t.Errorf("unexpected host %q", r.Host)
		}
		body, _ := io.ReadAll(r.Body)
		if r.Header.Get("X-Amz-Target") != "SageMaker.ListEndpoints" ||
			!strings.HasPrefix(r.Header.Get("Authorization"), "AWS4-HMAC-SHA256 Credential=AKIDTEST/") ||
			!strings.Contains(r.Header.Get("Authorization"), "/us-east-1/sagemaker/aws4_request") ||
			!strings.Contains(string(body), `"StatusEquals":"InService"`) {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"__type":"AccessDeniedException"}`))
			return
		}
		if strings.Contains(string(body), `"NextToken":"page2"`) {
			_, _ = w.Write([]byte(`{"Endpoints":[{"EndpointName":"bifrost-embed"}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"Endpoints":[{"EndpointName":"bifrost-llama"},{"EndpointName":"bifrost-qwen"}],"NextToken":"page2"}`))
	}))
	t.Cleanup(ts.Close)
	return ts, &calls
}

func newDiscoveryProvider(t *testing.T, ts *httptest.Server) *SageMakerProvider {
	t.Helper()
	config := &schemas.ProviderConfig{NetworkConfig: schemas.NetworkConfig{
		DefaultRequestTimeoutInSeconds: 5,
		InsecureSkipVerify:             true,
		AllowPrivateNetwork:            true,
	}}
	provider, err := NewSageMakerProvider(config, noopLogger{})
	require.NoError(t, err)
	// Keep the real api.sagemaker URL; only the connection goes to the fake server.
	addr := ts.Listener.Addr().String()
	provider.client.Dial = func(string) (net.Conn, error) { return net.Dial("tcp", addr) }
	return provider
}

func discoveryKey(models ...string) schemas.Key {
	return schemas.Key{
		Name:   "k",
		Models: schemas.WhiteList(models),
		SageMakerKeyConfig: &schemas.SageMakerKeyConfig{
			AccessKey: *schemas.NewSecretVar("AKIDTEST"),
			SecretKey: *schemas.NewSecretVar("secret"),
			Region:    schemas.NewSecretVar("us-east-1"),
		},
	}
}

func listIDs(t *testing.T, provider *SageMakerProvider, key schemas.Key) []string {
	t.Helper()
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	defer ctx.Cancel()
	resp, bErr := provider.ListModels(ctx, []schemas.Key{key}, &schemas.BifrostListModelsRequest{Provider: schemas.SageMaker})
	require.Nil(t, bErr, "%+v", bErr)
	var ids []string
	for _, m := range resp.Data {
		ids = append(ids, m.ID)
	}
	return ids
}

func TestListModelsDiscoversEndpoints(t *testing.T) {
	ts, calls := fakeControlPlane(t)
	provider := newDiscoveryProvider(t, ts)

	require.ElementsMatch(t, []string{"sagemaker/bifrost-llama", "sagemaker/bifrost-qwen", "sagemaker/bifrost-embed"}, listIDs(t, provider, discoveryKey("*")))
	require.EqualValues(t, 2, calls.Load(), "both pages fetched")

	require.ElementsMatch(t, []string{"sagemaker/bifrost-llama", "sagemaker/bifrost-qwen"}, listIDs(t, provider, discoveryKey("regex:^bifrost-(llama|qwen)$")))
}

func TestListModelsLiteralNamesKeepUndiscoveredEntries(t *testing.T) {
	ts, _ := fakeControlPlane(t)
	provider := newDiscoveryProvider(t, ts)

	// bifrost-gone is not InService and shared-ep/qwen3-ic is an inference component; both stay listed.
	require.ElementsMatch(t,
		[]string{"sagemaker/bifrost-llama", "sagemaker/bifrost-gone", "sagemaker/shared-ep/qwen3-ic"},
		listIDs(t, provider, discoveryKey("bifrost-llama", "bifrost-gone", "shared-ep/qwen3-ic")))
}

func TestListModelsBearerKeySkipsDiscovery(t *testing.T) {
	ts, calls := fakeControlPlane(t)
	provider := newDiscoveryProvider(t, ts)

	bearer := discoveryKey("*")
	bearer.Value = *schemas.NewSecretVar("sagemaker-api-key-test")
	require.Empty(t, listIDs(t, provider, bearer))
	require.Zero(t, calls.Load(), "a pre-minted token cannot sign ListEndpoints")
}

func TestListModelsDiscoveryErrorSurfaces(t *testing.T) {
	ts, _ := fakeControlPlane(t)
	provider := newDiscoveryProvider(t, ts)
	key := discoveryKey("*")
	key.SageMakerKeyConfig.AccessKey = *schemas.NewSecretVar("AKIDWRONG")

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	defer ctx.Cancel()
	_, bErr := provider.ListModels(ctx, []schemas.Key{key}, &schemas.BifrostListModelsRequest{Provider: schemas.SageMaker})
	require.NotNil(t, bErr)
	require.Contains(t, bErr.Error.Message, "AccessDeniedException")
}
