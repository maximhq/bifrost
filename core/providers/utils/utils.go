// Package providers implements various LLM providers and their utility functions.
// This file contains common utility functions used across different provider implementations.
package utils

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net"
	"net/http"
	"net/textproto"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/network"
	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
	"github.com/valyala/fasthttp"
	"github.com/valyala/fasthttp/fasthttpproxy"
)

// sortedAPI is a sonic encoder/decoder that sorts map keys during marshaling.
// This ensures deterministic JSON output for map[string]interface{} values,
// which is critical for LLM prompt caching (e.g., Anthropic cache keying).
var sortedAPI = sonic.Config{SortMapKeys: true}.Froze()

// MarshalSorted marshals v to JSON with map keys sorted alphabetically.
func MarshalSorted(v interface{}) ([]byte, error) {
	return sortedAPI.Marshal(v)
}

// MarshalSortedIndent marshals v to indented JSON with map keys sorted alphabetically.
func MarshalSortedIndent(v interface{}, prefix, indent string) ([]byte, error) {
	return sortedAPI.MarshalIndent(v, prefix, indent)
}

// SetJSONField sets a field in JSON bytes without disturbing other fields' ordering.
// Uses in-place byte manipulation for minimal allocations and preserves nested structure.
func SetJSONField(data []byte, path string, value interface{}) ([]byte, error) {
	return sjson.SetBytes(data, path, value)
}

// DeleteJSONField deletes a field from JSON bytes without disturbing other fields' ordering.
// Uses in-place byte manipulation for minimal allocations and preserves nested structure.
func DeleteJSONField(data []byte, path string) ([]byte, error) {
	return sjson.DeleteBytes(data, path)
}

// JSONFieldExists checks if a field exists in JSON bytes.
func JSONFieldExists(data []byte, path string) bool {
	return gjson.GetBytes(data, path).Exists()
}

// GetJSONField retrieves a field value from JSON bytes without parsing the entire document.
func GetJSONField(data []byte, path string) gjson.Result {
	return gjson.GetBytes(data, path)
}

// logger is the global logger for the provider utils (thread-safe via atomic.Pointer).
var logger atomic.Pointer[schemas.Logger]

// noopLogger is a no-op implementation of schemas.Logger.
type noopLogger struct{}

func (noopLogger) Debug(string, ...any)                   {}
func (noopLogger) Info(string, ...any)                    {}
func (noopLogger) Warn(string, ...any)                    {}
func (noopLogger) Error(string, ...any)                   {}
func (noopLogger) Fatal(string, ...any)                   {}
func (noopLogger) SetLevel(schemas.LogLevel)              {}
func (noopLogger) SetOutputType(schemas.LoggerOutputType) {}
func (noopLogger) LogHTTPRequest(schemas.LogLevel, string) schemas.LogEventBuilder {
	return schemas.NoopLogEvent
}

// Initialize with noop logger
func init() {
	var noop schemas.Logger = &noopLogger{}
	logger.Store(&noop)
}

// SetLogger sets the logger for the provider utils (thread-safe).
func SetLogger(l schemas.Logger) {
	logger.Store(&l)
}

// getLogger returns the current logger (thread-safe).
func getLogger() schemas.Logger {
	return *logger.Load()
}

var UnsupportedSpeechStreamModels = []string{"tts-1", "tts-1-hd"}

// noop is a reusable no-op function returned by MakeRequestWithContext on the normal path.
var noop = func() {}

// MakeRequestWithContext makes a request with a context and returns the latency, error, and a
// wait function. The wait function MUST be called (typically via defer) before releasing the
// request or response objects. On the normal path it is a no-op. On the context-cancellation
// path it blocks until the background client.Do goroutine finishes, preventing a data race
// between the still-running goroutine and the caller's release of req/resp.
//
// IMPORTANT: This function does NOT truly cancel the underlying fasthttp network request if the
// context is done. The fasthttp client call will continue in its goroutine until it completes
// or times out based on its own settings. This function merely stops *waiting* for the
// fasthttp call and returns an error related to the context.
func MakeRequestWithContext(ctx context.Context, client *fasthttp.Client, req *fasthttp.Request, resp *fasthttp.Response) (time.Duration, *schemas.BifrostError, func()) {
	startTime := time.Now()
	errChan := make(chan error, 1)

	go func() {
		// client.Do is a blocking call.
		// It will send an error (or nil for success) to errChan when it completes.
		errChan <- client.Do(req, resp)
	}()

	select {
	case <-ctx.Done():
		// Context was cancelled (e.g., deadline exceeded or manual cancellation).
		// Calculate latency even for cancelled requests
		latency := time.Since(startTime)
		// Return a wait function that blocks until the background goroutine finishes.
		// The caller MUST invoke this (via defer) before releasing req/resp to avoid
		// a data race with the still-running client.Do goroutine.
		return latency, &schemas.BifrostError{
			IsBifrostError: true,
			Error: &schemas.ErrorField{
				Type:    schemas.Ptr(schemas.RequestCancelled),
				Message: fmt.Sprintf("Request cancelled or timed out by context: %v", ctx.Err()),
				Error:   ctx.Err(),
			},
		}, func() { <-errChan }
	case err := <-errChan:
		// The fasthttp.Do call completed.
		// Calculate latency for both successful and failed requests
		latency := time.Since(startTime)
		if err != nil {
			if errors.Is(err, context.Canceled) {
				return latency, &schemas.BifrostError{
					IsBifrostError: false,
					Error: &schemas.ErrorField{
						Type:    schemas.Ptr(schemas.RequestCancelled),
						Message: schemas.ErrRequestCancelled,
						Error:   err,
					},
				}, noop
			}
			// Check for timeout errors first before checking net.OpError to avoid misclassification
			if errors.Is(err, fasthttp.ErrTimeout) || errors.Is(err, context.DeadlineExceeded) {
				return latency, NewBifrostOperationError(schemas.ErrProviderRequestTimedOut, err, ""), noop
			}
			// Check if error implements net.Error and has Timeout() == true
			var netErr net.Error
			if errors.As(err, &netErr) && netErr.Timeout() {
				return latency, NewBifrostOperationError(schemas.ErrProviderRequestTimedOut, err, ""), noop
			}
			// Check for DNS lookup and network errors after timeout checks
			var opErr *net.OpError
			var dnsErr *net.DNSError
			if errors.As(err, &opErr) || errors.As(err, &dnsErr) {
				return latency, &schemas.BifrostError{
					IsBifrostError: false,
					Error: &schemas.ErrorField{
						Message: schemas.ErrProviderNetworkError,
						Error:   err,
					},
				}, noop
			}
			// The HTTP request itself failed (e.g., connection error, fasthttp timeout).
			return latency, &schemas.BifrostError{
				IsBifrostError: false,
				Error: &schemas.ErrorField{
					Message: schemas.ErrProviderDoRequest,
					Error:   err,
				},
			}, noop
		}
		// HTTP request was successful from fasthttp's perspective (err is nil).
		// The caller should check resp.StatusCode() for HTTP-level errors (4xx, 5xx).
		return latency, nil, noop
	}
}

// Deprecated: ConfigureRetry is now handled internally by ConfigureDialer.
// This function is kept for backward compatibility but is no longer needed.
func ConfigureRetry(client *fasthttp.Client) *fasthttp.Client {
	client.RetryIfErr = network.StaleConnectionRetryIfErr
	return client
}

// ConfigureDialer configures the client's connection behavior:
//  1. Sets up the stale-connection retry policy (see network.StaleConnectionRetryIfErr).
//  2. Wraps the Dial function to enable TCP keepalive on all connections,
//     proactively detecting dead connections before fasthttp tries to reuse them.
//
// Must be called AFTER ConfigureProxy (which may set client.Dial to a proxy
// dialer), so the keepalive wrapper composes on top of the proxy connection.
//
// Keepalive parameters:
//   - Idle 10s: first probe after 10s of inactivity (well under the 30s MaxIdleConnDuration)
//   - Interval 5s: subsequent probes every 5s
//   - Count 3: close after 3 failed probes
//
// Dead connections are detected within ~25s (10 + 5*3), before the 30s
// MaxIdleConnDuration expires and the connection is reused.
func ConfigureDialer(client *fasthttp.Client) *fasthttp.Client {
	// Configure stale-connection retry policy
	client.RetryIfErr = network.StaleConnectionRetryIfErr

	existingDial := client.Dial
	existingDialTimeout := client.DialTimeout

	keepAliveCfg := net.KeepAliveConfig{
		Enable:   true,
		Idle:     10 * time.Second,
		Interval: 5 * time.Second,
		Count:    3,
	}

	client.Dial = func(addr string) (net.Conn, error) {
		var conn net.Conn
		var err error

		switch {
		case existingDial != nil:
			// Proxy or custom dial function is set — use it, then enable keepalive
			conn, err = existingDial(addr)
		case existingDialTimeout != nil:
			// Preserve dial-timeout behavior
			conn, err = existingDialTimeout(addr, client.ReadTimeout)
		default:
			conn, err = (&net.Dialer{
				Timeout:         client.ReadTimeout,
				KeepAliveConfig: keepAliveCfg,
			}).Dial("tcp", addr)
		}
		if err != nil {
			return nil, err
		}

		// Enable TCP keepalive on the connection
		if tcpConn, ok := conn.(*net.TCPConn); ok {
			_ = tcpConn.SetKeepAliveConfig(keepAliveCfg)
		}
		return conn, nil
	}

	return client
}

// ConfigureProxy sets up a proxy for the fasthttp client based on the provided configuration.
// It supports HTTP, SOCKS5, and environment-based proxy configurations.
// Returns the configured client or the original client if proxy configuration is invalid.
func ConfigureProxy(client *fasthttp.Client, proxyConfig *schemas.ProxyConfig, logger schemas.Logger) *fasthttp.Client {
	if proxyConfig == nil {
		return client
	}

	var dialFunc fasthttp.DialFunc
	// Create the appropriate proxy based on type
	switch proxyConfig.Type {
	case schemas.NoProxy:
		return client
	case schemas.HTTPProxy:
		if proxyConfig.URL == "" {
			getLogger().Warn("Warning: HTTP proxy URL is required for setting up proxy")
			return client
		}
		proxyURL := proxyConfig.URL
		if proxyConfig.Username != "" && proxyConfig.Password != "" {
			parsedURL, err := url.Parse(proxyConfig.URL)
			if err != nil {
				getLogger().Warn("Invalid proxy configuration: invalid HTTP proxy URL")
				return client
			}
			// Set user and password in the parsed URL
			parsedURL.User = url.UserPassword(proxyConfig.Username, proxyConfig.Password)
			proxyURL = parsedURL.String()
		}
		dialFunc = fasthttpproxy.FasthttpHTTPDialer(proxyURL)
	case schemas.Socks5Proxy:
		if proxyConfig.URL == "" {
			getLogger().Warn("Warning: SOCKS5 proxy URL is required for setting up proxy")
			return client
		}
		proxyURL := proxyConfig.URL
		// Add authentication if provided
		if proxyConfig.Username != "" && proxyConfig.Password != "" {
			parsedURL, err := url.Parse(proxyConfig.URL)
			if err != nil {
				getLogger().Warn("Invalid proxy configuration: invalid SOCKS5 proxy URL")
				return client
			}
			// Set user and password in the parsed URL
			parsedURL.User = url.UserPassword(proxyConfig.Username, proxyConfig.Password)
			proxyURL = parsedURL.String()
		}
		dialFunc = fasthttpproxy.FasthttpSocksDialer(proxyURL)
	case schemas.EnvProxy:
		// Use environment variables for proxy configuration
		dialFunc = fasthttpproxy.FasthttpProxyHTTPDialer()
	default:
		getLogger().Warn("Invalid proxy configuration: unsupported proxy type: %s", proxyConfig.Type)
		return client
	}

	if dialFunc != nil {
		client.Dial = dialFunc
	}

	// Configure custom CA certificate if provided
	if proxyConfig.CACertPEM != "" {
		tlsConfig, err := createTLSConfigWithCA(proxyConfig.CACertPEM)
		if err != nil {
			getLogger().Warn("Failed to configure custom CA certificate: %v", err)
		} else {
			client.TLSConfig = tlsConfig
		}
	}

	return client
}

// createTLSConfigWithCA creates a TLS configuration with a custom CA certificate
// appended to the system root CA pool.
func createTLSConfigWithCA(caCertPEM string) (*tls.Config, error) {
	// Get the system root CA pool
	rootCAs, err := x509.SystemCertPool()
	if err != nil {
		// If we can't get system certs, create a new pool
		rootCAs = x509.NewCertPool()
	}

	// Append the custom CA certificate
	if !rootCAs.AppendCertsFromPEM([]byte(caCertPEM)) {
		return nil, fmt.Errorf("failed to parse CA certificate PEM")
	}

	return &tls.Config{
		RootCAs:    rootCAs,
		MinVersion: tls.VersionTLS12,
	}, nil
}

