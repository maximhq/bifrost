package bedrock

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
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

// regionTaggingTransport redirects every request to the test server, recording the host the
// provider built (which carries the key's region) so the handler can answer per region.
type regionTaggingTransport struct {
	target    *url.URL
	transport http.RoundTripper
}

func (r *regionTaggingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	cloned := req.Clone(req.Context())
	cloned.Header.Set("X-Test-Original-Host", req.URL.Host)
	cloned.URL.Scheme = r.target.Scheme
	cloned.URL.Host = r.target.Host
	cloned.Host = r.target.Host
	return r.transport.RoundTrip(cloned)
}

// newRegionalS3Provider serves the S3 bucket from bucketRegion: a key signed for that region
// gets 404 (the object does not exist), a key for any other region gets S3's 301 redirect
// with x-amz-bucket-region, as AWS answers when the regional endpoint is the wrong one.
func newRegionalS3Provider(t *testing.T, bucketRegion string) *BedrockProvider {
	t.Helper()
	return newRegionalS3ProviderAnswering(t, bucketRegion, http.StatusMovedPermanently)
}

// newRegionalS3ProviderAnswering is newRegionalS3Provider with the status S3 sends a key in another
// region: 301 for older regions, 400 for a bucket in a region launched after March 2019.
func newRegionalS3ProviderAnswering(t *testing.T, bucketRegion string, wrongRegionStatus int) *BedrockProvider {
	t.Helper()
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.Header.Get("X-Test-Original-Host"), ".s3."+bucketRegion+".") {
			w.Header().Set("x-amz-bucket-region", bucketRegion)
			w.WriteHeader(wrongRegionStatus)
			if r.Method != http.MethodHead {
				body := `<Error><Code>PermanentRedirect</Code><Message>The bucket you are attempting to access must be addressed using the specified endpoint.</Message></Error>`
				if wrongRegionStatus == http.StatusBadRequest {
					body = `<Error><Code>AuthorizationHeaderMalformed</Code><Message>The authorization header is malformed; the region is wrong.</Message></Error>`
				}
				_, _ = w.Write([]byte(body))
			}
			return
		}
		// S3 names the bucket's region on its own answers too; a matching region is the real answer.
		w.Header().Set("x-amz-bucket-region", bucketRegion)
		w.WriteHeader(http.StatusNotFound)
		if r.Method != http.MethodHead {
			_, _ = w.Write([]byte(`<Error><Code>NoSuchKey</Code><Message>The specified key does not exist.</Message></Error>`))
		}
	}))
	t.Cleanup(ts.Close)
	provider := newTestProviderWithServer(t, ts)
	target, err := url.Parse(ts.URL)
	require.NoError(t, err)
	provider.client = &http.Client{Transport: &regionTaggingTransport{target: target, transport: ts.Client().Transport}, Timeout: 5 * time.Second}
	return provider
}

func s3TestKey(region string) schemas.Key {
	return schemas.Key{
		Name: "key-" + region,
		BedrockKeyConfig: &schemas.BedrockKeyConfig{
			AccessKey: *schemas.NewSecretVar("AKIATESTKEY"),
			SecretKey: *schemas.NewSecretVar("test-secret"),
			Region:    schemas.NewSecretVar(region),
		},
	}
}

// s3FileOp runs one of the S3-backed file operations against fileID with keys.
func s3FileOp(t *testing.T, provider *BedrockProvider, op string, keys []schemas.Key, fileID string) *schemas.BifrostError {
	t.Helper()
	ctx := testBedrockCtx()
	var bifrostErr *schemas.BifrostError
	switch op {
	case "retrieve":
		_, bifrostErr = provider.FileRetrieve(ctx, keys, &schemas.BifrostFileRetrieveRequest{Provider: schemas.Bedrock, FileID: fileID})
	case "delete":
		_, bifrostErr = provider.FileDelete(ctx, keys, &schemas.BifrostFileDeleteRequest{Provider: schemas.Bedrock, FileID: fileID})
	case "content":
		_, bifrostErr = provider.FileContent(ctx, keys, &schemas.BifrostFileContentRequest{Provider: schemas.Bedrock, FileID: fileID})
	default:
		t.Fatalf("unknown op %q", op)
	}
	return bifrostErr
}

