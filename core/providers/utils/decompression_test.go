package utils

import (
	"bytes"
	"compress/gzip"
	"compress/zlib"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"

	"github.com/andybalholm/brotli"
	"github.com/klauspost/compress/zstd"
)

const poolTestIterations = 10

var testPayload = []byte(`{"model":"gpt-4","messages":[{"role":"user","content":"hello world"}]}`)

// ---------------------------------------------------------------------------
// helpers — one compressor per encoding
// ---------------------------------------------------------------------------

func compressGzip(data []byte) []byte {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	if _, err := gz.Write(data); err != nil {
		panic(fmt.Errorf("gzip write: %w", err))
	}
	if err := gz.Close(); err != nil {
		panic(fmt.Errorf("gzip close: %w", err))
	}
	return buf.Bytes()
}

// compressFlate produces zlib-wrapped DEFLATE (RFC 1950) — the correct format
// for HTTP Content-Encoding "deflate" per RFC 9110 §8.4.1.2.
func compressFlate(data []byte) []byte {
	var buf bytes.Buffer
	w, err := zlib.NewWriterLevel(&buf, zlib.DefaultCompression)
	if err != nil {
		panic(fmt.Errorf("zlib new writer: %w", err))
	}
	if _, err := w.Write(data); err != nil {
		panic(fmt.Errorf("zlib write: %w", err))
	}
	if err := w.Close(); err != nil {
		panic(fmt.Errorf("zlib close: %w", err))
	}
	return buf.Bytes()
}

func compressBrotli(data []byte) []byte {
	var buf bytes.Buffer
	w := brotli.NewWriter(&buf)
	if _, err := w.Write(data); err != nil {
		panic(fmt.Errorf("brotli write: %w", err))
	}
	if err := w.Close(); err != nil {
		panic(fmt.Errorf("brotli close: %w", err))
	}
	return buf.Bytes()
}

func compressZstd(data []byte) []byte {
	var buf bytes.Buffer
	enc, err := zstd.NewWriter(&buf)
	if err != nil {
		panic(fmt.Errorf("zstd new writer: %w", err))
	}
	if _, err := enc.Write(data); err != nil {
		panic(fmt.Errorf("zstd write: %w", err))
	}
	if err := enc.Close(); err != nil {
		panic(fmt.Errorf("zstd close: %w", err))
	}
	return buf.Bytes()
}

// ---------------------------------------------------------------------------
// Pool cycle tests — each runs Acquire → ReadAll → Release N times to verify
// that pooled instances are reused correctly across iterations.
// ---------------------------------------------------------------------------

func TestAcquireReleaseGzipReader(t *testing.T) {
	compressed := compressGzip(testPayload)
	for i := 0; i < poolTestIterations; i++ {
		gz, err := AcquireGzipReader(bytes.NewReader(compressed))
		if err != nil {
			t.Fatalf("iteration %d: AcquireGzipReader error: %v", i, err)
		}
		got, err := io.ReadAll(gz)
		if err != nil {
			t.Fatalf("iteration %d: ReadAll error: %v", i, err)
		}
		if !bytes.Equal(got, testPayload) {
			t.Errorf("iteration %d: got %q, want %q", i, got, testPayload)
		}
		ReleaseGzipReader(gz)
	}
}

func TestAcquireReleaseFlateReader(t *testing.T) {
	compressed := compressFlate(testPayload)
	for i := 0; i < poolTestIterations; i++ {
		fr, err := AcquireFlateReader(bytes.NewReader(compressed))
		if err != nil {
			t.Fatalf("iteration %d: AcquireFlateReader error: %v", i, err)
		}
		got, err := io.ReadAll(fr)
		if err != nil {
			t.Fatalf("iteration %d: ReadAll error: %v", i, err)
		}
		if !bytes.Equal(got, testPayload) {
			t.Errorf("iteration %d: got %q, want %q", i, got, testPayload)
		}
		ReleaseFlateReader(fr)
	}
}

func TestAcquireReleaseBrotliReader(t *testing.T) {
	compressed := compressBrotli(testPayload)
	for i := 0; i < poolTestIterations; i++ {
		br := AcquireBrotliReader(bytes.NewReader(compressed))
		got, err := io.ReadAll(br)
		if err != nil {
			t.Fatalf("iteration %d: ReadAll error: %v", i, err)
		}
		if !bytes.Equal(got, testPayload) {
			t.Errorf("iteration %d: got %q, want %q", i, got, testPayload)
		}
		ReleaseBrotliReader(br)
	}
}

