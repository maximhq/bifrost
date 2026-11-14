package objectstore

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/schemas"
)

func TestGzipRoundTrip(t *testing.T) {
	original := []byte(`{"input_history":"[{\"role\":\"user\",\"content\":\"hello world\"}]","output_message":"{\"role\":\"assistant\",\"content\":\"hi there\"}"}`)
	compressed, err := gzipCompress(original)
	if err != nil {
		t.Fatalf("gzipCompress: %v", err)
	}
	if len(compressed) >= len(original) {
		// For very small inputs gzip may be larger; just verify round-trip.
		t.Logf("compressed (%d) >= original (%d), but checking round-trip", len(compressed), len(original))
	}
	decompressed, err := gzipDecompress(compressed)
	if err != nil {
		t.Fatalf("gzipDecompress: %v", err)
	}
	if !bytes.Equal(original, decompressed) {
		t.Fatalf("round-trip mismatch: got %q, want %q", decompressed, original)
	}
}

func TestGzipDecompress_NonGzipData(t *testing.T) {
	// gzipDecompress should return error for non-gzip data.
	_, err := gzipDecompress([]byte("not gzip"))
	if err == nil {
		t.Fatal("expected error for non-gzip data")
	}
}

func TestEncodeTags(t *testing.T) {
	tags := map[string]string{
		"provider": "anthropic",
		"model":    "claude-3",
	}
	encoded := encodeTags(tags)
	// URL-encoded tags, order may vary.
	if encoded == "" {
		t.Fatal("expected non-empty encoded tags")
	}
	// Verify both tags are present.
	if !bytes.Contains([]byte(encoded), []byte("provider=anthropic")) {
		t.Errorf("missing provider tag in %q", encoded)
	}
	if !bytes.Contains([]byte(encoded), []byte("model=claude-3")) {
		t.Errorf("missing model tag in %q", encoded)
	}
}