// ConfigureTLS applies TLS settings from NetworkConfig to the fasthttp client.
// It merges with any existing TLSConfig (e.g., from ConfigureProxy).
func ConfigureTLS(client *fasthttp.Client, networkConfig schemas.NetworkConfig, logger schemas.Logger) *fasthttp.Client {
	if !networkConfig.InsecureSkipVerify && networkConfig.CACertPEM == "" {
		return client
	}

	tlsConfig := client.TLSConfig
	if tlsConfig == nil {
		tlsConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	} else {
		tlsConfig = tlsConfig.Clone()
	}

	if networkConfig.InsecureSkipVerify {
		logger.Warn("insecure_skip_verify is enabled for provider — TLS certificate verification is disabled. Not recommended for production.")
		tlsConfig.InsecureSkipVerify = true
	}

	if networkConfig.CACertPEM != "" {
		caTLSConfig, err := createTLSConfigWithCA(networkConfig.CACertPEM)
		if err != nil {
			logger.Warn("Failed to configure custom CA certificate for provider: %v", err)
		} else {
			if tlsConfig.RootCAs != nil {
				tlsConfig.RootCAs = tlsConfig.RootCAs.Clone()
				// Merge: append network CA to existing pool (e.g. from proxy)
				if !tlsConfig.RootCAs.AppendCertsFromPEM([]byte(networkConfig.CACertPEM)) {
					logger.Warn("Failed to append CA certificate to existing TLS config")
				}
			} else {
				tlsConfig.RootCAs = caTLSConfig.RootCAs
			}
		}
	}

	client.TLSConfig = tlsConfig
	return client
}

// hopByHopHeaders are HTTP/1.1 headers that must not be forwarded by proxies.
var hopByHopHeaders = map[string]bool{
	"connection":          true,
	"proxy-connection":    true,
	"keep-alive":          true,
	"proxy-authenticate":  true,
	"proxy-authorization": true,
	"te":                  true,
	"trailer":             true,
	"transfer-encoding":   true,
	"upgrade":             true,
}

// filterHeaders filters out hop-by-hop headers and returns only the allowed headers.
func filterHeaders(headers map[string][]string) map[string][]string {
	filtered := make(map[string][]string, len(headers))
	for k, v := range headers {
		if !hopByHopHeaders[strings.ToLower(k)] {
			filtered[k] = v
		}
	}
	return filtered
}

// providerResponseFilterHeaders are headers to exclude when forwarding provider response headers.
// These are transport-level headers that don't apply when re-serving the response.
var providerResponseFilterHeaders = map[string]bool{
	"content-length":                   true,
	"content-encoding":                 true,
	"transfer-encoding":                true,
	"connection":                       true,
	"keep-alive":                       true,
	"proxy-connection":                 true,
	"proxy-authenticate":               true,
	"proxy-authorization":              true,
	"authorization":                    true,
	"cookie":                           true,
	"set-cookie":                       true,
	"set-cookie2":                      true,
	"www-authenticate":                 true,
	"te":                               true,
	"trailer":                          true,
	"upgrade":                          true,
	"host":                             true,
	"date":                             true,
	"server":                           true,
	"alt-svc":                          true,
	"strict-transport-security":        true,
	"content-type":                     true,
	"access-control-allow-origin":      true,
	"access-control-allow-methods":     true,
	"access-control-allow-headers":     true,
	"access-control-expose-headers":    true,
	"access-control-allow-credentials": true,
	"access-control-max-age":           true,
}

// ExtractProviderResponseHeaders extracts and filters response headers from a
// fasthttp response. Transport-level headers are excluded.
func ExtractProviderResponseHeaders(resp *fasthttp.Response) map[string]string {
	if resp == nil {
		return nil
	}
	headers := make(map[string]string)
	resp.Header.VisitAll(func(key, value []byte) {
		k := string(key)
		if providerResponseFilterHeaders[strings.ToLower(k)] {
			return
		}
		v := string(value)
		if existing, ok := headers[k]; ok && existing != "" {
			headers[k] = existing + ", " + v
		} else {
			headers[k] = v
		}
	})
	if len(headers) == 0 {
		return nil
	}
	return headers
}

// ExtractProviderResponseHeadersFromHTTP extracts and filters response headers
// from a standard net/http response. Transport-level headers are excluded.
// Used by providers like Bedrock that use net/http instead of fasthttp.
func ExtractProviderResponseHeadersFromHTTP(resp *http.Response) map[string]string {
	if resp == nil {
		return nil
	}
	headers := make(map[string]string)
	for k, values := range resp.Header {
		if !providerResponseFilterHeaders[strings.ToLower(k)] && len(values) > 0 {
			headers[k] = strings.Join(values, ", ")
		}
	}
	if len(headers) == 0 {
		return nil
	}
	return headers
}

// SetExtraHeaders sets additional headers from NetworkConfig to the fasthttp request.
// This allows users to configure custom headers for their provider requests.
// Header keys are canonicalized using textproto.CanonicalMIMEHeaderKey to avoid duplicates.
// It accepts a list of headers (all canonicalized) to skip for security reasons.
// Headers are only set if they don't already exist on the request to avoid overwriting important headers.
func SetExtraHeaders(ctx context.Context, req *fasthttp.Request, extraHeaders map[string]string, skipHeaders []string) {
	for key, value := range extraHeaders {
		canonicalKey := textproto.CanonicalMIMEHeaderKey(key)
		if skipHeaders != nil {
			if slices.Contains(skipHeaders, key) {
				continue
			}
		}
		// Only set the header if it doesn't already exist to avoid overwriting important headers
		if len(req.Header.Peek(canonicalKey)) == 0 {
			req.Header.Set(canonicalKey, value)
		}
	}
	// Give priority to extra headers in the context
	if extraHeaders, ok := (ctx).Value(schemas.BifrostContextKeyExtraHeaders).(map[string][]string); ok {
		for k, values := range filterHeaders(extraHeaders) {
			for i, v := range values {
				if i == 0 {
					req.Header.Set(k, v)
				} else {
					req.Header.Add(k, v)
				}
			}
		}
	}
}

// GetPathFromContext gets the path from the context, if it exists, otherwise returns the default path.
func GetPathFromContext(ctx context.Context, defaultPath string) string {
	if pathInContext, ok := ctx.Value(schemas.BifrostContextKeyURLPath).(string); ok {
		return pathInContext
	}
	return defaultPath
}

// GetRequestPath gets the request path from the context, if it exists, checking for path overrides in the custom provider config.
// It returns the resolved value and a boolean indicating whether the value is a full absolute URL.
// If the boolean is false, the returned string is a path (leading slash ensured).
func GetRequestPath(ctx context.Context, defaultPath string, customProviderConfig *schemas.CustomProviderConfig, requestType schemas.RequestType) (string, bool) {
	// If path/url set in context, return it.
	if pathInContext, ok := ctx.Value(schemas.BifrostContextKeyURLPath).(string); ok {
		trimmed := strings.TrimSpace(pathInContext)
		if u, err := url.Parse(trimmed); err == nil && u != nil && u.IsAbs() && u.Host != "" {
			return trimmed, true
		}
		return trimmed, false
	}

	// If path override set in custom provider config, return it.
	if customProviderConfig != nil && customProviderConfig.RequestPathOverrides != nil {
		if raw, ok := customProviderConfig.RequestPathOverrides[requestType]; ok {
			override := strings.TrimSpace(raw)
			if override == "" {
				return defaultPath, false
			}

			// Treat absolute URLs with scheme+host as full URLs.
			if u, err := url.Parse(override); err == nil && u != nil && u.IsAbs() && u.Host != "" {
				return override, true
			}

			// Otherwise treat as a path override (ensure leading slash).
			if !strings.HasPrefix(override, "/") {
				override = "/" + override
			}
			return override, false
		}
	}

	// Return default path.
	return defaultPath, false
}

type RequestBodyGetter interface {
	GetRawRequestBody() []byte
}

// CheckAndGetRawRequestBody checks if the raw request body should be used, and returns it if it exists.
func CheckAndGetRawRequestBody(ctx context.Context, request RequestBodyGetter) ([]byte, bool) {
	if rawBody, ok := ctx.Value(schemas.BifrostContextKeyUseRawRequestBody).(bool); ok && rawBody {
		return request.GetRawRequestBody(), true
	}
	return nil, false
}

type RequestBodyWithExtraParams interface {
	GetExtraParams() map[string]interface{}
}

type RequestBodyConverter func() (RequestBodyWithExtraParams, error)

// IsLargePayloadPassthroughEnabled returns true when large payload mode has already
// prepared an upstream body reader in context.
func IsLargePayloadPassthroughEnabled(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	isLargePayload, ok := ctx.Value(schemas.BifrostContextKeyLargePayloadMode).(bool)
	if !ok || !isLargePayload {
		return false
	}
	reader, ok := ctx.Value(schemas.BifrostContextKeyLargePayloadReader).(io.Reader)
	return ok && reader != nil
}

// ApplyLargePayloadRequestBody applies the request body reader from context to the
// outgoing provider request. Returns true when a streaming body was applied.
func ApplyLargePayloadRequestBody(ctx context.Context, req *fasthttp.Request) bool {
	return ApplyLargePayloadRequestBodyWithModelNormalization(ctx, req, "")
}

const largePayloadModelRewriteScanBytes = 256 * 1024

// ApplyLargePayloadRequestBodyWithModelNormalization applies the streaming body
// reader from context and optionally rewrites prefixed model values for JSON
// passthrough requests (for example "openai/gpt-5" -> "gpt-5").
// This preserves low-memory streaming while keeping large-payload behavior
// aligned with the normal parsed path that strips provider prefixes.
func ApplyLargePayloadRequestBodyWithModelNormalization(
	ctx context.Context,
	req *fasthttp.Request,
	defaultProvider schemas.ModelProvider,
) bool {
	if req == nil || !IsLargePayloadPassthroughEnabled(ctx) {
		return false
	}

	bodyReader, _ := ctx.Value(schemas.BifrostContextKeyLargePayloadReader).(io.Reader)
	bodySize := -1
	if contentLength, ok := ctx.Value(schemas.BifrostContextKeyLargePayloadContentLength).(int); ok {
		bodySize = contentLength
	}

	if contentType, ok := ctx.Value(schemas.BifrostContextKeyLargePayloadContentType).(string); ok && contentType != "" {
		ctLower := strings.ToLower(contentType)
		if metadata, ok := ctx.Value(schemas.BifrostContextKeyLargePayloadMetadata).(*schemas.LargePayloadMetadata); ok && metadata != nil {
			if rawModel := strings.TrimSpace(metadata.Model); rawModel != "" && defaultProvider != "" {
				_, normalizedModel := schemas.ParseModelString(rawModel, defaultProvider)
				if normalizedModel != "" && normalizedModel != rawModel {
					if strings.Contains(ctLower, "application/json") {
						rewrittenReader, sizeDelta := RewriteLargePayloadModelInJSONPrefix(bodyReader, rawModel, normalizedModel)
						bodyReader = rewrittenReader
						if bodySize >= 0 {
							bodySize += sizeDelta
						}
					} else if strings.Contains(ctLower, "multipart/form-data") {
						rewrittenReader, sizeDelta := RewriteLargePayloadModelInMultipartPrefix(bodyReader, rawModel, normalizedModel)
						bodyReader = rewrittenReader
						if bodySize >= 0 {
							bodySize += sizeDelta
						}
					}
				}
			}
		}
		req.Header.SetContentType(contentType)
	}
	req.SetBodyStream(bodyReader, bodySize)
	return true
}

// RewriteLargePayloadModelInJSONPrefix reads the first 256KB of a streaming body,
// rewrites the "model" JSON value from fromModel to toModel, and returns a
// combined reader (rewritten prefix + remaining stream) with the size delta.
func RewriteLargePayloadModelInJSONPrefix(reader io.Reader, fromModel, toModel string) (io.Reader, int) {
	if reader == nil || fromModel == "" || toModel == "" || fromModel == toModel {
		return reader, 0
	}
	prefix := make([]byte, largePayloadModelRewriteScanBytes)
	n, err := io.ReadFull(reader, prefix)
	if n == 0 && err != nil {
		return reader, 0
	}
	prefix = prefix[:n]

	rewrittenPrefix, changed := rewriteJSONModelValue(prefix, fromModel, toModel)
	if !changed {
		return io.MultiReader(bytes.NewReader(prefix), reader), 0
	}
	return io.MultiReader(bytes.NewReader(rewrittenPrefix), reader), len(rewrittenPrefix) - len(prefix)
}

