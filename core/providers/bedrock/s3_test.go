package bedrock

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
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

// A key that lists buckets in batch_s3_config serves file and batch operations for
// those buckets only, and within a bucket's prefix when one is set. A key that
// lists none keeps accepting whichever bucket the request names.
func TestKeysAllowingS3Object(t *testing.T) {
	open := schemas.Key{ID: "open", BedrockKeyConfig: &schemas.BedrockKeyConfig{}}
	noConfig := schemas.Key{ID: "no-config"}
	listed := schemas.Key{ID: "listed", BedrockKeyConfig: &schemas.BedrockKeyConfig{BatchS3Config: &schemas.BatchS3Config{Buckets: []schemas.S3BucketConfig{
		{BucketName: "team-batch", Prefix: "jobs/"},
		{BucketName: "team-files"},
	}}}}

	tests := []struct {
		name      string
		keys      []schemas.Key
		bucket    string
		objectKey string
		wantIDs   []string
	}{
		{name: "a key with no list accepts any bucket", keys: []schemas.Key{open, noConfig}, bucket: "anything", objectKey: "a/b", wantIDs: []string{"open", "no-config"}},
		{name: "a listed bucket is accepted", keys: []schemas.Key{listed}, bucket: "team-files", objectKey: "a/b.jsonl", wantIDs: []string{"listed"}},
		{name: "an unlisted bucket is refused", keys: []schemas.Key{listed}, bucket: "someone-elses", objectKey: "a/b.jsonl"},
		{name: "the bucket name must match exactly", keys: []schemas.Key{listed}, bucket: "team-files-private", objectKey: "a"},
		{name: "inside the configured prefix is accepted", keys: []schemas.Key{listed}, bucket: "team-batch", objectKey: "jobs/input.jsonl", wantIDs: []string{"listed"}},
		{name: "the prefix itself is accepted", keys: []schemas.Key{listed}, bucket: "team-batch", objectKey: "jobs/", wantIDs: []string{"listed"}},
		{name: "outside the configured prefix is refused", keys: []schemas.Key{listed}, bucket: "team-batch", objectKey: "secrets/key.pem"},
		{name: "a sibling of the prefix is refused", keys: []schemas.Key{listed}, bucket: "team-batch", objectKey: "jobs-archive/x"},
		{name: "path traversal out of the prefix is refused", keys: []schemas.Key{listed}, bucket: "team-batch", objectKey: "jobs/../secrets/key.pem"},
		{name: "the bucket root is refused when a prefix is set", keys: []schemas.Key{listed}, bucket: "team-batch", objectKey: ""},
		{name: "only the keys that list the bucket are used", keys: []schemas.Key{listed, open}, bucket: "someone-elses", objectKey: "a", wantIDs: []string{"open"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, bifrostErr := keysAllowingS3Object(tt.keys, tt.bucket, tt.objectKey)
			if len(tt.wantIDs) == 0 {
				if bifrostErr == nil {
					t.Fatalf("expected a refusal, got keys %v", got)
				}
				if bifrostErr.StatusCode == nil || *bifrostErr.StatusCode != 403 {
					t.Fatalf("expected status 403, got %v", bifrostErr.StatusCode)
				}
				return
			}
			if bifrostErr != nil {
				t.Fatalf("unexpected refusal: %v", bifrostErr.Error.Message)
			}
			ids := make([]string, 0, len(got))
			for _, k := range got {
				ids = append(ids, k.ID)
			}
			if strings.Join(ids, ",") != strings.Join(tt.wantIDs, ",") {
				t.Fatalf("keys = %v, want %v", ids, tt.wantIDs)
			}
		})
	}
}