func TestAcquireReleaseZstdDecoder(t *testing.T) {
	compressed := compressZstd(testPayload)
	for i := 0; i < poolTestIterations; i++ {
		dec, err := AcquireZstdDecoder(bytes.NewReader(compressed))
		if err != nil {
			t.Fatalf("iteration %d: AcquireZstdDecoder error: %v", i, err)
		}
		got, err := io.ReadAll(dec)
		if err != nil {
			t.Fatalf("iteration %d: ReadAll error: %v", i, err)
		}
		if !bytes.Equal(got, testPayload) {
			t.Errorf("iteration %d: got %q, want %q", i, got, testPayload)
		}
		ReleaseZstdDecoder(dec)
	}
}

// ---------------------------------------------------------------------------
// Panic-safety tests — verify that corrupt or wrong-typed pooled instances
// are handled gracefully (no panics, fallback to fresh allocation).
//
// Each poison test drains the target pool first so stale values from earlier
// tests cannot interfere. sync.Pool.Get offers no ordering guarantee, so
// draining ensures the poisoned value is the only item available.
// ---------------------------------------------------------------------------

// drainPool removes all previously pooled values so the next Put/Get pair
// exercises the intended fallback path. Temporarily sets New to nil to ensure
// Get returns nil when the pool is empty.
func drainPool(p *sync.Pool) {
	origNew := p.New
	p.New = nil
	for p.Get() != nil {
	}
	p.New = origNew
}

// TestPool_WrongType_NoPanic poisons each pool with a wrong-typed value,
// then verifies Acquire still succeeds by falling back to a fresh allocation.
func TestPool_WrongType_NoPanic(t *testing.T) {
	wrongValue := "not a reader"
	compressed := compressGzip(testPayload)

	t.Run("gzip", func(t *testing.T) {
		drainPool(&gzipReaderPool)
		gzipReaderPool.Put(wrongValue)
		gz, err := AcquireGzipReader(bytes.NewReader(compressed))
		if err != nil {
			t.Fatalf("AcquireGzipReader should fall back, got error: %v", err)
		}
		got, _ := io.ReadAll(gz)
		if !bytes.Equal(got, testPayload) {
			t.Errorf("got %q, want %q", got, testPayload)
		}
		ReleaseGzipReader(gz)
	})

	t.Run("deflate", func(t *testing.T) {
		drainPool(&deflateReaderPool)
		deflateReaderPool.Put(wrongValue)
		fr, err := AcquireFlateReader(bytes.NewReader(compressFlate(testPayload)))
		if err != nil {
			t.Fatalf("AcquireFlateReader should fall back, got error: %v", err)
		}
		got, _ := io.ReadAll(fr)
		if !bytes.Equal(got, testPayload) {
			t.Errorf("got %q, want %q", got, testPayload)
		}
		ReleaseFlateReader(fr)
	})

	t.Run("brotli", func(t *testing.T) {
		drainPool(&brotliReaderPool)
		brotliReaderPool.Put(wrongValue)
		br := AcquireBrotliReader(bytes.NewReader(compressBrotli(testPayload)))
		got, err := io.ReadAll(br)
		if err != nil {
			t.Fatalf("ReadAll error: %v", err)
		}
		if !bytes.Equal(got, testPayload) {
			t.Errorf("got %q, want %q", got, testPayload)
		}
		ReleaseBrotliReader(br)
	})

	t.Run("zstd", func(t *testing.T) {
		drainPool(&zstdDecoderPool)
		zstdDecoderPool.Put(wrongValue)
		dec, err := AcquireZstdDecoder(bytes.NewReader(compressZstd(testPayload)))
		if err != nil {
			t.Fatalf("AcquireZstdDecoder should fall back, got error: %v", err)
		}
		got, _ := io.ReadAll(dec)
		if !bytes.Equal(got, testPayload) {
			t.Errorf("got %q, want %q", got, testPayload)
		}
		ReleaseZstdDecoder(dec)
	})
}

// TestPool_NilInPool_NoPanic puts an explicit nil into each pool, then
// verifies Acquire still succeeds by falling back to a fresh allocation.
func TestPool_NilInPool_NoPanic(t *testing.T) {
	t.Run("gzip", func(t *testing.T) {
		drainPool(&gzipReaderPool)
		gzipReaderPool.Put((*gzip.Reader)(nil))
		gz, err := AcquireGzipReader(bytes.NewReader(compressGzip(testPayload)))
		if err != nil {
			t.Fatalf("should fall back, got error: %v", err)
		}
		got, _ := io.ReadAll(gz)
		if !bytes.Equal(got, testPayload) {
			t.Errorf("got %q, want %q", got, testPayload)
		}
		ReleaseGzipReader(gz)
	})

	t.Run("zstd", func(t *testing.T) {
		drainPool(&zstdDecoderPool)
		zstdDecoderPool.Put((*zstd.Decoder)(nil))
		dec, err := AcquireZstdDecoder(bytes.NewReader(compressZstd(testPayload)))
		if err != nil {
			t.Fatalf("should fall back, got error: %v", err)
		}
		got, _ := io.ReadAll(dec)
		if !bytes.Equal(got, testPayload) {
			t.Errorf("got %q, want %q", got, testPayload)
		}
		ReleaseZstdDecoder(dec)
	})
}

