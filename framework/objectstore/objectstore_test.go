package objectstore

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/bytedance/sonic"
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
