package randid

import (
	"bytes"
	"sync"
	"testing"

	"github.com/google/uuid"
)

// TestNewUUIDIsVersion4 checks every UUID carries the version 4 and RFC 4122 variant bits, as
// uuid.New sets them.
func TestNewUUIDIsVersion4(t *testing.T) {
	for range 2000 {
		u := NewUUID()
		if u.Version() != 4 || u.Variant() != uuid.RFC4122 {
			t.Fatalf("UUID %s has version %d and variant %s, want 4 and %s", u, u.Version(), u.Variant(), uuid.RFC4122)
		}
	}
}

// TestReadHandsOutEachByteOnce draws from many goroutines across many refills and checks no two
// reads received the same bytes.
func TestReadHandsOutEachByteOnce(t *testing.T) {
	const workers, perWorker = 16, 5000 // 1.28 MB: hundreds of batches
	var mu sync.Mutex
	seen := make(map[[16]byte]struct{}, workers*perWorker)
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			local := make([][16]byte, perWorker)
			for i := range local {
				Read(local[i][:])
			}
			mu.Lock()
			defer mu.Unlock()
			for _, p := range local {
				if _, dup := seen[p]; dup {
					t.Errorf("bytes %x handed out twice", p)
					return
				}
				seen[p] = struct{}{}
			}
		}()
	}
	wg.Wait()
}

// TestReadFillsAnyLength reads lengths around and beyond the batch size, in an order that forces a
// refill with bytes still left, and checks no 16-byte block of the result is left zero.
func TestReadFillsAnyLength(t *testing.T) {
	zero := make([]byte, 16)
	for _, n := range []int{0, 1, 16, batchSize - 1, batchSize, batchSize + 1, 3 * batchSize} {
		p := make([]byte, n)
		Read(p)
		for off := 0; off+16 <= n; off += 16 {
			if bytes.Equal(p[off:off+16], zero) {
				t.Fatalf("Read(%d bytes) left bytes %d to %d zero", n, off, off+16)
			}
		}
	}
}

// BenchmarkNewUUID compares NewUUID with uuid.New under concurrent callers.
func BenchmarkNewUUID(b *testing.B) {
	for _, bc := range []struct {
		name string
		fn   func() uuid.UUID
	}{
		{"randid", NewUUID},
		{"uuid.New", uuid.New},
	} {
		b.Run(bc.name, func(b *testing.B) {
			b.ReportAllocs()
			b.RunParallel(func(pb *testing.PB) {
				for pb.Next() {
					_ = bc.fn()
				}
			})
		})
	}
}
