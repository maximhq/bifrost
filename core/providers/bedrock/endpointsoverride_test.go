package bedrock

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type endpointRecordedRequest struct {
	method string
	host   string
	uri    string
	header http.Header
}

type endpointRequestRecorder struct {
	mu   sync.Mutex
	reqs []endpointRecordedRequest
}

func (recorder *endpointRequestRecorder) record(request *http.Request) {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	recorder.reqs = append(recorder.reqs, endpointRecordedRequest{
		method: request.Method,
		host:   request.Host,
		uri:    request.URL.RequestURI(),
		header: request.Header.Clone(),
	})
}

func (recorder *endpointRequestRecorder) all() []endpointRecordedRequest {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	return append([]endpointRecordedRequest(nil), recorder.reqs...)
}

func newEndpointRecordingServer(t *testing.T, tls bool, contentType, body string) (*httptest.Server, *endpointRequestRecorder) {
	t.Helper()
	recorder := &endpointRequestRecorder{}
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		recorder.record(request)
		writer.Header().Set("Content-Type", contentType)
		_, _ = writer.Write([]byte(body))
	}))
	if tls {
		server.StartTLS()
	} else {
		server.Start()
	}
	t.Cleanup(server.Close)
	return server, recorder
}

func TestCompleteRequestHonorsBaseURL(t *testing.T) {
	clearEndpointEnv(t)
	server, recorder := newEndpointRecordingServer(t, false, "application/json", `{}`)
	provider := newTestProviderWithBaseURL(t, server.URL+"/gateway")

	body, _, _, bifrostErr := provider.completeRequest(testBedrockCtx(), []byte(`{"messages":[]}`), "model-id/converse", testBedrockKey(), "model-id")
	require.Nil(t, bifrostErr)
	assert.Equal(t, `{}`, string(body))

	requests := recorder.all()
	require.Len(t, requests, 1)
	assert.Equal(t, http.MethodPost, requests[0].method)
	assert.Equal(t, "/gateway/model/model-id/converse", requests[0].uri)
}

func TestListModelsUsesEndpointOverridesNotBaseURL(t *testing.T) {
	clearEndpointEnv(t)
	baseServer, baseRecorder := newEndpointRecordingServer(t, false, "application/json", `{}`)
	controlServer, controlRecorder := newEndpointRecordingServer(t, true, "application/json", `{"modelSummaries":[]}`)
	mantleServer, mantleRecorder := newEndpointRecordingServer(t, true, "application/json", `{"data":[]}`)
	provider := newTestProviderWithBaseURL(t, baseServer.URL)
	key := testBedrockKey()
	key.BedrockKeyConfig.Endpoints = &schemas.BedrockEndpoints{
		ControlPlane: schemas.NewSecretVar(controlServer.URL),
		Mantle:       schemas.NewSecretVar(mantleServer.URL),
	}

	response, bifrostErr := provider.listModelsByKey(testBedrockCtx(), key, &schemas.BifrostListModelsRequest{})
	require.Nil(t, bifrostErr)
	require.NotNil(t, response)
	assert.Empty(t, baseRecorder.all())
	require.Len(t, controlRecorder.all(), 1)
	require.Len(t, mantleRecorder.all(), 1)
	assert.True(t, strings.HasPrefix(controlRecorder.all()[0].uri, "/foundation-models"))
	assert.Equal(t, "/v1/models", mantleRecorder.all()[0].uri)
}

func TestFileUploadHonorsS3EndpointOverride(t *testing.T) {
	clearEndpointEnv(t)
	server, recorder := newEndpointRecordingServer(t, true, "application/octet-stream", "")
	provider := newTestProviderWithBaseURL(t, "")
	key := testBedrockKey()
	key.Value = schemas.SecretVar{}
	key.BedrockKeyConfig.AccessKey = *schemas.NewSecretVar("AKIAIOSFODNN7EXAMPLE")
	key.BedrockKeyConfig.SecretKey = *schemas.NewSecretVar("wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY")
	key.BedrockKeyConfig.Endpoints = &schemas.BedrockEndpoints{S3: schemas.NewSecretVar(server.URL)}

	response, bifrostErr := provider.FileUpload(testBedrockCtx(), key, &schemas.BifrostFileUploadRequest{
		File:        []byte("batch data"),
		Filename:    "a b/c#d",
		ExtraParams: map[string]interface{}{"s3_bucket": "test-bucket"},
	})
	require.Nil(t, bifrostErr)
	require.NotNil(t, response)

	requests := recorder.all()
	require.Len(t, requests, 1)
	assert.Equal(t, "/test-bucket/a%20b/c%23d", requests[0].uri)
	assert.Contains(t, requests[0].header.Get("Authorization"), "/us-east-1/s3/aws4_request")
}

