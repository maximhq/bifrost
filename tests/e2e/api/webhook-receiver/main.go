// Command webhook-receiver is a capture server for the webhooks e2e suite.
// Bifrost delivers webhooks to /hook/{scenario}; the Newman collection then
// reads what arrived via /captures and steers response behavior via /mode.
//
// Endpoints:
//
//	POST /hook/{scenario}        capture the delivery, answer with the
//	                             scenario's configured status (default 204)
//	GET  /captures?scenario=x    captured deliveries for a scenario, oldest first
//	DELETE /captures             drop all captures and scenario modes
//	POST /mode/{scenario}?status=500   set a scenario's response status
//	GET  /healthz                readiness probe
package main

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

type capture struct {
	Scenario   string              `json:"scenario"`
	Headers    map[string][]string `json:"headers"`
	Body       string              `json:"body"`
	ReceivedAt time.Time           `json:"received_at"`
}

type receiver struct {
	mu       sync.Mutex
	captures []capture
	modes    map[string]int
}

func (r *receiver) hook(w http.ResponseWriter, req *http.Request) {
	scenario := strings.TrimPrefix(req.URL.Path, "/hook/")
	body, _ := io.ReadAll(req.Body)

	r.mu.Lock()
	r.captures = append(r.captures, capture{
		Scenario:   scenario,
		Headers:    req.Header.Clone(),
		Body:       string(body),
		ReceivedAt: time.Now().UTC(),
	})
	status, ok := r.modes[scenario]
	r.mu.Unlock()

	if !ok {
		status = http.StatusNoContent
	}
	w.WriteHeader(status)
}

func (r *receiver) listCaptures(w http.ResponseWriter, req *http.Request) {
	if req.Method == http.MethodDelete {
		r.mu.Lock()
		r.captures = nil
		r.modes = make(map[string]int)
		r.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
		return
	}
	scenario := req.URL.Query().Get("scenario")
	r.mu.Lock()
	matched := make([]capture, 0, len(r.captures))
	for _, c := range r.captures {
		if scenario == "" || c.Scenario == scenario {
			matched = append(matched, c)
		}
	}
	r.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"captures": matched, "count": len(matched)})
}

func (r *receiver) setMode(w http.ResponseWriter, req *http.Request) {
	scenario := strings.TrimPrefix(req.URL.Path, "/mode/")
	status, err := strconv.Atoi(req.URL.Query().Get("status"))
	if err != nil || scenario == "" || status < 100 || status > 599 {
		http.Error(w, "usage: POST /mode/{scenario}?status=500", http.StatusBadRequest)
		return
	}
	r.mu.Lock()
	r.modes[scenario] = status
	r.mu.Unlock()
	w.WriteHeader(http.StatusNoContent)
}

func main() {
	port := os.Getenv("WEBHOOK_RECEIVER_PORT")
	if port == "" {
		port = "3005"
	}
	r := &receiver{modes: make(map[string]int)}
	mux := http.NewServeMux()
	mux.HandleFunc("/hook/", r.hook)
	mux.HandleFunc("/captures", r.listCaptures)
	mux.HandleFunc("/mode/", r.setMode)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })

	// Bind to loopback only: the receiver is an unauthenticated test sidecar
	// whose /captures and /mode endpoints expose captured bodies, signing
	// headers, and per-scenario response control. It must never be reachable
	// beyond the host running the suite.
	addr := "127.0.0.1:" + port
	log.Printf("webhook-receiver listening on %s", addr)
	log.Fatal(http.ListenAndServe(addr, mux))
}