func TestInMemoryObjectStore(t *testing.T) {
	ctx := context.Background()
	store := NewInMemoryObjectStore()

	// Put
	if err := store.Put(ctx, "key1", []byte("data1"), map[string]string{"tag": "val"}); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if store.Len() != 1 {
		t.Fatalf("Len: got %d, want 1", store.Len())
	}

	// Get
	data, err := store.Get(ctx, "key1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !bytes.Equal(data, []byte("data1")) {
		t.Fatalf("Get: got %q, want %q", data, "data1")
	}

	// GetTags
	tags := store.GetTags("key1")
	if tags["tag"] != "val" {
		t.Fatalf("GetTags: got %v", tags)
	}

	// Get missing key
	_, err = store.Get(ctx, "missing")
	if err == nil {
		t.Fatal("expected error for missing key")
	}

	// Delete
	if err := store.Delete(ctx, "key1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if store.Len() != 0 {
		t.Fatalf("Len after delete: got %d, want 0", store.Len())
	}

	// DeleteBatch
	_ = store.Put(ctx, "a", []byte("1"), nil)
	_ = store.Put(ctx, "b", []byte("2"), nil)
	_ = store.Put(ctx, "c", []byte("3"), nil)
	if err := store.DeleteBatch(ctx, []string{"a", "c"}); err != nil {
		t.Fatalf("DeleteBatch: %v", err)
	}
	if store.Len() != 1 {
		t.Fatalf("Len after batch delete: got %d, want 1", store.Len())
	}

	// ListByPrefix
	_ = store.Put(ctx, "prefix/one", []byte("1"), nil)
	_ = store.Put(ctx, "prefix/two", []byte("2"), nil)
	_ = store.Put(ctx, "other/three", []byte("3"), nil)
	objects, err := store.ListByPrefix(ctx, "prefix/")
	if err != nil {
		t.Fatalf("ListByPrefix: %v", err)
	}
	if len(objects) != 2 {
		t.Fatalf("ListByPrefix got %d objects, want 2: %v", len(objects), objects)
	}
	gotKeys := []string{objects[0].Key, objects[1].Key}
	sort.Strings(gotKeys)
	wantKeys := []string{"prefix/one", "prefix/two"}
	if gotKeys[0] != wantKeys[0] || gotKeys[1] != wantKeys[1] {
		t.Fatalf("ListByPrefix keys: got %v, want %v", gotKeys, wantKeys)
	}

	// Ping and Close
	if err := store.Ping(ctx); err != nil {
		t.Fatalf("Ping: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestInMemoryObjectStore_SimulateErrors(t *testing.T) {
	ctx := context.Background()
	store := NewInMemoryObjectStore()

	store.PutErr = fmt.Errorf("simulated put error")
	if err := store.Put(ctx, "key", []byte("data"), nil); err == nil {
		t.Fatal("expected error from Put")
	}
	store.PutErr = nil

	if err := store.Put(ctx, "key", []byte("data"), nil); err != nil {
		t.Fatalf("Put: %v", err)
	}
	store.GetErr = fmt.Errorf("simulated get error")
	if _, err := store.Get(ctx, "key"); err == nil {
		t.Fatal("expected error from Get")
	}
}

func TestConfigGetPrefix(t *testing.T) {
	c := &Config{Prefix: "custom"}
	if got := c.GetPrefix(); got != "custom" {
		t.Fatalf("GetPrefix: got %q, want %q", got, "custom")
	}
	c2 := &Config{}
	if got := c2.GetPrefix(); got != "bifrost" {
		t.Fatalf("GetPrefix default: got %q, want %q", got, "bifrost")
	}
}

func TestConfigResolvedFormatAndCompression(t *testing.T) {
	cases := []struct {
		name            string
		cfg             Config
		wantFormat      string
		wantCompression string
		wantGzipObjects bool
		wantErr         string
	}{
		{name: "defaults", cfg: Config{Type: StoreTypeS3}, wantFormat: FormatJSON, wantCompression: CompressionNone},
		{name: "legacy compress", cfg: Config{Type: StoreTypeS3, Compress: true}, wantFormat: FormatJSON, wantCompression: CompressionGzip, wantGzipObjects: true},
		{name: "json gzip", cfg: Config{Type: StoreTypeS3, Format: FormatJSON, Compression: CompressionGzip}, wantFormat: FormatJSON, wantCompression: CompressionGzip, wantGzipObjects: true},
		{name: "explicit none beats legacy compress", cfg: Config{Type: StoreTypeS3, Compress: true, Compression: CompressionNone}, wantFormat: FormatJSON, wantCompression: CompressionNone},
		{name: "json snappy rejected", cfg: Config{Type: StoreTypeS3, Compression: CompressionSnappy}, wantErr: "not supported for json"},
		{name: "json zstd rejected", cfg: Config{Type: StoreTypeS3, Compression: CompressionZstd}, wantErr: "not supported for json"},
		{name: "parquet default snappy", cfg: Config{Type: StoreTypeS3, Format: FormatParquet}, wantFormat: FormatParquet, wantCompression: CompressionSnappy},
		{name: "parquet legacy compress means gzip pages", cfg: Config{Type: StoreTypeS3, Format: FormatParquet, Compress: true}, wantFormat: FormatParquet, wantCompression: CompressionGzip, wantGzipObjects: true},
		{name: "parquet zstd", cfg: Config{Type: StoreTypeS3, Format: FormatParquet, Compression: CompressionZstd}, wantFormat: FormatParquet, wantCompression: CompressionZstd},
		{name: "parquet gzip does not gzip whole objects", cfg: Config{Type: StoreTypeS3, Format: FormatParquet, Compression: CompressionGzip}, wantFormat: FormatParquet, wantCompression: CompressionGzip},
		{name: "parquet none", cfg: Config{Type: StoreTypeS3, Format: FormatParquet, Compression: CompressionNone}, wantFormat: FormatParquet, wantCompression: CompressionNone},
		{name: "parquet on gcs rejected", cfg: Config{Type: StoreTypeGCS, Format: FormatParquet}, wantErr: "not supported for gcs"},
		{name: "unknown format", cfg: Config{Type: StoreTypeS3, Format: "avro"}, wantErr: "unsupported format"},
		{name: "unknown parquet codec", cfg: Config{Type: StoreTypeS3, Format: FormatParquet, Compression: "lz4"}, wantErr: "unsupported compression"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.cfg.Validate()
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("Validate: got %v, want error containing %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Validate: %v", err)
			}
			if got := tc.cfg.ResolvedFormat(); got != tc.wantFormat {
				t.Errorf("ResolvedFormat: got %q, want %q", got, tc.wantFormat)
			}
			if got := tc.cfg.ResolvedCompression(); got != tc.wantCompression {
				t.Errorf("ResolvedCompression: got %q, want %q", got, tc.wantCompression)
			}
			if got := tc.cfg.gzipObjects(); got != tc.wantGzipObjects {
				t.Errorf("gzipObjects: got %v, want %v", got, tc.wantGzipObjects)
			}
		})
	}
}

func TestNewObjectStoreRejectsInvalidFormat(t *testing.T) {
	_, err := NewObjectStore(context.Background(), &Config{Type: StoreTypeGCS, Format: FormatParquet}, nil)
	if err == nil || !strings.Contains(err.Error(), "parquet") {
		t.Fatalf("NewObjectStore: got %v, want parquet/gcs validation error", err)
	}
}

func TestParquetRoundTrip(t *testing.T) {
	original := []byte(`{"input_history":"[{\"role\":\"user\",\"content\":\"<b>hi</b> & \u00e9\"}]","nested":{"a":[1,2,{"b":null}]},"num":42.5,"flag":true,"missing":null,"empty":"","list":["x","y"]}`)
	for _, codec := range []string{CompressionNone, CompressionGzip, CompressionSnappy, CompressionZstd} {
		t.Run(codec, func(t *testing.T) {
			encoded, err := EncodeJSONObjectToParquet(original, codec)
			if err != nil {
				t.Fatalf("encode: %v", err)
			}
			if !IsParquet(encoded) {
				t.Fatalf("encoded bytes do not carry the parquet magic")
			}
			decoded, err := DecodeParquetToJSONObject(encoded)
			if err != nil {
				t.Fatalf("decode: %v", err)
			}
			var want, got map[string]any
			if err := sonic.Unmarshal(original, &want); err != nil {
				t.Fatal(err)
			}
			if err := sonic.Unmarshal(decoded, &got); err != nil {
				t.Fatalf("decoded is not JSON: %v\n%s", err, decoded)
			}
			// DeepEqual, not fmt.Sprint: a number or bool decoded back as a
			// string prints the same but is a different JSON value.
			if !reflect.DeepEqual(want, got) {
				t.Fatalf("round-trip mismatch:\n got  %s\n want %s", decoded, original)
			}
			if _, ok := got["missing"]; !ok {
				t.Fatalf("null field dropped: %s", decoded)
			}
			// Keys come back sorted so the output is byte-stable.
			again, err := DecodeParquetToJSONObject(encoded)
			if err != nil || !bytes.Equal(decoded, again) {
				t.Fatalf("decode not deterministic: %s vs %s (%v)", decoded, again, err)
			}
			if !strings.HasPrefix(string(decoded), `{"empty":`) {
				t.Fatalf("keys not sorted: %s", decoded)
			}
		})
	}
}

