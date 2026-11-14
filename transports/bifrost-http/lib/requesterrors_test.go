package lib

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/valyala/fasthttp"
)

// recordingLogger keeps warnings so tests can check what the server logged.
type recordingLogger struct {
	warnings []string
}

// Warn records one warning.
func (l *recordingLogger) Warn(format string, args ...any) {
	l.warnings = append(l.warnings, fmt.Sprintf(format, args...))
}

// postOversized sends only the headers of a request whose Content-Length is one byte over 1 MB. fasthttp
// rejects it from the header and closes the connection, so writing the body would race a broken pipe.
func postOversized(t *testing.T, addr, path string) *http.Response {
	t.Helper()
	conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	conn.SetDeadline(time.Now().Add(5 * time.Second)) //nolint:errcheck
	if _, err := fmt.Fprintf(conn, "POST %s HTTP/1.1\r\nHost: %s\r\nContent-Type: application/json\r\nContent-Length: %d\r\n\r\n", path, addr, (1<<20)+1); err != nil {
		t.Fatal(err)
	}
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

// TestRequestErrorHandlerExplainsBodyLimit answers an oversized request with 413 and a message that
// names the configured limit and its setting, logs the rejection, and leaves other errors as before.
func TestRequestErrorHandlerExplainsBodyLimit(t *testing.T) {
	logs := &recordingLogger{}
	server := &fasthttp.Server{
		Handler:            func(ctx *fasthttp.RequestCtx) { ctx.SetStatusCode(http.StatusOK) },
		MaxRequestBodySize: 1 << 20,
		ErrorHandler:       RequestErrorHandler(1, logs.Warn),
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go server.Serve(listener) //nolint:errcheck
	client := &http.Client{Timeout: 5 * time.Second}

	resp := postOversized(t, listener.Addr().String(), "/v1/chat/completions")
	defer resp.Body.Close()
	var body struct {
		Error struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("413 body is not JSON: %v", err)
	}
	if resp.StatusCode != http.StatusRequestEntityTooLarge || body.Error.Type != "request_too_large" ||
		!strings.Contains(body.Error.Message, "1 MB") || !strings.Contains(body.Error.Message, "max_request_body_size_mb") {
		t.Fatalf("status=%d body=%+v", resp.StatusCode, body.Error)
	}
	if len(logs.warnings) != 1 || !strings.Contains(logs.warnings[0], "request body exceeds") {
		t.Fatalf("rejection not logged: %v", logs.warnings)
	}

	small, err := client.Post("http://"+listener.Addr().String()+"/v1/chat/completions", "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	small.Body.Close()
	if small.StatusCode != http.StatusOK {
		t.Fatalf("request under the limit got %d", small.StatusCode)
	}

	// fasthttp decodes %0A in the path, so the warning must escape it or the client can forge a log line.
	postOversized(t, listener.Addr().String(), "/v1/a%0Aforged").Body.Close()
	if len(logs.warnings) != 2 || strings.Contains(logs.warnings[1], "\n") {
		t.Fatalf("decoded path not escaped in warning: %q", logs.warnings)
	}
}
