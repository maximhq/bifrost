package objectstore

import (
	"bytes"
	"compress/gzip"
	"io"
	"sync"
)

// Pooled gzip writer/reader to avoid allocation per compress/decompress call.
// Follows the same pattern as core/providers/utils/decompression.go.

var gzipWriterPool = sync.Pool{
	New: func() any {
		return gzip.NewWriter(nil)
	},
}

var gzipReaderPool = sync.Pool{
	New: func() any {
		return &gzip.Reader{}
	},
}

// gzipCompress compresses data using a pooled gzip writer.
func gzipCompress(data []byte) ([]byte, error) {
	var buf bytes.Buffer
	buf.Grow(len(data) / 2) // Pre-allocate rough estimate.

	w, _ := gzipWriterPool.Get().(*gzip.Writer)
	if w == nil {
		w = gzip.NewWriter(&buf)
	} else {
		w.Reset(&buf)
	}

	if _, err := w.Write(data); err != nil {
		// Don't return the writer to the pool on error — it may be in a bad state.
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}

	w.Reset(io.Discard)
	gzipWriterPool.Put(w)
	return buf.Bytes(), nil
}

// gzipDecompress decompresses gzip data using a pooled gzip reader.
func gzipDecompress(data []byte) ([]byte, error) {
	v := gzipReaderPool.Get()
	r, ok := v.(*gzip.Reader)
	if !ok || r == nil {
		// Pool had a wrong type or nil — allocate fresh.
		var err error
		r, err = gzip.NewReader(bytes.NewReader(data))
		if err != nil {
			return nil, err
		}
	} else {
		if err := r.Reset(bytes.NewReader(data)); err != nil {
			// Reset failed — discard and allocate fresh.
			var err2 error
			r, err2 = gzip.NewReader(bytes.NewReader(data))
			if err2 != nil {
				return nil, err2
			}
		}
	}

	result, err := io.ReadAll(r)
	_ = r.Close()
	if err != nil {
		return nil, err
	}

	gzipReaderPool.Put(r)
	return result, nil
}
