package driver

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
)

const pushTokenHeader = "A2A-Notification-Token"

// Sink is an in-process push callback receiver that records the checkpoint and
// completion events for a task.
type Sink struct {
	// URL is the callback URL the gateway should deliver to.
	URL   string
	Token string

	server         *http.Server
	mu             sync.Mutex
	deliveries     int
	checkpointTask a2a.TaskID
	completedTask  a2a.TaskID
	notify         chan struct{}
}

type pushPayload struct {
	ArtifactUpdate *struct {
		Append   bool `json:"append"`
		Artifact struct {
			Parts []struct {
				Text string `json:"text"`
			} `json:"parts"`
		} `json:"artifact"`
		TaskID a2a.TaskID `json:"taskId"`
	} `json:"artifactUpdate"`
	StatusUpdate *struct {
		Status struct {
			State string `json:"state"`
		} `json:"status"`
		TaskID a2a.TaskID `json:"taskId"`
	} `json:"statusUpdate"`
}

// StartSink listens on bind and advertises host in its callback URL.
func StartSink(bind, host string) (*Sink, error) {
	listener, err := net.Listen("tcp", bind)
	if err != nil {
		return nil, fmt.Errorf("listen for push callback: %w", err)
	}
	s := &Sink{
		Token:  "lifecycle-client-token",
		notify: make(chan struct{}, 1),
		URL:    "http://" + net.JoinHostPort(host, strconv.Itoa(listener.Addr().(*net.TCPAddr).Port)) + "/callback",
	}
	s.server = &http.Server{Handler: s, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = s.server.Serve(listener) }()
	return s, nil
}

// Close stops the callback listener.
func (s *Sink) Close() error { return s.server.Close() }

// Deliveries returns how many authenticated callbacks were accepted.
func (s *Sink) Deliveries() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.deliveries
}

// ServeHTTP accepts authenticated push deliveries and records expected updates.
func (s *Sink) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	if r.Header.Get(pushTokenHeader) != s.Token {
		http.Error(w, "invalid notification token", http.StatusUnauthorized)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	var payload pushPayload
	if err != nil || json.Unmarshal(body, &payload) != nil {
		http.Error(w, "invalid notification body", http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	s.deliveries++
	if u := payload.ArtifactUpdate; u != nil && u.Append && len(u.Artifact.Parts) > 0 && u.Artifact.Parts[0].Text == "; checkpoint 2" {
		s.checkpointTask = u.TaskID
	}
	if u := payload.StatusUpdate; u != nil && u.Status.State == "TASK_STATE_COMPLETED" {
		s.completedTask = u.TaskID
	}
	s.mu.Unlock()
	select {
	case s.notify <- struct{}{}:
	default:
	}
	w.WriteHeader(http.StatusNoContent)
}

// WaitFor blocks until both the checkpoint and completion pushes for the task
// arrived or ctx ends.
func (s *Sink) WaitFor(ctx context.Context, task a2a.TaskID) error {
	for {
		s.mu.Lock()
		done := s.checkpointTask == task && s.completedTask == task
		s.mu.Unlock()
		if done {
			return nil
		}
		select {
		case <-s.notify:
		case <-ctx.Done():
			return fmt.Errorf("timed out waiting for checkpoint and completed push updates: %w", ctx.Err())
		}
	}
}