func rewriteJSONModelValue(data []byte, fromModel, toModel string) ([]byte, bool) {
	if len(data) == 0 || fromModel == "" || toModel == "" || fromModel == toModel {
		return data, false
	}
	pattern := []byte(`"model"`)
	searchFrom := 0
	for {
		match := bytes.Index(data[searchFrom:], pattern)
		if match < 0 {
			return data, false
		}
		idx := searchFrom + match + len(pattern)

		for idx < len(data) && (data[idx] == ' ' || data[idx] == '\t' || data[idx] == '\r' || data[idx] == '\n') {
			idx++
		}
		if idx >= len(data) || data[idx] != ':' {
			searchFrom += match + len(pattern)
			continue
		}
		idx++
		for idx < len(data) && (data[idx] == ' ' || data[idx] == '\t' || data[idx] == '\r' || data[idx] == '\n') {
			idx++
		}
		if idx >= len(data) || data[idx] != '"' {
			searchFrom += match + len(pattern)
			continue
		}

		valueStart := idx + 1
		valueEnd := valueStart
		escaped := false
		for valueEnd < len(data) {
			ch := data[valueEnd]
			if escaped {
				escaped = false
				valueEnd++
				continue
			}
			if ch == '\\' {
				escaped = true
				valueEnd++
				continue
			}
			if ch == '"' {
				break
			}
			valueEnd++
		}
		if valueEnd >= len(data) {
			return data, false
		}

		if string(data[valueStart:valueEnd]) != fromModel {
			searchFrom = valueEnd + 1
			continue
		}

		rewritten := make([]byte, 0, len(data)-len(fromModel)+len(toModel))
		rewritten = append(rewritten, data[:valueStart]...)
		rewritten = append(rewritten, toModel...)
		rewritten = append(rewritten, data[valueEnd:]...)
		return rewritten, true
	}
}

// RewriteLargePayloadModelInMultipartPrefix reads the first 256KB of a streaming
// multipart body, finds the model form field value, and rewrites it from fromModel
// to toModel. The model field appears early in multipart bodies (typically the first
// form field), so scanning the prefix is sufficient.
func RewriteLargePayloadModelInMultipartPrefix(reader io.Reader, fromModel, toModel string) (io.Reader, int) {
	if reader == nil || fromModel == "" || toModel == "" || fromModel == toModel {
		return reader, 0
	}
	prefix := make([]byte, largePayloadModelRewriteScanBytes)
	n, err := io.ReadFull(reader, prefix)
	if n == 0 && err != nil {
		return reader, 0
	}
	prefix = prefix[:n]

	// In multipart, the model value appears as:
	//   ...name="model"\r\n\r\nopenai/whisper-1\r\n--boundary...
	// A direct byte replacement of fromModel→toModel in the prefix is safe because
	// the model string (e.g. "openai/whisper-1") is unique within the form metadata.
	from := []byte(fromModel)
	to := []byte(toModel)
	if idx := bytes.Index(prefix, from); idx >= 0 {
		rewritten := make([]byte, 0, len(prefix)-len(from)+len(to))
		rewritten = append(rewritten, prefix[:idx]...)
		rewritten = append(rewritten, to...)
		rewritten = append(rewritten, prefix[idx+len(from):]...)
		return io.MultiReader(bytes.NewReader(rewritten), reader), len(rewritten) - len(prefix)
	}
	return io.MultiReader(bytes.NewReader(prefix), reader), 0
}

// DrainLargePayloadRemainder drains any unread bytes from the large payload reader.
// This is useful for request types that may receive an upstream response before the
// incoming client upload is fully consumed (for example, lightweight preflight APIs).
// Example failure this prevents: fronting proxy returns 502/broken-pipe when backend
// responds early while client is still uploading a large body.
func DrainLargePayloadRemainder(ctx context.Context) {
	if !IsLargePayloadPassthroughEnabled(ctx) {
		return
	}
	bodyReader, _ := ctx.Value(schemas.BifrostContextKeyLargePayloadReader).(io.Reader)
	if bodyReader == nil {
		return
	}
	_, _ = io.Copy(io.Discard, bodyReader)
}

// CloneFastHTTPClientConfig creates a fresh fasthttp.Client by copying only
// config fields from base.
// Never copy fasthttp.Client by value: it contains internal pools and locks.
// Example failure this prevents: parallel load regressions with unexpected buffering
// behavior after `cloned := *base` copies of active clients.
func CloneFastHTTPClientConfig(base *fasthttp.Client) *fasthttp.Client {
	if base == nil {
		return &fasthttp.Client{}
	}

	return &fasthttp.Client{
		Transport:                     base.Transport,
		DialTimeout:                   base.DialTimeout,
		Dial:                          base.Dial,
		TLSConfig:                     base.TLSConfig,
		RetryIf:                       base.RetryIf, // nolint:staticcheck
		RetryIfErr:                    base.RetryIfErr,
		ConfigureClient:               base.ConfigureClient,
		Name:                          base.Name,
		MaxConnsPerHost:               base.MaxConnsPerHost,
		MaxIdleConnDuration:           base.MaxIdleConnDuration,
		MaxConnDuration:               base.MaxConnDuration,
		MaxIdemponentCallAttempts:     base.MaxIdemponentCallAttempts,
		ReadBufferSize:                base.ReadBufferSize,
		WriteBufferSize:               base.WriteBufferSize,
		ReadTimeout:                   base.ReadTimeout,
		WriteTimeout:                  base.WriteTimeout,
		MaxResponseBodySize:           base.MaxResponseBodySize,
		MaxConnWaitTimeout:            base.MaxConnWaitTimeout,
		ConnPoolStrategy:              base.ConnPoolStrategy,
		NoDefaultUserAgentHeader:      base.NoDefaultUserAgentHeader,
		DialDualStack:                 base.DialDualStack,
		DisableHeaderNamesNormalizing: base.DisableHeaderNamesNormalizing,
		DisablePathNormalizing:        base.DisablePathNormalizing,
		StreamResponseBody:            base.StreamResponseBody,
	}
}

// decompressBodyStreamIfGzip checks Content-Encoding for gzip and wraps the stream
// with on-the-fly decompression using a pooled gzip.Reader. Clears Content-Encoding
// header so downstream consumers don't double-decompress. Returns original reader
// unchanged if not gzip-encoded or if gzip reader creation fails.
func decompressBodyStreamIfGzip(resp *fasthttp.Response, stream io.Reader) (*gzip.Reader, io.Reader, bool) {
	ce := strings.ToLower(strings.TrimSpace(string(resp.Header.Peek("Content-Encoding"))))
	if !strings.Contains(ce, "gzip") {
		return nil, stream, false
	}
	gz, err := AcquireGzipReader(stream)
	if err != nil {
		ReleaseGzipReader(gz)
		return nil, stream, false
	}
	resp.Header.Del("Content-Encoding")
	return gz, gz, true
}

// DecompressStreamBody returns a reader for consuming the response body, with
// on-the-fly gzip decompression when Content-Encoding indicates gzip. The response
// object is NOT modified (no SetBodyStream call), so the original requestStream
// remains live for proper cleanup by ReleaseStreamingResponse. Clears the
// Content-Encoding header to prevent double-decompression.
//
// Returns:
//   - io.Reader: the reader to use for scanning (gzip reader if gzip-encoded,
//     original body stream otherwise).
//   - func(): cleanup function that releases the gzip reader back to the pool.
//     Must be called (typically via defer) after streaming is complete.
func DecompressStreamBody(resp *fasthttp.Response) (io.Reader, func()) {
	bodyStream := resp.BodyStream()
	if bodyStream == nil {
		// Return an empty reader instead of nil to prevent panics in callers
		// that pass the reader to bufio.NewScanner without nil checks.
		return bytes.NewReader(nil), func() {}
	}
	gz, decompressed, wasGzip := decompressBodyStreamIfGzip(resp, bodyStream)
	if !wasGzip {
		return bodyStream, func() {}
	}
	return decompressed, func() {
		ReleaseGzipReader(gz)
	}
}

// DrainNonSSEStreamResponse checks if the upstream response is a Server-Sent Events stream.
// If not SSE, drains the body to io.Discard to prevent bufio.Scanner buffer bloat on
// non-line-delimited data. Returns true if body was drained (caller should skip scanner).
// We intentionally do not touch valid SSE bodies here: callers must continue reading from
// the reader returned by DecompressStreamBody, and draining SSE in this helper would consume
// the stream before the scanner/manual event loop starts.
func DrainNonSSEStreamResponse(resp *fasthttp.Response) bool {
	ct := strings.ToLower(string(resp.Header.ContentType()))
	if strings.Contains(ct, "text/event-stream") {
		return false
	}
	if bodyStream := resp.BodyStream(); bodyStream != nil {
		_, _ = io.Copy(io.Discard, bodyStream)
	}
	return true
}

// MergeExtraParams merges extraParams into jsonMap, handling nested maps recursively.
func MergeExtraParams(jsonMap map[string]interface{}, extraParams map[string]interface{}) {
	for k, v := range extraParams {
		if existingVal, exists := jsonMap[k]; exists {
			if existingMap, ok := existingVal.(map[string]interface{}); ok {
				if newMap, ok := v.(map[string]interface{}); ok {
					MergeExtraParams(existingMap, newMap)
					continue
				}
			}
		}
		jsonMap[k] = v
	}
}

// MergeExtraParamsIntoJSON merges extra params into serialized JSON while preserving
// the original key ordering. This avoids the order-destroying roundtrip through
// map[string]interface{} that would lose key ordering in tool schemas and other
// order-sensitive JSON structures.
func MergeExtraParamsIntoJSON(jsonBody []byte, extraParams map[string]interface{}) ([]byte, error) {
	trimmed := bytes.TrimSpace(jsonBody)
	if len(trimmed) < 2 || trimmed[0] != '{' {
		return jsonBody, nil // not a JSON object, return as-is
	}

	// Parse existing JSON into ordered key-value pairs using encoding/json
	// (not sonic) to preserve document key order via token-by-token parsing.
	dec := json.NewDecoder(bytes.NewReader(trimmed))
	dec.UseNumber()

	if _, err := dec.Token(); err != nil { // '{'
		return jsonBody, nil
	}

	type kvPair struct {
		key string
		val json.RawMessage
	}
	var pairs []kvPair
	seen := make(map[string]int)

	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return jsonBody, nil
		}
		key, ok := tok.(string)
		if !ok {
			return jsonBody, nil
		}
		var val json.RawMessage
		if err := dec.Decode(&val); err != nil {
			return jsonBody, nil
		}
		seen[key] = len(pairs)
		pairs = append(pairs, kvPair{key, val})
	}

	// Add/merge extra params (deterministic order for new keys)
	extraKeys := make([]string, 0, len(extraParams))
	for k := range extraParams {
		extraKeys = append(extraKeys, k)
	}
	sort.Strings(extraKeys)
	for _, k := range extraKeys {
		v := extraParams[k]
		newValBytes, err := sonic.Marshal(v)
		if err != nil {
			continue
		}
		if idx, exists := seen[k]; exists {
			// If both existing and new are JSON objects, merge recursively
			existingTrimmed := bytes.TrimSpace(pairs[idx].val)
			newTrimmed := bytes.TrimSpace(newValBytes)
			if len(existingTrimmed) > 0 && existingTrimmed[0] == '{' &&
				len(newTrimmed) > 0 && newTrimmed[0] == '{' {
				var existingMap, newMap map[string]interface{}
				existingDec := json.NewDecoder(bytes.NewReader(existingTrimmed))
				existingDec.UseNumber()
				newDec := json.NewDecoder(bytes.NewReader(newTrimmed))
				newDec.UseNumber()
				if existingDec.Decode(&existingMap) == nil {
					if newDec.Decode(&newMap) == nil {
						MergeExtraParams(existingMap, newMap)
						if merged, err := sortedAPI.Marshal(existingMap); err == nil {
							pairs[idx].val = merged
							continue
						}
					}
				}
			}
			// Non-object or merge failed: overwrite in place (preserving position)
			pairs[idx].val = newValBytes
		} else {
			// New key: append at end
			pairs = append(pairs, kvPair{k, newValBytes})
		}
	}

	// Rebuild compact JSON, then indent for consistent formatting
	var compact bytes.Buffer
	compact.WriteByte('{')
	for i, kv := range pairs {
		if i > 0 {
			compact.WriteByte(',')
		}
		keyBytes, err := sonic.Marshal(kv.key)
		if err != nil {
			return jsonBody, err
		}
		compact.Write(keyBytes)
		compact.WriteByte(':')
		// Use trimmed value to remove any existing indentation
		compact.Write(bytes.TrimSpace(kv.val))
	}
	compact.WriteByte('}')

	// Re-indent to match the expected formatting
	var indented bytes.Buffer
	if err := json.Indent(&indented, compact.Bytes(), "", "  "); err != nil {
		return compact.Bytes(), nil
	}
	return indented.Bytes(), nil
}

