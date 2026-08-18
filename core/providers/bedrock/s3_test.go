package bedrock

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestConvertBedrockRequestsToJSONL_NoModelIDInModelInput guards against
// regressing the Bedrock batch bug where modelId was injected into each
// record's modelInput. Bedrock requires each JSONL line to be strictly
// {recordId, modelInput} with modelId only at the job level, otherwise it
// rejects records with "modelId: Extra inputs are not permitted".
func TestConvertBedrockRequestsToJSONL_NoModelIDInModelInput(t *testing.T) {
	modelID := "us.anthropic.claude-opus-4-6-v1"
	requests := []schemas.BatchRequestItem{
		{
			CustomID: "item-00043",
			Body: map[string]interface{}{
				"anthropic_version": "bedrock-2023-05-31",
				"max_tokens":        16,
				"messages": []map[string]interface{}{
					{"role": "user", "content": "Reply with the number 43."},
				},
				"model": modelID, // should be stripped, not leaked into modelInput
			},
		},
		{
			CustomID: "item-00044",
			Params: map[string]interface{}{
				"max_tokens": 8,
				"model":      modelID,
			},
		},
	}

	data, err := ConvertBedrockRequestsToJSONL(requests, &modelID)
	require.NoError(t, err)

	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	require.Len(t, lines, 2)

	for i, line := range lines {
		var record struct {
			RecordID   string                 `json:"recordId"`
			ModelInput map[string]interface{} `json:"modelInput"`
		}
		require.NoError(t, json.Unmarshal([]byte(line), &record), "line %d should be valid JSON", i)

		assert.NotEmpty(t, record.RecordID, "recordId should be set")

		// The core regression assertions: neither modelId nor model may appear
		// inside modelInput.
		_, hasModelID := record.ModelInput["modelId"]
		assert.False(t, hasModelID, "modelInput must not contain modelId (line %d)", i)
		_, hasModel := record.ModelInput["model"]
		assert.False(t, hasModel, "modelInput must not contain model (line %d)", i)
	}

	// First record's body should be carried through verbatim (minus model).
	var first struct {
		ModelInput map[string]interface{} `json:"modelInput"`
	}
	require.NoError(t, json.Unmarshal([]byte(lines[0]), &first))
	assert.Equal(t, "bedrock-2023-05-31", first.ModelInput["anthropic_version"])
	assert.Contains(t, first.ModelInput, "messages")
}

// TestConvertBedrockRequestsToJSONL_RequiresModelID confirms the job-level
// model is still mandatory.
func TestConvertBedrockRequestsToJSONL_RequiresModelID(t *testing.T) {
	requests := []schemas.BatchRequestItem{{CustomID: "item-1", Body: map[string]interface{}{"max_tokens": 16}}}

	_, err := ConvertBedrockRequestsToJSONL(requests, nil)
	assert.Error(t, err)

	empty := ""
	_, err = ConvertBedrockRequestsToJSONL(requests, &empty)
	assert.Error(t, err)
}

type recordedHTTPRequest struct {
	method string
	host   string
	path   string
}

type recordingRoundTripper struct {
	mu   sync.Mutex
	reqs []recordedHTTPRequest
	next http.RoundTripper
}

func (r *recordingRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	r.mu.Lock()
	r.reqs = append(r.reqs, recordedHTTPRequest{method: req.Method, host: req.URL.Host, path: req.URL.Path})
	r.mu.Unlock()
	return r.next.RoundTrip(req)
}

func (r *recordingRoundTripper) all() []recordedHTTPRequest {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]recordedHTTPRequest(nil), r.reqs...)
}

