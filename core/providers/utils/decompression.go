package utils

import (
	"compress/gzip"
	"compress/zlib"
	"io"
	"sync"

	"github.com/andybalholm/brotli"
	"github.com/klauspost/compress/zstd"
)

// ---------------------------------------------------------------------------
// Pooled decompression readers
//
// Each encoding gets a sync.Pool with Acquire/Release helpers that follow the
// same contract:
//   - Acquire(r io.Reader) returns a ready-to-read decompressor, reusing a
//     pooled instance when possible, falling back to a fresh allocation.
//   - Release returns the decompressor to the pool for future reuse.
//     Callers MUST call Release exactly once after the reader is fully consumed.
//
// All pool operations are panic-safe: type assertions use the comma-ok form,
// Reset calls are wrapped in recover, and nil checks guard every dereference.
// A corrupt or wrong-typed pooled instance is silently discarded (GC reclaims
// it) and a fresh allocation takes its place.
//
// Gzip, deflate, and brotli readers are stateless between streams — Close (if
// applicable) then Reset is safe. Zstd decoders run background goroutines so
// Close is terminal; pooled decoders are reset without closing.
// ---------------------------------------------------------------------------

// safeReset calls resetFn and recovers from any panic. Returns true on success.
// A corrupt pooled reader may panic inside Reset; this prevents that from
// bringing down the server.
func safeReset(resetFn func() error) (ok bool) {
	defer func() {
		if r := recover(); r != nil {
			ok = false
		}
	}()
	return resetFn() == nil
}

// ---- gzip ----

var gzipReaderPool = sync.Pool{
	New: func() any {
		return &gzip.Reader{}
	},
}

// AcquireGzipReader gets a gzip.Reader from the pool and resets it to read from r,
// or creates a new one if the pool is empty or reset fails.
func AcquireGzipReader(r io.Reader) (*gzip.Reader, error) {
	if v := gzipReaderPool.Get(); v != nil {
		if gz, ok := v.(*gzip.Reader); ok {
			if safeReset(func() error { return gz.Reset(r) }) {
				return gz, nil
			}
		}
		// Wrong type or reset failed/panicked — discard, let GC reclaim.
	}
	return gzip.NewReader(r)
}

// ReleaseGzipReader closes and returns a gzip.Reader to the pool.
func ReleaseGzipReader(gz *gzip.Reader) {
	if gz == nil {
		return
	}
	_ = gz.Close()
	gzipReaderPool.Put(gz)
}

// ---- deflate ----
//
// HTTP Content-Encoding "deflate" is zlib-wrapped DEFLATE (RFC 1950), NOT raw
// DEFLATE (RFC 1951). This matches fasthttp's implementation and the HTTP spec
// (RFC 9110 §8.4.1.2). We use compress/zlib, not compress/flate.

// deflateReader is the interface that zlib readers support for pooling.
// The concrete type from zlib.NewReader is unexported, but implements Resetter.
type deflateReader interface {
	io.ReadCloser
	Reset(r io.Reader, dict []byte) error
}

// No New func: zlib.NewReader validates the header eagerly, so it cannot be
// created with a nil reader. Pooled readers are populated via Release.
var deflateReaderPool = sync.Pool{}

// AcquireFlateReader gets a zlib (HTTP "deflate") reader from the pool and
// resets it to read from r, or creates a new one if the pool is empty or
// reset fails.
func AcquireFlateReader(r io.Reader) (io.ReadCloser, error) {
	if v := deflateReaderPool.Get(); v != nil {
		if dr, ok := v.(deflateReader); ok {
			if safeReset(func() error { return dr.Reset(r, nil) }) {
				return dr, nil
			}
		}
	}
	return zlib.NewReader(r)
}

// ReleaseFlateReader closes and returns a deflate reader to the pool.
func ReleaseFlateReader(fr io.ReadCloser) {
	if fr == nil {
		return
	}
	_ = fr.Close()
	deflateReaderPool.Put(fr)
}

// ---- brotli ----

var brotliReaderPool = sync.Pool{
	New: func() any {
		return brotli.NewReader(nil)
	},
}

// AcquireBrotliReader gets a brotli.Reader from the pool and resets it to read
// from r, or creates a new one if the pool is empty or reset panics.
func AcquireBrotliReader(r io.Reader) *brotli.Reader {
	if v := brotliReaderPool.Get(); v != nil {
		if br, ok := v.(*brotli.Reader); ok {
			// brotli.Reset is void (no error), but wrap in safeReset for
			// consistency: a corrupt pooled reader could panic on Reset.
			if safeReset(func() error { br.Reset(r); return nil }) {
				return br
			}
		}
		// Wrong type or reset panicked — discard, let GC reclaim.
	}
	return brotli.NewReader(r)
}

// ReleaseBrotliReader returns a brotli.Reader to the pool.
// Brotli readers have no Close method; Reset(nil) is sufficient to drop the
// reference to the underlying reader.
func ReleaseBrotliReader(br *brotli.Reader) {
	if br == nil {
		return
	}
	br.Reset(nil)
	brotliReaderPool.Put(br)
}

// ---- zstd ----

var zstdDecoderPool = sync.Pool{
	New: func() any {
		dec, err := zstd.NewReader(nil, zstd.WithDecoderConcurrency(1))
		if err != nil {
			// NewReader(nil) failing is unexpected; return nil so Acquire
			// falls through to a fresh allocation with the real reader.
			return nil
		}
		return dec
	},
}

// AcquireZstdDecoder gets a zstd.Decoder from the pool and resets it to read
// from r, or creates a new one if the pool is empty or reset fails.
// Decoders are created with concurrency=1 to minimise goroutine overhead.
func AcquireZstdDecoder(r io.Reader) (*zstd.Decoder, error) {
	if v := zstdDecoderPool.Get(); v != nil {
		if dec, ok := v.(*zstd.Decoder); ok && dec != nil {
			if safeReset(func() error { return dec.Reset(r) }) {
				return dec, nil
			}
			// Reset failed/panicked — release references before discarding.
			// Don't call Close (terminal); Reset(nil) is safe per pool contract.
			_ = dec.Reset(nil)
		}
	}
	return zstd.NewReader(r, zstd.WithDecoderConcurrency(1))
}

// ReleaseZstdDecoder returns a zstd.Decoder to the pool.
// Unlike other decoders, zstd.Close() is terminal (stops background goroutines
// permanently). We only call Reset(nil) to release the source reference, then
// re-pool. Close is never called on pooled decoders.
func ReleaseZstdDecoder(dec *zstd.Decoder) {
	if dec == nil {
		return
	}
	_ = dec.Reset(nil)
	zstdDecoderPool.Put(dec)
}