func TestParquetStringPayloadRoundTripsExactly(t *testing.T) {
	// The request-log payload is a map[string]string: it must round-trip to
	// the same map, with strings stored unquoted in the parquet columns.
	in := map[string]string{"input_history": `[{"role":"user"}]`, "provider": "openai", "raw_request": ""}
	data, err := sonic.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := EncodeJSONObjectToParquet(data, CompressionSnappy)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if !bytes.Contains(encoded, []byte("bifrost.schema_version")) {
		t.Fatalf("schema version metadata missing")
	}
	if bytes.Contains(encoded, []byte("bifrost.json_columns")) {
		t.Fatalf("string-only payload should not list json columns")
	}
	decoded, err := DecodeParquetToJSONObject(encoded)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	var out map[string]string
	if err := sonic.Unmarshal(decoded, &out); err != nil {
		t.Fatalf("decoded is not a string map: %v (%s)", err, decoded)
	}
	if fmt.Sprint(in) != fmt.Sprint(out) {
		t.Fatalf("got %v, want %v", out, in)
	}
}

func TestParquetOmitsValueStatistics(t *testing.T) {
	// For a one-row file, min/max bounds equal the values themselves. Writing
	// them copies every payload field into page headers and the footer, which
	// made a 26KB payload a 138KB file. The file must stay smaller than the
	// JSON it encodes, and must not repeat a value.
	m := make(map[string]string, 40)
	for i := 0; i < 40; i++ {
		m[fmt.Sprintf("field_%02d", i)] = strings.Repeat(fmt.Sprintf(`{"role":"user","content":"message %d"}`, i), 14)
	}
	m["secret_marker"] = "UNIQUE-VALUE-0123456789"
	data, err := sonic.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := EncodeJSONObjectToParquet(data, CompressionNone)
	if err != nil {
		t.Fatal(err)
	}
	if n := bytes.Count(encoded, []byte("UNIQUE-VALUE-0123456789")); n != 1 {
		t.Fatalf("value stored %d times, want 1 (statistics leaked into metadata)", n)
	}
	snappy, err := EncodeJSONObjectToParquet(data, CompressionSnappy)
	if err != nil {
		t.Fatal(err)
	}
	if len(snappy) >= len(data) {
		t.Fatalf("parquet file (%d bytes) not smaller than its JSON (%d bytes)", len(snappy), len(data))
	}
}

func TestParquetEncodeRejectsNonObjects(t *testing.T) {
	for _, in := range []string{`[]`, `"x"`, `{}`, `not json`, `{"":"x"}`} {
		if _, err := EncodeJSONObjectToParquet([]byte(in), CompressionNone); err == nil {
			t.Errorf("encode %s: expected error", in)
		}
	}
	if _, err := EncodeJSONObjectToParquet([]byte(`{"a":"b"}`), "lz4"); err == nil {
		t.Error("expected error for unsupported codec")
	}
}

func TestIsParquet(t *testing.T) {
	if IsParquet([]byte(`{"a":"b"}`)) || IsParquet([]byte("PAR1")) || IsParquet(nil) {
		t.Fatal("IsParquet true for non-parquet data")
	}
	encoded, err := EncodeJSONObjectToParquet([]byte(`{"a":"b"}`), CompressionNone)
	if err != nil {
		t.Fatal(err)
	}
	if !IsParquet(encoded) {
		t.Fatal("IsParquet false for parquet data")
	}
	if _, err := DecodeParquetToJSONObject([]byte(`{"a":"b"}`)); err == nil {
		t.Fatal("decode of JSON bytes should fail")
	}
}

func TestBuildPutObjectInput(t *testing.T) {
	data := []byte(`{"a":"b"}`)
	t.Run("json gzip", func(t *testing.T) {
		in, err := buildPutObjectInput("bkt", "k.json.gz", data, map[string]string{"provider": "openai"}, true, "")
		if err != nil {
			t.Fatal(err)
		}
		if aws.ToString(in.ContentType) != ContentTypeJSON || aws.ToString(in.ContentEncoding) != "gzip" {
			t.Fatalf("got content-type %q encoding %q", aws.ToString(in.ContentType), aws.ToString(in.ContentEncoding))
		}
		if aws.ToString(in.Tagging) != "provider=openai" {
			t.Fatalf("tagging: %q", aws.ToString(in.Tagging))
		}
		body, _ := io.ReadAll(in.Body)
		if plain, err := gzipDecompress(body); err != nil || !bytes.Equal(plain, data) {
			t.Fatalf("body not gzip of input: %v", err)
		}
	})
	t.Run("parquet skips gzip", func(t *testing.T) {
		encoded, err := EncodeJSONObjectToParquet(data, CompressionSnappy)
		if err != nil {
			t.Fatal(err)
		}
		in, err := buildPutObjectInput("bkt", "k.parquet", encoded, nil, false, ContentTypeParquet)
		if err != nil {
			t.Fatal(err)
		}
		if aws.ToString(in.ContentType) != ContentTypeParquet || in.ContentEncoding != nil || in.Tagging != nil {
			t.Fatalf("got content-type %q encoding %v tagging %v", aws.ToString(in.ContentType), in.ContentEncoding, in.Tagging)
		}
		body, _ := io.ReadAll(in.Body)
		if !bytes.Equal(body, encoded) {
			t.Fatal("parquet body was altered")
		}
	})
}