// TestPool_InvalidData_NoPanic verifies that Acquire handles corrupt/invalid
// compressed data without panicking. The error should surface as a return
// value or a read error, never a panic.
func TestPool_InvalidData_NoPanic(t *testing.T) {
	garbage := []byte("this is not compressed data at all")

	t.Run("gzip", func(t *testing.T) {
		// gzip.NewReader validates the header immediately, so Acquire returns error.
		_, err := AcquireGzipReader(bytes.NewReader(garbage))
		if err == nil {
			t.Fatal("expected error for invalid gzip data")
		}
	})

	t.Run("deflate", func(t *testing.T) {
		// zlib.NewReader validates the header eagerly, so Acquire returns error.
		_, err := AcquireFlateReader(bytes.NewReader(garbage))
		if err == nil {
			t.Fatal("expected error for invalid deflate (zlib) data")
		}
	})

	t.Run("brotli", func(t *testing.T) {
		// brotli doesn't validate eagerly; error surfaces on Read.
		br := AcquireBrotliReader(bytes.NewReader(garbage))
		_, readErr := io.ReadAll(br)
		if readErr == nil {
			t.Fatal("expected read error for invalid brotli data")
		}
		ReleaseBrotliReader(br)
	})

	t.Run("zstd", func(t *testing.T) {
		// zstd.Decoder doesn't validate eagerly; error surfaces on Read.
		dec, err := AcquireZstdDecoder(bytes.NewReader(garbage))
		if err != nil {
			t.Fatalf("AcquireZstdDecoder should not error eagerly: %v", err)
		}
		_, readErr := io.ReadAll(dec)
		if readErr == nil {
			t.Fatal("expected read error for invalid zstd data")
		}
		ReleaseZstdDecoder(dec)
	})
}

// TestPool_CorruptPooledInstance_NoPanic simulates a corrupt pooled reader
// whose Reset panics. Verifies safeReset catches the panic and Acquire
// falls through to a fresh allocation.
func TestPool_CorruptPooledInstance_NoPanic(t *testing.T) {
	t.Run("safeReset_catches_panic", func(t *testing.T) {
		ok := safeReset(func() error {
			panic("simulated corrupt reader")
		})
		if ok {
			t.Fatal("safeReset should return false when resetFn panics")
		}
	})

	t.Run("gzip_after_corrupt", func(t *testing.T) {
		// Poison pool with a gzip.Reader that has corrupt internal state:
		// a zero-value reader that has been closed without ever being used.
		drainPool(&gzipReaderPool)
		corrupt := &gzip.Reader{}
		gzipReaderPool.Put(corrupt)

		// Acquire should recover, discard the corrupt reader, and create fresh.
		compressed := compressGzip(testPayload)
		gz, err := AcquireGzipReader(bytes.NewReader(compressed))
		if err != nil {
			t.Fatalf("should recover from corrupt pooled reader, got: %v", err)
		}
		got, _ := io.ReadAll(gz)
		if !bytes.Equal(got, testPayload) {
			t.Errorf("got %q, want %q", got, testPayload)
		}
		ReleaseGzipReader(gz)
	})
}

// TestRelease_Nil_NoPanic verifies Release functions are safe to call with nil.
func TestRelease_Nil_NoPanic(t *testing.T) {
	ReleaseGzipReader(nil)
	ReleaseFlateReader(nil)
	ReleaseBrotliReader(nil)
	ReleaseZstdDecoder(nil)
}

