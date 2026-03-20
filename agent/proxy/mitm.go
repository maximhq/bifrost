package proxy

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/textproto"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/maximhq/bifrost/agent/config"
	"github.com/maximhq/bifrost/agent/tunnel"
	"golang.org/x/net/http2"
)

// #region agent log
func agentDebugLog(id, message string, data map[string]interface{}, hypothesisID string) {
	entry := map[string]interface{}{
		"sessionId":    "8579a9",
		"id":           id,
		"timestamp":    time.Now().UnixMilli(),
		"location":     "mitm.go(agent)",
		"message":      message,
		"data":         data,
		"hypothesisId": hypothesisID,
	}
	line, _ := json.Marshal(entry)
	f, err := os.OpenFile("/Users/akshay/Codebase/universe/bifrost/.cursor/debug-8579a9.log", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err == nil {
		f.Write(append(line, '\n'))
		f.Close()
	}
}

// #endregion

type gatewayForwarder interface {
	ForwardRoundTrip(req *http.Request) (*http.Response, error)
}

type requestDoer interface {
	Do(req *http.Request) (*http.Response, error)
}

// MITMProxy terminates TLS for intercepted AI API connections, rewrites HTTP
// headers to route through the Bifrost gateway, and relays responses back.
type MITMProxy struct {
	certStore       *CertStore
	forwarder       gatewayForwarder // for gateway requests
	directForwarder requestDoer      // for direct relay (bypasses TUN)
	gatewayURL      string
	virtualKey      string
}

// NewMITMProxy creates a MITM proxy instance.
func NewMITMProxy(certStore *CertStore, gatewayURL string, virtualKey string) *MITMProxy {
	return newMITMProxyWithClients(
		certStore,
		gatewayURL,
		virtualKey,
		NewForwarder(),
		&http.Client{
			Timeout: 0, // no timeout for streaming
			Transport: &http.Transport{
				DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
					// Bypass TUN by dialing through the physical interface
					host, port, _ := net.SplitHostPort(addr)
					p := 443
					if port != "" {
						fmt.Sscanf(port, "%d", &p)
					}
					return tunnel.DialBypassTUN(host, p)
				},
				TLSClientConfig:     &tls.Config{},
				MaxIdleConns:        100,
				MaxIdleConnsPerHost: 20,
				IdleConnTimeout:     90 * time.Second,
				ForceAttemptHTTP2:   true,
			},
		},
	)
}

func newMITMProxyWithClients(
	certStore *CertStore,
	gatewayURL string,
	virtualKey string,
	forwarder gatewayForwarder,
	directForwarder requestDoer,
) *MITMProxy {
	return &MITMProxy{
		certStore:       certStore,
		forwarder:       forwarder,
		directForwarder: directForwarder,
		gatewayURL:      gatewayURL,
		virtualKey:      virtualKey,
	}
}

// HandleConnection performs the full MITM flow on an intercepted TCP connection:
//  1. Generate a per-domain TLS certificate signed by the org CA
//  2. Complete the TLS handshake with the client
//  3. Read the HTTP request from the decrypted stream
//  4. Rewrite headers (inject x-bf-vk, change Host/URL to gateway)
//  5. Forward to the Bifrost gateway
//  6. Relay the gateway's response back through the TLS connection
func (p *MITMProxy) HandleConnection(conn net.Conn, hostname string, rule *config.DomainRule) {
	defer conn.Close()

	// Step 1: Get or generate a TLS certificate for this domain
	cert, err := p.certStore.GetOrCreate(hostname)
	if err != nil {
		log.Printf("cert generation failed for %s: %v", hostname, err)
		return
	}

	// Step 2: TLS handshake with the client, presenting our forged cert
	tlsConfig := &tls.Config{
		Certificates: []tls.Certificate{*cert},
		NextProtos:   []string{"h2", "http/1.1"},
	}
	tlsConn := tls.Server(conn, tlsConfig)
	tlsConn.SetDeadline(time.Now().Add(10 * time.Second))
	if err := tlsConn.Handshake(); err != nil {
		log.Printf("TLS handshake failed for %s: %v", hostname, err)
		return
	}
	tlsConn.SetDeadline(time.Time{})
	defer tlsConn.Close()

	negotiatedProto := tlsConn.ConnectionState().NegotiatedProtocol
	if negotiatedProto == "" {
		negotiatedProto = "http/1.1"
	}
	log.Printf("TLS handshake complete for %s (alpn=%s)", hostname, negotiatedProto)

	// Step 3: Read HTTP request(s) from the decrypted stream.
	// Handle keep-alive by processing multiple requests on the same connection.
	if negotiatedProto == http2.NextProtoTLS {
		p.handleHTTP2(tlsConn, hostname, rule)
	} else {
		p.handleHTTP1(tlsConn, hostname, rule)
	}
}