func TestFileUploadHonorsS3EnvOverride(t *testing.T) {
	clearEndpointEnv(t)
	server, recorder := newEndpointRecordingServer(t, true, "application/octet-stream", "")
	t.Setenv("AWS_ENDPOINT_URL_S3", server.URL)
	provider := newTestProviderWithBaseURL(t, "")
	key := testBedrockKey()
	key.Value = schemas.SecretVar{}
	key.BedrockKeyConfig.AccessKey = *schemas.NewSecretVar("AKIAIOSFODNN7EXAMPLE")
	key.BedrockKeyConfig.SecretKey = *schemas.NewSecretVar("wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY")

	response, bifrostErr := provider.FileUpload(testBedrockCtx(), key, &schemas.BifrostFileUploadRequest{
		File:        []byte("batch data"),
		Filename:    "input.jsonl",
		ExtraParams: map[string]interface{}{"s3_bucket": "test-bucket"},
	})
	require.Nil(t, bifrostErr)
	require.NotNil(t, response)
	requests := recorder.all()
	require.Len(t, requests, 1)
	assert.Equal(t, "/test-bucket/input.jsonl", requests[0].uri)
}

func TestBatchCreateInlineUploadHonorsS3EndpointOverride(t *testing.T) {
	clearEndpointEnv(t)
	s3Server, s3Recorder := newEndpointRecordingServer(t, true, "application/xml", "")
	controlServer, controlRecorder := newEndpointRecordingServer(t, true, "application/json", `{"jobArn":"arn:aws:bedrock:us-east-1:123456789012:model-invocation-job/test"}`)
	provider := newTestProviderWithBaseURL(t, "")
	key := testBedrockKey()
	key.Value = schemas.SecretVar{}
	key.BedrockKeyConfig.AccessKey = *schemas.NewSecretVar("AKIAIOSFODNN7EXAMPLE")
	key.BedrockKeyConfig.SecretKey = *schemas.NewSecretVar("wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY")
	key.BedrockKeyConfig.Endpoints = &schemas.BedrockEndpoints{
		S3:           schemas.NewSecretVar(s3Server.URL),
		ControlPlane: schemas.NewSecretVar(controlServer.URL),
	}

	response, bifrostErr := provider.BatchCreate(testBedrockCtx(), key, &schemas.BifrostBatchCreateRequest{
		Model: schemas.Ptr("anthropic.claude-3-haiku-20240307-v1:0"),
		Requests: []schemas.BatchRequestItem{
			{CustomID: "request-1", Body: map[string]interface{}{"messages": []interface{}{}}},
		},
		ExtraParams: map[string]interface{}{
			"role_arn":      "arn:aws:iam::123456789012:role/batch",
			"output_s3_uri": "s3://test-bucket/output/",
		},
	})
	require.Nil(t, bifrostErr)
	require.NotNil(t, response)
	s3Requests := s3Recorder.all()
	require.Len(t, s3Requests, 1)
	assert.True(t, strings.HasPrefix(s3Requests[0].uri, "/test-bucket/bifrost-batch-input/"))
	assert.Contains(t, s3Requests[0].header.Get("Authorization"), "/us-east-1/s3/aws4_request")
	require.Len(t, controlRecorder.all(), 2)
}

func TestDialGuardBlocksPrivateEndpointOverride(t *testing.T) {
	clearEndpointEnv(t)
	provider := newTestProviderWithBaseURL(t, "")
	key := testBedrockKey()
	key.BedrockKeyConfig.Endpoints = &schemas.BedrockEndpoints{Runtime: schemas.NewSecretVar("10.255.255.1:9")}

	_, _, _, bifrostErr := provider.completeRequest(testBedrockCtx(), []byte(`{}`), "model-id/converse", key, "model-id")
	require.NotNil(t, bifrostErr)
	require.NotNil(t, bifrostErr.Error)
	require.NotNil(t, bifrostErr.Error.Error)
	assert.Contains(t, bifrostErr.Error.Error.Error(), "private IP")
}
