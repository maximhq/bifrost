package agent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/stretchr/testify/require"

	"github.com/maximhq/bifrost/core/schemas"
)

func TestRawCaptureHTTPErrorBody(t *testing.T) {
	for _, stalled := range []bool{false, true} {
		t.Run(fmt.Sprint(stalled), func(t *testing.T) {
			const body = `{"error":"upstream failure"}`
			clientConn, serverConn := net.Pipe()
			defer clientConn.Close()
			defer serverConn.Close()
			serverDone := make(chan struct{})
			go func() {
				defer close(serverDone)
				defer serverConn.Close()
				if _, err := http.ReadRequest(bufio.NewReader(serverConn)); err != nil {
					return
				}
				if stalled {
					_, _ = fmt.Fprintf(serverConn, "HTTP/1.1 500 Internal Server Error\r\nContent-Length: %d\r\n\r\n%s", len(body), body[:5])
					_, _ = io.Copy(io.Discard, serverConn)
					return
				}
				_, _ = fmt.Fprintf(serverConn, "HTTP/1.1 500 Internal Server Error\r\nContent-Length: %d\r\n\r\n%s", len(body), body)
			}()
			transport := &http.Transport{DialContext: func(context.Context, string, string) (net.Conn, error) {
				return clientConn, nil
			}}
			defer transport.CloseIdleConnections()
			capture := &schemas.A2ARawCapture{}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			var captures schemas.A2ARawCaptureManager
			ctx = context.WithValue(ctx, schemas.A2ARawCaptureContextKey, captures.Start("capture-test"))
			capture = schemas.GetA2ARawCapture(ctx)
			req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://agent", nil)
			require.NoError(t, err)
			start := time.Now()
			resp, err := (&credentialTransport{base: transport}).RoundTrip(req)
			require.Nil(t, resp)
			var statusErr *upstreamHTTPStatusError
			require.ErrorAs(t, err, &statusErr)
			require.Equal(t, http.StatusInternalServerError, statusErr.StatusCode)
			<-serverDone
			_, raw := capture.Snapshot()
			if stalled {
				require.Less(t, time.Since(start), 3*time.Second)
				require.NoError(t, ctx.Err(), "capture timeout must not cancel the caller")
				require.Nil(t, raw, "a partial error body must not look complete")
			} else {
				require.NotNil(t, raw)
				decoded, err := base64.StdEncoding.DecodeString(*raw)
				require.NoError(t, err)
				require.Equal(t, body, string(decoded))
			}
		})
	}
}

type rawCaptureTransport struct {
	body    string
	request []byte
	replay  bool
}

// RoundTrip supplies an upstream body without normalizing whitespace or JSON keys.
func (t *rawCaptureTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if t.replay && req.GetBody != nil {
		body, err := req.GetBody()
		if err != nil {
			return nil, err
		}
		defer body.Close()
		t.request, _ = io.ReadAll(body)
	} else if req.Body != nil {
		t.request, _ = io.ReadAll(req.Body)
	}
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(bytes.NewBufferString(t.body))}, nil
}

// TestRawCaptureHTTP verifies exact envelopes and SSE framing survive capture and decoding.
func TestRawCaptureHTTP(t *testing.T) {
	request := "{ \"jsonrpc\": \"2.0\", \"params\": {\"z\":1,\"a\":2} }\n"
	response := "id: 1\r\ndata: { \"result\": {\"unknown\":true} }\r\n\r\n"
	capture := &schemas.A2ARawCapture{}
	var captures schemas.A2ARawCaptureManager
	ctx := context.WithValue(context.Background(), schemas.A2ARawCaptureContextKey, captures.Start("capture-test"))
	capture = schemas.GetA2ARawCapture(ctx)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://agent", bytes.NewBufferString(request))
	if err != nil {
		t.Fatal(err)
	}
	base := &rawCaptureTransport{body: response}
	transport := &credentialTransport{base: base}
	resp, err := transport.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	got, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	rawReq, rawResp := capture.Snapshot()
	if rawReq == nil || rawResp == nil {
		t.Fatal("missing capture")
	}
	decodedReq, _ := base64.StdEncoding.DecodeString(*rawReq)
	decodedResp, _ := base64.StdEncoding.DecodeString(*rawResp)
	if string(decodedReq) != request || string(base.request) != request || string(decodedResp) != response || string(got) != response {
		t.Fatal("transport bytes changed")
	}
}

// ingressCapturePipeline observes the same context consumed by logging post-hooks.
type ingressCapturePipeline struct {
	PluginPipeline
	request, response *string
	sawCapture        bool
}