// handleHTTP1 handles HTTP/1.1 requests on a MITM'd TLS connection.
// Supports keep-alive by processing multiple requests sequentially.
func (p *MITMProxy) handleHTTP1(tlsConn *tls.Conn, hostname string, rule *config.DomainRule) {
	reader := bufio.NewReader(tlsConn)

	for {
		// Read the HTTP request
		req, err := http.ReadRequest(reader)
		if err != nil {
			if err != io.EOF {
				log.Printf("HTTP read error from %s: %v", hostname, err)
			}
			return
		}

		resp, proxied, err := p.roundTripRequest(req, hostname, rule)
		if err != nil {
			writeHTTPError(tlsConn, http.StatusBadGateway, formatForwardError(proxied, err))
			return
		}

		// #region agent log
		respCL := ""
		respTE := ""
		if resp.Header != nil {
			respCL = resp.Header.Get("Content-Length")
			respTE = resp.Header.Get("Transfer-Encoding")
		}
		agentDebugLog("h1-pre-write", "About to write HTTP/1.1 response", map[string]interface{}{
			"path":             req.URL.Path,
			"statusCode":       resp.StatusCode,
			"contentLength":    respCL,
			"transferEncoding": respTE,
			"goContentLength":  resp.ContentLength,
		}, "R")
		// #endregion

		if err := writeHTTPResponse(tlsConn, resp); err != nil {
			// #region agent log
			agentDebugLog("h1-write-err", "writeHTTPResponse failed", map[string]interface{}{
				"path":  req.URL.Path,
				"error": err.Error(),
			}, "R")
			// #endregion
			log.Printf("response write error for %s: %v", hostname, err)
			resp.Body.Close()
			return
		}
		// #region agent log
		agentDebugLog("h1-write-ok", "writeHTTPResponse completed", map[string]interface{}{
			"path": req.URL.Path,
		}, "R")
		// #endregion
		resp.Body.Close()

		// Check if the client wants to keep the connection alive
		if !shouldKeepAlive(req) {
			return
		}
	}
}

func (p *MITMProxy) handleHTTP2(tlsConn *tls.Conn, hostname string, rule *config.DomainRule) {
	server := &http2.Server{}
	server.ServeConn(tlsConn, &http2.ServeConnOpts{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			resp, proxied, err := p.roundTripRequest(req, hostname, rule)
			if err != nil {
				http.Error(w, formatForwardError(proxied, err), http.StatusBadGateway)
				return
			}
			defer resp.Body.Close()

			// #region agent log
			respCT := ""
			respCL := ""
			if resp.Header != nil {
				respCT = resp.Header.Get("Content-Type")
				respCL = resp.Header.Get("Content-Length")
			}
			agentDebugLog("h2-pre-write", "About to write HTTP/2 response to browser", map[string]interface{}{
				"path":            req.URL.Path,
				"statusCode":      resp.StatusCode,
				"contentType":     respCT,
				"contentLength":   respCL,
				"goContentLength": resp.ContentLength,
				"proxied":         proxied,
			}, "U")
			// #endregion

			if err := writeHTTP2Response(w, resp); err != nil {
				// #region agent log
				agentDebugLog("h2-write-err", "writeHTTP2Response failed", map[string]interface{}{
					"path":  req.URL.Path,
					"error": err.Error(),
				}, "U")
				// #endregion
				log.Printf("response write error for %s: %v", hostname, err)
			} else {
				// #region agent log
				agentDebugLog("h2-write-ok", "writeHTTP2Response completed", map[string]interface{}{
					"path": req.URL.Path,
				}, "U")
				// #endregion
			}
		}),
	})
}