// CheckContextAndGetRequestBody checks if the raw request body should be used, and returns it if it exists.
func CheckContextAndGetRequestBody(ctx context.Context, request RequestBodyGetter, requestConverter RequestBodyConverter, providerType schemas.ModelProvider) ([]byte, *schemas.BifrostError) {
	if IsLargePayloadPassthroughEnabled(ctx) {
		return nil, nil
	}

	rawBody, ok := CheckAndGetRawRequestBody(ctx, request)
	if !ok {
		convertedBody, err := requestConverter()
		if err != nil {
			return nil, NewBifrostOperationError(schemas.ErrRequestBodyConversion, err, providerType)
		}
		if convertedBody == nil {
			return nil, NewBifrostOperationError("request body is not provided", nil, providerType)
		}

		jsonBody, err := sonic.MarshalIndent(convertedBody, "", "  ")
		if err != nil {
			return nil, NewBifrostOperationError(schemas.ErrProviderRequestMarshal, err, providerType)
		}
		// Merge ExtraParams into the JSON if passthrough is enabled
		if ctx.Value(schemas.BifrostContextKeyPassthroughExtraParams) != nil && ctx.Value(schemas.BifrostContextKeyPassthroughExtraParams) == true {
			extraParams := convertedBody.GetExtraParams()
			if len(extraParams) > 0 {
				// Use order-preserving merge to avoid destroying key ordering in
				// tool schemas and other order-sensitive JSON structures.
				jsonBody, err = MergeExtraParamsIntoJSON(jsonBody, extraParams)
				if err != nil {
					return nil, NewBifrostOperationError(schemas.ErrProviderRequestMarshal, err, providerType)
				}
			}
		}
		return jsonBody, nil
	} else {
		return rawBody, nil
	}
}

// SetExtraHeadersHTTP sets additional headers from NetworkConfig to the standard HTTP request.
// This allows users to configure custom headers for their provider requests.
// Header keys are canonicalized using textproto.CanonicalMIMEHeaderKey to avoid duplicates.
// It accepts a list of headers (all canonicalized) to skip for security reasons.
// Headers are only set if they don't already exist on the request to avoid overwriting important headers.
func SetExtraHeadersHTTP(ctx context.Context, req *http.Request, extraHeaders map[string]string, skipHeaders []string) {
	for key, value := range extraHeaders {
		canonicalKey := textproto.CanonicalMIMEHeaderKey(key)
		if skipHeaders != nil {
			if slices.Contains(skipHeaders, key) {
				continue
			}
		}
		// Only set the header if it doesn't already exist to avoid overwriting important headers
		if req.Header.Get(canonicalKey) == "" {
			req.Header.Set(canonicalKey, value)
		}
	}

	// Give priority to extra headers in the context
	if extraHeaders, ok := (ctx).Value(schemas.BifrostContextKeyExtraHeaders).(map[string][]string); ok {
		for k, values := range filterHeaders(extraHeaders) {
			for i, v := range values {
				if i == 0 {
					req.Header.Set(k, v)
				} else {
					req.Header.Add(k, v)
				}
			}
		}
	}
}

// HandleProviderAPIError processes error responses from provider APIs.
// It attempts to unmarshal the error response and returns a BifrostError
// with the appropriate status code and error information.
// HTML detection only runs if JSON parsing fails to avoid expensive regex operations
// on responses that are almost certainly valid JSON. errorResp must be a pointer to
// the target struct for unmarshaling.
func HandleProviderAPIError(resp *fasthttp.Response, errorResp any) *schemas.BifrostError {
	statusCode := resp.StatusCode()

	// Decode body
	decodedBody, err := CheckAndDecodeBody(resp)
	if err != nil {
		// Decode failed - still capture raw body for RawResponse
		rawBody := resp.Body()
		var rawErrorResponse interface{}
		if len(rawBody) > 0 {
			// Try to unmarshal, but if that fails, store as string
			if unmarshalErr := sonic.Unmarshal(rawBody, &rawErrorResponse); unmarshalErr != nil {
				rawErrorResponse = string(rawBody)
			}
		}

		return &schemas.BifrostError{
			IsBifrostError: false,
			StatusCode:     &statusCode,
			Error: &schemas.ErrorField{
				Message: err.Error(),
			},
			ExtraFields: schemas.BifrostErrorExtraFields{
				RawResponse: rawErrorResponse,
			},
		}
	}

	// Try to unmarshal decoded body for RawResponse
	var rawErrorResponse interface{}
	if err := sonic.Unmarshal(decodedBody, &rawErrorResponse); err != nil {
		// Store raw body as string for RawResponse when JSON parsing fails
		// Continue to HTML detection and proper error handling below
		rawErrorResponse = string(decodedBody)
	}

	// Check for empty response
	trimmed := strings.TrimSpace(string(decodedBody))
	if len(trimmed) == 0 {
		// Provide a more descriptive error message based on HTTP status code
		var errorMessage string
		switch statusCode {
		case 401:
			errorMessage = "authentication failed: unauthorized (401) - check your API key"
		case 403:
			errorMessage = "access forbidden (403) - your API key may not have permission for this operation"
		case 404:
			errorMessage = "resource not found (404)"
		case 429:
			errorMessage = "rate limit exceeded (429)"
		case 500, 502, 503, 504:
			errorMessage = fmt.Sprintf("provider server error (%d)", statusCode)
		default:
			errorMessage = fmt.Sprintf("%s (HTTP %d)", schemas.ErrProviderResponseEmpty, statusCode)
		}
		return &schemas.BifrostError{
			IsBifrostError: false,
			StatusCode:     &statusCode,
			Error: &schemas.ErrorField{
				Message: errorMessage,
			},
			ExtraFields: schemas.BifrostErrorExtraFields{
				RawResponse: rawErrorResponse,
			},
		}
	}

	// Try JSON parsing first
	if err := sonic.Unmarshal(decodedBody, errorResp); err == nil {
		// JSON parsing succeeded, return success
		return &schemas.BifrostError{
			IsBifrostError: false,
			StatusCode:     &statusCode,
			Error:          &schemas.ErrorField{},
			ExtraFields: schemas.BifrostErrorExtraFields{
				RawResponse: rawErrorResponse,
			},
		}
	}

	// JSON parsing failed - now check if it's an HTML response (expensive operation)
	if IsHTMLResponse(resp, decodedBody) {
		return &schemas.BifrostError{
			IsBifrostError: false,
			StatusCode:     &statusCode,
			Error: &schemas.ErrorField{
				Message: schemas.ErrProviderResponseHTML,
				Error:   errors.New(string(decodedBody)),
			},
			ExtraFields: schemas.BifrostErrorExtraFields{
				RawResponse: rawErrorResponse,
			},
		}
	}

	// Not HTML either - return raw response as error message
	message := fmt.Sprintf("provider API error: %s", string(decodedBody))
	return &schemas.BifrostError{
		IsBifrostError: false,
		StatusCode:     &statusCode,
		Error: &schemas.ErrorField{
			Message: message,
		},
		ExtraFields: schemas.BifrostErrorExtraFields{
			RawResponse: rawErrorResponse,
		},
	}
}

// EnrichError attaches the raw request and response to a BifrostError.
// Returns the request and response from provider embedded in BifrostError.ExtraFields.
func EnrichError(
	ctx *schemas.BifrostContext,
	bifrostErr *schemas.BifrostError,
	requestBody []byte,
	responseBody []byte,
	sendBackRawRequest bool,
	sendBackRawResponse bool,
) *schemas.BifrostError {
	if bifrostErr == nil {
		return bifrostErr
	}

	if ShouldSendBackRawRequest(ctx, sendBackRawRequest) && len(requestBody) > 0 {
		// Store as json.RawMessage to preserve exact JSON bytes (including key ordering).
		// Compact to remove insignificant whitespace that would break SSE framing.
		bifrostErr.ExtraFields.RawRequest = compactRawJSON(requestBody)
	} else {
		bifrostErr.ExtraFields.RawRequest = nil
	}

	if ShouldSendBackRawResponse(ctx, sendBackRawResponse) {
		if len(responseBody) > 0 {
			bifrostErr.ExtraFields.RawResponse = compactRawJSON(responseBody)
		}
	} else {
		bifrostErr.ExtraFields.RawResponse = nil
	}

	return bifrostErr
}

// HandleProviderResponse handles common response parsing logic for provider responses.
// It attempts to parse the response body into the provided response type
// and returns either the parsed response or a BifrostError if parsing fails.
// If sendBackRawResponse is true, it returns the raw response interface, otherwise nil.
// HTML detection only runs if JSON parsing fails to avoid expensive regex operations
// on responses that are almost certainly valid JSON.
func HandleProviderResponse[T any](responseBody []byte, response *T, requestBody []byte, sendBackRawRequest bool, sendBackRawResponse bool) (rawRequest interface{}, rawResponse interface{}, bifrostErr *schemas.BifrostError) {
	// Check for empty response
	trimmed := strings.TrimSpace(string(responseBody))
	if len(trimmed) == 0 {
		return nil, nil, &schemas.BifrostError{
			IsBifrostError: true,
			Error: &schemas.ErrorField{
				Message: schemas.ErrProviderResponseEmpty,
			},
		}
	}

	// Skip raw request capture if requestBody is nil (e.g., for GET requests)
	shouldCaptureRawRequest := sendBackRawRequest && requestBody != nil

	if shouldCaptureRawRequest {
		// Store as json.RawMessage to preserve the exact JSON bytes (including key ordering).
		// Previously this used sonic.Unmarshal into interface{} which created map[string]interface{}
		// and destroyed key ordering in tool schemas and other order-sensitive structures.
		// Compact to remove insignificant whitespace that would break SSE framing.
		rawRequest = compactRawJSON(requestBody)
	}

	if sendBackRawResponse {
		rawResponse = compactRawJSON(responseBody)
	}

	// Unmarshal the structured response
	structuredErr := sonic.Unmarshal(responseBody, response)
	if structuredErr != nil {
		// JSON parsing failed - check if it's an HTML response (expensive operation)
		if IsHTMLResponse(nil, responseBody) {
			return nil, nil, &schemas.BifrostError{
				IsBifrostError: false,
				Error: &schemas.ErrorField{
					Message: schemas.ErrProviderResponseHTML,
					Error:   errors.New(string(responseBody)),
				},
			}
		}

		return nil, nil, &schemas.BifrostError{
			IsBifrostError: true,
			Error: &schemas.ErrorField{
				Message: schemas.ErrProviderResponseUnmarshal,
				Error:   structuredErr,
			},
		}
	}

	if shouldCaptureRawRequest || sendBackRawResponse {
		return rawRequest, rawResponse, nil
	}

	return nil, nil, nil
}

// compactRawJSON removes insignificant whitespace from JSON bytes, returning a
// json.RawMessage safe for SSE streaming (no literal newlines). Falls back to
// the original bytes if compaction fails (e.g., invalid JSON).
func compactRawJSON(data []byte) json.RawMessage {
	var buf bytes.Buffer
	if err := schemas.Compact(&buf, data); err == nil {
		return json.RawMessage(buf.Bytes())
	}
	return json.RawMessage(data)
}

// ParseAndSetRawRequest stores the raw request body in the extra fields.
// Uses json.RawMessage to preserve the exact JSON bytes (including key ordering).
// The body is compacted to remove insignificant whitespace, which prevents
// literal newlines from breaking SSE data-line framing during streaming.
func ParseAndSetRawRequest(extraFields *schemas.BifrostResponseExtraFields, jsonBody []byte) {
	if len(jsonBody) > 0 {
		extraFields.RawRequest = compactRawJSON(jsonBody)
	}
}

// ParseAndSetRawRequestIfJSON parses the request body if it's JSON and sets the raw request in the extra fields.
func ParseAndSetRawRequestIfJSON(fasthttpReq *fasthttp.Request, extraFields *schemas.BifrostResponseExtraFields) {
	extraFields.RawRequest = nil
	contentType := strings.ToLower(string(fasthttpReq.Header.ContentType()))
	if strings.Contains(contentType, "application/json") {
		body := append([]byte(nil), fasthttpReq.Body()...)
		ParseAndSetRawRequest(extraFields, body)
	}
}

