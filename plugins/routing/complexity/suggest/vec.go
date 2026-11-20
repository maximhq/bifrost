package suggest

import (
	"math"
	"runtime"
	"sync"
)

func dot(a, b []float32) float32 {
	var s float32
	for i := range a {
		s += a[i] * b[i]
	}
	return s
}

// normalize returns v scaled to unit length, or v itself when it is zero.
func normalize(v []float32) []float32 {
	var sum float64
	for _, x := range v {
		sum += float64(x) * float64(x)
	}
	n := float32(math.Sqrt(sum))
	if n == 0 {
		return v
	}
	out := make([]float32, len(v))
	for i, x := range v {
		out[i] = x / n
	}
	return out
}

func norm(v []float32) float64 {
	var sum float64
	for _, x := range v {
		sum += float64(x) * float64(x)
	}
	return math.Sqrt(sum)
}

// parallelRange splits [0,n) across the CPUs and waits for every chunk.
func parallelRange(n int, fn func(lo, hi int)) {
	if n == 0 {
		return
	}
	workers := min(runtime.NumCPU(), n)
	chunk := (n + workers - 1) / workers
	var wg sync.WaitGroup
	for lo := 0; lo < n; lo += chunk {
		hi := min(lo+chunk, n)
		wg.Go(func() { fn(lo, hi) })
	}
	wg.Wait()
}

// forEach runs fn over [0,n) on at most workers goroutines and returns the
// first error. Every index runs even after an error, so results stay aligned.
func forEach(n, workers int, fn func(i int) error) error {
	workers = max(1, min(workers, n))
	var (
		wg    sync.WaitGroup
		mu    sync.Mutex
		first error
		next  = make(chan int)
	)
	for range workers {
		wg.Go(func() {
			for i := range next {
				if err := fn(i); err != nil {
					mu.Lock()
					if first == nil {
						first = err
					}
					mu.Unlock()
				}
			}
		})
	}
	for i := range n {
		next <- i
	}
	close(next)
	wg.Wait()
	return first
}