func (p *MITMProxy) roundTripRequest(req *http.Request, hostname string, rule *config.DomainRule) (*http.Response, bool, error) {
	normalizeInboundRequest(req, hostname)

	originalPath := req.URL.Path
	requestURL := formatURL(hostname, originalPath, req.URL.RawQuery)
	log.Printf("MITM request: %s %s (proto=%s)", req.Method, requestURL, req.Proto)

	// #region agent log
	cookieVals := req.Header.Values("Cookie")
	totalCookieLen := 0
	for _, c := range cookieVals {
		totalCookieLen += len(c)
	}
	agentDebugLog("inbound-cookie", "Cookie from browser", map[string]interface{}{
		"path":           originalPath,
		"cookieCount":    len(cookieVals),
		"totalCookieLen": totalCookieLen,
		"proto":          req.Proto,
	}, "J")
	// #endregion

	bodyBytes, err := bufferRequestBody(req)
	if err != nil {
		log.Printf("body read error for %s: %v", hostname, err)
		return nil, false, fmt.Errorf("body read error: %w", err)
	}

	// Loop prevention: if the gateway set X-Bf-Agent-Direct, this request originated
	// from the Bifrost gateway's outbound call and was re-captured by the TUN.
	// Relay it directly to the origin to break the loop.
	directHeader := req.Header.Get("X-Bf-Agent-Direct")
	// #region agent log
	agentDebugLog("loop-check", "Checking X-Bf-Agent-Direct header", map[string]interface{}{
		"path":          originalPath,
		"proto":         req.Proto,
		"directHeader":  directHeader,
		"headerCount":   len(req.Header),
	}, "Q")
	// #endregion
	if directHeader != "" {
		req.Header.Del("X-Bf-Agent-Direct")
		log.Printf("MITM → DIRECT (loop prevention): %s %s", req.Method, requestURL)
		req.URL.Host = hostname
		req.URL.Scheme = "https"
		req.Host = hostname
		prepareOutboundRequest(req)
		// #region agent log
		agentDebugLog("loop-direct-start", "Starting direct relay for looped request", map[string]interface{}{
			"path": originalPath,
			"url":  req.URL.String(),
		}, "Q")
		// #endregion
		resp, err := p.directForwarder.Do(req)
		if err != nil {
			// #region agent log
			agentDebugLog("loop-direct-err", "Direct relay failed", map[string]interface{}{
				"path":  originalPath,
				"error": err.Error(),
			}, "Q")
			// #endregion
			log.Printf("direct relay (loop prevention) error for %s: %v", requestURL, err)
			return nil, false, err
		}
		// #region agent log
		agentDebugLog("loop-direct-ok", "Direct relay succeeded", map[string]interface{}{
			"path":       originalPath,
			"statusCode": resp.StatusCode,
		}, "Q")
		// #endregion
		return resp, false, nil
	}

	shouldProxy := rule.ShouldProxyPath(originalPath)
	if shouldProxy {
		log.Printf("MITM → GATEWAY: %s %s", req.Method, requestURL)
		logProxiedRequest(req.Method, hostname, originalPath, req.URL.RawQuery, bodyBytes)

		if err := RewriteRequest(req, rule, p.gatewayURL, p.virtualKey); err != nil {
			log.Printf("rewrite error for %s: %v", hostname, err)
			return nil, true, fmt.Errorf("rewrite error: %w", err)
		}

		prepareOutboundRequest(req)

		// #region agent log
		cookieValsAfter := req.Header.Values("Cookie")
		totalCookieLenAfter := 0
		for _, c := range cookieValsAfter {
			totalCookieLenAfter += len(c)
		}

		cookieMapKeys := 0
		for k := range req.Header {
			if strings.EqualFold(k, "cookie") {
				cookieMapKeys++
			}
		}

		var headerBuf bytes.Buffer
		req.Header.Write(&headerBuf)
		headerStr := headerBuf.String()
		cookieLinesInWire := strings.Count(headerStr, "Cookie:")

		agentDebugLog("outbound-cookie", "Cookie to gateway", map[string]interface{}{
			"path":             originalPath,
			"cookieCount":      len(cookieValsAfter),
			"totalCookieLen":   totalCookieLenAfter,
			"cookieMapKeys":    cookieMapKeys,
			"cookieLinesWire":  cookieLinesInWire,
			"totalHeaderBytes": len(headerStr),
		}, "K")
		// #endregion

		resp, err := p.forwarder.ForwardRoundTrip(req)
		if err != nil {
			log.Printf("gateway forward error for %s: %v", hostname, err)
			return nil, true, err
		}
		return resp, true, nil
	}

	log.Printf("MITM → DIRECT:  %s %s", req.Method, requestURL)
	req.URL.Host = hostname
	req.URL.Scheme = "https"
	req.Host = hostname
	prepareOutboundRequest(req)

	resp, err := p.directForwarder.Do(req)
	if err != nil {
		log.Printf("direct relay error for %s: %v", requestURL, err)
		return nil, false, err
	}
	return resp, false, nil
}