// RunA2APostHooks records captured bytes at operation completion.
func (p *ingressCapturePipeline) RunA2APostHooks(ctx *schemas.BifrostContext, resp *schemas.BifrostA2AResponse, err *schemas.BifrostError, count int) (*schemas.BifrostA2AResponse, *schemas.BifrostError) {
	if capture := schemas.GetA2ARawCapture(ctx); capture != nil {
		p.sawCapture = true
		p.request, p.response = capture.Snapshot()
	}
	return resp, err
}

// TestRawCapturePushIngress verifies incoming bytes reach logging without reserialization.
func TestRawCapturePushIngress(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(fmt.Sprint(enabled), func(t *testing.T) {
			m, h, _, upstream := pushGateway(t)
			_, err := h.CreateTaskPushConfig(context.Background(), &a2a.PushConfig{TaskID: "task-1", ID: "cfg-1", URL: "http://client.example/callback"})
			require.NoError(t, err)
			m.rawCaptureFn = func() bool { return enabled }
			pipeline := &ingressCapturePipeline{PluginPipeline: &fakePipeline{}}
			m.SetPluginPipeline(func() PluginPipeline { return pipeline }, func(PluginPipeline) {})
			payload := append([]byte(" \n"), pushEventPayload(t, "task-1")...)
			status, err := m.AcceptPushCallback(context.Background(), "fixture", upstream.lastCreated(t).Token, payload)
			require.NoError(t, err)
			require.Equal(t, http.StatusAccepted, status)
			require.Nil(t, pipeline.response)
			if enabled {
				require.NotNil(t, pipeline.request)
				decoded, err := base64.StdEncoding.DecodeString(*pipeline.request)
				require.NoError(t, err)
				require.Equal(t, payload, decoded)
			} else {
				require.Nil(t, pipeline.request)
			}
		})
	}
}

// TestRawCaptureHTTPReplay verifies recreated bodies used by HTTP retries remain captured.
func TestRawCaptureHTTPReplay(t *testing.T) {
	for _, push := range []bool{false, true} {
		t.Run(fmt.Sprint(push), func(t *testing.T) {
			payload := `{ "taskId": "task-1" }` + "\n"
			capture := &schemas.A2ARawCapture{}
			ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
			var captures schemas.A2ARawCaptureManager
			ctx.SetValue(schemas.A2ARawCaptureContextKey, captures.Start("capture-test"))
			capture = schemas.GetA2ARawCapture(ctx)
			base := &rawCaptureTransport{replay: true, body: "{}"}
			if push {
				m := &Manager{pushDeliveryClient: &http.Client{Transport: base}}
				require.NoError(t, m.sendPushDownstream(ctx, &schemas.AgentPushConfig{URL: "http://callback"}, payload))
			} else {
				req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://agent", bytes.NewBufferString(payload))
				require.NoError(t, err)
				resp, err := (&credentialTransport{base: base}).RoundTrip(req)
				require.NoError(t, err)
				defer resp.Body.Close()
				_, err = io.ReadAll(resp.Body)
				require.NoError(t, err)
			}
			request, _ := capture.Snapshot()
			require.NotNil(t, request)
			decoded, err := base64.StdEncoding.DecodeString(*request)
			require.NoError(t, err)
			require.Equal(t, payload, string(decoded))
		})
	}
}

// TestRawCaptureIncompleteBody verifies bounded reads never expose a prefix as a complete body.
func TestRawCaptureIncompleteBody(t *testing.T) {
	for _, size := range []int{1 << 20, (1 << 20) + 1} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			capture := &schemas.A2ARawCapture{}
			body := &captureReadCloser{ReadCloser: io.NopCloser(bytes.NewReader(make([]byte, size))), capture: capture}
			_, err := io.Copy(io.Discard, io.LimitReader(body, 1<<20))
			require.NoError(t, err)
			require.NoError(t, body.Close())
			_, response := capture.Snapshot()
			require.Nil(t, response)
		})
	}
	captureJSON := &schemas.A2ARawCapture{}
	largeJSON := append([]byte(`{"error":"`), bytes.Repeat([]byte("x"), 1<<20)...)
	largeJSON = append(largeJSON, []byte(`"}`)...)
	request, err := http.NewRequest(http.MethodPost, "http://agent", nil)
	require.NoError(t, err)
	request.Header.Set("Accept", "text/event-stream")
	responseJSON := &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}}, Body: &captureReadCloser{ReadCloser: io.NopCloser(bytes.NewReader(largeJSON)), capture: captureJSON, expected: int64(len(largeJSON))}}
	wrapJSONStreamError(request, responseJSON)
	_, rawJSON := captureJSON.Snapshot()
	require.Nil(t, rawJSON)

	capture := &schemas.A2ARawCapture{}
	body := &captureReadCloser{ReadCloser: io.NopCloser(bytes.NewBufferString("complete")), capture: capture, expected: 8}
	buffer := make([]byte, 8)
	_, err = io.ReadFull(body, buffer)
	require.NoError(t, err)
	_, response := capture.Snapshot()
	require.NotNil(t, response)
}