// NewUnsupportedOperationError creates a standardized error for unsupported operations.
// This helper reduces code duplication across providers that don't support certain operations.
func NewUnsupportedOperationError(requestType schemas.RequestType, providerName schemas.ModelProvider) *schemas.BifrostError {
	return &schemas.BifrostError{
		IsBifrostError: false,
		Error: &schemas.ErrorField{
			Message: fmt.Sprintf("%s is not supported by %s provider", requestType, providerName),
			Code:    schemas.Ptr("unsupported_operation"),
		},
		ExtraFields: schemas.BifrostErrorExtraFields{
			Provider:    providerName,
			RequestType: requestType,
		},
	}
}

// CheckOperationAllowed enforces per-op gating using schemas.Operation.
// Behavior:
// - If no gating is configured (config == nil or AllowedRequests == nil), the operation is allowed.
// - If gating is configured, returns an error when the operation is not explicitly allowed.
func CheckOperationAllowed(defaultProvider schemas.ModelProvider, config *schemas.CustomProviderConfig, operation schemas.RequestType) *schemas.BifrostError {
	// No gating configured => allowed
	if config == nil || config.AllowedRequests == nil {
		return nil
	}
	// Explicitly allowed?
	if config.IsOperationAllowed(operation) {
		return nil
	}
	// Gated and not allowed
	resolved := GetProviderName(defaultProvider, config)
	return NewUnsupportedOperationError(operation, resolved)
}

// CheckAndDecodeBody checks the content encoding and decodes the body accordingly.
// It returns a copy of the body to avoid race conditions when the response is released
// back to fasthttp's buffer pool. Uses pooled gzip readers to reduce GC pressure.
func CheckAndDecodeBody(resp *fasthttp.Response) ([]byte, error) {
	contentEncoding := strings.ToLower(strings.TrimSpace(string(resp.Header.Peek("Content-Encoding"))))
	if strings.Contains(contentEncoding, "gzip") {
		body := resp.Body()
		if len(body) == 0 {
			return nil, nil
		}

		reader := bytes.NewReader(body)
		gz, err := AcquireGzipReader(reader)
		if err != nil {
			return nil, err
		}
		defer ReleaseGzipReader(gz)

		decompressed, err := io.ReadAll(gz)
		if err != nil {
			return nil, err
		}
		return decompressed, nil
	}
	// Copy the body to avoid race conditions when response is released back to pool
	body := resp.Body()
	result := make([]byte, len(body))
	copy(result, body)
	return result, nil
}

// IsHTMLResponse checks if the response is HTML by examining the Content-Type header
// and/or the response body for HTML indicators.
func IsHTMLResponse(resp *fasthttp.Response, body []byte) bool {
	// Check Content-Type header first (most reliable indicator)
	if resp != nil {
		contentType := strings.ToLower(string(resp.Header.Peek("Content-Type")))
		if strings.Contains(contentType, "text/html") {
			return true
		}
	}

	// If body is small, it's unlikely to be HTML
	if len(body) < 20 {
		return false
	}

	// Check for HTML indicators in body
	bodyLower := strings.ToLower(string(body))

	// Look for common HTML tags or DOCTYPE
	htmlIndicators := []string{
		"<!doctype html",
		"<html",
		"<head",
		"<body",
		"<title>",
		"<h1>",
		"<h2>",
		"<h3>",
		"<p>",
		"<div",
	}

	for _, indicator := range htmlIndicators {
		if strings.Contains(bodyLower, indicator) {
			return true
		}
	}

	return false
}

// Limit body size to prevent ReDoS on very large malicious responses
const maxBodySize = 32 * 1024 // 32KB

// ExtractHTMLErrorMessage extracts meaningful error information from an HTML response.
// It attempts to find error messages from title tags, headers, and visible text.
// UNUSED for now but could be useful in the future
func ExtractHTMLErrorMessage(body []byte) string {
	if len(body) > maxBodySize {
		body = body[:maxBodySize]
	}

	bodyStr := string(body)
	bodyLower := strings.ToLower(bodyStr)

	// Try to extract title first
	if idx := strings.Index(bodyLower, "<title>"); idx != -1 {
		endIdx := strings.Index(bodyLower[idx:], "</title>")
		if endIdx != -1 {
			title := strings.TrimSpace(bodyStr[idx+7 : idx+endIdx])
			if title != "" && title != "Error" {
				return title
			}
		}
	}

	// Try to extract from h1, h2, h3 tags (common for error pages)
	for _, tag := range []string{"h1", "h2", "h3"} {
		pattern := fmt.Sprintf("<%s[^>]*>([^<]+)</%s>", tag, tag)
		re := regexp.MustCompile("(?i)" + pattern)
		if matches := re.FindStringSubmatch(bodyStr); len(matches) > 1 {
			msg := strings.TrimSpace(matches[1])
			if msg != "" {
				return msg
			}
		}
	}

	// Try to extract from meta description
	pattern := `<meta\s+name="description"\s+content="([^"]+)"`
	re := regexp.MustCompile("(?i)" + pattern)
	if matches := re.FindStringSubmatch(bodyStr); len(matches) > 1 {
		msg := strings.TrimSpace(matches[1])
		if msg != "" {
			return msg
		}
	}

	// Extract visible text: remove script and style tags, then extract text
	// Remove script and style tags and their content
	re = regexp.MustCompile(`(?i)<script[^>]*>.*?</script>|<style[^>]*>.*?</style>`)
	cleaned := re.ReplaceAllString(bodyStr, "")

	// Remove HTML tags
	re = regexp.MustCompile(`<[^>]+>`)
	cleaned = re.ReplaceAllString(cleaned, " ")

	// Clean up whitespace and get first meaningful sentence
	sentences := strings.FieldsFunc(cleaned, func(r rune) bool {
		return r == '\n' || r == '\r'
	})

	for _, sentence := range sentences {
		trimmed := strings.TrimSpace(sentence)
		if len(trimmed) > 10 && len(trimmed) < 500 {
			// Limit to first 200 chars to avoid very long messages
			if len(trimmed) > 200 {
				trimmed = trimmed[:200] + "..."
			}
			return trimmed
		}
	}

	// If all else fails, return a generic message with status code context
	return "HTML error response received from provider"
}

// JSONLParseResult holds parsed items and any line-level errors encountered during parsing.
type JSONLParseResult struct {
	Errors []schemas.BatchError
}

// ParseJSONL parses JSONL data line by line, calling the provided callback for each line.
// It collects parse errors with line numbers rather than silently skipping failed lines.
// The callback receives the line bytes and returns an error if parsing fails.
// This function operates directly on byte slices to avoid unnecessary string conversions.
func ParseJSONL(data []byte, parseLine func(line []byte) error) JSONLParseResult {
	result := JSONLParseResult{}

	lineNum := 0
	start := 0

	for i := 0; i <= len(data); i++ {
		// Check for newline or end of data
		if i == len(data) || data[i] == '\n' {
			lineNum++

			// Extract the line (excluding the newline character)
			end := i
			if end > start {
				line := data[start:end]

				// Trim trailing carriage return for Windows-style line endings
				if len(line) > 0 && line[len(line)-1] == '\r' {
					line = line[:len(line)-1]
				}

				// Skip empty lines
				if len(line) > 0 {
					if err := parseLine(line); err != nil {
						lineNumCopy := lineNum
						result.Errors = append(result.Errors, schemas.BatchError{
							Code:    "parse_error",
							Message: err.Error(),
							Line:    &lineNumCopy,
						})
					}
				}
			}

			start = i + 1
		}
	}

	return result
}

// NewConfigurationError creates a standardized error for configuration errors.
// This helper reduces code duplication across providers that have configuration errors.
func NewConfigurationError(message string, providerType schemas.ModelProvider) *schemas.BifrostError {
	return &schemas.BifrostError{
		IsBifrostError: false,
		Error: &schemas.ErrorField{
			Message: message,
		},
		ExtraFields: schemas.BifrostErrorExtraFields{
			Provider: providerType,
		},
	}
}

// NewBifrostOperationError creates a standardized error for bifrost operation errors.
// This helper reduces code duplication across providers that have bifrost operation errors.
func NewBifrostOperationError(message string, err error, providerType schemas.ModelProvider) *schemas.BifrostError {
	return &schemas.BifrostError{
		IsBifrostError: true,
		Error: &schemas.ErrorField{
			Message: message,
			Error:   err,
		},
		ExtraFields: schemas.BifrostErrorExtraFields{
			Provider: providerType,
		},
	}
}

// NewProviderAPIError creates a standardized error for provider API errors.
// This helper reduces code duplication across providers that have provider API errors.
func NewProviderAPIError(message string, err error, statusCode int, providerType schemas.ModelProvider, errorType *string, eventID *string) *schemas.BifrostError {
	return &schemas.BifrostError{
		IsBifrostError: false,
		StatusCode:     &statusCode,
		Type:           errorType,
		EventID:        eventID,
		Error: &schemas.ErrorField{
			Message: message,
			Error:   err,
			Type:    errorType,
		},
		ExtraFields: schemas.BifrostErrorExtraFields{
			Provider: providerType,
		},
	}
}

// RequestMetadata contains metadata about a request for error reporting.
// This struct is used to pass request context to parseError functions.
type RequestMetadata struct {
	Provider    schemas.ModelProvider
	Model       string
	RequestType schemas.RequestType
}

// ShouldSendBackRawRequest checks if the raw request should be captured.
// Context overrides are intentionally restricted to asymmetric behavior: a context value can only
// promote false→true and will not override a true config to false, avoiding accidental suppression.
// Both full send-back mode and logging-only mode (store_raw_request_response) set
// BifrostContextKeySendBackRawRequest=true in the request context so a single flag is checked here.
// In logging-only mode the payload is stripped before the response reaches the client.
func ShouldSendBackRawRequest(ctx context.Context, defaultSendBackRawRequest bool) bool {
	if sendBackRawRequest, ok := ctx.Value(schemas.BifrostContextKeySendBackRawRequest).(bool); ok && sendBackRawRequest {
		return sendBackRawRequest
	}
	return defaultSendBackRawRequest
}

// ShouldSendBackRawResponse checks if the raw response should be captured.
// Context overrides are intentionally restricted to asymmetric behavior: a context value can only
// promote false→true and will not override a true config to false, avoiding accidental suppression.
// Both full send-back mode and logging-only mode (store_raw_request_response) set
// BifrostContextKeySendBackRawResponse=true in the request context so a single flag is checked here.
// In logging-only mode the payload is stripped before the response reaches the client.
func ShouldSendBackRawResponse(ctx context.Context, defaultSendBackRawResponse bool) bool {
	if sendBackRawResponse, ok := ctx.Value(schemas.BifrostContextKeySendBackRawResponse).(bool); ok && sendBackRawResponse {
		return sendBackRawResponse
	}
	return defaultSendBackRawResponse
}

// SendCreatedEventResponsesChunk sends a ResponsesStreamResponseTypeCreated event.
func SendCreatedEventResponsesChunk(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, provider schemas.ModelProvider, model string, startTime time.Time, responseChan chan *schemas.BifrostStreamChunk) {
	firstChunk := &schemas.BifrostResponsesStreamResponse{
		Type:           schemas.ResponsesStreamResponseTypeCreated,
		SequenceNumber: 0,
		Response:       &schemas.BifrostResponsesResponse{},
		ExtraFields: schemas.BifrostResponseExtraFields{
			RequestType:    schemas.ResponsesStreamRequest,
			Provider:       provider,
			ModelRequested: model,
			ChunkIndex:     0,
			Latency:        time.Since(startTime).Milliseconds(),
		},
	}
	//TODO add bifrost response pooling here
	bifrostResponse := &schemas.BifrostResponse{
		ResponsesStreamResponse: firstChunk,
	}
	ProcessAndSendResponse(ctx, postHookRunner, bifrostResponse, responseChan)
}

// SendInProgressEventResponsesChunk sends a ResponsesStreamResponseTypeInProgress event
func SendInProgressEventResponsesChunk(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, provider schemas.ModelProvider, model string, startTime time.Time, responseChan chan *schemas.BifrostStreamChunk) {
	chunk := &schemas.BifrostResponsesStreamResponse{
		Type:           schemas.ResponsesStreamResponseTypeInProgress,
		SequenceNumber: 1,
		Response:       &schemas.BifrostResponsesResponse{},
		ExtraFields: schemas.BifrostResponseExtraFields{
			RequestType:    schemas.ResponsesStreamRequest,
			Provider:       provider,
			ModelRequested: model,
			ChunkIndex:     1,
			Latency:        time.Since(startTime).Milliseconds(),
		},
	}
	//TODO add bifrost response pooling here
	bifrostResponse := &schemas.BifrostResponse{
		ResponsesStreamResponse: chunk,
	}
	ProcessAndSendResponse(ctx, postHookRunner, bifrostResponse, responseChan)
}