// writeHTTPResponse writes an HTTP response to a raw connection.
func writeHTTPResponse(w io.Writer, resp *http.Response) error {
	// Write status line
	statusLine := fmt.Sprintf("HTTP/1.1 %d %s\r\n", resp.StatusCode, http.StatusText(resp.StatusCode))
	if _, err := w.Write([]byte(statusLine)); err != nil {
		return err
	}

	// Write headers
	header := resp.Header.Clone()
	stripHopByHopHeaders(header)

	// HTTP/2 responses often have no Content-Length or Transfer-Encoding. When
	// forwarding over HTTP/1.1, the receiver needs body framing — otherwise it
	// waits forever on a keep-alive connection. Use chunked encoding when the
	// content length is unknown.
	useChunked := header.Get("Content-Length") == "" && resp.ContentLength < 0
	if useChunked {
		header.Set("Transfer-Encoding", "chunked")
	}

	// #region agent log
	hasCL := header.Get("Content-Length") != ""
	hasTE := header.Get("Transfer-Encoding") != ""
	headerKeys := make([]string, 0, len(header))
	for k := range header {
		headerKeys = append(headerKeys, k)
	}
	agentDebugLog("write-resp-headers", "Headers after hop-by-hop strip", map[string]interface{}{
		"hasContentLength":    hasCL,
		"contentLength":       header.Get("Content-Length"),
		"hasTransferEncoding": hasTE,
		"useChunked":          useChunked,
		"headerKeys":          headerKeys,
		"statusCode":          resp.StatusCode,
		"goContentLength":     resp.ContentLength,
	}, "R")
	// #endregion

	for key, values := range header {
		for _, v := range values {
			if _, err := fmt.Fprintf(w, "%s: %s\r\n", key, v); err != nil {
				return err
			}
		}
	}
	if _, err := w.Write([]byte("\r\n")); err != nil {
		return err
	}

	buf := make([]byte, 32*1024)
	totalWritten := 0

	if useChunked {
		// Write body in chunked transfer encoding so the HTTP/1.1 receiver
		// can detect the end of the body without closing the connection.
		for {
			n, err := resp.Body.Read(buf)
			if n > 0 {
				// Write chunk size in hex + CRLF + data + CRLF
				chunkHeader := fmt.Sprintf("%x\r\n", n)
				if _, writeErr := w.Write([]byte(chunkHeader)); writeErr != nil {
					return writeErr
				}
				if _, writeErr := w.Write(buf[:n]); writeErr != nil {
					return writeErr
				}
				if _, writeErr := w.Write([]byte("\r\n")); writeErr != nil {
					return writeErr
				}
				totalWritten += n
			}
			if err != nil {
				// Write terminating chunk: 0\r\n\r\n
				if _, writeErr := w.Write([]byte("0\r\n\r\n")); writeErr != nil {
					return writeErr
				}
				if err == io.EOF {
					// #region agent log
					agentDebugLog("write-resp-body-eof", "Body write complete (EOF, chunked)", map[string]interface{}{
						"totalWritten": totalWritten,
					}, "R")
					// #endregion
					return nil
				}
				// #region agent log
				agentDebugLog("write-resp-body-read-err", "Body read error (chunked)", map[string]interface{}{
					"totalWritten": totalWritten,
					"error":        err.Error(),
				}, "R")
				// #endregion
				return err
			}
		}
	}

	// Content-Length is known: stream body directly
	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			if _, writeErr := w.Write(buf[:n]); writeErr != nil {
				// #region agent log
				agentDebugLog("write-resp-body-err", "Body write failed", map[string]interface{}{
					"totalWritten": totalWritten,
					"error":        writeErr.Error(),
				}, "R")
				// #endregion
				return writeErr
			}
			totalWritten += n
			if f, ok := w.(*bufio.Writer); ok {
				f.Flush()
			}
		}
		if err != nil {
			if err == io.EOF {
				// #region agent log
				agentDebugLog("write-resp-body-eof", "Body write complete (EOF)", map[string]interface{}{
					"totalWritten": totalWritten,
				}, "R")
				// #endregion
				return nil
			}
			// #region agent log
			agentDebugLog("write-resp-body-read-err", "Body read error", map[string]interface{}{
				"totalWritten": totalWritten,
				"error":        err.Error(),
			}, "R")
			// #endregion
			return err
		}
	}
}