func TestS3JSONObjectsFollowCompression(t *testing.T) {
	// json objects keep the .json.gz key suffix either way; only
	// compression "gzip" (or legacy compress) gzips the body and sets
	// Content-Encoding. compression "none" stores plain JSON.
	data := []byte(`{"a":"b"}`)
	cases := []struct {
		name     string
		cfg      Config
		wantGzip bool
	}{
		{name: "none", cfg: Config{Type: StoreTypeS3, Format: FormatJSON, Compression: CompressionNone}},
		{name: "default", cfg: Config{Type: StoreTypeS3}},
		{name: "gzip", cfg: Config{Type: StoreTypeS3, Format: FormatJSON, Compression: CompressionGzip}, wantGzip: true},
		{name: "legacy compress", cfg: Config{Type: StoreTypeS3, Compress: true}, wantGzip: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := &S3ObjectStore{bucket: "bkt", compress: tc.cfg.gzipObjects()}
			in, err := buildPutObjectInput(s.bucket, "p/logs/2026/10/13/18/id.json.gz", data, nil, s.gzipBody(PutOptions{}), "")
			if err != nil {
				t.Fatal(err)
			}
			if aws.ToString(in.ContentType) != ContentTypeJSON {
				t.Fatalf("content type %q", aws.ToString(in.ContentType))
			}
			body, _ := io.ReadAll(in.Body)
			if tc.wantGzip {
				if aws.ToString(in.ContentEncoding) != "gzip" {
					t.Fatalf("want Content-Encoding gzip, got %v", in.ContentEncoding)
				}
				if plain, err := gzipDecompress(body); err != nil || !bytes.Equal(plain, data) {
					t.Fatalf("body is not gzip of the payload: %v", err)
				}
				return
			}
			if in.ContentEncoding != nil {
				t.Fatalf("compression none must not set Content-Encoding, got %q", aws.ToString(in.ContentEncoding))
			}
			if !bytes.Equal(body, data) {
				t.Fatalf("compression none must store plain JSON, got %q", body)
			}
		})
	}
}

func TestS3PutWithOptionsSkipCompression(t *testing.T) {
	// The S3 store gzips plain Puts when configured to; SkipCompression must
	// bypass that so parquet bodies stay readable by parquet tooling.
	gzipping := &S3ObjectStore{compress: true}
	if !gzipping.gzipBody(PutOptions{}) {
		t.Fatal("plain put on a compressing store should gzip")
	}
	if gzipping.gzipBody(PutOptions{SkipCompression: true}) {
		t.Fatal("SkipCompression ignored")
	}
	if (&S3ObjectStore{}).gzipBody(PutOptions{}) {
		t.Fatal("non-compressing store gzipped")
	}
}

func TestInMemoryObjectStoreContentType(t *testing.T) {
	ctx := context.Background()
	store := NewInMemoryObjectStore()
	if err := store.Put(ctx, "a", []byte(`{}`), nil); err != nil {
		t.Fatal(err)
	}
	if err := PutWith(ctx, store, "b", []byte("PAR1...."), nil, PutOptions{ContentType: ContentTypeParquet, SkipCompression: true}); err != nil {
		t.Fatal(err)
	}
	if got := store.ContentType("a"); got != ContentTypeJSON {
		t.Fatalf("a: got %q", got)
	}
	if got := store.ContentType("b"); got != ContentTypeParquet {
		t.Fatalf("b: got %q", got)
	}
	if err := store.Delete(ctx, "b"); err != nil {
		t.Fatal(err)
	}
	if got := store.ContentType("b"); got != "" {
		t.Fatalf("content type kept after delete: %q", got)
	}
}

// putOnlyStore implements ObjectStore without OptionsPutter.
type putOnlyStore struct {
	ObjectStore
	puts int
}

func (p *putOnlyStore) Put(context.Context, string, []byte, map[string]string) error {
	p.puts++
	return nil
}

func TestPutWithFallsBackToPut(t *testing.T) {
	store := &putOnlyStore{}
	if err := PutWith(context.Background(), store, "k", []byte("x"), nil, PutOptions{ContentType: ContentTypeParquet}); err != nil {
		t.Fatal(err)
	}
	if store.puts != 1 {
		t.Fatalf("Put not called: %d", store.puts)
	}
}

// benchmarkLogPayload builds a request-log shaped payload: ~40 string fields,
// ~20KB total, matching what the hybrid log store uploads per log.
func benchmarkLogPayload(b *testing.B) []byte {
	b.Helper()
	m := make(map[string]string, 40)
	for i := 0; i < 40; i++ {
		m[fmt.Sprintf("field_%02d", i)] = strings.Repeat(fmt.Sprintf(`{"role":"user","content":"message %d"}`, i), 14)
	}
	data, err := sonic.Marshal(m)
	if err != nil {
		b.Fatal(err)
	}
	return data
}