// BuildClientStreamChunk constructs a BifrostStreamChunk from post-hook results.
// It never mutates the shared processedResponse or processedError objects — when in
// logging-only mode (BifrostContextKeyRawRequestResponseForLogging) it shallow-copies
// each inner response struct and the BifrostError, nils only the raw fields on those
// copies, and returns them as the outgoing chunk. This is safe for concurrent PostLLMHook
// goroutines that still hold references to the originals.
func BuildClientStreamChunk(ctx context.Context, processedResponse *schemas.BifrostResponse, processedError *schemas.BifrostError) *schemas.BifrostStreamChunk {
	dropRaw, _ := ctx.Value(schemas.BifrostContextKeyRawRequestResponseForLogging).(bool)
	streamResponse := &schemas.BifrostStreamChunk{}
	if processedResponse != nil {
		streamResponse.BifrostTextCompletionResponse = processedResponse.TextCompletionResponse
		streamResponse.BifrostChatResponse = processedResponse.ChatResponse
		streamResponse.BifrostResponsesStreamResponse = processedResponse.ResponsesStreamResponse
		streamResponse.BifrostSpeechStreamResponse = processedResponse.SpeechStreamResponse
		streamResponse.BifrostTranscriptionStreamResponse = processedResponse.TranscriptionStreamResponse
		streamResponse.BifrostImageGenerationStreamResponse = processedResponse.ImageGenerationStreamResponse
		// Strip raw fields from client-facing copies without mutating the shared objects
		// that PostLLMHook goroutines may still be reading.
		if dropRaw {
			if streamResponse.BifrostTextCompletionResponse != nil {
				cp := *streamResponse.BifrostTextCompletionResponse
				cp.ExtraFields.RawRequest = nil
				cp.ExtraFields.RawResponse = nil
				streamResponse.BifrostTextCompletionResponse = &cp
			}
			if streamResponse.BifrostChatResponse != nil {
				cp := *streamResponse.BifrostChatResponse
				cp.ExtraFields.RawRequest = nil
				cp.ExtraFields.RawResponse = nil
				streamResponse.BifrostChatResponse = &cp
			}
			if streamResponse.BifrostResponsesStreamResponse != nil {
				cp := *streamResponse.BifrostResponsesStreamResponse
				cp.ExtraFields.RawRequest = nil
				cp.ExtraFields.RawResponse = nil
				streamResponse.BifrostResponsesStreamResponse = &cp
			}
			if streamResponse.BifrostSpeechStreamResponse != nil {
				cp := *streamResponse.BifrostSpeechStreamResponse
				cp.ExtraFields.RawRequest = nil
				cp.ExtraFields.RawResponse = nil
				streamResponse.BifrostSpeechStreamResponse = &cp
			}
			if streamResponse.BifrostTranscriptionStreamResponse != nil {
				cp := *streamResponse.BifrostTranscriptionStreamResponse
				cp.ExtraFields.RawRequest = nil
				cp.ExtraFields.RawResponse = nil
				streamResponse.BifrostTranscriptionStreamResponse = &cp
			}
			if streamResponse.BifrostImageGenerationStreamResponse != nil {
				cp := *streamResponse.BifrostImageGenerationStreamResponse
				cp.ExtraFields.RawRequest = nil
				cp.ExtraFields.RawResponse = nil
				streamResponse.BifrostImageGenerationStreamResponse = &cp
			}
		}
	}
	if processedError != nil {
		if dropRaw {
			// Strip raw fields from a client-facing copy without mutating the shared error object.
			errCopy := *processedError
			errCopy.ExtraFields.RawRequest = nil
			errCopy.ExtraFields.RawResponse = nil
			streamResponse.BifrostError = &errCopy
		} else {
			streamResponse.BifrostError = processedError
		}
	}
	return streamResponse
}

// ProcessAndSendResponse handles post-hook processing and sends the response to the channel.
// This utility reduces code duplication across streaming implementations by encapsulating
// the common pattern of running post hooks, handling errors, and sending responses with
// proper context cancellation handling.
// It also completes the deferred LLM span when the final chunk is sent (StreamEndIndicator is true).
func ProcessAndSendResponse(
	ctx *schemas.BifrostContext,
	postHookRunner schemas.PostHookRunner,
	response *schemas.BifrostResponse,
	responseChan chan *schemas.BifrostStreamChunk,
) {
	// Accumulate chunk for tracing (common for all providers)
	if tracer, ok := ctx.Value(schemas.BifrostContextKeyTracer).(schemas.Tracer); ok && tracer != nil {
		if traceID, ok := ctx.Value(schemas.BifrostContextKeyTraceID).(string); ok && traceID != "" {
			tracer.AddStreamingChunk(traceID, response)
		}
	}

	// Run post hooks on the response (note: accumulated chunks above contain pre-hook data)
	processedResponse, processedError := postHookRunner(ctx, response, nil)

	if HandleStreamControlSkip(processedError) {
		// Even if skipping, complete the deferred span if this is the final chunk
		if isFinalChunk := ctx.Value(schemas.BifrostContextKeyStreamEndIndicator); isFinalChunk != nil {
			if final, ok := isFinalChunk.(bool); ok && final {
				completeDeferredSpan(ctx, processedResponse, processedError)
			}
		}
		return
	}

	streamResponse := BuildClientStreamChunk(ctx, processedResponse, processedError)

	select {
	case responseChan <- streamResponse:
	case <-ctx.Done():
		return
	}

	// Check if this is the final chunk and complete deferred span with post-processed data
	if isFinalChunk := ctx.Value(schemas.BifrostContextKeyStreamEndIndicator); isFinalChunk != nil {
		if final, ok := isFinalChunk.(bool); ok && final {
			completeDeferredSpan(ctx, processedResponse, processedError)
		}
	}
}

// ProcessAndSendBifrostError handles post-hook processing and sends the bifrost error to the channel.
// This utility reduces code duplication across streaming implementations by encapsulating
// the common pattern of running post hooks, handling errors, and sending responses with
// proper context cancellation handling.
// It also completes the deferred LLM span when the final chunk is sent (StreamEndIndicator is true).
func ProcessAndSendBifrostError(
	ctx *schemas.BifrostContext,
	postHookRunner schemas.PostHookRunner,
	bifrostErr *schemas.BifrostError,
	responseChan chan *schemas.BifrostStreamChunk,
	logger schemas.Logger,
) {
	// Run post hooks first so span reflects post-processed data
	processedResponse, processedError := postHookRunner(ctx, nil, bifrostErr)

	if HandleStreamControlSkip(processedError) {
		// Even if skipping, complete the deferred span if this is the final chunk
		if isFinalChunk := ctx.Value(schemas.BifrostContextKeyStreamEndIndicator); isFinalChunk != nil {
			if final, ok := isFinalChunk.(bool); ok && final {
				completeDeferredSpan(ctx, processedResponse, processedError)
			}
		}
		return
	}

	streamResponse := BuildClientStreamChunk(ctx, processedResponse, processedError)

	select {
	case responseChan <- streamResponse:
	case <-ctx.Done():
	}

	// Check if this is the final chunk and complete deferred span with post-processed data
	if isFinalChunk := ctx.Value(schemas.BifrostContextKeyStreamEndIndicator); isFinalChunk != nil {
		if final, ok := isFinalChunk.(bool); ok && final {
			completeDeferredSpan(ctx, processedResponse, processedError)
		}
	}
}

// SetupStreamCancellation spawns a goroutine that closes the body stream when
// the context is cancelled or deadline exceeded, unblocking any blocked Read/Scan operations.
// Returns a cleanup function that MUST be called when streaming is done to
// prevent the goroutine from closing the stream during normal operation.
// Works with both fasthttp's BodyStream() (io.Reader) and net/http's resp.Body (io.ReadCloser).
func SetupStreamCancellation(ctx context.Context, bodyStream io.Reader, logger schemas.Logger) (cleanup func()) {
	done := make(chan struct{})
	closed := make(chan struct{})

	go func() {
		defer close(closed)
		select {
		case <-ctx.Done():
			// Context cancelled or deadline exceeded - close the body stream to unblock reads
			if closer, ok := bodyStream.(io.Closer); ok {
				if err := closer.Close(); err != nil {
					getLogger().Debug(fmt.Sprintf("Error closing body stream on context done: %v", err))
				}
			}
		case <-done:
			// If context was also cancelled (race between done and ctx.Done),
			// still close the body stream to unblock the drain in ReleaseStreamingResponse.
			if ctx.Err() != nil {
				if closer, ok := bodyStream.(io.Closer); ok {
					if err := closer.Close(); err != nil {
						getLogger().Debug(fmt.Sprintf("Error closing body stream on done with cancelled context: %v", err))
					}
				}
			}
		}
	}()

	return func() {
		close(done)
		<-closed // Wait for goroutine to finish closing the stream before ReleaseStreamingResponse drains
	}
}

// DefaultStreamIdleTimeout is how long a stream read can block with zero data
// before bifrost considers the connection stalled and closes it. This protects
// against providers that stop sending data but keep the TCP connection open
// (e.g., Azure TPM throttling).
const DefaultStreamIdleTimeout = 60 * time.Second

// SetStreamIdleTimeoutIfEmpty sets the stream idle timeout on the context from
// the provider's network config, but only if no valid timeout is already present.
// This allows upstream layers (transport, headers) to set the timeout first,
// with the provider config acting as a fallback.
func SetStreamIdleTimeoutIfEmpty(ctx *schemas.BifrostContext, configSeconds int) {
	if existing, ok := ctx.Value(schemas.BifrostContextKeyStreamIdleTimeout).(time.Duration); ok && existing > 0 {
		return // already set from upstream (transport/header), respect it
	}
	if configSeconds > 0 {
		ctx.SetValue(schemas.BifrostContextKeyStreamIdleTimeout, time.Duration(configSeconds)*time.Second)
	}
}

// GetStreamIdleTimeout reads the per-chunk idle timeout from context,
// falling back to DefaultStreamIdleTimeout if not set.
func GetStreamIdleTimeout(ctx *schemas.BifrostContext) time.Duration {
	if timeout, ok := ctx.Value(schemas.BifrostContextKeyStreamIdleTimeout).(time.Duration); ok && timeout > 0 {
		return timeout
	}
	return DefaultStreamIdleTimeout
}

// idleTimeoutReader wraps an io.Reader and closes the underlying body stream
// if no data arrives within the configured timeout. This unblocks any pending
// Read() call on the wrapped reader.
type idleTimeoutReader struct {
	reader     io.Reader
	bodyStream io.Reader // closed via type assertion to io.Closer on timeout
	timeout    time.Duration
	timer      *time.Timer
	once       sync.Once
}

// NewIdleTimeoutReader wraps reader with idle detection. If reader.Read() returns
// no data for the given timeout duration, bodyStream is closed to unblock the read.
// bodyStream must implement io.Closer for the timeout to take effect; if it does not,
// the wrapper still functions but cannot force-close the stream.
// Returns the wrapped reader and a cleanup function that MUST be called (via defer)
// when streaming is complete, to stop the timer and prevent premature closure.
func NewIdleTimeoutReader(reader io.Reader, bodyStream io.Reader, timeout time.Duration) (io.Reader, func()) {
	if timeout <= 0 {
		timeout = DefaultStreamIdleTimeout
	}
	r := &idleTimeoutReader{
		reader:     reader,
		bodyStream: bodyStream,
		timeout:    timeout,
	}
	r.timer = time.AfterFunc(timeout, func() {
		r.once.Do(func() {
			if closer, ok := r.bodyStream.(io.Closer); ok {
				closer.Close()
			}
		})
	})
	return r, func() { r.timer.Stop() }
}

func (r *idleTimeoutReader) Read(p []byte) (int, error) {
	n, err := r.reader.Read(p)
	if n > 0 {
		r.timer.Reset(r.timeout)
	}
	return n, err
}