func writeHTTP2Response(w http.ResponseWriter, resp *http.Response) error {
	header := w.Header()
	copyHeaders(header, resp.Header)
	stripHopByHopHeaders(header)

	w.WriteHeader(resp.StatusCode)

	flusher, _ := w.(http.Flusher)
	buf := make([]byte, 32*1024)
	totalWritten := 0
	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			if _, writeErr := w.Write(buf[:n]); writeErr != nil {
				// #region agent log
				agentDebugLog("h2-body-write-err", "HTTP/2 body write failed", map[string]interface{}{
					"totalWritten": totalWritten,
					"error":        writeErr.Error(),
				}, "U")
				// #endregion
				return writeErr
			}
			totalWritten += n
			if flusher != nil {
				flusher.Flush()
			}
		}
		if err != nil {
			if err == io.EOF {
				// #region agent log
				agentDebugLog("h2-body-eof", "HTTP/2 body write complete", map[string]interface{}{
					"totalWritten": totalWritten,
				}, "U")
				// #endregion
				return nil
			}
			// #region agent log
			agentDebugLog("h2-body-read-err", "HTTP/2 body read error", map[string]interface{}{
				"totalWritten": totalWritten,
				"error":        err.Error(),
			}, "U")
			// #endregion
			return err
		}
	}
}

// writeHTTPError writes an HTTP error response.
func writeHTTPError(w io.Writer, code int, msg string) {
	resp := fmt.Sprintf("HTTP/1.1 %d %s\r\nContent-Type: text/plain\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s",
		code, http.StatusText(code), len(msg), msg)
	w.Write([]byte(resp))
}

// shouldKeepAlive returns true if the request wants a persistent connection.
func shouldKeepAlive(req *http.Request) bool {
	if req.Close {
		return false
	}
	conn := req.Header.Get("Connection")
	if strings.EqualFold(conn, "close") {
		return false
	}
	// HTTP/1.1 defaults to keep-alive
	return req.ProtoAtLeast(1, 1)
}

const maxPayloadLog = 2048 // truncate logged payloads to 2KB

// logProxiedRequest logs a proxied request with its URL and payload.
func logProxiedRequest(method, hostname, path, rawQuery string, body []byte) {
	url := formatURL(hostname, path, rawQuery)

	if len(body) == 0 {
		log.Printf("PROXY: %s %s (no body)", method, url)
		return
	}

	payload := string(body)
	if len(payload) > maxPayloadLog {
		payload = payload[:maxPayloadLog] + fmt.Sprintf("... (%d bytes truncated)", len(body)-maxPayloadLog)
	}

	log.Printf("PROXY: %s %s\n  payload: %s", method, url, payload)
}

func normalizeInboundRequest(req *http.Request, hostname string) {
	if req.URL == nil {
		req.URL = &url.URL{}
	}
	if req.URL.Host == "" {
		req.URL.Host = hostname
	}
	if req.URL.Scheme == "" {
		req.URL.Scheme = "https"
	}
	if req.Host == "" {
		req.Host = hostname
	}
}

func bufferRequestBody(req *http.Request) ([]byte, error) {
	if req.Body == nil {
		return nil, nil
	}

	bodyBytes, err := io.ReadAll(req.Body)
	req.Body.Close()
	if err != nil {
		return nil, err
	}
	req.Body = io.NopCloser(bytes.NewReader(bodyBytes))
	return bodyBytes, nil
}

func prepareOutboundRequest(req *http.Request) {
	req.RequestURI = ""
	stripHopByHopHeaders(req.Header)
}

func stripHopByHopHeaders(header http.Header) {
	for _, value := range header.Values("Connection") {
		for _, token := range strings.Split(value, ",") {
			token = textproto.TrimString(token)
			if token != "" {
				header.Del(textproto.CanonicalMIMEHeaderKey(token))
			}
		}
	}

	for _, key := range []string{
		"Connection",
		"Proxy-Connection",
		"Keep-Alive",
		"Proxy-Authenticate",
		"Proxy-Authorization",
		"Te",
		"Trailer",
		"Transfer-Encoding",
		"Upgrade",
	} {
		header.Del(key)
	}
}

func copyHeaders(dst, src http.Header) {
	for key, values := range src {
		for _, value := range values {
			dst.Add(key, value)
		}
	}
}

func formatURL(hostname, path, rawQuery string) string {
	url := "https://" + hostname + path
	if rawQuery != "" {
		url += "?" + rawQuery
	}
	return url
}

func formatForwardError(proxied bool, err error) string {
	if proxied {
		return "gateway error: " + err.Error()
	}
	return "origin error: " + err.Error()
}