func newIMDSServer(t *testing.T) *httptest.Server {
	t.Helper()
	const credsJSON = `{
  "Code": "Success",
  "Type": "AWS-HMAC",
  "AccessKeyId": "ec2-access-key",
  "SecretAccessKey": "ec2-secret-key",
  "Token": "token",
  "Expiration": "2100-01-01T00:00:00Z",
  "LastUpdated": "2009-11-23T00:00:00Z"
}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/latest/api/token":
			w.Header().Set("X-Aws-Ec2-Metadata-Token-Ttl-Seconds", r.Header.Get("X-Aws-Ec2-Metadata-Token-Ttl-Seconds"))
			_, _ = w.Write([]byte("validToken"))
		case "/latest/meta-data/iam/security-credentials/":
			_, _ = w.Write([]byte("RoleName"))
		case "/latest/meta-data/iam/security-credentials/RoleName":
			_, _ = w.Write([]byte(credsJSON))
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

func newS3PutServer(t *testing.T) (*httptest.Server, *int) {
	t.Helper()
	var puts int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut {
			puts++
			_, _ = io.Copy(io.Discard, r.Body)
			w.Header().Set("ETag", `"test-etag"`)
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)
	return server, &puts
}

func isolateAWSDefaultChain(t *testing.T, imdsEndpoint string) {
	t.Helper()
	empty := filepath.Join(t.TempDir(), "aws")
	for _, key := range []string{
		"AWS_ACCESS_KEY_ID", "AWS_ACCESS_KEY",
		"AWS_SECRET_ACCESS_KEY", "AWS_SECRET_KEY",
		"AWS_SESSION_TOKEN",
		"AWS_PROFILE", "AWS_DEFAULT_PROFILE",
		"AWS_CONTAINER_CREDENTIALS_RELATIVE_URI",
		"AWS_CONTAINER_CREDENTIALS_FULL_URI",
		"AWS_WEB_IDENTITY_TOKEN_FILE",
		"AWS_ROLE_ARN",
		"AWS_EC2_METADATA_DISABLED",
	} {
		t.Setenv(key, "")
	}
	t.Setenv("AWS_CONFIG_FILE", empty+"-config")
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", empty+"-creds")
	t.Setenv("AWS_EC2_METADATA_SERVICE_ENDPOINT", imdsEndpoint)
}

func hasIMDSPath(reqs []recordedHTTPRequest) bool {
	for _, req := range reqs {
		if strings.Contains(req.path, "/latest/api/token") || strings.Contains(req.path, "/latest/meta-data/") {
			return true
		}
	}
	return false
}

func TestUploadToS3_EmptyStaticCredentialsDoesNotUseGuardedClientForIMDS(t *testing.T) {
	imdsServer := newIMDSServer(t)
	s3Server, puts := newS3PutServer(t)
	isolateAWSDefaultChain(t, imdsServer.URL)

	recorder := &recordingRoundTripper{next: http.DefaultTransport}
	guarded := &http.Client{Transport: recorder, Timeout: 10 * time.Second}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	err := uploadToS3(ctx, "", "", nil, "us-east-1", "batch-bucket", "bifrost-batch-input/job.jsonl", s3Server.URL, guarded, []byte(`{"recordId":"1"}`+"\n"))
	require.Nil(t, err)

	reqs := recorder.all()
	assert.False(t, hasIMDSPath(reqs), "IMDS credential fetches must not use the SSRF-guarded client: %+v", reqs)
	require.Greater(t, *puts, 0, "S3 PutObject should reach the custom endpoint")

	var sawS3 bool
	for _, req := range reqs {
		if req.method == http.MethodPut && strings.Contains(req.path, "/batch-bucket/") {
			sawS3 = true
			assert.Contains(t, req.path, "bifrost-batch-input/job.jsonl")
		}
	}
	assert.True(t, sawS3, "S3 PUT must use the guarded client with path-style addressing: %+v", reqs)
}

func TestUploadToS3_StaticCredentialsUsesGuardedClientAndPathStyle(t *testing.T) {
	s3Server, puts := newS3PutServer(t)

	recorder := &recordingRoundTripper{next: http.DefaultTransport}
	guarded := &http.Client{Transport: recorder, Timeout: 10 * time.Second}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	err := uploadToS3(ctx, "AKIAIOSFODNN7EXAMPLE", "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY", nil, "us-east-1", "batch-bucket", "bifrost-batch-input/job.jsonl", s3Server.URL, guarded, []byte(`{"recordId":"1"}`+"\n"))
	require.Nil(t, err)
	require.Greater(t, *puts, 0)

	reqs := recorder.all()
	assert.False(t, hasIMDSPath(reqs), "static credentials must not fetch IMDS through the guarded client: %+v", reqs)

	var sawS3 bool
	for _, req := range reqs {
		if req.method == http.MethodPut && strings.Contains(req.path, "/batch-bucket/bifrost-batch-input/job.jsonl") {
			sawS3 = true
		}
	}
	assert.True(t, sawS3, "static-credential uploads must keep custom endpoint path-style PUTs on the guarded client: %+v", reqs)
}