// File and batch operations refuse a location outside the key's bucket list
// before any request is signed or sent.
func TestBedrockFileOperationsFollowTheKeyBucketList(t *testing.T) {
	provider := &BedrockProvider{}
	ctx := schemas.NewBifrostContext(t.Context(), schemas.NoDeadline)
	key := schemas.Key{ID: "listed", BedrockKeyConfig: &schemas.BedrockKeyConfig{
		BatchS3Config: &schemas.BatchS3Config{Buckets: []schemas.S3BucketConfig{{BucketName: "team-files", Prefix: "uploads/"}}},
	}}
	keys := []schemas.Key{key}
	requireForbidden := func(t *testing.T, bifrostErr *schemas.BifrostError) {
		t.Helper()
		require.NotNil(t, bifrostErr)
		require.NotNil(t, bifrostErr.StatusCode, "error: %+v", bifrostErr.Error)
		assert.Equal(t, http.StatusForbidden, *bifrostErr.StatusCode)
	}

	t.Run("retrieve", func(t *testing.T) {
		_, bifrostErr := provider.FileRetrieve(ctx, keys, &schemas.BifrostFileRetrieveRequest{Provider: schemas.Bedrock, FileID: "s3://other-bucket/uploads/a.jsonl"})
		requireForbidden(t, bifrostErr)
	})
	t.Run("content outside the prefix", func(t *testing.T) {
		_, bifrostErr := provider.FileContent(ctx, keys, &schemas.BifrostFileContentRequest{Provider: schemas.Bedrock, FileID: "s3://team-files/private/a.jsonl"})
		requireForbidden(t, bifrostErr)
	})
	t.Run("delete", func(t *testing.T) {
		_, bifrostErr := provider.FileDelete(ctx, keys, &schemas.BifrostFileDeleteRequest{Provider: schemas.Bedrock, FileID: "s3://other-bucket/uploads/a.jsonl"})
		requireForbidden(t, bifrostErr)
	})
	t.Run("list", func(t *testing.T) {
		_, bifrostErr := provider.FileList(ctx, keys, &schemas.BifrostFileListRequest{
			Provider:      schemas.Bedrock,
			StorageConfig: &schemas.FileStorageConfig{S3: &schemas.S3StorageConfig{Bucket: "other-bucket"}},
		})
		requireForbidden(t, bifrostErr)
	})
	t.Run("upload", func(t *testing.T) {
		_, bifrostErr := provider.FileUpload(ctx, key, &schemas.BifrostFileUploadRequest{
			Provider: schemas.Bedrock, Filename: "a.jsonl", File: []byte("{}"),
			StorageConfig: &schemas.FileStorageConfig{S3: &schemas.S3StorageConfig{Bucket: "other-bucket", Prefix: "uploads/"}},
		})
		requireForbidden(t, bifrostErr)
	})
}