// TestPool_RecoveryAndReuse verifies that after a corrupt instance is discarded,
// the pool recovers and subsequent cycles work normally.
func TestPool_RecoveryAndReuse(t *testing.T) {
	// Drain then poison all pools with wrong types.
	drainPool(&gzipReaderPool)
	drainPool(&deflateReaderPool)
	drainPool(&brotliReaderPool)
	drainPool(&zstdDecoderPool)
	gzipReaderPool.Put("wrong")
	deflateReaderPool.Put(42)
	brotliReaderPool.Put(struct{}{})
	zstdDecoderPool.Put(false)

	// Run a normal Acquire → ReadAll → Release cycle for each.
	// This verifies the pool recovers: the wrong-typed value is discarded,
	// a fresh instance is created and released back, making the pool healthy.
	t.Run("gzip", func(t *testing.T) {
		compressed := compressGzip(testPayload)
		for i := 0; i < 3; i++ {
			gz, err := AcquireGzipReader(bytes.NewReader(compressed))
			if err != nil {
				t.Fatalf("iteration %d: %v", i, err)
			}
			got, _ := io.ReadAll(gz)
			if !bytes.Equal(got, testPayload) {
				t.Errorf("iteration %d: mismatch", i)
			}
			ReleaseGzipReader(gz)
		}
	})

	t.Run("deflate", func(t *testing.T) {
		compressed := compressFlate(testPayload)
		for i := 0; i < 3; i++ {
			fr, err := AcquireFlateReader(bytes.NewReader(compressed))
			if err != nil {
				t.Fatalf("iteration %d: %v", i, err)
			}
			got, _ := io.ReadAll(fr)
			if !bytes.Equal(got, testPayload) {
				t.Errorf("iteration %d: mismatch", i)
			}
			ReleaseFlateReader(fr)
		}
	})

	t.Run("brotli", func(t *testing.T) {
		compressed := compressBrotli(testPayload)
		for i := 0; i < 3; i++ {
			br := AcquireBrotliReader(bytes.NewReader(compressed))
			got, err := io.ReadAll(br)
			if err != nil {
				t.Fatalf("iteration %d: %v", i, err)
			}
			if !bytes.Equal(got, testPayload) {
				t.Errorf("iteration %d: mismatch", i)
			}
			ReleaseBrotliReader(br)
		}
	})

	t.Run("zstd", func(t *testing.T) {
		compressed := compressZstd(testPayload)
		for i := 0; i < 3; i++ {
			dec, err := AcquireZstdDecoder(bytes.NewReader(compressed))
			if err != nil {
				t.Fatalf("iteration %d: %v", i, err)
			}
			got, _ := io.ReadAll(dec)
			if !bytes.Equal(got, testPayload) {
				t.Errorf("iteration %d: mismatch", i)
			}
			ReleaseZstdDecoder(dec)
		}
	})
}

// TestPool_EmptyReader_NoPanic verifies Acquire handles an empty reader
// (zero bytes) without panicking. Gzip/zstd should return an error (no header),
// deflate/brotli should return empty or error on Read.
func TestPool_EmptyReader_NoPanic(t *testing.T) {
	empty := bytes.NewReader(nil)

	t.Run("gzip", func(t *testing.T) {
		_, err := AcquireGzipReader(empty)
		if err == nil {
			t.Fatal("expected error for empty gzip input")
		}
	})

	t.Run("deflate", func(t *testing.T) {
		// zlib.NewReader validates the header eagerly; empty input has no header.
		_, err := AcquireFlateReader(bytes.NewReader(nil))
		if err == nil {
			t.Fatal("expected error for empty deflate (zlib) input")
		}
	})

	t.Run("brotli", func(t *testing.T) {
		br := AcquireBrotliReader(bytes.NewReader(nil))
		data, _ := io.ReadAll(br)
		if len(data) != 0 {
			t.Errorf("expected empty output, got %d bytes", len(data))
		}
		ReleaseBrotliReader(br)
	})

	t.Run("zstd", func(t *testing.T) {
		dec, err := AcquireZstdDecoder(bytes.NewReader(nil))
		if err != nil {
			// Some versions error eagerly, that's fine.
			return
		}
		data, _ := io.ReadAll(dec)
		// Empty input with no zstd frame yields empty output or read error.
		_ = data
		ReleaseZstdDecoder(dec)
	})
}

// TestSafeReset verifies safeReset correctly handles panics and errors.
func TestSafeReset(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		ok := safeReset(func() error { return nil })
		if !ok {
			t.Fatal("expected true for successful reset")
		}
	})

	t.Run("error", func(t *testing.T) {
		ok := safeReset(func() error { return io.ErrUnexpectedEOF })
		if ok {
			t.Fatal("expected false for failed reset")
		}
	})

	t.Run("panic_string", func(t *testing.T) {
		ok := safeReset(func() error { panic("boom") })
		if ok {
			t.Fatal("expected false for panicking reset")
		}
	})

	t.Run("panic_nil", func(t *testing.T) {
		ok := safeReset(func() error { panic(nil) })
		if ok {
			t.Fatal("expected false for nil panic")
		}
	})

	t.Run("panic_error", func(t *testing.T) {
		ok := safeReset(func() error {
			panic(fmt.Errorf("internal corruption: %s", strings.Repeat("x", 100)))
		})
		if ok {
			t.Fatal("expected false for error panic")
		}
	})
}