// HandleStreamCancellation should be called when a streaming goroutine exits
// due to context cancellation. It ensures proper cleanup by:
// 1. Checking if StreamEndIndicator was already set (to avoid duplicate handling)
// 2. Setting StreamEndIndicator to true
// 3. Sending a cancellation error through PostHook chain
//
// This is critical for the logging plugin to update log status from "processing" to "error"
// when a client disconnects mid-stream.
func HandleStreamCancellation(
	ctx *schemas.BifrostContext,
	postHookRunner schemas.PostHookRunner,
	responseChan chan *schemas.BifrostStreamChunk,
	provider schemas.ModelProvider,
	model string,
	requestType schemas.RequestType,
	logger schemas.Logger,
) {
	// Check if already handled (StreamEndIndicator already set)
	if indicator := ctx.GetAndSetValue(schemas.BifrostContextKeyStreamEndIndicator, true); indicator != nil {
		if set, ok := indicator.(bool); ok && set {
			return // Already handled
		}
	}
	// Create cancellation error
	cancelErr := &schemas.BifrostError{
		StatusCode: schemas.Ptr(499), // Client Closed Request
		Error: &schemas.ErrorField{
			Message: "Request cancelled: client disconnected",
			Type:    schemas.Ptr(schemas.RequestCancelled),
		},
		ExtraFields: schemas.BifrostErrorExtraFields{
			Provider:       provider,
			ModelRequested: model,
			RequestType:    requestType,
		},
	}

	// Send through PostHook chain - this updates the log to "error" status
	ProcessAndSendBifrostError(ctx, postHookRunner, cancelErr, responseChan, logger)
}

// HandleStreamTimeout should be called when a streaming goroutine exits
// due to context deadline exceeded. It ensures proper cleanup by:
// 1. Checking if StreamEndIndicator was already set (to avoid duplicate handling)
// 2. Setting StreamEndIndicator to true
// 3. Sending a timeout error through PostHook chain
//
// This is critical for the logging plugin to update log status from "processing" to "error"
// when a request times out mid-stream.
func HandleStreamTimeout(
	ctx *schemas.BifrostContext,
	postHookRunner schemas.PostHookRunner,
	responseChan chan *schemas.BifrostStreamChunk,
	provider schemas.ModelProvider,
	model string,
	requestType schemas.RequestType,
	logger schemas.Logger,
) {
	// Check if already handled (StreamEndIndicator already set)
	if indicator := ctx.GetAndSetValue(schemas.BifrostContextKeyStreamEndIndicator, true); indicator != nil {
		if set, ok := indicator.(bool); ok && set {
			return // Already handled
		}
	}
	// Create timeout error
	timeoutErr := &schemas.BifrostError{
		StatusCode: schemas.Ptr(504), // Gateway Timeout
		Error: &schemas.ErrorField{
			Message: "Request timed out: deadline exceeded",
			Type:    schemas.Ptr(schemas.RequestTimedOut),
		},
		ExtraFields: schemas.BifrostErrorExtraFields{
			Provider:       provider,
			ModelRequested: model,
			RequestType:    requestType,
		},
	}

	// Send through PostHook chain - this updates the log to "error" status
	ProcessAndSendBifrostError(ctx, postHookRunner, timeoutErr, responseChan, logger)
}

// ProcessAndSendError handles post-hook processing and sends the error to the channel.
// This utility reduces code duplication across streaming implementations by encapsulating
// the common pattern of running post hooks, handling errors, and sending responses with
// proper context cancellation handling.
func ProcessAndSendError(
	ctx *schemas.BifrostContext,
	postHookRunner schemas.PostHookRunner,
	err error,
	responseChan chan *schemas.BifrostStreamChunk,
	requestType schemas.RequestType,
	providerName schemas.ModelProvider,
	model string,
	logger schemas.Logger,
) {
	// Send scanner error through channel
	bifrostError :=
		&schemas.BifrostError{
			IsBifrostError: true,
			Error: &schemas.ErrorField{
				Message: fmt.Sprintf("Error reading stream: %v", err),
				Error:   err,
			},
			ExtraFields: schemas.BifrostErrorExtraFields{
				RequestType:    requestType,
				Provider:       providerName,
				ModelRequested: model,
			},
		}
	processedResponse, processedError := postHookRunner(ctx, nil, bifrostError)

	if HandleStreamControlSkip(processedError) {
		return
	}

	streamResponse := &schemas.BifrostStreamChunk{}
	if processedResponse != nil {
		streamResponse.BifrostTextCompletionResponse = processedResponse.TextCompletionResponse
		streamResponse.BifrostChatResponse = processedResponse.ChatResponse
		streamResponse.BifrostResponsesStreamResponse = processedResponse.ResponsesStreamResponse
		streamResponse.BifrostSpeechStreamResponse = processedResponse.SpeechStreamResponse
		streamResponse.BifrostTranscriptionStreamResponse = processedResponse.TranscriptionStreamResponse
	}
	if processedError != nil {
		streamResponse.BifrostError = processedError
	}

	select {
	case responseChan <- streamResponse:
	case <-ctx.Done():
	}
}

// CreateBifrostTextCompletionChunkResponse creates a bifrost text completion chunk response.
func CreateBifrostTextCompletionChunkResponse(
	id string,
	usage *schemas.BifrostLLMUsage,
	finishReason *string,
	currentChunkIndex int,
	requestType schemas.RequestType,
	providerName schemas.ModelProvider,
	model string,
) *schemas.BifrostTextCompletionResponse {
	response := &schemas.BifrostTextCompletionResponse{
		ID:     id,
		Object: "text_completion",
		Usage:  usage,
		Choices: []schemas.BifrostResponseChoice{
			{
				FinishReason:                 finishReason,
				TextCompletionResponseChoice: &schemas.TextCompletionResponseChoice{}, // empty delta
			},
		},
		ExtraFields: schemas.BifrostResponseExtraFields{
			RequestType:    requestType,
			Provider:       providerName,
			ModelRequested: model,
			ChunkIndex:     currentChunkIndex + 1,
		},
	}
	return response
}

// CreateBifrostChatCompletionChunkResponse creates a bifrost chat completion chunk response.
func CreateBifrostChatCompletionChunkResponse(
	id string,
	usage *schemas.BifrostLLMUsage,
	finishReason *string,
	currentChunkIndex int,
	requestType schemas.RequestType,
	providerName schemas.ModelProvider,
	model string,
) *schemas.BifrostChatResponse {
	response := &schemas.BifrostChatResponse{
		ID:     id,
		Object: "chat.completion.chunk",
		Usage:  usage,
		Choices: []schemas.BifrostResponseChoice{
			{
				FinishReason: finishReason,
				ChatStreamResponseChoice: &schemas.ChatStreamResponseChoice{
					Delta: &schemas.ChatStreamResponseChoiceDelta{}, // empty delta
				},
			},
		},
		ExtraFields: schemas.BifrostResponseExtraFields{
			RequestType:    requestType,
			Provider:       providerName,
			ModelRequested: model,
			ChunkIndex:     currentChunkIndex + 1,
		},
	}
	return response
}

// HandleStreamControlSkip checks if the stream control should be skipped.
func HandleStreamControlSkip(bifrostErr *schemas.BifrostError) bool {
	if bifrostErr == nil || bifrostErr.StreamControl == nil {
		return false
	}
	if bifrostErr.StreamControl.SkipStream != nil && *bifrostErr.StreamControl.SkipStream {
		if bifrostErr.StreamControl.LogError != nil && *bifrostErr.StreamControl.LogError {
			getLogger().Warn("Error in stream: " + bifrostErr.Error.Message)
		}
		return true
	}
	return false
}

// GetProviderName extracts the provider name from custom provider configuration.
// If a custom provider key is specified, it returns that; otherwise, it returns the default provider.
// Note: CustomProviderKey is internally set by Bifrost and should always match the provider name.
func GetProviderName(defaultProvider schemas.ModelProvider, customConfig *schemas.CustomProviderConfig) schemas.ModelProvider {
	if customConfig != nil {
		if key := strings.TrimSpace(customConfig.CustomProviderKey); key != "" {
			return schemas.ModelProvider(key)
		}
	}
	return defaultProvider
}

// ProviderSendsDoneMarker returns true if the provider sends the [DONE] marker in streaming responses.
// Some OpenAI-compatible providers (like Cerebras) don't send [DONE] and instead end the stream
// after sending the finish_reason. This function helps determine the correct stream termination logic.
func ProviderSendsDoneMarker(providerName schemas.ModelProvider) bool {
	switch providerName {
	case schemas.Cerebras, schemas.Perplexity, schemas.HuggingFace:
		// Cerebras, Perplexity, and HuggingFace don't send [DONE] marker, ends stream after finish_reason
		return false
	default:
		// Default to expecting [DONE] marker for safety
		return true
	}
}

func ProviderIsResponsesAPINative(providerName schemas.ModelProvider) bool {
	switch providerName {
	case schemas.OpenAI, schemas.OpenRouter, schemas.Azure:
		return true
	default:
		return false
	}
}

// ReleaseStreamingResponse releases a streaming response by draining the body stream and releasing the response.
func ReleaseStreamingResponse(resp *fasthttp.Response) {
	defer func() {
		if r := recover(); r != nil {
			getLogger().Error("recovered panic in ReleaseStreamingResponse: %v", r)
		}
		// Always release the response to prevent leaks, even after a panic
		fasthttp.ReleaseResponse(resp)
	}()
	// Drain any remaining data from the body stream before releasing.
	// This prevents "whitespace in header" errors when the connection is reused
	// (see: https://github.com/valyala/fasthttp/issues/1743).
	if bodyStream := resp.BodyStream(); bodyStream != nil {
		if _, err := io.Copy(io.Discard, bodyStream); err != nil {
			getLogger().Warn("failed to drain streaming response body before release (may cause stale connection reuse): %v", err)
		}
		if closer, ok := bodyStream.(io.Closer); ok {
			if err := closer.Close(); err != nil {
				getLogger().Warn("failed to close streaming response body: %v", err)
			}
		}
	}
}

// GetBifrostResponseForStreamResponse converts the provided responses to a bifrost response.
func GetBifrostResponseForStreamResponse(
	textCompletionResponse *schemas.BifrostTextCompletionResponse,
	chatResponse *schemas.BifrostChatResponse,
	responsesStreamResponse *schemas.BifrostResponsesStreamResponse,
	speechStreamResponse *schemas.BifrostSpeechStreamResponse,
	transcriptionStreamResponse *schemas.BifrostTranscriptionStreamResponse,
	imageGenerationStreamResponse *schemas.BifrostImageGenerationStreamResponse,
) *schemas.BifrostResponse {
	//TODO add bifrost response pooling here
	bifrostResponse := &schemas.BifrostResponse{}

	switch {
	case textCompletionResponse != nil:
		bifrostResponse.TextCompletionResponse = textCompletionResponse
		return bifrostResponse
	case chatResponse != nil:
		bifrostResponse.ChatResponse = chatResponse
		return bifrostResponse
	case responsesStreamResponse != nil:
		bifrostResponse.ResponsesStreamResponse = responsesStreamResponse
		return bifrostResponse
	case speechStreamResponse != nil:
		bifrostResponse.SpeechStreamResponse = speechStreamResponse
		return bifrostResponse
	case transcriptionStreamResponse != nil:
		bifrostResponse.TranscriptionStreamResponse = transcriptionStreamResponse
		return bifrostResponse
	case imageGenerationStreamResponse != nil:
		bifrostResponse.ImageGenerationStreamResponse = imageGenerationStreamResponse
		return bifrostResponse
	}
	return nil
}

// aggregateListModelsResponses merges multiple BifrostListModelsResponse objects into a single response.
// It concatenates all model arrays, deduplicates based on model ID, sums up latencies across all responses,
// and concatenates raw responses into an array.
// When duplicate IDs are found, the first occurrence is kept to maintain the original ordering.
func aggregateListModelsResponses(responses []*schemas.BifrostListModelsResponse) *schemas.BifrostListModelsResponse {
	if len(responses) == 0 {
		return &schemas.BifrostListModelsResponse{
			Data: []schemas.Model{},
		}
	}

	// Always apply deduplication, even for single responses

	// Use a map to track unique model IDs for efficient deduplication
	seenIDs := make(map[string]struct{})
	aggregated := &schemas.BifrostListModelsResponse{
		Data: make([]schemas.Model, 0),
	}

	// Aggregate all models with deduplication, and collect raw responses
	var rawResponses []interface{}

	for _, response := range responses {
		if response == nil {
			continue
		}

		// Add models, skipping duplicates based on ID
		for _, model := range response.Data {
			if _, exists := seenIDs[model.ID]; !exists {
				seenIDs[model.ID] = struct{}{}
				aggregated.Data = append(aggregated.Data, model)
			}
		}

		// Collect raw response if present
		if response.ExtraFields.RawResponse != nil {
			rawResponses = append(rawResponses, response.ExtraFields.RawResponse)
		}
	}

	// Sort models alphabetically by ID
	sort.Slice(aggregated.Data, func(i, j int) bool {
		return aggregated.Data[i].ID < aggregated.Data[j].ID
	})

	if len(rawResponses) > 0 {
		aggregated.ExtraFields.RawResponse = rawResponses
	}

	return aggregated
}

