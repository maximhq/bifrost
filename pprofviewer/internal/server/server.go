package server

import (
	"crypto/rand"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/maximhq/bifrost/pprofviewer/internal/analyzer"
)

//go:embed static/*
var staticFS embed.FS

var results = resultCache{items: make(map[string][]byte)}

type resultCache struct {
	mu    sync.RWMutex
	items map[string][]byte
}

func New() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", index)
	mux.HandleFunc("/api/analyze", analyze)
	mux.HandleFunc("/api/analyze-set", analyzeSet)
	mux.HandleFunc("/api/result", cachedResult)
	return mux
}

func index(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	data, err := staticFS.ReadFile("static/index.html")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(data)
}

func analyze(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost && r.Method != http.MethodGet {
		http.Error(w, "use POST with a pprof file or GET ?path=/path/to/profile", http.StatusMethodNotAllowed)
		return
	}

	sample := r.URL.Query().Get("sample")
	reader, closeFn, err := profileReader(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	defer closeFn()

	result, err := analyzer.Analyze(reader, sample)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	result.ID = newResultID()
	writeCachedJSON(w, result.ID, result)
}

func analyzeSet(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost && r.Method != http.MethodGet {
		http.Error(w, "use POST multipart profile fields or GET *_path query params", http.StatusMethodNotAllowed)
		return
	}

	readers, closeFn, err := profileSetReaders(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	defer closeFn()

	result, err := analyzer.AnalyzeSet(readers)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	result.ID = newResultID()
	writeCachedJSON(w, result.ID, result)
}

func cachedResult(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "use GET", http.StatusMethodNotAllowed)
		return
	}
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	if id == "" {
		http.Error(w, "missing id", http.StatusBadRequest)
		return
	}
	data, ok := results.get(id)
	if !ok {
		http.Error(w, "result not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(data)
}

func writeCachedJSON(w http.ResponseWriter, id string, v any) {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	results.set(id, data)
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(data)
}

func newResultID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return ""
	}
	return hex.EncodeToString(b[:])
}

func (c *resultCache) set(id string, data []byte) {
	if id == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.items[id] = append([]byte(nil), data...)
}

func (c *resultCache) get(id string) ([]byte, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	data, ok := c.items[id]
	if !ok {
		return nil, false
	}
	return append([]byte(nil), data...), true
}

func profileReader(r *http.Request) (io.Reader, func(), error) {
	if r.Method == http.MethodGet {
		path := r.URL.Query().Get("path")
		if strings.TrimSpace(path) == "" {
			return nil, func() {}, fmt.Errorf("missing path")
		}
		f, err := os.Open(path)
		if err != nil {
			return nil, func() {}, err
		}
		return f, func() { _ = f.Close() }, nil
	}

	contentType := r.Header.Get("Content-Type")
	if strings.HasPrefix(contentType, "multipart/form-data") {
		if err := r.ParseMultipartForm(512 << 20); err != nil {
			return nil, func() {}, err
		}
		f, _, err := r.FormFile("profile")
		if err != nil {
			return nil, func() {}, fmt.Errorf("missing multipart field %q", "profile")
		}
		return f, func() { _ = f.Close() }, nil
	}

	return r.Body, func() { _ = r.Body.Close() }, nil
}

func profileSetReaders(r *http.Request) (map[string]io.Reader, func(), error) {
	readers := make(map[string]io.Reader)
	var closers []io.Closer
	closeFn := func() {
		for _, closer := range closers {
			_ = closer.Close()
		}
	}

	if r.Method == http.MethodGet {
		for _, name := range analyzer.ProfileSetNames() {
			path := strings.TrimSpace(r.URL.Query().Get(name + "_path"))
			if path == "" {
				continue
			}
			f, err := os.Open(path)
			if err != nil {
				closeFn()
				return nil, func() {}, fmt.Errorf("%s_path: %w", name, err)
			}
			readers[name] = f
			closers = append(closers, f)
		}
		return readers, closeFn, nil
	}

	contentType := r.Header.Get("Content-Type")
	if !strings.HasPrefix(contentType, "multipart/form-data") {
		return nil, func() {}, fmt.Errorf("combined analysis requires multipart form data")
	}
	if err := r.ParseMultipartForm(512 << 20); err != nil {
		return nil, func() {}, err
	}
	for _, name := range analyzer.ProfileSetNames() {
		f, header, err := r.FormFile(name)
		if err != nil {
			continue
		}
		addProfileReader(readers, &closers, name, f, header)
	}
	if r.MultipartForm != nil {
		for _, headers := range r.MultipartForm.File {
			for _, header := range headers {
				name := inferProfileName(header.Filename)
				if name == "" {
					continue
				}
				if _, exists := readers[name]; exists {
					continue
				}
				f, err := header.Open()
				if err != nil {
					closeFn()
					return nil, func() {}, err
				}
				addProfileReader(readers, &closers, name, f, header)
			}
		}
	}
	return readers, closeFn, nil
}

func addProfileReader(readers map[string]io.Reader, closers *[]io.Closer, name string, f multipart.File, _ *multipart.FileHeader) {
	if _, exists := readers[name]; exists {
		_ = f.Close()
		return
	}
	readers[name] = f
	*closers = append(*closers, f)
}

func inferProfileName(name string) string {
	base := strings.ToLower(filepath.Base(name))
	base = strings.TrimSuffix(base, ".gz")
	base = strings.TrimSuffix(base, ".pb")
	base = strings.TrimSuffix(base, ".pprof")
	base = strings.TrimSuffix(base, ".prof")
	base = strings.TrimSuffix(base, ".txt")
	base = strings.TrimSuffix(base, ".out")
	switch base {
	case "heap", "inuse", "mem", "memory":
		return "heap"
	case "alloc", "allocs", "allocations":
		return "allocs"
	case "cpu", "profile":
		return "cpu"
	case "goroutine", "goroutines":
		return "goroutine"
	case "block", "blocking":
		return "block"
	case "mutex", "mutator":
		return "mutex"
	}
	normalized := strings.NewReplacer("-", "_", ".", "_", " ", "_").Replace(base)
	for _, part := range strings.Split(normalized, "_") {
		switch part {
		case "heap", "inuse":
			return "heap"
		case "alloc", "allocs":
			return "allocs"
		case "cpu":
			return "cpu"
		case "goroutine", "goroutines":
			return "goroutine"
		case "block":
			return "block"
		case "mutex":
			return "mutex"
		}
	}
	return ""
}
