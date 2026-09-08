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
// reply back to stdout. Every control verb keeps working unchanged, counters
// stay in one place, and what is actually under test here (subprocess
// lifecycle, pipe framing, respawn after death) is exercised for real, because
// the process really is a subprocess and really does speak the protocol over
// pipes.
//
// The child registers its process id with the fleet on startup so a scenario
// can kill it, which is the fault that matters most on this transport and
// cannot be injected any other way.

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
	if err := registerStdioPID(client, cfg.FleetURL, cfg.ServerName, os.Getpid()); err != nil {
		fmt.Fprintf(os.Stderr, "chaosmock: could not register pid: %v\n", err)
	}

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
		if newSession != "" {
			sessionID = newSession
		}
		if len(bytes.TrimSpace(respBody)) == 0 {
			// A notification. Nothing goes back on stdout, which is what the
			// framing expects.
			continue
		}
		if _, err := out.Write(append(bytes.TrimSpace(respBody), '\n')); err != nil {
			return fmt.Errorf("stdio bridge could not write: %w", err)
		}
	}
	return scanner.Err()
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

func registerStdioPID(client *http.Client, fleetURL, serverName string, pid int) error {
	body, err := json.Marshal(map[string]int{"pid": pid})
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