// TestS3FileOpsKeepTheBucketRegionAnswer guards the multi-key loop of the S3 file operations.
// File operations try every key, so a provider with a key in the bucket's region and a key in
// another region gets S3's real answer (404 here) from the first and a wrong-region 301 from the
// second. The loop kept the last error, so the caller saw "status 301" instead of the 404, and a
// 3xx is not a status a client can act on. Seen in the provider harness as
// "bedrock file retrieve: valid bucket, missing object reaches AWS" returning 301.
func TestS3FileOpsKeepTheBucketRegionAnswer(t *testing.T) {
	const fileID = "s3://bifrost-harness-bucket/no-such-object"
	for _, op := range []string{"retrieve", "delete", "content"} {
		t.Run(op+"/bucket region key first", func(t *testing.T) {
			provider := newRegionalS3Provider(t, "us-east-1")
			bifrostErr := s3FileOp(t, provider, op, []schemas.Key{s3TestKey("us-east-1"), s3TestKey("us-west-2")}, fileID)
			require.NotNil(t, bifrostErr)
			require.NotNil(t, bifrostErr.StatusCode, "no status on %+v", bifrostErr.Error)
			assert.Equal(t, http.StatusNotFound, *bifrostErr.StatusCode, "the wrong-region key's 301 replaced the bucket region's answer: %+v", bifrostErr.Error)
		})
		t.Run(op+"/bucket region key last", func(t *testing.T) {
			provider := newRegionalS3Provider(t, "us-east-1")
			bifrostErr := s3FileOp(t, provider, op, []schemas.Key{s3TestKey("us-west-2"), s3TestKey("us-east-1")}, fileID)
			require.NotNil(t, bifrostErr)
			require.NotNil(t, bifrostErr.StatusCode, "no status on %+v", bifrostErr.Error)
			assert.Equal(t, http.StatusNotFound, *bifrostErr.StatusCode)
		})
		// A bucket in a region launched after March 2019 answers a wrong-region request with 400, not a
		// redirect. That 400 is still the wrong key's answer, recognized by x-amz-bucket-region, and must
		// not replace the bucket region's 404.
		t.Run(op+"/new-region bucket answers other keys with 400", func(t *testing.T) {
			provider := newRegionalS3ProviderAnswering(t, "ap-southeast-5", http.StatusBadRequest)
			bifrostErr := s3FileOp(t, provider, op, []schemas.Key{s3TestKey("ap-southeast-5"), s3TestKey("us-east-1")}, fileID)
			require.NotNil(t, bifrostErr)
			require.NotNil(t, bifrostErr.StatusCode, "no status on %+v", bifrostErr.Error)
			assert.Equal(t, http.StatusNotFound, *bifrostErr.StatusCode, "the wrong-region key's 400 replaced the bucket region's answer: %+v", bifrostErr.Error)
		})
		t.Run(op+"/no key in the bucket region", func(t *testing.T) {
			provider := newRegionalS3Provider(t, "eu-west-1")
			bifrostErr := s3FileOp(t, provider, op, []schemas.Key{s3TestKey("us-east-1"), s3TestKey("us-west-2")}, fileID)
			require.NotNil(t, bifrostErr)
			require.NotNil(t, bifrostErr.StatusCode, "no status on %+v", bifrostErr.Error)
			assert.Equal(t, http.StatusBadRequest, *bifrostErr.StatusCode, "a 3xx must not reach the caller as the status")
			require.NotNil(t, bifrostErr.Error)
			assert.Contains(t, bifrostErr.Error.Message, "eu-west-1", "the error should name the bucket's region so the operator can add a key there")
		})
	}
}