func BenchmarkEncodeJSONObjectToParquet(b *testing.B) {
	data := benchmarkLogPayload(b)
	b.ReportAllocs()
	b.SetBytes(int64(len(data)))
	for b.Loop() {
		if _, err := EncodeJSONObjectToParquet(data, CompressionSnappy); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkDecodeParquetToJSONObject(b *testing.B) {
	data := benchmarkLogPayload(b)
	encoded, err := EncodeJSONObjectToParquet(data, CompressionSnappy)
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.SetBytes(int64(len(data)))
	for b.Loop() {
		if _, err := DecodeParquetToJSONObject(encoded); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkGzipJSONBaseline is the json+gzip write path the parquet encoder is
// compared against.
func BenchmarkGzipJSONBaseline(b *testing.B) {
	data := benchmarkLogPayload(b)
	b.ReportAllocs()
	b.SetBytes(int64(len(data)))
	for b.Loop() {
		if _, err := gzipCompress(data); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkGunzipJSONBaseline is the json+gzip read path the parquet decoder is
// compared against.
func BenchmarkGunzipJSONBaseline(b *testing.B) {
	compressed, err := gzipCompress(benchmarkLogPayload(b))
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	for b.Loop() {
		if _, err := gzipDecompress(compressed); err != nil {
			b.Fatal(err)
		}
	}
}

// azureRESTFixture stores actual uploaded wire bytes and implements the Azure
// operations used by the store, including service errors and continuation pages.
// It deliberately does not echo request bodies back from the upload endpoint.
type azureRESTFixture struct {
	mu          sync.Mutex
	objects     map[string]azureRESTObject
	failures    map[string]string // method + space + key, or method + " *"
	requests    map[string]int
	listMarkers []string
	autoDecoded bool
	server      *httptest.Server
}

type azureRESTObject struct {
	body         []byte
	contentType  string
	encoding     string
	metadata     map[string]string
	lastModified time.Time
}

var azureRESTModified = time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC)

func newAzureRESTFixture(t testing.TB, cfg Config, autoDecompress bool) (*AzureObjectStore, *azureRESTFixture) {
	t.Helper()
	f := &azureRESTFixture{
		objects:  make(map[string]azureRESTObject),
		failures: make(map[string]string),
		requests: make(map[string]int),
	}
	f.server = httptest.NewServer(http.HandlerFunc(f.serveHTTP))
	t.Cleanup(f.server.Close)
	connectionString := "DefaultEndpointsProtocol=http;AccountName=testaccount;AccountKey=dGVzdGtleQ==;BlobEndpoint=" + f.server.URL
	cfg.Type = StoreTypeAzure
	cfg.Bucket = *schemas.NewSecretVar("logs")
	cfg.ConnectionString = schemas.NewSecretVar(connectionString)
	store, err := NewObjectStore(context.Background(), &cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	a := store.(*AzureObjectStore)
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.DisableCompression = !autoDecompress
	t.Cleanup(transport.CloseIdleConnections)
	a.client, err = azblob.NewClientFromConnectionString(connectionString, &azblob.ClientOptions{
		ClientOptions: policy.ClientOptions{
			Retry: policy.RetryOptions{MaxRetries: -1},
			Transport: azureHTTPTransport{
				base: transport,
				observe: func(response *http.Response) {
					f.mu.Lock()
					f.autoDecoded = f.autoDecoded || response.Uncompressed
					f.mu.Unlock()
				},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return a, f
}

type azureHTTPTransport struct {
	base    http.RoundTripper
	observe func(*http.Response)
}

func (t azureHTTPTransport) Do(request *http.Request) (*http.Response, error) {
	response, err := t.base.RoundTrip(request)
	if response != nil && t.observe != nil {
		t.observe(response)
	}
	return response, err
}

func azureRESTError(w http.ResponseWriter, code string) {
	status := http.StatusForbidden
	if code == "BlobNotFound" || code == "ContainerNotFound" {
		status = http.StatusNotFound
	}
	w.Header().Set("x-ms-error-code", code)
	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(status)
	fmt.Fprintf(w, "<Error><Code>%s</Code><Message>fixture error</Message></Error>", code)
}

func (f *azureRESTFixture) serveHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := strings.TrimPrefix(r.URL.Path, "/logs/")
	f.requests[r.Method+" "+key]++
	if r.URL.Path != "/logs" && !strings.HasPrefix(r.URL.Path, "/logs/") {
		azureRESTError(w, "ContainerNotFound")
		return
	}
	code := f.failures[r.Method+" "+key]
	if code == "" {
		code = f.failures[r.Method+" *"]
	}
	if code != "" {
		azureRESTError(w, code)
		return
	}
	w.Header().Set("x-ms-request-id", "fixture-request")
	w.Header().Set("ETag", `"fixture-etag"`)
	w.Header().Set("Last-Modified", azureRESTModified.Format(http.TimeFormat))
	if r.Method == http.MethodGet && r.URL.Query().Get("comp") == "list" {
		f.listMarkers = append(f.listMarkers, r.URL.Query().Get("marker"))
		prefix := r.URL.Query().Get("prefix")
		keys := make([]string, 0)
		for name := range f.objects {
			if strings.HasPrefix(name, prefix) {
				keys = append(keys, name)
			}
		}
		sort.Strings(keys)
		start := 0
		if marker := r.URL.Query().Get("marker"); marker != "" {
			start, _ = strconv.Atoi(marker)
		}
		var body bytes.Buffer
		body.WriteString("<EnumerationResults><Blobs>")
		next := ""
		if start < len(keys) {
			body.WriteString("<Blob><Name>")
			_ = xml.EscapeText(&body, []byte(keys[start]))
			fmt.Fprintf(&body, "</Name><Properties><Last-Modified>%s</Last-Modified><Etag>fixture</Etag><Content-Length>%d</Content-Length><BlobType>BlockBlob</BlobType></Properties></Blob>", f.objects[keys[start]].lastModified.Format(http.TimeFormat), len(f.objects[keys[start]].body))
			if start+1 < len(keys) {
				next = strconv.Itoa(start + 1)
			}
		}
		fmt.Fprintf(&body, "</Blobs><NextMarker>%s</NextMarker></EnumerationResults>", next)
		w.Header().Set("Content-Type", "application/xml")
		_, _ = w.Write(body.Bytes())
		return
	}
	switch r.Method {
	case http.MethodPut:
		if r.Header.Get("x-ms-blob-type") != "BlockBlob" {
			http.Error(w, "expected native block blob upload", http.StatusBadRequest)
			return
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		metadata := make(map[string]string)
		for name := range r.Header {
			if strings.HasPrefix(strings.ToLower(name), "x-ms-meta-") {
				metadata[strings.TrimPrefix(strings.ToLower(name), "x-ms-meta-")] = r.Header.Get(name)
			}
		}
		f.objects[key] = azureRESTObject{
			body: body, contentType: r.Header.Get("x-ms-blob-content-type"),
			encoding: r.Header.Get("x-ms-blob-content-encoding"), metadata: metadata,
			lastModified: azureRESTModified,
		}
		w.WriteHeader(http.StatusCreated)
	case http.MethodGet:
		object, ok := f.objects[key]
		if !ok {
			azureRESTError(w, "BlobNotFound")
			return
		}
		w.Header().Set("Content-Type", object.contentType)
		w.Header().Set("Content-Length", strconv.Itoa(len(object.body)))
		if object.encoding != "" {
			w.Header().Set("Content-Encoding", object.encoding)
		}
		_, _ = w.Write(object.body)
	case http.MethodDelete:
		if _, ok := f.objects[key]; !ok {
			azureRESTError(w, "BlobNotFound")
			return
		}
		delete(f.objects, key)
		w.WriteHeader(http.StatusAccepted)
	default:
		http.Error(w, "unexpected operation", http.StatusBadRequest)
	}
}

func TestAzureObjectStoreRoundTrips(t *testing.T) {
	for _, tc := range []struct {
		name           string
		cfg            Config
		autoDecompress bool
		wantGzip       bool
	}{
		{name: "none", cfg: Config{Compression: CompressionNone}},
		{name: "gzip raw response", cfg: Config{Compression: CompressionGzip}, wantGzip: true},
		{name: "gzip transport decoded", cfg: Config{Compression: CompressionGzip}, autoDecompress: true, wantGzip: true},
		{name: "legacy gzip", cfg: Config{Compress: true}, wantGzip: true},
		{name: "explicit none overrides legacy", cfg: Config{Compress: true, Compression: CompressionNone}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, fixture := newAzureRESTFixture(t, tc.cfg, tc.autoDecompress)
			ctx := context.Background()
			data := []byte(`{"message":"héllo 世界"}`)
			tags := map[string]string{"model": "模型\nline two + %", "provider": "Azure OpenAI"}
			if err := store.Put(ctx, "payload/log.json", data, tags); err != nil {
				t.Fatal(err)
			}
			fixture.mu.Lock()
			wire := fixture.objects["payload/log.json"]
			fixture.mu.Unlock()
			if wire.contentType != ContentTypeJSON || (wire.encoding == "gzip") != tc.wantGzip {
				t.Fatalf("unexpected upload headers: %+v", wire)
			}
			decodedWire := wire.body
			if tc.wantGzip {
				var err error
				decodedWire, err = gzipDecompress(wire.body)
				if err != nil {
					t.Fatal(err)
				}
			}
			if !bytes.Equal(decodedWire, data) {
				t.Fatalf("uploaded bytes mismatch: %q", decodedWire)
			}
			for key, value := range tags {
				decoded, err := url.QueryUnescape(wire.metadata[key])
				if err != nil || decoded != value || wire.metadata[key] != url.QueryEscape(value) {
					t.Fatalf("metadata %s: got %q (%v)", key, wire.metadata[key], err)
				}
			}
			got, err := store.Get(ctx, "payload/log.json")
			if err != nil || !bytes.Equal(got, data) {
				t.Fatalf("Get: %q, %v", got, err)
			}
			if tc.autoDecompress {
				fixture.mu.Lock()
				autoDecoded := fixture.autoDecoded
				fixture.mu.Unlock()
				if !autoDecoded {
					t.Fatal("transport did not exercise transparent gzip decompression")
				}
			}
			binary := []byte{0, 255, 1, 0, 128, '\n'}
			if err := store.Put(ctx, "payload/log.json", binary, nil); err != nil {
				t.Fatal(err)
			}
			got, err = store.Get(ctx, "payload/log.json")
			if err != nil || !bytes.Equal(got, binary) {
				t.Fatalf("overwrite/binary skills bytes: %v, %v", got, err)
			}
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestAzureObjectStoreParquetOptions(t *testing.T) {
	for _, codec := range []string{CompressionNone, CompressionGzip, CompressionSnappy, CompressionZstd} {
		t.Run(codec, func(t *testing.T) {
			store, fixture := newAzureRESTFixture(t, Config{Format: FormatParquet, Compression: codec, Compress: true}, false)
			original := []byte(`{"message":"héllo","nested":{"n":42}}`)
			data, err := EncodeJSONObjectToParquet(original, codec)
			if err != nil {
				t.Fatal(err)
			}
			if err := PutWith(context.Background(), store, "payload.parquet", data, nil, PutOptions{ContentType: ContentTypeParquet, SkipCompression: true}); err != nil {
				t.Fatal(err)
			}
			fixture.mu.Lock()
			wire := fixture.objects["payload.parquet"]
			fixture.mu.Unlock()
			if wire.contentType != ContentTypeParquet || wire.encoding != "" || !bytes.Equal(wire.body, data) {
				t.Fatalf("Parquet upload was changed or mislabeled: type=%s encoding=%s", wire.contentType, wire.encoding)
			}
			got, err := store.Get(context.Background(), "payload.parquet")
			if err != nil || !bytes.Equal(got, data) {
				t.Fatalf("Parquet Get changed bytes: %v", err)
			}
			decoded, err := DecodeParquetToJSONObject(got)
			if err != nil {
				t.Fatal(err)
			}
			var wantObject, gotObject map[string]any
			_ = sonic.Unmarshal(original, &wantObject)
			_ = sonic.Unmarshal(decoded, &gotObject)
			if !reflect.DeepEqual(wantObject, gotObject) {
				t.Fatalf("Parquet round trip mismatch: %s", decoded)
			}
		})
	}
}

func TestAzureObjectStoreListAndDelete(t *testing.T) {
	store, fixture := newAzureRESTFixture(t, Config{}, false)
	ctx := context.Background()
	for _, key := range []string{"prefix/a & b", "prefix/b", "other/c"} {
		if err := store.Put(ctx, key, []byte(key), nil); err != nil {
			t.Fatal(err)
		}
	}
	objects, err := store.ListByPrefix(ctx, "prefix/")
	if err != nil {
		t.Fatal(err)
	}
	want := []ObjectInfo{{Key: "prefix/a & b", LastModified: azureRESTModified}, {Key: "prefix/b", LastModified: azureRESTModified}}
	if len(objects) != len(want) {
		t.Fatalf("ListByPrefix: got %+v, want %+v", objects, want)
	}
	for i := range want {
		if objects[i].Key != want[i].Key || !objects[i].LastModified.Equal(want[i].LastModified) {
			t.Fatalf("ListByPrefix[%d]: got %+v, want %+v", i, objects[i], want[i])
		}
	}
	fixture.mu.Lock()
	markers := append([]string(nil), fixture.listMarkers...)
	fixture.failures["DELETE prefix/b"] = "AuthorizationPermissionMismatch"
	fixture.failures["DELETE denied"] = "AuthorizationPermissionMismatch"
	fixture.mu.Unlock()
	if !reflect.DeepEqual(markers, []string{"", "1"}) {
		t.Fatalf("pagination markers: %v", markers)
	}
	err = store.DeleteBatch(ctx, []string{"prefix/b", "missing", "denied", "prefix/a & b", "other/c"})
	if err == nil || !strings.Contains(err.Error(), "prefix/b") || !strings.Contains(err.Error(), "denied") {
		t.Fatalf("DeleteBatch should aggregate all failed keys: %v", err)
	}
	fixture.mu.Lock()
	remaining := len(fixture.objects)
	fixture.mu.Unlock()
	if remaining != 1 {
		t.Fatalf("DeleteBatch did not continue after failure: %d objects remain", remaining)
	}
	if err := store.Delete(ctx, "missing"); err != nil {
		t.Fatalf("missing delete not idempotent: %v", err)
	}
	if err := store.DeleteBatch(ctx, nil); err != nil {
		t.Fatal(err)
	}
	empty, err := store.ListByPrefix(ctx, "absent/")
	if err != nil || len(empty) != 0 {
		t.Fatalf("empty list: %v, %v", empty, err)
	}
}

func TestAzureObjectStoreErrorClassification(t *testing.T) {
	store, fixture := newAzureRESTFixture(t, Config{}, false)
	ctx := context.Background()
	if _, err := store.Get(ctx, "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing blob must wrap ErrNotFound: %v", err)
	}
	for _, code := range []string{"AuthorizationPermissionMismatch", "ContainerNotFound"} {
		t.Run(code, func(t *testing.T) {
			fixture.mu.Lock()
			fixture.failures["GET key"] = code
			fixture.failures["DELETE key"] = code
			fixture.mu.Unlock()
			if _, err := store.Get(ctx, "key"); err == nil || errors.Is(err, ErrNotFound) {
				t.Fatalf("service error classified as absence: %v", err)
			}
			if err := store.Delete(ctx, "key"); err == nil {
				t.Fatalf("service error swallowed by Delete: %s", code)
			}
		})
	}
	fixture.mu.Lock()
	fixture.objects["corrupt"] = azureRESTObject{body: []byte("not gzip"), encoding: "gzip", contentType: ContentTypeJSON}
	fixture.mu.Unlock()
	if _, err := store.Get(ctx, "corrupt"); err == nil || errors.Is(err, ErrNotFound) {
		t.Fatalf("corrupt gzip should fail, not imply absence: %v", err)
	}
	transportErr := errors.New("fixture transport failure")
	store.client, _ = azblob.NewClientWithNoCredential("https://fixture.invalid", &azblob.ClientOptions{
		ClientOptions: policy.ClientOptions{Retry: policy.RetryOptions{MaxRetries: -1}, Transport: azureFailingTransport{err: transportErr}},
	})
	if _, err := store.Get(ctx, "key"); !errors.Is(err, transportErr) || errors.Is(err, ErrNotFound) {
		t.Fatalf("transport error classification: %v", err)
	}
}

type azureFailingTransport struct{ err error }

func (t azureFailingTransport) Do(*http.Request) (*http.Response, error) {
	return nil, t.err
}

func TestAzureObjectStoreCancellation(t *testing.T) {
	store, fixture := newAzureRESTFixture(t, Config{Compress: true}, false)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	operations := []func() error{
		func() error { return store.Put(ctx, "key", []byte("body"), nil) },
		func() error { _, err := store.Get(ctx, "key"); return err },
		func() error { _, err := store.ListByPrefix(ctx, "prefix"); return err },
		func() error { return store.Delete(ctx, "key") },
		func() error { return store.DeleteBatch(ctx, []string{"key", "second"}) },
		func() error { return store.Ping(ctx) },
	}
	for i, operation := range operations {
		if err := operation(); !errors.Is(err, context.Canceled) {
			t.Errorf("operation %d: expected cancellation, got %v", i, err)
		}
	}
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	if len(fixture.requests) != 0 {
		t.Fatalf("cancelled operations reached server: %v", fixture.requests)
	}
}

func TestAzureObjectStorePing(t *testing.T) {
	for _, failure := range []string{"", http.MethodPut, http.MethodDelete} {
		t.Run("failure="+failure, func(t *testing.T) {
			store, fixture := newAzureRESTFixture(t, Config{}, false)
			if failure != "" {
				fixture.mu.Lock()
				fixture.failures[failure+" *"] = "AuthorizationPermissionMismatch"
				fixture.mu.Unlock()
			}
			err := store.Ping(context.Background())
			if (err != nil) != (failure != "") {
				t.Fatalf("Ping: %v", err)
			}
			if failure == "" {
				if err := store.Ping(context.Background()); err != nil {
					t.Fatal(err)
				}
			}
			fixture.mu.Lock()
			defer fixture.mu.Unlock()
			if failure != http.MethodDelete && len(fixture.objects) != 0 {
				t.Fatal("probe leaked an object")
			}
			putKeys := 0
			for request, count := range fixture.requests {
				if strings.HasPrefix(request, "PUT __bifrost_ping__/") {
					putKeys++
					if count != 1 {
						t.Fatalf("probe key reused or retried: %s (%d)", request, count)
					}
				}
				if failure == http.MethodPut && strings.HasPrefix(request, "DELETE ") {
					t.Fatal("probe deleted after unsuccessful upload")
				}
			}
			if failure == "" && putKeys != 2 {
				t.Fatalf("expected unique keys for two probes, got %d", putKeys)
			}
		})
	}
}

func TestAzureObjectStoreConfiguration(t *testing.T) {
	bucket := *schemas.NewSecretVar("logs")
	for _, tc := range []struct {
		name string
		cfg  *Config
		want string
	}{
		{name: "nil", want: "config is nil"},
		{name: "container required", cfg: &Config{}, want: "container"},
		{name: "endpoint required", cfg: &Config{Bucket: bucket}, want: "endpoint"},
		{name: "empty endpoint", cfg: &Config{Bucket: bucket, Endpoint: schemas.NewSecretVar(" ")}, want: "non-empty"},
		{name: "empty connection string", cfg: &Config{Bucket: bucket, ConnectionString: schemas.NewSecretVar("")}, want: "non-empty"},
		{name: "both auth forms", cfg: &Config{Bucket: bucket, ConnectionString: schemas.NewSecretVar("invalid"), Endpoint: schemas.NewSecretVar("https://example.invalid")}, want: "mutually exclusive"},
		{name: "invalid connection string", cfg: &Config{Bucket: bucket, ConnectionString: schemas.NewSecretVar("invalid")}, want: "create azure client"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewAzureObjectStore(context.Background(), tc.cfg, nil)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want %s", err, tc.want)
			}
		})
	}
}

// BenchmarkAzurePut measures the real upload path, including SDK request
// construction, metadata encoding, compression, and local HTTP transmission.
func BenchmarkAzurePut(b *testing.B) {
	data := benchmarkLogPayload(b)
	for _, compress := range []bool{false, true} {
		b.Run(fmt.Sprintf("gzip=%t", compress), func(b *testing.B) {
			store, _ := newAzureRESTFixture(b, Config{Compress: compress}, false)
			tags := map[string]string{"provider": "azure", "model": "model-name"}
			ctx := context.Background()
			b.ReportAllocs()
			b.SetBytes(int64(len(data)))
			b.ResetTimer()
			for b.Loop() {
				if err := store.Put(ctx, "benchmark/log.json", data, tags); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
