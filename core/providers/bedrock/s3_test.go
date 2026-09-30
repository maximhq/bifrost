package bedrock

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestUploadToS3_UsesConfiguredProfileOverAmbientCredentials(t *testing.T) {
	configureBedrockProfileTestCredentials(t)

	authHeaders := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authHeaders <- r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	t.Setenv("AWS_ENDPOINT_URL_S3", server.URL)
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:1")
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:1")
	t.Setenv("NO_PROXY", "127.0.0.1,localhost")

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	keyConfig := &schemas.BedrockKeyConfig{
		Profile: schemas.NewSecretVar("team-a"),
		Region:  schemas.NewSecretVar("us-east-1"),
	}
	if err := uploadToS3(ctx, keyConfig, "us-east-1", "test-bucket", "input.jsonl", []byte("{}\n")); err != nil {
		t.Fatalf("uploadToS3 failed: %s", err.Error.Message)
	}

	select {
	case authorization := <-authHeaders:
		assert.Contains(t, authorization, "Credential=PROFILE_A_ACCESS_KEY/")
		assert.NotContains(t, authorization, "AMBIENT_ACCESS_KEY")
	default:
		t.Fatal("S3 endpoint did not receive an upload")
	}
}

func TestUploadToS3_MissingProfileDoesNotUseAmbientCredentials(t *testing.T) {
	configureBedrockProfileTestCredentials(t)

	requests := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests <- struct{}{}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	t.Setenv("AWS_ENDPOINT_URL_S3", server.URL)
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:1")
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:1")
	t.Setenv("NO_PROXY", "127.0.0.1,localhost")

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	keyConfig := &schemas.BedrockKeyConfig{
		Profile: schemas.NewSecretVar("missing-team"),
		Region:  schemas.NewSecretVar("us-east-1"),
	}
	err := uploadToS3(ctx, keyConfig, "us-east-1", "test-bucket", "input.jsonl", []byte("{}\n"))
	require.NotNil(t, err)
	select {
	case <-requests:
		t.Fatal("S3 upload used ambient credentials after the selected profile was missing")
	default:
	}
}

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

// TestValidateS3Bucket_RejectsHostInjection pins the fix for caller-supplied s3://
// file IDs choosing the request host: the bucket is the leading label of
// "https://{bucket}.{s3host}/", so "s3://evil.example#/x" reached evil.example.
func TestValidateS3Bucket_RejectsHostInjection(t *testing.T) {
	valid := []string{
		"test-ai-dev",
		"my.bucket.name",
		"abc",
		strings.Repeat("a", 63),
	}
	for _, bucket := range valid {
		assert.Nil(t, validateS3Bucket(bucket), "bucket %q should be accepted", bucket)
	}

	invalid := []string{
		"", "ab", strings.Repeat("a", 64),
		"evil.example#",   // truncates the authority, picks the host
		"127.0.0.1:8080#", // the live-verified SSRF payload
		"bucket/../other", // path delimiter
		"bucket?x", "bucket@evil", "bucket:1", "bucket\\x",
		"Bucket", "-bucket", "bucket-",
		"bucket name", "bucket\nname",
	}
	for _, bucket := range invalid {
		err := validateS3Bucket(bucket)
		require.NotNil(t, err, "bucket %q should be rejected", bucket)
		require.NotNil(t, err.StatusCode)
		assert.Equal(t, 400, *err.StatusCode, "bucket %q should be a 400", bucket)
	}
}

// TestParseS3URIBucketFeedsValidator walks the caller-controlled shapes through
// parseS3URI the way the file routes do, so a parser change cannot silently
// reintroduce a host-bearing bucket.
func TestParseS3URIBucketFeedsValidator(t *testing.T) {
	for _, uri := range []string{
		"s3://127.0.0.1:8080#/x",
		"s3://evil.example#/x",
		"s3://evil.example#",
	} {
		bucket, _ := parseS3URI(uri)
		assert.NotNil(t, validateS3Bucket(bucket), "uri %q yielded accepted bucket %q", uri, bucket)
	}

	bucket, key := parseS3URI("s3://test-ai-dev/path/to/file.jsonl")
	assert.Equal(t, "test-ai-dev", bucket)
	assert.Equal(t, "path/to/file.jsonl", key)
	assert.Nil(t, validateS3Bucket(bucket))
}
