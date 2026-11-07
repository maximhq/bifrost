package chaosmock

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// The STDIO transport is served by a bridge rather than by a second copy of
// the protocol layer.
//
// A STDIO server is spawned by the system under test, not by the fleet, so it
// runs in its own process and cannot share memory with the virtual server
// whose behaviour it is supposed to have. Reimplementing the state there would
// mean two tool sets, two fault sets and two sets of counters to keep in step,
// and a control API that reached only one of them.
//
// Instead the child is deliberately thin: it reads newline-delimited JSON-RPC
// from stdin, forwards each message to the fleet over HTTP, and writes the
// reply back to stdout. Once the fleet issues a session it also holds that
// session's GET stream open and writes what the server pushes on it, such as
// tools/list_changed, to stdout between replies. Every control verb keeps
// working unchanged, counters stay in one place, and what is actually under
// test here (subprocess lifecycle, pipe framing, respawn after death) is
// exercised for real, because the process really is a subprocess and really
// does speak the protocol over pipes.
//
// The child registers its process id with the fleet on startup so a scenario
// can kill it, which is the fault that matters most on this transport and
// cannot be injected any other way. It registers the part of its environment
// named with stdioEnvPrefix too, so a scenario can check what the system under
// test spawned it with.

// stdioEnvPrefix marks the variables the bridge shares with the fleet. The
// child inherits the whole environment of the process that spawned it,
// credentials included, and the control API has no authentication, so
// nothing without the prefix leaves the process.
const stdioEnvPrefix = "CHAOS_"

// StdioBridgeConfig configures a child process serving one virtual server.
type StdioBridgeConfig struct {
	// FleetURL is the base URL of the fleet hosting the virtual server.
	FleetURL string
	// ServerName is the virtual server this process serves.
	ServerName string
	// In and Out default to os.Stdin and os.Stdout.
	In  io.Reader
	Out io.Writer
}

// RunStdioBridge serves one virtual server over stdin and stdout until the
// input closes, the context ends, or the fleet becomes unreachable.
//
// An unreachable fleet ends the process rather than being reported as a
// protocol error, and that is the intended behaviour: it is the closest
// analogue this transport has to an upstream server dying, and a client should
// see its subprocess exit rather than a well-formed error from a server that
// is gone.
func RunStdioBridge(ctx context.Context, cfg StdioBridgeConfig) error {
	if cfg.FleetURL == "" || cfg.ServerName == "" {
		return fmt.Errorf("stdio bridge needs both a fleet URL and a server name")
	}
	in := cfg.In
	if in == nil {
		in = os.Stdin
	}
	out := cfg.Out
	if out == nil {
		out = os.Stdout
	}

	endpoint := cfg.FleetURL + "/s/" + cfg.ServerName + "/mcp"
	client := &http.Client{Timeout: 60 * time.Second}

	// Registering the process id is what makes "kill the subprocess"
	// injectable. A failure to register is not fatal: the bridge is still
	// useful, only that one fault becomes unavailable, and saying so on stderr
	// is better than refusing to start.
	if err := registerStdio(client, cfg.FleetURL, cfg.ServerName, os.Getpid(), bridgeEnv()); err != nil {
		fmt.Fprintf(os.Stderr, "chaosmock: could not register pid: %v\n", err)
	}

	// Replies and relayed notifications share stdout, so each line is written whole.
	stdout := &lineWriter{w: out}
	relay := &notificationRelay{endpoint: endpoint, out: stdout}
	defer relay.stop()

	sessionID := ""
	scanner := bufio.NewScanner(in)
	// MCP messages can carry large tool schemas, so the default 64KB line
	// limit is not enough.
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)

	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return nil
		default:
		}

		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}

		payload := make([]byte, len(line))
		copy(payload, line)

		respBody, newSession, err := forward(ctx, client, endpoint, payload, sessionID)
		if err != nil {
			return fmt.Errorf("stdio bridge lost the fleet: %w", err)
		}
		if newSession != "" && newSession != sessionID {
			relay.follow(ctx, newSession)
		}
		if newSession != "" {
			sessionID = newSession
		}
		if len(bytes.TrimSpace(respBody)) == 0 {
			// A notification. Nothing goes back on stdout, which is what the
			// framing expects.
			continue
		}
		if err := stdout.writeLine(respBody); err != nil {
			return fmt.Errorf("stdio bridge could not write: %w", err)
		}
	}
	return scanner.Err()
}