// Batch locations follow the key's bucket list the way file operations do: the output
// folder in either spelling, the input Bifrost generates for an inline batch, and the
// manifest it reads back for a job's counts.
func TestBedrockBatchLocationsFollowTheKeyBucketList(t *testing.T) {
	listed := s3TestKey("us-east-1")
	listed.ID = "listed"
	listed.BedrockKeyConfig.BatchS3Config = &schemas.BatchS3Config{Buckets: []schemas.S3BucketConfig{
		{BucketName: "team-batch", Prefix: "jobs/"},
		{BucketName: "team-files"},
	}}
	open := s3TestKey("us-east-1")
	open.ID = "open"

	t.Run("an output folder is accepted with or without its trailing slash", func(t *testing.T) {
		for _, folder := range []string{"jobs", "jobs/", "jobs/out", "jobs/out/"} {
			_, bifrostErr := keysAllowingS3Folder([]schemas.Key{listed}, "team-batch", folder)
			assert.Nil(t, bifrostErr, "folder %q is inside the listed prefix", folder)
		}
		for _, folder := range []string{"jobs-archive", "jobsx/", "secrets/", ""} {
			_, bifrostErr := keysAllowingS3Folder([]schemas.Key{listed}, "team-batch", folder)
			require.NotNil(t, bifrostErr, "folder %q is outside the listed prefix", folder)
			assert.Equal(t, http.StatusForbidden, *bifrostErr.StatusCode)
		}
	})

	t.Run("generated inline input stays inside the prefix that covers the output", func(t *testing.T) {
		for _, output := range []string{"s3://team-batch/jobs/out/", "s3://team-batch/jobs/", "s3://team-batch/jobs"} {
			bucket, objectKey := batchInputS3Location(listed, output, "nightly")
			assert.Equal(t, "team-batch", bucket)
			assert.True(t, strings.HasPrefix(objectKey, "jobs/bifrost-batch-input/nightly-"), "output %q put the input at %q", output, objectKey)
			assert.True(t, keyAllowsS3Object(listed, bucket, objectKey), "the key must be allowed to write its own input at %q", objectKey)
		}
		// A listed bucket with no prefix, and a key with no list, keep the bucket-level folder.
		_, objectKey := batchInputS3Location(listed, "s3://team-files/out/", "nightly")
		assert.True(t, strings.HasPrefix(objectKey, "bifrost-batch-input/nightly-"), "got %q", objectKey)
		_, objectKey = batchInputS3Location(open, "s3://anything/out/", "nightly")
		assert.True(t, strings.HasPrefix(objectKey, "bifrost-batch-input/nightly-"), "got %q", objectKey)
	})

	t.Run("the manifest is read only from a location the key lists", func(t *testing.T) {
		var hits atomic.Int64
		ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			hits.Add(1)
			w.WriteHeader(http.StatusNotFound)
		}))
		t.Cleanup(ts.Close)
		provider := newTestProviderWithServer(t, ts)
		target, err := url.Parse(ts.URL)
		require.NoError(t, err)
		provider.client = &http.Client{Transport: &regionTaggingTransport{target: target, transport: ts.Client().Transport}, Timeout: 5 * time.Second}

		assert.Nil(t, provider.fetchBatchManifest(testBedrockCtx(), listed, "us-east-1", "s3://someone-elses/out/"))
		assert.EqualValues(t, 0, hits.Load(), "a manifest outside the bucket list was requested with the key's credentials")

		provider.fetchBatchManifest(testBedrockCtx(), listed, "us-east-1", "s3://team-batch/jobs/out/")
		assert.EqualValues(t, 1, hits.Load(), "a manifest inside the bucket list is still read")
	})

	t.Run("generated input inside the output folder is not read back as a result", func(t *testing.T) {
		bucket, objectKey := batchInputS3Location(listed, "s3://team-batch/jobs/", "nightly")
		assert.False(t, isBatchResultFile("s3://"+bucket+"/"+objectKey), "the generated input %q would be parsed as batch results", objectKey)
		assert.True(t, isBatchResultFile("s3://team-batch/jobs/abc123/nightly.jsonl.out"))
		assert.False(t, isBatchResultFile("s3://team-batch/jobs/abc123/manifest.json.out"))
		// An output location can itself sit under a folder of that name. Its results are results.
		assert.True(t, isBatchResultFile("s3://team-batch/jobs/bifrost-batch-input/results/abc123/nightly.jsonl.out"))
		assert.True(t, isBatchResultFile("s3://team-batch/jobs/bifrost-batch-input/nightly.jsonl.out"))
		assert.True(t, isBatchResultFile("s3://team-batch/jobs/bifrost-batch-input/results/part.jsonl"))
		// A bucket can carry that name too. The folder is matched in the object key, never the bucket.
		assert.True(t, isBatchResultFile("s3://bifrost-batch-input/part.jsonl"))
		assert.True(t, isBatchResultFile("s3://bifrost-batch-input/out/part.jsonl.out"))
		assert.False(t, isBatchResultFile("s3://bifrost-batch-input/jobs/bifrost-batch-input/nightly-1.jsonl"))
	})

	// BatchCreate accepts an output written without its trailing slash as the folder it names.
	// BatchResults lists that folder, so it lists it in the same form the create accepted.
	t.Run("results are listed from the output as a folder", func(t *testing.T) {
		for output, want := range map[string]string{
			"s3://team-batch/jobs":     "s3://team-batch/jobs/",
			"s3://team-batch/jobs/":    "s3://team-batch/jobs/",
			"s3://team-batch/jobs/out": "s3://team-batch/jobs/out/",
			"s3://team-batch":          "s3://team-batch",
			"s3://team-batch/":         "s3://team-batch/",
		} {
			assert.Equal(t, want, s3FolderURI(output), "output %q", output)
		}
		bucket, prefix := parseS3URI(s3FolderURI("s3://team-batch/jobs"))
		_, bifrostErr := keysAllowingS3Object([]schemas.Key{listed}, bucket, prefix)
		assert.Nil(t, bifrostErr, "the listing of an accepted output folder is allowed")
	})

	t.Run("keys that list no bucket are returned as they came", func(t *testing.T) {
		keys := []schemas.Key{open, {ID: "no-config"}}
		got, bifrostErr := keysAllowingS3Object(keys, "anything", "a/b")
		require.Nil(t, bifrostErr)
		require.Len(t, got, 2)
		assert.Same(t, &keys[0], &got[0], "the caller's slice is reused when nothing is filtered")
		allocs := testing.AllocsPerRun(100, func() {
			_, _ = keysAllowingS3Object(keys, "anything", "a/b")
		})
		assert.Zero(t, allocs, "a deployment that lists no buckets pays no allocation for the check")
	})
}

// The bucket list check runs on every file and batch operation. A deployment that lists no
// buckets pays a nil check per key; one that does pays a prefix comparison per entry.
func BenchmarkKeysAllowingS3Object(b *testing.B) {
	open := []schemas.Key{{ID: "a", BedrockKeyConfig: &schemas.BedrockKeyConfig{}}, {ID: "b"}}
	listed := []schemas.Key{{ID: "listed", BedrockKeyConfig: &schemas.BedrockKeyConfig{BatchS3Config: &schemas.BatchS3Config{Buckets: []schemas.S3BucketConfig{
		{BucketName: "team-batch", Prefix: "jobs/"},
	}}}}}
	b.Run("no bucket list", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			_, _ = keysAllowingS3Object(open, "anything", "a/b.jsonl")
		}
	})
	b.Run("listed bucket and prefix", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			_, _ = keysAllowingS3Object(listed, "team-batch", "jobs/input.jsonl")
		}
	})
}