// TestRawCaptureOperationLifetime verifies post-hooks resolve capture state before cleanup.
func TestRawCaptureOperationLifetime(t *testing.T) {
	for _, outcome := range []string{"success", "error", "panic", "short-circuit", "no-pipeline"} {
		t.Run(outcome, func(t *testing.T) {
			m := &Manager{rawCaptureFn: func() bool { return true }}
			ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
			pipeline := &ingressCapturePipeline{PluginPipeline: &fakePipeline{}}
			if outcome == "short-circuit" {
				order := []string{}
				pipeline.PluginPipeline = &fakePipeline{plugins: []*recordingA2APlugin{{order: &order, shortCircuit: &schemas.A2APluginShortCircuit{}}}}
			}
			if outcome != "no-pipeline" {
				m.SetPluginPipeline(func() PluginPipeline { return pipeline }, nil)
			}
			_, _ = m.RunWithPluginPipeline(ctx, &schemas.BifrostA2ARequest{}, func(*schemas.BifrostA2ARequest, func(*schemas.BifrostA2AEvent)) (*schemas.BifrostA2AResponse, error) {
				capture := schemas.GetA2ARawCapture(ctx)
				require.NotNil(t, capture)
				capture.Append([]byte("request"), true)
				switch outcome {
				case "panic":
					panic("operation failed")
				case "error":
					return nil, errors.New("operation failed")
				}
				return &schemas.BifrostA2AResponse{}, nil
			})
			if outcome != "no-pipeline" {
				require.True(t, pipeline.sawCapture, "post-hooks must run before capture cleanup")
				if outcome != "short-circuit" {
					require.NotNil(t, pipeline.request)
				}
			}
			require.Nil(t, schemas.GetA2ARawCapture(ctx), "completed operations must release capture state")
		})
	}
}

// TestRawCaptureSharedRequestID verifies overlapping operations own separate captures.
func TestRawCaptureSharedRequestID(t *testing.T) {
	m := &Manager{rawCaptureFn: func() bool { return true }}
	ctxA := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctxB := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctxA.SetValue(schemas.BifrostContextKeyRequestID, "shared-request")
	ctxB.SetValue(schemas.BifrostContextKeyRequestID, "shared-request")
	_, err := m.RunWithPluginPipeline(ctxA, nil, func(*schemas.BifrostA2ARequest, func(*schemas.BifrostA2AEvent)) (*schemas.BifrostA2AResponse, error) {
		captureA := schemas.GetA2ARawCapture(ctxA)
		require.NotNil(t, captureA)
		captureA.Append([]byte("body A"), true)
		_, err := m.RunWithPluginPipeline(ctxB, nil, func(*schemas.BifrostA2ARequest, func(*schemas.BifrostA2AEvent)) (*schemas.BifrostA2AResponse, error) {
			captureB := schemas.GetA2ARawCapture(ctxB)
			require.NotNil(t, captureB)
			require.NotSame(t, captureA, captureB)
			captureB.Append([]byte("body B"), true)
			require.Same(t, captureA, schemas.GetA2ARawCapture(ctxA))
			raw, _ := captureB.Snapshot()
			require.NotNil(t, raw)
			require.Equal(t, base64.StdEncoding.EncodeToString([]byte("body B")), *raw)
			return nil, nil
		})
		require.Nil(t, err)
		require.Nil(t, schemas.GetA2ARawCapture(ctxB))
		require.Same(t, captureA, schemas.GetA2ARawCapture(ctxA), "another operation's cleanup must not delete this capture")
		raw, _ := captureA.Snapshot()
		require.NotNil(t, raw)
		require.Equal(t, base64.StdEncoding.EncodeToString([]byte("body A")), *raw)
		return nil, nil
	})
	require.Nil(t, err)
	require.Nil(t, schemas.GetA2ARawCapture(ctxA))
	require.Equal(t, "shared-request", ctxA.Value(schemas.BifrostContextKeyRequestID))
	require.Equal(t, "shared-request", ctxB.Value(schemas.BifrostContextKeyRequestID))
}