// extractSuccessfulListModelsResponses extracts successful responses from a results channel
// and tracks per-key status information. This utility reduces code duplication across providers
// for handling multi-key ListModels requests.
func extractSuccessfulListModelsResponses(
	results chan schemas.ListModelsByKeyResult,
	providerName schemas.ModelProvider,
) ([]*schemas.BifrostListModelsResponse, []schemas.KeyStatus, *schemas.BifrostError) {
	var successfulResponses []*schemas.BifrostListModelsResponse
	var keyStatuses []schemas.KeyStatus
	var lastError *schemas.BifrostError

	for result := range results {
		if result.Err != nil {
			errMsg := "unknown error"
			if errorField := result.Err.Error; errorField != nil {
				if errorField.Message != "" {
					errMsg = errorField.Message
				} else if errorField.Error != nil {
					errMsg = errorField.Error.Error()
				}
			}
			getLogger().Warn(fmt.Sprintf("failed to list models with key %s: %s", result.KeyID, errMsg))
			keyStatuses = append(keyStatuses, schemas.KeyStatus{
				KeyID:    result.KeyID,
				Provider: providerName,
				Status:   schemas.KeyStatusListModelsFailed,
				Error:    result.Err,
			})
			lastError = result.Err
			continue
		}

		keyStatuses = append(keyStatuses, schemas.KeyStatus{
			KeyID:    result.KeyID,
			Provider: providerName,
			Status:   schemas.KeyStatusSuccess,
		})
		successfulResponses = append(successfulResponses, result.Response)
	}

	if len(successfulResponses) == 0 {
		if lastError != nil {
			return nil, keyStatuses, lastError
		}
		return nil, keyStatuses, &schemas.BifrostError{
			IsBifrostError: false,
			Error: &schemas.ErrorField{
				Message: "all keys failed to list models",
			},
			ExtraFields: schemas.BifrostErrorExtraFields{
				Provider:    providerName,
				RequestType: schemas.ListModelsRequest,
			},
		}
	}

	return successfulResponses, keyStatuses, nil
}

// HandleKeylessListModelsRequest wraps a list models request for keyless providers
// and automatically populates the KeyStatuses field with provider-level status tracking.
// This centralizes the status tracking logic for keyless providers.
func HandleKeylessListModelsRequest(
	provider schemas.ModelProvider,
	listFunc func() (*schemas.BifrostListModelsResponse, *schemas.BifrostError),
) (*schemas.BifrostListModelsResponse, *schemas.BifrostError) {
	resp, bifrostErr := listFunc()

	keyStatus := schemas.KeyStatus{
		KeyID:    "", // Empty for keyless providers
		Provider: provider,
	}

	// If request failed, attach status to error
	if bifrostErr != nil {
		keyStatus.Status = schemas.KeyStatusListModelsFailed
		keyStatus.Error = bifrostErr
		bifrostErr.ExtraFields.KeyStatuses = []schemas.KeyStatus{keyStatus}
		return nil, bifrostErr
	}

	// Success case
	if resp != nil {
		keyStatus.Status = schemas.KeyStatusSuccess
		resp.KeyStatuses = []schemas.KeyStatus{keyStatus}
		return resp, nil
	}

	return resp, bifrostErr
}

// HandleMultipleListModelsRequests handles multiple list models requests concurrently for different keys.
// It launches concurrent requests for all keys and waits for all goroutines to complete.
// It returns the aggregated response with per-key status information or an error if the request fails.
func HandleMultipleListModelsRequests(
	ctx *schemas.BifrostContext,
	keys []schemas.Key,
	request *schemas.BifrostListModelsRequest,
	listModelsByKey func(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostListModelsRequest) (*schemas.BifrostListModelsResponse, *schemas.BifrostError),
) (*schemas.BifrostListModelsResponse, *schemas.BifrostError) {
	startTime := time.Now()

	results := make(chan schemas.ListModelsByKeyResult, len(keys))
	var wg sync.WaitGroup

	// Launch concurrent requests for all keys
	for _, key := range keys {
		wg.Add(1)
		go func(k schemas.Key) {
			defer wg.Done()
			resp, bifrostErr := listModelsByKey(ctx, k, request)
			results <- schemas.ListModelsByKeyResult{Response: resp, Err: bifrostErr, KeyID: k.ID}
		}(key)
	}

	// Wait for all goroutines to complete
	wg.Wait()
	close(results)

	successfulResponses, keyStatuses, err := extractSuccessfulListModelsResponses(results, request.Provider)
	if err != nil {
		// Attach key statuses to error's ExtraFields
		err.ExtraFields.KeyStatuses = keyStatuses
		return nil, err
	}

	// Aggregate all successful responses
	response := aggregateListModelsResponses(successfulResponses)
	response = response.ApplyPagination(request.PageSize, request.PageToken)

	// Attach key statuses to response
	response.KeyStatuses = keyStatuses

	// Set ExtraFields
	latency := time.Since(startTime)
	response.ExtraFields.Provider = request.Provider
	response.ExtraFields.RequestType = schemas.ListModelsRequest
	response.ExtraFields.Latency = latency.Milliseconds()

	return response, nil
}

// GetRandomString generates a random alphanumeric string of the given length.
func GetRandomString(length int) string {
	if length <= 0 {
		return ""
	}
	randomSource := rand.New(rand.NewSource(time.Now().UnixNano()))
	letters := []rune("abcdef0123456789")
	b := make([]rune, length)
	for i := range b {
		b[i] = letters[randomSource.Intn(len(letters))]
	}
	return string(b)
}

// GetReasoningEffortFromBudgetTokens maps a reasoning token budget to OpenAI reasoning effort.
// Valid values: none, low, medium, high
func GetReasoningEffortFromBudgetTokens(
	budgetTokens int,
	minBudgetTokens int,
	maxTokens int,
) string {
	if budgetTokens <= 0 {
		return "none"
	}

	// Defensive defaults
	if maxTokens <= 0 {
		return "medium"
	}

	// Normalize budget
	if budgetTokens < minBudgetTokens {
		budgetTokens = minBudgetTokens
	}
	if budgetTokens > maxTokens {
		budgetTokens = maxTokens
	}

	// Avoid division by zero
	if maxTokens <= minBudgetTokens {
		return "high"
	}

	ratio := float64(budgetTokens-minBudgetTokens) / float64(maxTokens-minBudgetTokens)

	switch {
	case ratio <= 0.25:
		return "low"
	case ratio <= 0.60:
		return "medium"
	default:
		return "high"
	}
}

// GetBudgetTokensFromReasoningEffort converts OpenAI reasoning effort
// into a reasoning token budget.
// effort ∈ {"none", "minimal", "low", "medium", "high"}
func GetBudgetTokensFromReasoningEffort(
	effort string,
	minBudgetTokens int,
	maxTokens int,
) (int, error) {
	if effort == "none" {
		return 0, nil
	}

	if minBudgetTokens > maxTokens {
		return 0, fmt.Errorf("max_tokens must be greater than %d for reasoning", minBudgetTokens)
	}

	// Defensive defaults
	if maxTokens <= minBudgetTokens {
		return minBudgetTokens, nil
	}

	var ratio float64

	switch effort {
	case "minimal":
		ratio = 0.025
	case "low":
		ratio = 0.15
	case "medium":
		ratio = 0.425
	case "high":
		ratio = 0.80
	default:
		// Unknown effort → safe default
		ratio = 0.425
	}

	budget := minBudgetTokens + int(ratio*float64(maxTokens-minBudgetTokens))

	return budget, nil
}

// completeDeferredSpan completes the deferred LLM span for streaming requests.
// This is called when the final chunk is processed (when StreamEndIndicator is true).
// It retrieves the deferred span handle from TraceStore using the trace ID from context,
// populates response attributes from accumulated chunks, and ends the span.
func completeDeferredSpan(ctx *schemas.BifrostContext, result *schemas.BifrostResponse, err *schemas.BifrostError) {
	if ctx == nil {
		return
	}

	// Get the trace ID from context (this IS available in the provider's goroutine)
	traceID, ok := ctx.Value(schemas.BifrostContextKeyTraceID).(string)
	if !ok || traceID == "" {
		return
	}

	// Get the tracer from context
	tracerVal := ctx.Value(schemas.BifrostContextKeyTracer)
	if tracerVal == nil {
		return
	}
	tracer, ok := tracerVal.(schemas.Tracer)
	if !ok || tracer == nil {
		return
	}

	// Get the deferred span handle from TraceStore using trace ID
	handle := tracer.GetDeferredSpanHandle(traceID)
	if handle == nil {
		return
	}

	// Set total latency from the final chunk
	if result != nil {
		extraFields := result.GetExtraFields()
		if extraFields.Latency > 0 {
			tracer.SetAttribute(handle, "gen_ai.response.total_latency_ms", extraFields.Latency)
		}
	}

	// Get accumulated response with full data (content, tool calls, reasoning, etc.)
	// This builds a complete BifrostResponse from all the streaming chunks
	accumulatedResp, ttftNs, chunkCount := tracer.GetAccumulatedChunks(traceID)

	// Set TTFT and chunk count attributes regardless of accumulated response availability
	// (GetAccumulatedChunks may return nil response while still providing valid metrics)
	if ttftNs > 0 {
		tracer.SetAttribute(handle, schemas.AttrTimeToFirstToken, ttftNs)
	}
	if chunkCount > 0 {
		tracer.SetAttribute(handle, schemas.AttrTotalChunks, chunkCount)
	}

	if accumulatedResp != nil {
		// Use accumulated response for attributes (includes full content, tool calls, etc.)
		tracer.PopulateLLMResponseAttributes(ctx, handle, accumulatedResp, err)
	} else if result != nil {
		// Fall back to final chunk if no accumulated data (shouldn't happen normally)
		tracer.PopulateLLMResponseAttributes(ctx, handle, result, err)
	}

	// Finalize aggregated post-hook spans before ending the LLM span
	// This creates one span per plugin with average execution time
	// We need to set the llm.call span ID in context so post-hook spans become its children
	if finalizer, ok := ctx.Value(schemas.BifrostContextKeyPostHookSpanFinalizer).(func(context.Context)); ok && finalizer != nil {
		// Get the deferred span ID (the llm.call span) to set as parent for post-hook spans
		spanID := tracer.GetDeferredSpanID(traceID)
		if spanID != "" {
			finalizerCtx := context.WithValue(ctx, schemas.BifrostContextKeySpanID, spanID)
			finalizer(finalizerCtx)
		} else {
			finalizer(ctx)
		}
	}

	// End span with appropriate status
	if err != nil {
		if err.Error != nil {
			tracer.SetAttribute(handle, "error", err.Error.Message)
		}
		if err.StatusCode != nil {
			tracer.SetAttribute(handle, "status_code", *err.StatusCode)
		}
		tracer.EndSpan(handle, schemas.SpanStatusError, "streaming request failed")
	} else {
		tracer.EndSpan(handle, schemas.SpanStatusOk, "")
	}

	// Clear the deferred span from TraceStore
	tracer.ClearDeferredSpan(traceID)
}

// CheckAndSetDefaultProvider checks if the default provider should be used based on the context.
// It returns the default provider if it should be used, otherwise it returns an empty string.
// Checks if the direct key is set in the context, or if key selection is skipped.
// Or if the available providers are set in the context and the default provider is in the list.
func CheckAndSetDefaultProvider(ctx *schemas.BifrostContext, defaultProvider schemas.ModelProvider) schemas.ModelProvider {
	if ctx != nil {
		if ctx.Value(schemas.BifrostContextKeyDirectKey) != nil || ctx.Value(schemas.BifrostContextKeySkipKeySelection) != nil {
			return defaultProvider
		}
		if ctx.Value(schemas.BifrostContextKeyAvailableProviders) != nil {
			availableProviders, ok := ctx.Value(schemas.BifrostContextKeyAvailableProviders).([]schemas.ModelProvider)
			if !ok || len(availableProviders) == 0 {
				return ""
			}
			getLogger().Debug("[Provider] Available providers: %v, checking %s", availableProviders, defaultProvider)
			if slices.Contains(availableProviders, defaultProvider) {
				return defaultProvider
			}
			return ""
		}
		return defaultProvider
	}
	return defaultProvider
}

// ModelMatchesDenylist reports whether any of the candidate model IDs matches
// an entry in denylist, using both exact and base-model (SameBaseModel) matching.
// Empty candidates are skipped. Returns false immediately if denylist is empty.
func ModelMatchesDenylist(denylist []string, candidates ...string) bool {
	if len(denylist) == 0 {
		return false
	}
	for _, c := range candidates {
		if c == "" {
			continue
		}
		if slices.Contains(denylist, c) {
			return true
		}
		for _, d := range denylist {
			if schemas.SameBaseModel(d, c) {
				return true
			}
		}
	}
	return false
}
