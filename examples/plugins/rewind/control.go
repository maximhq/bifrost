package main

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

type controlServer struct{ srv *http.Server }

// startControl serves the run API on its own port so it works on a stock
// Bifrost router without adding routes to it.
func startControl(addr string, get func() *Store) *controlServer {
	srv := &http.Server{Addr: addr, Handler: controlHandler(get), ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = srv.ListenAndServe() }()
	return &controlServer{srv: srv}
}

// controlHandler reads the current store on each request, so a second Init
// does not leave the API pointing at a stale store.
func controlHandler(get func() *Store) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/rewind/runs", func(w http.ResponseWriter, r *http.Request) {
		s := get()
		switch r.Method {
		case http.MethodGet:
			writeJSON(w, 200, s.List())
		case http.MethodPost:
			var spec RunSpec
			if err := json.NewDecoder(r.Body).Decode(&spec); err != nil {
				writeJSON(w, 400, map[string]string{"error": err.Error()})
				return
			}
			run, err := s.Open(spec)
			if err != nil {
				writeJSON(w, 400, map[string]string{"error": err.Error()})
				return
			}
			writeJSON(w, 201, run.summary())
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	})
	mux.HandleFunc("/rewind/runs/", func(w http.ResponseWriter, r *http.Request) {
		s := get()
		rest := strings.TrimPrefix(r.URL.Path, "/rewind/runs/")
		if id, ok := strings.CutSuffix(rest, "/close"); ok && r.Method == http.MethodPost {
			run, err := s.Close(id)
			if err != nil {
				writeJSON(w, 404, map[string]string{"error": err.Error()})
				return
			}
			writeJSON(w, 200, run.summary())
			return
		}
		run, err := s.Load(rest)
		if err != nil {
			writeJSON(w, 404, map[string]string{"error": err.Error()})
			return
		}
		run.mu.Lock()
		defer run.mu.Unlock()
		writeJSON(w, 200, run)
	})
	mux.HandleFunc("/rewind/health", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, 200, map[string]string{"status": "ok"})
	})
	return mux
}

func (c *controlServer) stop() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = c.srv.Shutdown(ctx)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