// lineWriter writes whole newline-terminated lines, one at a time.
type lineWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (l *lineWriter) writeLine(msg []byte) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	_, err := l.w.Write(append(bytes.TrimSpace(msg), '\n'))
	return err
}

// notificationRelay holds the fleet's GET stream for the bridge's session open
// and writes each message it carries to stdout, which is how a real STDIO
// server sends a notification such as tools/list_changed: unprompted, between
// replies.
type notificationRelay struct {
	endpoint string
	out      *lineWriter
	cancel   context.CancelFunc
}

// follow moves the relay to sessionID: a new session gets its own stream, and
// the old session is gone, so its stream is closed with it.
func (r *notificationRelay) follow(ctx context.Context, sessionID string) {
	r.stop()
	streamCtx, cancel := context.WithCancel(ctx)
	r.cancel = cancel
	go relayNotifications(streamCtx, r.endpoint, sessionID, r.out)
}

func (r *notificationRelay) stop() {
	if r.cancel != nil {
		r.cancel()
	}
}

// relayNotifications streams sessionID's GET stream to out until ctx ends or
// the fleet closes the stream. A stream that cannot be opened is left closed:
// the bridge still serves requests, and only server-initiated messages are
// lost.
func relayNotifications(ctx context.Context, endpoint, sessionID string, out *lineWriter) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return
	}
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set(HeaderStdioBridge, "1")
	req.Header.Set(HeaderSessionID, sessionID)
	// No client timeout: the stream is meant to stay open for the session.
	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return
	}
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for scanner.Scan() {
		data, ok := strings.CutPrefix(scanner.Text(), "data:")
		if !ok || strings.TrimSpace(data) == "" {
			continue
		}
		if err := out.writeLine([]byte(data)); err != nil {
			return
		}
	}
}

// forward sends one JSON-RPC message to the fleet and returns the reply plus
// any session id the fleet issued.
func forward(ctx context.Context, client *http.Client, endpoint string, payload []byte, sessionID string) ([]byte, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set(HeaderStdioBridge, "1")
	if sessionID != "" {
		req.Header.Set(HeaderSessionID, sessionID)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 8*1024*1024))
	if err != nil {
		return nil, "", err
	}

	switch resp.StatusCode {
	case http.StatusOK, http.StatusAccepted:
		return body, resp.Header.Get(HeaderSessionID), nil
	case http.StatusNotFound:
		// The session went away. Clearing it lets the next initialize mint a
		// fresh one, which is what a client re-initializing would cause.
		return marshalTransportError(payload, "session terminated"), "", nil
	default:
		// An injected HTTP status has no equivalent on a pipe, so it is
		// reported as a protocol error naming the status. A client sees a
		// failed call rather than a dead process, which is the honest
		// translation.
		return marshalTransportError(payload, fmt.Sprintf("upstream returned %d", resp.StatusCode)), "", nil
	}
}

// marshalTransportError builds a JSON-RPC error carrying the id of the message
// it is answering, so a client can match it to the request it made.
func marshalTransportError(payload []byte, msg string) []byte {
	var req request
	if err := json.Unmarshal(payload, &req); err != nil || req.isNotification() {
		return nil
	}
	out, err := json.Marshal(newError(req.ID, CodeInternalError, msg))
	if err != nil {
		return nil
	}
	return out
}

// bridgeEnv returns the variables named with stdioEnvPrefix.
func bridgeEnv() map[string]string {
	env := map[string]string{}
	for _, kv := range os.Environ() {
		if name, value, ok := strings.Cut(kv, "="); ok && strings.HasPrefix(name, stdioEnvPrefix) {
			env[name] = value
		}
	}
	return env
}

// registerStdio tells the fleet which process bridges serverName and what
// environment it shares.
func registerStdio(client *http.Client, fleetURL, serverName string, pid int, env map[string]string) error {
	body, err := json.Marshal(map[string]any{"pid": pid, "env": env})
	if err != nil {
		return err
	}
	resp, err := client.Post(fleetURL+"/control/"+serverName+"/stdio/register", "application/json", bytes.NewReader(body))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("register returned %d", resp.StatusCode)
	}
	return nil
}
