package bedrock

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/protocol/eventstream"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials/stscreds"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/bytedance/sonic"
	"github.com/google/uuid"
	"github.com/maximhq/bifrost/core/providers/anthropic"
	openai "github.com/maximhq/bifrost/core/providers/openai"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

// BedrockProvider implements the Provider interface for AWS Bedrock.
type BedrockProvider struct {
	logger                schemas.Logger                // Logger for provider operations
	client                *http.Client                  // HTTP client for unary API requests (Client.Timeout bounds overall response)
	streamingClient       *http.Client                  // HTTP client for streaming API requests (no Timeout; idle governed by NewIdleTimeoutReader)
	mantleClient          *fasthttp.Client              // fasthttp client for Bedrock Mantle unary requests (OpenAI-compatible and native-Anthropic paths)
	mantleStreamingClient *fasthttp.Client              // fasthttp streaming client for Bedrock Mantle streaming requests
	networkConfig         schemas.NetworkConfig         // Network configuration including extra headers
	customProviderConfig  *schemas.CustomProviderConfig // Custom provider config
	sendBackRawRequest    bool                          // Whether to include raw request in BifrostResponse
	sendBackRawResponse   bool                          // Whether to include raw response in BifrostResponse
}

// assumeRoleCredsCache caches *aws.CredentialsCache instances keyed by the
// unique combination of role parameters so that STS AssumeRole is not called
// on every request. Entries are evicted by assumeRoleCacheSweeper when idle
// (unused) for longer than assumeRoleCacheIdleTTL, so that distinct
// role/session/sourceIdentity combinations that stop being used (e.g. a
// caller that mints a new session name per request) don't accumulate
// forever in this process-lifetime cache.
var assumeRoleCredsCache sync.Map // map[string]*assumeRoleCacheEntry

// assumeRoleCacheEntry wraps a cached *aws.CredentialsCache with a
// lock-free last-used timestamp (unix seconds) so the sweeper can identify
// idle entries without adding any locking to the request path.
type assumeRoleCacheEntry struct {
	credsCache *aws.CredentialsCache
	lastUsed   atomic.Int64
}

// touch updates lastUsed to the current time, synchronously, on the
// caller's own goroutine. Deduplicated against redundant writes: if
// lastUsed already reflects the current second (e.g. another concurrent
// request on this same hot entry touched it moments ago), this is a
// read-only no-op. This matters because Load is cheap and read-only
// (doesn't dirty the cache line other cores may have cached), while Store
// invalidates that cache line across all cores - skipping redundant Stores
// avoids unnecessary cross-core cache-line ping-pong when many concurrent
// requests hit the same hot entry within the same second. Benchmarked
// against two async (channel+worker) alternatives (queued by pointer, and
// queued by key with a worker pool re-looking the entry up) - neither beat
// this direct approach under real multi-core contention (they tied it at
// best, lost to it at worst), so the extra machinery wasn't kept.
func (e *assumeRoleCacheEntry) touch() {
	now := time.Now().Unix()
	if e.lastUsed.Load() != now {
		e.lastUsed.Store(now)
	}
}

// assumeRoleCacheSweepInterval is how often the background sweeper scans
// assumeRoleCredsCache for idle entries. Var (not const) so tests/benchmarks
// can shrink it without waiting on wall-clock time.
var assumeRoleCacheSweepInterval = 5 * time.Minute

// assumeRoleCacheIdleTTL is how long an entry may go unused before the
// sweeper evicts it. This is intentionally well above the STS AssumeRole
// session duration (15 minutes by default) so that active callers never
// have their entry evicted mid-use. Var (not const) so tests/benchmarks can
// shrink it without waiting on wall-clock time.
var assumeRoleCacheIdleTTL = 1 * time.Hour

// assumeRoleCacheSweeperOnce ensures the background sweeper goroutine is
// started at most once per process, lazily, on first use of the cache -
// so packages that never touch Bedrock AssumeRole never pay for it.
var assumeRoleCacheSweeperOnce sync.Once

// ensureAssumeRoleCacheSweeper lazily starts a background goroutine that
// periodically evicts assumeRoleCredsCache entries idle for longer than
// assumeRoleCacheIdleTTL. Eviction only drops Bifrost's local reference to
// the cached *aws.CredentialsCache (and the sts.Client / AssumeRoleProvider
// it holds) - it has no effect on AWS's side; any already-issued temporary
// credentials remain valid until their own natural expiry regardless of
// local cache state.
//
// This is called from signAWSRequest's roleARN branch (not from
// NewBedrockProvider) so the goroutine only ever starts for deployments
// that actually use AssumeRole - most Bedrock configs use static
// access-key/secret-key credentials and never populate this cache at all,
// so gating on first real use avoids running a background ticker forever
// for the common case where it would stay permanently idle.
//
// assumeRoleCacheSweeperOnce is a package-level (not per-provider-instance)
// sync.Once, so this goroutine is started at most once per process even
// across Bedrock provider hot-reloads (UpdateProvider rebuilds a fresh
// *BedrockProvider via NewBedrockProvider on every config change, but
// assumeRoleCredsCache/assumeRoleCacheSweeperOnce are shared globals, not
// fields on that struct).
//
// Known limitation: schemas.Provider has no Close()/Shutdown() hook today,
// and Bifrost.Shutdown() never tears down individual providers - so once
// started, this goroutine runs for the life of the process with no
// graceful-stop path, same as e.g. fasthttp's own per-host connsCleaner.
// Wiring a real stop signal would require adding a teardown hook to
// schemas.Provider and call sites across every provider - out of scope
// here; flagged as a follow-up if it's ever needed.
func ensureAssumeRoleCacheSweeper() {
	assumeRoleCacheSweeperOnce.Do(func() {
		go func() {
			ticker := time.NewTicker(assumeRoleCacheSweepInterval)
			defer ticker.Stop()
			for range ticker.C {
				sweepAssumeRoleCredsCache(time.Now(), assumeRoleCacheIdleTTL)
			}
		}()
	})
}

// sweepAssumeRoleCredsCache performs one eviction pass over
// assumeRoleCredsCache, removing every entry whose lastUsed is older than
// ttl relative to now. Split out from ensureAssumeRoleCacheSweeper's ticker
// loop so it can be driven directly (with a synthetic now/ttl) by tests and
// benchmarks instead of waiting on the real ticker/clock. Returns the
// number of entries evicted.
//
// Known race (lock-free by design, same tradeoff class as the LoadOrStore
// race in getOrCreateAssumeRoleCredsCache's miss path): a concurrent
// touch() can land on an entry between the staleness Load below and the
// Delete call. The re-check immediately before Delete closes almost all of
// that window (it's re-verified as close to the delete as possible without
// a lock), but cannot close it entirely without adding per-entry
// synchronization. Worst case if it does happen: the in-flight request
// keeps using its already-retrieved *aws.CredentialsCache (still valid,
// still usable - Go doesn't invalidate a reference just because the map
// entry pointing to it was deleted), the next request for that key pays
// one redundant sts:AssumeRole call to populate a fresh entry, and the
// cache self-heals from there. No wrong/stale credentials are ever
// returned, and no unbounded growth results - see
// TestAssumeRoleCredsCache_ConcurrentTouchDuringSweep for a stress test
// confirming this stays race-detector-clean and leaves the cache usable
// under maximum touch/sweep contention.
func sweepAssumeRoleCredsCache(now time.Time, ttl time.Duration) int {
	nowUnix := now.Unix()
	ttlSeconds := int64(ttl.Seconds())
	evicted := 0
	assumeRoleCredsCache.Range(func(key, value any) bool {
		entry, ok := value.(*assumeRoleCacheEntry)
		if !ok {
			return true
		}
		if nowUnix-entry.lastUsed.Load() > ttlSeconds {
			// Re-check immediately before deleting: narrows (does not
			// eliminate - see doc comment above) the race window against a
			// concurrent touch() that lands after the first Load above.
			if nowUnix-entry.lastUsed.Load() > ttlSeconds {
				assumeRoleCredsCache.Delete(key)
				evicted++
			}
		}
		return true
	})
	return evicted
}

// getOrCreateAssumeRoleCredsCache returns the cached *aws.CredentialsCache
// for cacheKey, creating (and touching) a new entry via newCredsCache() on
// a cache miss. Extracted from signAWSRequest so the exact real population
// code path is directly callable from benchmarks/tests without needing a
// full signed HTTP request or live AWS credentials (newCredsCache does not
// itself make any network call - the STS AssumeRole call only happens
// lazily, later, inside CredentialsCache.Retrieve).
func getOrCreateAssumeRoleCredsCache(cacheKey string, newCredsCache func() *aws.CredentialsCache) *aws.CredentialsCache {
	ensureAssumeRoleCacheSweeper()

	if cached, ok := assumeRoleCredsCache.Load(cacheKey); ok {
		entry := cached.(*assumeRoleCacheEntry)
		entry.touch() // synchronous, self-deduplicated (see touch's doc comment)
		return entry.credsCache
	}

	// Cache miss / creation path stays synchronous: the sweeper reads
	// lastUsed's zero-value as "very old", so a brand-new entry MUST have
	// an accurate timestamp immediately, or it risks being evicted on the
	// very next sweep pass before it's ever been used.
	newEntry := &assumeRoleCacheEntry{credsCache: newCredsCache()}
	newEntry.touch()
	actual, _ := assumeRoleCredsCache.LoadOrStore(cacheKey, newEntry)
	entry := actual.(*assumeRoleCacheEntry)
	entry.touch() // covers the race where another goroutine created this entry first
	return entry.credsCache
}

// bedrockChatResponsePool provides a pool for Bedrock response objects.
var bedrockChatResponsePool = sync.Pool{
	New: func() interface{} {
		return &BedrockConverseResponse{}
	},
}

// acquireBedrockChatResponse gets a Bedrock response from the pool and resets it.
func acquireBedrockChatResponse() *BedrockConverseResponse {
	resp := bedrockChatResponsePool.Get().(*BedrockConverseResponse)
	*resp = BedrockConverseResponse{} // Reset the struct
	return resp
}

// releaseBedrockChatResponse returns a Bedrock response to the pool.
func releaseBedrockChatResponse(resp *BedrockConverseResponse) {
	if resp != nil {
		bedrockChatResponsePool.Put(resp)
	}
}

// NewBedrockProvider creates a new Bedrock provider instance.
// It initializes the HTTP client with the provided configuration and sets up response pools.
// The client is configured with timeouts and AWS-specific settings.
func NewBedrockProvider(config *schemas.ProviderConfig, logger schemas.Logger) (*BedrockProvider, error) {
	config.CheckAndSetDefaults()

	requestTimeout := time.Second * time.Duration(config.NetworkConfig.DefaultRequestTimeoutInSeconds)

	transport := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		MaxConnsPerHost:       config.NetworkConfig.MaxConnsPerHost,
		MaxIdleConns:          schemas.DefaultMaxIdleConnsPerHost,
		MaxIdleConnsPerHost:   schemas.DefaultMaxIdleConnsPerHost,
		IdleConnTimeout:       time.Second * time.Duration(config.NetworkConfig.KeepAliveTimeoutInSeconds),
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: requestTimeout,
		ExpectContinueTimeout: 1 * time.Second,
		ForceAttemptHTTP2:     config.NetworkConfig.EnforceHTTP2,
	}

	// Disable HTTP/2 auto-negotiation when not explicitly enforced.
	// ForceAttemptHTTP2=false alone does NOT prevent HTTP/2 — Go's http2 package
	// auto-registers h2 via TLSNextProto in init(). Setting TLSNextProto to an
	// empty map prevents ALPN negotiation from upgrading connections to h2.
	if !config.NetworkConfig.EnforceHTTP2 {
		transport.TLSNextProto = make(map[string]func(authority string, c *tls.Conn) http.RoundTripper)
	}

	// Apply TLS settings from NetworkConfig
	caCertPEM := ""
	if config.NetworkConfig.CACertPEM != nil {
		caCertPEM = config.NetworkConfig.CACertPEM.GetValue()
	}
	if config.NetworkConfig.InsecureSkipVerify || caCertPEM != "" {
		tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12}
		if config.NetworkConfig.InsecureSkipVerify {
			tlsConfig.InsecureSkipVerify = true
		}
		if caCertPEM != "" {
			certPool, err := x509.SystemCertPool()
			if err != nil {
				certPool = x509.NewCertPool()
			}
			if !certPool.AppendCertsFromPEM([]byte(caCertPEM)) {
				return nil, fmt.Errorf("failed to parse CA certificate PEM")
			}
			tlsConfig.RootCAs = certPool
		}
		transport.TLSClientConfig = tlsConfig
	}

	client := &http.Client{Transport: transport, Timeout: requestTimeout}
	streamingClient := providerUtils.BuildStreamingHTTPClient(client)

	// fasthttp clients for Bedrock Mantle (shared by OpenAI-compatible and native-Anthropic paths).
	// ReadTimeout is the shared provider request timeout, not an OpenAI-specific value; oversized
	// Anthropic responses are handled by PrepareResponseStreaming, not by these static settings.
	mantleFasthttpClient := &fasthttp.Client{
		ReadTimeout:         requestTimeout,
		WriteTimeout:        requestTimeout,
		MaxConnsPerHost:     config.NetworkConfig.MaxConnsPerHost,
		MaxIdleConnDuration: time.Second * time.Duration(config.NetworkConfig.KeepAliveTimeoutInSeconds),
		MaxConnWaitTimeout:  requestTimeout,
		MaxConnDuration:     time.Second * time.Duration(schemas.DefaultMaxConnDurationInSeconds),
		ConnPoolStrategy:    fasthttp.FIFO,
	}
	mantleFasthttpClient = providerUtils.ConfigureProxy(mantleFasthttpClient, config.ProxyConfig, logger)
	mantleFasthttpClient = providerUtils.ConfigureDialer(mantleFasthttpClient, config.NetworkConfig.AllowPrivateNetwork)
	mantleFasthttpClient = providerUtils.ConfigureTLS(mantleFasthttpClient, config.NetworkConfig, logger)
	mantleStreamingFasthttpClient := providerUtils.BuildStreamingClient(mantleFasthttpClient)

	// Pre-warm response pools
	for i := 0; i < config.ConcurrencyAndBufferSize.Concurrency; i++ {
		bedrockChatResponsePool.Put(&BedrockConverseResponse{})
	}

	return &BedrockProvider{
		logger:                logger,
		client:                client,
		streamingClient:       streamingClient,
		mantleClient:          mantleFasthttpClient,
		mantleStreamingClient: mantleStreamingFasthttpClient,
		networkConfig:         config.NetworkConfig,
		customProviderConfig:  config.CustomProviderConfig,
		sendBackRawRequest:    config.SendBackRawRequest,
		sendBackRawResponse:   config.SendBackRawResponse,
	}, nil
}

// GetProviderKey returns the provider identifier for Bedrock.
func (provider *BedrockProvider) GetProviderKey() schemas.ModelProvider {
	return providerUtils.GetProviderName(schemas.Bedrock, provider.customProviderConfig)
}

// isStreamTransportError reports whether err is a transport-level connection
// failure that occurred while reading the EventStream body — as opposed to a
// semantic error (JSON parse failure, AWS exception event, etc.).
//
// Transport errors are caused by the underlying TCP/HTTP/2 connection being
// closed or reset (e.g. AWS Bedrock closing idle connections after ~60 s).
// They are retryable: the request has not yet been partially processed by the
// provider, so a fresh connection can be used to retry transparently.
//
// Detected cases:
//   - *net.OpError  — "use of closed network connection", connection reset, etc.
//   - *net.DNSError — transient DNS failure
//   - io.ErrUnexpectedEOF — HTTP/2 stream closed mid-frame (body abruptly ended)
func isStreamTransportError(err error) bool {
	if errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	var checksumErr eventstream.ChecksumError
	if errors.As(err, &checksumErr) {
		return true
	}
	var opErr *net.OpError
	var dnsErr *net.DNSError
	return errors.As(err, &opErr) || errors.As(err, &dnsErr)
}

// retryableBedrockExceptions maps AWS Bedrock EventStream exception types (the
// camelCase shape names AWS uses for ConverseStream / InvokeModelWithResponseStream
// in-stream exception members) to a retryable HTTP status code. These exceptions
// are transient and should be retried — the retry gate in executeRequestWithRetries
// checks StatusCode against transientServerStatusCodes (500, 502, 503, 504) for
// same-key retries and perKeyFailureStatusCodes (429) for rotation-triggered retries.
//
// Some AWS exceptions have a native status code the gate does not recognize
// (modelStreamErrorException=424, modelTimeoutException=408); they are mapped to
// the nearest gate-recognized transient code so the retry still fires. The client
// still sees the original exception type — only the internal retry hint is mapped.
//
// modelNotReadyException is an HTTP-level error (not an in-stream member) but is
// kept here because AWS auto-retries it; it is harmless when it never matches a
// stream event. validationException / accessDeniedException / resourceNotFoundException
// are intentionally absent — they are terminal request-bound errors.
var retryableBedrockExceptions = map[string]int{
	"throttlingException":         429,
	"serviceUnavailableException": 503,
	"modelNotReadyException":      503,
	"internalServerException":     500,
	"modelStreamErrorException":   503, // native 424; AWS guidance: "Retry your request"
	"modelTimeoutException":       504, // native 408; processing timeout, transient
}

// newBedrockStreamException builds a BifrostError from an AWS EventStream
// exception message (any :message-type other than "event"). It preserves the
// upstream exception type — the payload's "__type" when present, else the
// :exception-type header value (excType) — so downstream conversion
// (ToBedrockError) forwards it instead of falling back to "InternalServerError".
//
// Retryable exceptions are emitted with IsBifrostError:false and the equivalent
// HTTP status so the retry gate in executeRequestWithRetries handles them;
// non-retryable ones are terminal (IsBifrostError:true). providerName is an
// optional label prefix for the message.
func newBedrockStreamException(providerName, excType string, payload []byte) *schemas.BifrostError {
	errMsg := string(payload)
	var bedrockErr BedrockError
	if err := sonic.Unmarshal(payload, &bedrockErr); err == nil && bedrockErr.Message != "" {
		errMsg = bedrockErr.Message
	}

	fwdType := bedrockErr.Type
	if fwdType == "" {
		fwdType = excType
	}

	prefix := "stream"
	if providerName != "" {
		prefix = providerName + " stream"
	}

	streamErr := &schemas.BifrostError{
		IsBifrostError: false,
		Error: &schemas.ErrorField{
			Message: fmt.Sprintf("%s %s: %s", prefix, excType, errMsg),
		},
	}
	if fwdType != "" {
		streamErr.Type = &fwdType
	}
	if statusCode, ok := retryableBedrockExceptions[excType]; ok {
		sc := statusCode
		streamErr.StatusCode = &sc
	} else {
		streamErr.IsBifrostError = true
	}
	return streamErr
}

// completeRequest sends a request to Bedrock's API and handles the response.
// It constructs the API URL, sets up AWS authentication, and processes the response.
// Returns the response body, request latency, or an error if the request fails.
func (provider *BedrockProvider) completeRequest(ctx *schemas.BifrostContext, jsonData []byte, path string, key schemas.Key, model string) ([]byte, time.Duration, map[string]string, *schemas.BifrostError) {
	config := key.BedrockKeyConfig
	region := resolveBedrockRegion(ctx, key, model)

	// Create the request with the JSON body
	requestURL := fmt.Sprintf("https://bedrock-runtime.%s.amazonaws.com/model/%s", region, path)
	req, err := http.NewRequestWithContext(ctx, "POST", requestURL, bytes.NewBuffer(jsonData))
	if err != nil {
		return nil, 0, nil, &schemas.BifrostError{
			IsBifrostError: true,
			Error: &schemas.ErrorField{
				Message: "error creating request",
				Error:   err,
			},
		}
	}

	// Set any extra headers from network config
	providerUtils.SetExtraHeadersHTTP(ctx, req, provider.networkConfig.ExtraHeaders, nil)

	if filtered := anthropic.FilterBetaHeadersForProvider(anthropic.MergeBetaHeaders(ctx, provider.networkConfig.ExtraHeaders), schemas.Bedrock, provider.networkConfig.BetaHeaderOverrides); len(filtered) > 0 {
		req.Header.Set(anthropic.AnthropicBetaHeader, strings.Join(filtered, ","))
	} else {
		req.Header.Del(anthropic.AnthropicBetaHeader)
	}

	// If Value is set, use API Key authentication - else use IAM role authentication
	if key.Value.GetValue() != "" {
		req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", key.Value.GetValue()))
	} else {
		// Sign the request using either explicit credentials or IAM role authentication
		if err := signAWSRequest(ctx, req, config, region, bedrockSigningService); err != nil {
			return nil, 0, nil, err
		}
	}

	body, latency, providerResponseHeaders, bErr := provider.executeBedrockRequest(req)
	return body, latency, providerResponseHeaders, bErr
}

// executeBedrockRequest sends an already-built (and authenticated) request via the
// unary HTTP client, measures latency, and parses a Bedrock error envelope on non-200
// responses. Used by completeRequest for the bedrock-runtime (Converse) path.
func (provider *BedrockProvider) executeBedrockRequest(req *http.Request) ([]byte, time.Duration, map[string]string, *schemas.BifrostError) {
	// Execute the request and measure latency
	startTime := time.Now()
	resp, err := providerUtils.DoHTTPRequest(provider.client, req)
	latency := time.Since(startTime)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return nil, latency, nil, providerUtils.SetErrorLatency(&schemas.BifrostError{
				IsBifrostError: false,
				Error: &schemas.ErrorField{
					Type:    schemas.Ptr(schemas.RequestCancelled),
					Message: schemas.ErrRequestCancelled,
					Error:   err,
				},
			}, latency)
		}
		// Check for timeout first using net.Error before checking net.OpError
		var netErr net.Error
		if errors.As(err, &netErr) && netErr.Timeout() {
			return nil, latency, nil, providerUtils.SetErrorLatency(providerUtils.NewBifrostTimeoutError(schemas.ErrProviderRequestTimedOut, err), latency)
		}
		if errors.Is(err, http.ErrHandlerTimeout) || errors.Is(err, context.DeadlineExceeded) {
			return nil, latency, nil, providerUtils.SetErrorLatency(providerUtils.NewBifrostTimeoutError(schemas.ErrProviderRequestTimedOut, err), latency)
		}
		// Check for DNS lookup and network errors after timeout checks
		var opErr *net.OpError
		var dnsErr *net.DNSError
		if errors.As(err, &opErr) || errors.As(err, &dnsErr) {
			return nil, latency, nil, providerUtils.SetErrorLatency(&schemas.BifrostError{
				IsBifrostError: false,
				Error: &schemas.ErrorField{
					Message: schemas.ErrProviderNetworkError,
					Error:   err,
				},
			}, latency)
		}
		return nil, latency, nil, providerUtils.SetErrorLatency(&schemas.BifrostError{
			IsBifrostError: false,
			Error: &schemas.ErrorField{
				Message: schemas.ErrProviderDoRequest,
				Error:   err,
			},
		}, latency)
	}

	// Extract provider response headers before closing the body
	providerResponseHeaders := providerUtils.ExtractProviderResponseHeadersFromHTTP(resp)
	defer resp.Body.Close()

	// Read response body
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, latency, providerResponseHeaders, providerUtils.SetErrorLatency(&schemas.BifrostError{
			IsBifrostError: true,
			Error: &schemas.ErrorField{
				Message: "error reading request",
				Error:   err,
			},
		}, latency)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, latency, providerResponseHeaders, providerUtils.SetErrorLatency(parseBedrockHTTPError(resp.StatusCode, resp.Header, body), latency)
	}

	return body, latency, providerResponseHeaders, nil
}

// completeAgentRuntimeRequest sends a request to Bedrock Agent Runtime API and handles the response.
// This is used for operations (like rerank) that are served by bedrock-agent-runtime.
func (provider *BedrockProvider) completeAgentRuntimeRequest(ctx *schemas.BifrostContext, jsonData []byte, path string, key schemas.Key) ([]byte, time.Duration, map[string]string, *schemas.BifrostError) {
	config := key.BedrockKeyConfig

	region := DefaultBedrockRegion
	if config.Region != nil && config.Region.GetValue() != "" {
		region = config.Region.GetValue()
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, fmt.Sprintf("https://bedrock-agent-runtime.%s.amazonaws.com%s", region, path), bytes.NewBuffer(jsonData))
	if err != nil {
		return nil, 0, nil, &schemas.BifrostError{
			IsBifrostError: true,
			Error: &schemas.ErrorField{
				Message: "error creating request",
				Error:   err,
			},
		}
	}

	providerUtils.SetExtraHeadersHTTP(ctx, req, provider.networkConfig.ExtraHeaders, nil)

	if key.Value.GetValue() != "" {
		req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", key.Value.GetValue()))
	} else {
		if err := signAWSRequest(ctx, req, config, region, bedrockSigningService); err != nil {
			return nil, 0, nil, err
		}
	}

	startTime := time.Now()
	resp, err := providerUtils.DoHTTPRequest(provider.client, req)
	latency := time.Since(startTime)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return nil, latency, nil, providerUtils.SetErrorLatency(&schemas.BifrostError{
				IsBifrostError: false,
				Error: &schemas.ErrorField{
					Type:    schemas.Ptr(schemas.RequestCancelled),
					Message: schemas.ErrRequestCancelled,
					Error:   err,
				},
			}, latency)
		}
		var netErr net.Error
		if errors.As(err, &netErr) && netErr.Timeout() {
			return nil, latency, nil, providerUtils.SetErrorLatency(providerUtils.NewBifrostTimeoutError(schemas.ErrProviderRequestTimedOut, err), latency)
		}
		if errors.Is(err, http.ErrHandlerTimeout) || errors.Is(err, context.DeadlineExceeded) {
			return nil, latency, nil, providerUtils.SetErrorLatency(providerUtils.NewBifrostTimeoutError(schemas.ErrProviderRequestTimedOut, err), latency)
		}
		var opErr *net.OpError
		var dnsErr *net.DNSError
		if errors.As(err, &opErr) || errors.As(err, &dnsErr) {
			return nil, latency, nil, providerUtils.SetErrorLatency(&schemas.BifrostError{
				IsBifrostError: false,
				Error: &schemas.ErrorField{
					Message: schemas.ErrProviderNetworkError,
					Error:   err,
				},
			}, latency)
		}
		return nil, latency, nil, providerUtils.SetErrorLatency(&schemas.BifrostError{
			IsBifrostError: false,
			Error: &schemas.ErrorField{
				Message: schemas.ErrProviderDoRequest,
				Error:   err,
			},
		}, latency)
	}

	// Extract provider response headers before closing the body
	providerResponseHeaders := providerUtils.ExtractProviderResponseHeadersFromHTTP(resp)
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, latency, providerResponseHeaders, providerUtils.SetErrorLatency(&schemas.BifrostError{
			IsBifrostError: true,
			Error: &schemas.ErrorField{
				Message: "error reading request",
				Error:   err,
			},
		}, latency)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, latency, providerResponseHeaders, providerUtils.SetErrorLatency(parseBedrockHTTPError(resp.StatusCode, resp.Header, body), latency)
	}

	return body, latency, providerResponseHeaders, nil
}

// makeStreamingRequest creates a streaming request to Bedrock's API.
// It formats the request, sends it to Bedrock, and returns the response.
// Returns the response body and an error if the request fails.
func (provider *BedrockProvider) makeStreamingRequest(ctx *schemas.BifrostContext, jsonData []byte, key schemas.Key, model string, action string) (*http.Response, *schemas.BifrostError) {
	// Parse region and path in one pass to avoid running the regex twice.
	path, region := provider.getModelPathAndRegion(ctx, action, model, key)

	// Create HTTP request for streaming
	requestURL := fmt.Sprintf("https://bedrock-runtime.%s.amazonaws.com/model/%s", region, path)
	req, reqErr := http.NewRequestWithContext(ctx, http.MethodPost, requestURL, bytes.NewReader(jsonData))
	if reqErr != nil {
		return nil, providerUtils.NewBifrostOperationError("error creating request", reqErr)
	}

	// Set any extra headers from network config
	providerUtils.SetExtraHeadersHTTP(ctx, req, provider.networkConfig.ExtraHeaders, nil)

	if filtered := anthropic.FilterBetaHeadersForProvider(anthropic.MergeBetaHeaders(ctx, provider.networkConfig.ExtraHeaders), schemas.Bedrock, provider.networkConfig.BetaHeaderOverrides); len(filtered) > 0 {
		req.Header.Set(anthropic.AnthropicBetaHeader, strings.Join(filtered, ","))
	} else {
		req.Header.Del(anthropic.AnthropicBetaHeader)
	}

	// If Value is set, use API Key authentication - else use IAM role authentication
	req.Header.Set("Accept", "application/vnd.amazon.eventstream")
	// Force identity encoding so Go's net/http transport does NOT auto-negotiate
	// gzip. A gzip-compressed eventstream buffers upstream until the stream
	// completes, collapsing TTFB to the total generation time (issue #4542).
	req.Header.Set("Accept-Encoding", "identity")
	if key.Value.GetValue() != "" {
		req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", key.Value.GetValue()))
	} else {
		// Sign the request using either explicit credentials or IAM role authentication
		if err := signAWSRequest(ctx, req, key.BedrockKeyConfig, region, bedrockSigningService); err != nil {
			return nil, err
		}
	}

	// Make the request
	startTime := time.Now()
	resp, respErr := providerUtils.DoHTTPRequest(provider.streamingClient, req)
	latency := time.Since(startTime)
	if respErr != nil {
		if errors.Is(respErr, context.Canceled) {
			return nil, providerUtils.SetErrorLatency(&schemas.BifrostError{
				IsBifrostError: false,
				Error: &schemas.ErrorField{
					Type:    schemas.Ptr(schemas.RequestCancelled),
					Message: schemas.ErrRequestCancelled,
					Error:   respErr,
				},
			}, latency)
		}
		// Check for timeout first using net.Error before checking net.OpError
		var netErr net.Error
		if errors.As(respErr, &netErr) && netErr.Timeout() {
			return nil, providerUtils.SetErrorLatency(providerUtils.NewBifrostTimeoutError(schemas.ErrProviderRequestTimedOut, respErr), latency)
		}
		if errors.Is(respErr, http.ErrHandlerTimeout) || errors.Is(respErr, context.DeadlineExceeded) {
			return nil, providerUtils.SetErrorLatency(providerUtils.NewBifrostTimeoutError(schemas.ErrProviderRequestTimedOut, respErr), latency)
		}
		// Check for DNS lookup and network errors after timeout checks
		var opErr *net.OpError
		var dnsErr *net.DNSError
		if errors.As(respErr, &opErr) || errors.As(respErr, &dnsErr) {
			return nil, providerUtils.SetErrorLatency(&schemas.BifrostError{
				IsBifrostError: false,
				Error: &schemas.ErrorField{
					Message: schemas.ErrProviderNetworkError,
					Error:   respErr,
				},
			}, latency)
		}
		return nil, providerUtils.SetErrorLatency(&schemas.BifrostError{
			IsBifrostError: false,
			Error: &schemas.ErrorField{
				Message: schemas.ErrProviderDoRequest,
				Error:   respErr,
			},
		}, latency)
	}

	// Extract provider response headers before status check so error responses also forward them
	ctx.SetValue(schemas.BifrostContextKeyProviderResponseHeaders, providerUtils.ExtractProviderResponseHeadersFromHTTP(resp))

	// Check for HTTP errors — use parseBedrockHTTPError to preserve upstream error details
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return nil, providerUtils.SetErrorLatency(parseBedrockHTTPError(resp.StatusCode, resp.Header, body), latency)
	}

	return resp, nil
}

// Returns a BifrostError if signing fails.
func signAWSRequest(
	ctx *schemas.BifrostContext,
	req *http.Request,
	keyCfg *schemas.BedrockKeyConfig,
	region, service string,
) *schemas.BifrostError {
	var accessKey, secretKey schemas.SecretVar
	var sessionToken, roleARN, externalID, sessionName *schemas.SecretVar

	if keyCfg != nil {
		accessKey = keyCfg.AccessKey
		secretKey = keyCfg.SecretKey
		sessionToken = keyCfg.SessionToken
		roleARN = keyCfg.RoleARN
		externalID = keyCfg.ExternalID
		sessionName = keyCfg.RoleSessionName
	}

	// Set required headers before signing (only if not already set)
	if req.Header.Get("Content-Type") == "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if req.Header.Get("Accept") == "" {
		req.Header.Set("Accept", "application/json")
	}

	// Calculate SHA256 hash of the request body
	var bodyHash string
	if req.Body != nil {
		bodyBytes, err := io.ReadAll(req.Body)
		if err != nil {
			return providerUtils.NewBifrostOperationError("error reading request body", err)
		}
		// Restore the body for subsequent reads
		req.Body = io.NopCloser(bytes.NewBuffer(bodyBytes))

		hash := sha256.Sum256(bodyBytes)
		bodyHash = hex.EncodeToString(hash[:])
	} else {
		// For empty body, use the hash of an empty string
		hash := sha256.Sum256([]byte{})
		bodyHash = hex.EncodeToString(hash[:])
	}

	// Set x-amz-content-sha256 header (required for S3, harmless for other AWS services)
	req.Header.Set("x-amz-content-sha256", bodyHash)

	var cfg aws.Config
	var err error

	// If both accessKey and secretKey are empty, use the default credential provider chain
	// This will automatically use IAM roles, environment variables, shared credentials, etc.
	if accessKey.GetValue() == "" && secretKey.GetValue() == "" {
		cfg, err = config.LoadDefaultConfig(ctx,
			config.WithRegion(region),
		)
	} else {
		// Use explicit credentials when provided
		cfg, err = config.LoadDefaultConfig(ctx,
			config.WithRegion(region),
			config.WithCredentialsProvider(aws.CredentialsProviderFunc(func(ctx context.Context) (aws.Credentials, error) {
				creds := aws.Credentials{
					AccessKeyID:     accessKey.GetValue(),
					SecretAccessKey: secretKey.GetValue(),
				}
				if sessionToken != nil && sessionToken.GetValue() != "" {
					creds.SessionToken = sessionToken.GetValue()
				}
				return creds, nil
			})),
		)
	}
	if err != nil {
		return providerUtils.NewBifrostOperationError("failed to load aws config", err)
	}

	if roleARN != nil && roleARN.GetValue() != "" {
		extID := ""
		if externalID != nil {
			extID = externalID.GetValue()
		}
		sessName := "bifrost-session"
		if sessionName != nil && sessionName.GetValue() != "" {
			sessName = sessionName.GetValue()
		}
		sourceIdentity := "default_chain"
		if accessKey.GetValue() != "" || secretKey.GetValue() != "" {
			sourceIdentity = accessKey.GetValue()
			if sessionToken != nil && sessionToken.GetValue() != "" {
				tokenHash := sha256.Sum256([]byte(sessionToken.GetValue()))
				sourceIdentity = sourceIdentity + "|" + hex.EncodeToString(tokenHash[:8])
			}
		}
		cacheKey := strings.Join([]string{
			region,
			roleARN.GetValue(),
			extID,
			sessName,
			sourceIdentity,
		}, "|")

		cfg.Credentials = getOrCreateAssumeRoleCredsCache(cacheKey, func() *aws.CredentialsCache {
			stsClient := sts.NewFromConfig(cfg)

			opts := func(o *stscreds.AssumeRoleOptions) {
				if extID != "" {
					o.ExternalID = aws.String(extID)
				}
				o.RoleSessionName = sessName
			}

			return aws.NewCredentialsCache(
				stscreds.NewAssumeRoleProvider(
					stsClient,
					roleARN.GetValue(),
					opts,
				),
			)
		})
	}

	// Create the AWS signer
	signer := v4.NewSigner()

	// Get credentials
	creds, err := cfg.Credentials.Retrieve(ctx)
	if err != nil {
		return providerUtils.NewBifrostOperationError("failed to retrieve aws credentials", err)
	}

	// Sign the request with AWS Signature V4
	if err := signer.SignHTTP(ctx, creds, req, bodyHash, service, region, time.Now()); err != nil {
		return providerUtils.NewBifrostOperationError("failed to sign request", err)
	}

	return nil
}

// listModelsByKey performs a list models request to Bedrock's API for a single key.
// It retrieves all foundation models available in Amazon Bedrock for a specific key.
// listMantleModels lists models from the Bedrock Mantle (OpenAI-compatible) /v1/models
// endpoint, converted to a Bifrost response with the same allow/blacklist/alias gating as
// the foundation-model path. The bare /v1/models path returns the full mantle catalog
// (including the mantle-only gpt-5.x / gemma-4 models that ListFoundationModels omits).
// The request is signed as it is sent (mantleSigV4Headers signs POST and can't be reused
// for this GET). Best-effort: returns nil on any failure so the foundation-model list is
// still returned.
func (provider *BedrockProvider) listMantleModels(ctx *schemas.BifrostContext, key schemas.Key, region string, unfiltered bool) *schemas.BifrostListModelsResponse {
	mURL := mantleOpenAIURL(region, "", "models")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, mURL, nil)
	if err != nil {
		provider.logger.Warn("failed to build mantle list-models request: %v", err)
		return nil
	}
	providerUtils.SetExtraHeadersHTTP(ctx, req, WithMantleProject(provider.networkConfig.ExtraHeaders, MantleOpenAIProjectHeader, resolveMantleProjectID(ctx, key)), nil)
	if key.Value.GetValue() != "" {
		req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", key.Value.GetValue()))
	} else if bifrostErr := signAWSRequest(ctx, req, key.BedrockKeyConfig, region, bedrockMantleSigningService); bifrostErr != nil {
		provider.logger.Warn("failed to sign mantle list-models request: %v", bifrostErr.Error.Message)
		return nil
	}

	resp, err := providerUtils.DoHTTPRequest(provider.client, req)
	if err != nil {
		provider.logger.Warn("mantle list-models request failed: %v", err)
		return nil
	}
	responseBody, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		provider.logger.Warn("failed to read mantle list-models response: %v", err)
		return nil
	}
	if resp.StatusCode != http.StatusOK {
		provider.logger.Warn("mantle list-models returned status %d: %s", resp.StatusCode, string(responseBody))
		return nil
	}

	mantleResponse := &openai.OpenAIListModelsResponse{}
	if err := sonic.Unmarshal(responseBody, mantleResponse); err != nil {
		provider.logger.Warn("failed to parse mantle list-models response: %v", err)
		return nil
	}
	return mantleResponse.ToBifrostListModelsResponse(provider.GetProviderKey(), key.Models, key.BlacklistedModels, key.Aliases, unfiltered)
}

func (provider *BedrockProvider) listModelsByKey(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostListModelsRequest) (*schemas.BifrostListModelsResponse, *schemas.BifrostError) {
	providerName := provider.GetProviderKey()
	config := key.BedrockKeyConfig
	region := DefaultBedrockRegion
	if config.Region != nil && config.Region.GetValue() != "" {
		region = config.Region.GetValue()
	}

	// Build query parameters
	params := url.Values{}
	if request.ExtraParams != nil {
		if byCustomizationType, ok := request.ExtraParams["byCustomizationType"].(string); ok && byCustomizationType != "" {
			params.Set("byCustomizationType", byCustomizationType)
		}
		if byInferenceType, ok := request.ExtraParams["byInferenceType"].(string); ok && byInferenceType != "" {
			params.Set("byInferenceType", byInferenceType)
		}
		if byOutputModality, ok := request.ExtraParams["byOutputModality"].(string); ok && byOutputModality != "" {
			params.Set("byOutputModality", byOutputModality)
		}
		if byProvider, ok := request.ExtraParams["byProvider"].(string); ok && byProvider != "" {
			params.Set("byProvider", byProvider)
		}
	}

	// List models endpoint uses the bedrock service (not bedrock-runtime)
	url := fmt.Sprintf("https://bedrock.%s.amazonaws.com/foundation-models?%s", region, params.Encode())

	// Create the GET request without a body
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, &schemas.BifrostError{
			IsBifrostError: true,
			Error: &schemas.ErrorField{
				Message: "error creating request",
				Error:   err,
			},
		}
	}

	// Set any extra headers from network config
	providerUtils.SetExtraHeadersHTTP(ctx, req, provider.networkConfig.ExtraHeaders, nil)

	// If Value is set, use API Key authentication - else use IAM role authentication
	if key.Value.GetValue() != "" {
		req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", key.Value.GetValue()))
	} else {
		// Sign the request using either explicit credentials or IAM role authentication

		if err := signAWSRequest(ctx, req, config, region, bedrockSigningService); err != nil {
			return nil, err
		}
	}

	startTime := time.Now()

	// Execute the request
	resp, err := providerUtils.DoHTTPRequest(provider.client, req)
	latency := time.Since(startTime)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return nil, providerUtils.SetErrorLatency(&schemas.BifrostError{
				IsBifrostError: false,
				Error: &schemas.ErrorField{
					Type:    schemas.Ptr(schemas.RequestCancelled),
					Message: schemas.ErrRequestCancelled,
					Error:   err,
				},
			}, latency)
		}
		// Check for timeout first using net.Error before checking net.OpError
		var netErr net.Error
		if errors.As(err, &netErr) && netErr.Timeout() {
			return nil, providerUtils.SetErrorLatency(providerUtils.NewBifrostTimeoutError(schemas.ErrProviderRequestTimedOut, err), latency)
		}
		if errors.Is(err, http.ErrHandlerTimeout) || errors.Is(err, context.DeadlineExceeded) {
			return nil, providerUtils.SetErrorLatency(providerUtils.NewBifrostTimeoutError(schemas.ErrProviderRequestTimedOut, err), latency)
		}
		// Check for DNS lookup and network errors after timeout checks
		var opErr *net.OpError
		var dnsErr *net.DNSError
		if errors.As(err, &opErr) || errors.As(err, &dnsErr) {
			return nil, providerUtils.SetErrorLatency(&schemas.BifrostError{
				IsBifrostError: false,
				Error: &schemas.ErrorField{
					Message: schemas.ErrProviderNetworkError,
					Error:   err,
				},
			}, latency)
		}
		return nil, providerUtils.SetErrorLatency(&schemas.BifrostError{
			IsBifrostError: false,
			Error: &schemas.ErrorField{
				Message: schemas.ErrProviderDoRequest,
				Error:   err,
			},
		}, latency)
	}

	// Read response body and close
	responseBody, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		return nil, providerUtils.SetErrorLatency(&schemas.BifrostError{
			IsBifrostError: true,
			Error: &schemas.ErrorField{
				Message: "error reading request",
				Error:   err,
			},
		}, latency)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, providerUtils.SetErrorLatency(parseBedrockHTTPError(resp.StatusCode, resp.Header, responseBody), latency)
	}

	// Parse Bedrock-specific response
	bedrockResponse := &BedrockListModelsResponse{}
	rawRequest, rawResponse, bifrostErr := providerUtils.HandleProviderResponse(responseBody, bedrockResponse, nil, providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest), providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse))
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Convert to Bifrost response
	response := bedrockResponse.ToBifrostListModelsResponse(providerName, key.Models, key.BlacklistedModels, key.Aliases, request.Unfiltered)
	if response == nil {
		return nil, providerUtils.NewBifrostOperationError("failed to convert Bedrock model list response", nil)
	}

	// Merge in the mantle catalog: ListFoundationModels omits the mantle-only models
	// (gpt-5.x, gemma-4, ...) served on the OpenAI-compatible endpoint. Same gating, dedup by id.
	if mantleResponse := provider.listMantleModels(ctx, key, region, request.Unfiltered); mantleResponse != nil {
		seen := make(map[string]struct{}, len(response.Data))
		for _, m := range response.Data {
			seen[m.ID] = struct{}{}
		}
		for _, m := range mantleResponse.Data {
			if _, ok := seen[m.ID]; ok {
				continue
			}
			seen[m.ID] = struct{}{}
			response.Data = append(response.Data, m)
		}
	}

	response.ExtraFields.Latency = time.Since(startTime).Milliseconds()

	// Set raw request if enabled
	if providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest) {
		response.ExtraFields.RawRequest = rawRequest
	}

	// Set raw response if enabled
	if providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse) {
		response.ExtraFields.RawResponse = rawResponse
	}

	return response, nil
}

// ListModels performs a list models request to Bedrock's API.
// It retrieves all foundation models available in Amazon Bedrock.
// Requests are made concurrently for improved performance.
func (provider *BedrockProvider) ListModels(ctx *schemas.BifrostContext, keys []schemas.Key, request *schemas.BifrostListModelsRequest) (*schemas.BifrostListModelsResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.Bedrock, provider.customProviderConfig, schemas.ListModelsRequest); err != nil {
		return nil, err
	}
	return providerUtils.HandleMultipleListModelsRequests(
		ctx,
		keys,
		request,
		provider.listModelsByKey,
	)
}

// TextCompletion performs a text completion request to Bedrock's API.
// It formats the request, sends it to Bedrock, and processes the response.
// Returns a BifrostResponse containing the completion results or an error if the request fails.
func (provider *BedrockProvider) TextCompletion(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostTextCompletionRequest) (*schemas.BifrostTextCompletionResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.Bedrock, provider.customProviderConfig, schemas.TextCompletionRequest); err != nil {
		return nil, err
	}

	jsonData, bifrostErr := providerUtils.CheckContextAndGetRequestBody(
		ctx,
		request,
		func() (providerUtils.RequestBodyWithExtraParams, error) {
			return ToBedrockTextCompletionRequest(request), nil
		})
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	path, _ := provider.getModelPathAndRegion(ctx, "invoke", request.Model, key)
	body, latency, providerResponseHeaders, err := provider.completeRequest(ctx, jsonData, path, key, request.Model)
	if providerResponseHeaders != nil {
		ctx.SetValue(schemas.BifrostContextKeyProviderResponseHeaders, providerResponseHeaders)
	}
	if err != nil {
		return nil, providerUtils.EnrichError(ctx, err, jsonData, nil, provider.sendBackRawRequest, provider.sendBackRawResponse, latency)
	}

	// Handle model-specific response conversion
	var bifrostResponse *schemas.BifrostTextCompletionResponse
	switch {
	case schemas.IsAnthropicModelFamily(ctx, request.Model):
		var response BedrockAnthropicTextResponse
		if err := sonic.Unmarshal(body, &response); err != nil {
			return nil, providerUtils.NewBifrostOperationError("error parsing anthropic response", err)
		}
		bifrostResponse = response.ToBifrostTextCompletionResponse()

	case schemas.IsMistralModelFamily(ctx, request.Model):
		var response BedrockMistralTextResponse
		if err := sonic.Unmarshal(body, &response); err != nil {
			return nil, providerUtils.NewBifrostOperationError("error parsing mistral response", err)
		}
		bifrostResponse = response.ToBifrostTextCompletionResponse()

	default:
		return nil, providerUtils.NewConfigurationError(fmt.Sprintf("unsupported model type for text completion: %s", request.Model))
	}

	// Set ExtraFields
	bifrostResponse.ExtraFields.Latency = latency.Milliseconds()
	bifrostResponse.ExtraFields.ProviderResponseHeaders = providerResponseHeaders

	// Set raw request if enabled
	if providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest) {
		providerUtils.ParseAndSetRawRequest(&bifrostResponse.ExtraFields, jsonData)
	}

	// Parse raw response if enabled
	if providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse) {
		var rawResponse interface{}
		if err := sonic.Unmarshal(body, &rawResponse); err != nil {
			return nil, providerUtils.NewBifrostOperationError("error parsing raw response", err)
		}
		bifrostResponse.ExtraFields.RawResponse = rawResponse
	}

	return bifrostResponse, nil
}

// TextCompletionStream performs a streaming text completion request to Bedrock's API.
// It formats the request, sends it to Bedrock, and processes the response.
// Returns a channel of BifrostStreamChunk objects or an error if the request fails.
func (provider *BedrockProvider) TextCompletionStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, postHookSpanFinalizer func(context.Context), key schemas.Key, request *schemas.BifrostTextCompletionRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.Bedrock, provider.customProviderConfig, schemas.TextCompletionStreamRequest); err != nil {
		return nil, err
	}

	providerName := provider.GetProviderKey()

	jsonData, bifrostErr := providerUtils.CheckContextAndGetRequestBody(
		ctx,
		request,
		func() (providerUtils.RequestBodyWithExtraParams, error) {
			return ToBedrockTextCompletionRequest(request), nil
		})
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	startTime := time.Now()
	resp, bifrostErr := provider.makeStreamingRequest(ctx, jsonData, key, request.Model, "invoke-with-response-stream")
	if bifrostErr != nil {
		return nil, providerUtils.EnrichError(ctx, bifrostErr, jsonData, nil, provider.sendBackRawRequest, provider.sendBackRawResponse)
	}

	ctx.SetValue(schemas.BifrostContextKeyProviderResponseHeaders, providerUtils.ExtractProviderResponseHeadersFromHTTP(resp))

	// Create response channel
	responseChan := make(chan *schemas.BifrostStreamChunk, schemas.DefaultStreamBufferSize)

	providerUtils.SetStreamIdleTimeoutIfEmpty(ctx, provider.networkConfig.StreamIdleTimeoutInSeconds)

	// Start streaming in a goroutine
	go func() {
		defer providerUtils.EnsureStreamFinalizerCalled(ctx, postHookSpanFinalizer)
		defer func() {
			if ctx.Err() == context.Canceled {
				providerUtils.HandleStreamCancellation(ctx, postHookRunner, responseChan, provider.logger, postHookSpanFinalizer, jsonData)
			} else if ctx.Err() == context.DeadlineExceeded {
				providerUtils.HandleStreamTimeout(ctx, postHookRunner, responseChan, provider.logger, postHookSpanFinalizer, jsonData)
			}
			providerUtils.CloseStream(ctx, responseChan)
		}()
		defer resp.Body.Close()

		// Wrap body with idle timeout to detect stalled streams.
		idleReader, stopIdleTimeout := providerUtils.NewIdleTimeoutReader(resp.Body, resp.Body, providerUtils.GetStreamIdleTimeout(ctx), ctx)
		defer stopIdleTimeout()

		// Setup cancellation handler to close body stream on ctx cancellation
		stopCancellation := providerUtils.SetupStreamCancellation(ctx, resp.Body, provider.logger)
		defer stopCancellation()

		// Process AWS Event Stream format
		decoder := eventstream.NewDecoder()
		payloadBuf := make([]byte, 0, 1024*1024) // 1MB payload buffer

		for {
			// If context was cancelled/timed out, let defer handle it
			if ctx.Err() != nil {
				return
			}
			// Decode a single EventStream message
			message, err := decoder.Decode(idleReader, payloadBuf)
			if err != nil {
				// If context was cancelled/timed out, let defer handle it
				if ctx.Err() != nil {
					return
				}
				if err == io.EOF {
					// End of stream - this is normal
					break
				}
				ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
				provider.logger.Warn("error decoding %s EventStream message: %v", providerName, err)
				// Transport-level errors (stale/closed connection, unexpected EOF) are retryable.
				// Use IsBifrostError:false so the retry gate in executeRequestWithRetries can retry.
				if isStreamTransportError(err) {
					providerUtils.ProcessAndSendBifrostError(ctx, postHookRunner, &schemas.BifrostError{
						IsBifrostError: false,
						Error: &schemas.ErrorField{
							Message: schemas.ErrProviderNetworkError,
							Error:   err,
						},
					}, responseChan, provider.logger, postHookSpanFinalizer)
				} else {
					providerUtils.ProcessAndSendError(ctx, postHookRunner, err, responseChan, provider.logger, postHookSpanFinalizer)
				}
				return
			}

			// Process the decoded message payload (contains JSON for normal events)
			if len(message.Payload) > 0 {
				if msgTypeHeader := message.Headers.Get(":message-type"); msgTypeHeader != nil {
					if msgType := msgTypeHeader.String(); msgType != "event" {
						excType := msgType
						if excHeader := message.Headers.Get(":exception-type"); excHeader != nil {
							if v := excHeader.String(); v != "" {
								excType = v
							}
						}
						streamErr := newBedrockStreamException(string(providerName), excType, message.Payload)
						providerUtils.ProcessAndSendBifrostError(ctx, postHookRunner, streamErr, responseChan, provider.logger, postHookSpanFinalizer)
						return
					}
				}

				// Parse the chunk payload
				var chunkPayload struct {
					Bytes []byte `json:"bytes"`
				}
				if err := sonic.Unmarshal(message.Payload, &chunkPayload); err != nil {
					provider.logger.Debug("Failed to parse JSON from event buffer: %v, data: %s", err, string(message.Payload))
					providerUtils.ProcessAndSendError(ctx, postHookRunner, err, responseChan, provider.logger, postHookSpanFinalizer)
					return
				}

				// Create BifrostStreamChunk response containing the raw model-specific JSON chunk
				textResponse := &schemas.BifrostTextCompletionResponse{
					ExtraFields: schemas.BifrostResponseExtraFields{
						Latency: time.Since(startTime).Milliseconds(),
						// Pass the raw JSON string from the chunk bytes
						RawResponse: string(chunkPayload.Bytes),
					},
				}

				providerUtils.ProcessAndSendResponse(ctx, postHookRunner, providerUtils.GetBifrostResponseForStreamResponse(textResponse, nil, nil, nil, nil, nil), responseChan, postHookSpanFinalizer)
			}
		}
	}()

	return responseChan, nil
}

// ChatCompletion performs a chat completion request to Bedrock's API.
// OpenAI-family and Gemma 4 models route via the Bedrock Mantle OpenAI-compatible endpoint.
// All other models (including Anthropic/Claude) use the Bedrock Converse API.
// Returns a BifrostResponse containing the completion results or an error if the request fails.
func (provider *BedrockProvider) ChatCompletion(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostChatRequest) (*schemas.BifrostChatResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.Bedrock, provider.customProviderConfig, schemas.ChatCompletionRequest); err != nil {
		return nil, err
	}

	if isMantleModel(ctx, request.Model) {
		return provider.mantleChatCompletions(ctx, key, request)
	}

	// Use Bedrock Converse API for all other models
	jsonData, bifrostErr := providerUtils.CheckContextAndGetRequestBody(
		ctx,
		request,
		func() (providerUtils.RequestBodyWithExtraParams, error) {
			return ToBedrockChatCompletionRequest(ctx, request)
		})
	if bifrostErr != nil {
		return nil, bifrostErr
	}
	path, _ := provider.getModelPathAndRegion(ctx, "converse", request.Model, key)

	// Create the signed request
	responseBody, latency, providerResponseHeaders, bifrostErr := provider.completeRequest(ctx, jsonData, path, key, request.Model)
	if providerResponseHeaders != nil {
		ctx.SetValue(schemas.BifrostContextKeyProviderResponseHeaders, providerResponseHeaders)
	}
	if bifrostErr != nil {
		return nil, providerUtils.EnrichError(ctx, bifrostErr, jsonData, nil, provider.sendBackRawRequest, provider.sendBackRawResponse, latency)
	}

	// Parse Bedrock Converse API response
	bedrockResponse := acquireBedrockChatResponse()
	defer releaseBedrockChatResponse(bedrockResponse)

	// Parse the response using the new Bedrock type
	if err := sonic.Unmarshal(responseBody, bedrockResponse); err != nil {
		return nil, providerUtils.EnrichError(ctx, providerUtils.NewBifrostOperationError("failed to parse bedrock response", err), jsonData, responseBody, provider.sendBackRawRequest, provider.sendBackRawResponse, latency)
	}

	// Convert using the new response converter
	bifrostResponse, err := bedrockResponse.ToBifrostChatResponse(ctx, request.Model)
	if err != nil {
		return nil, providerUtils.EnrichError(ctx, providerUtils.NewBifrostOperationError("failed to convert bedrock response", err), jsonData, responseBody, provider.sendBackRawRequest, provider.sendBackRawResponse, latency)
	}

	// Override finish reason for structured output (Converse API only)
	if _, ok := ctx.Value(schemas.BifrostContextKeyStructuredOutputToolName).(string); ok {
		if len(bifrostResponse.Choices) > 0 && bifrostResponse.Choices[0].FinishReason != nil {
			if *bifrostResponse.Choices[0].FinishReason == string(schemas.BifrostFinishReasonToolCalls) {
				bifrostResponse.Choices[0].FinishReason = schemas.Ptr(string(schemas.BifrostFinishReasonStop))
			}
		}
	}

	// Set ExtraFields
	bifrostResponse.ExtraFields.Latency = latency.Milliseconds()
	bifrostResponse.ExtraFields.ProviderResponseHeaders = providerResponseHeaders

	if providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest) {
		providerUtils.ParseAndSetRawRequest(&bifrostResponse.ExtraFields, jsonData)
	}
	if providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse) {
		var rawResponse interface{}
		if err := sonic.Unmarshal(responseBody, &rawResponse); err == nil {
			bifrostResponse.ExtraFields.RawResponse = rawResponse
		}
	}

	return bifrostResponse, nil
}

// normalizeCachedUsage folds the accumulated cached read/write token counts into
// PromptTokens. Bedrock reports TotalTokens directly on the stream, so only the
// prompt counter needs the fold. The accumulator must apply it before billing -
// including on a mid-stream cancel/timeout. The += is not idempotent; callers
// guard with a flag to apply it exactly once.
func normalizeCachedUsage(usage *schemas.BifrostLLMUsage) {
	if usage == nil || usage.PromptTokensDetails == nil {
		return
	}
	usage.PromptTokens += usage.PromptTokensDetails.CachedReadTokens + usage.PromptTokensDetails.CachedWriteTokens
}

func accumulateBedrockResponsesUsage(usage *schemas.ResponsesResponseUsage, billedUsage *schemas.BifrostLLMUsage, usageToProcess *BedrockTokenUsage) {
	if usage == nil || usageToProcess == nil {
		return
	}
	if usageToProcess.InputTokens > usage.InputTokens {
		usage.InputTokens = usageToProcess.InputTokens
		if billedUsage != nil {
			billedUsage.PromptTokens = usageToProcess.InputTokens
		}
	}
	if usageToProcess.OutputTokens > usage.OutputTokens {
		usage.OutputTokens = usageToProcess.OutputTokens
		if billedUsage != nil {
			billedUsage.CompletionTokens = usageToProcess.OutputTokens
		}
	}
	if usageToProcess.TotalTokens > usage.TotalTokens {
		usage.TotalTokens = usageToProcess.TotalTokens
		if billedUsage != nil {
			billedUsage.TotalTokens = usageToProcess.TotalTokens
		}
	}
	if usageToProcess.CacheReadInputTokens > 0 {
		if usage.InputTokensDetails == nil {
			usage.InputTokensDetails = &schemas.ResponsesResponseInputTokens{}
		}
		if billedUsage != nil && billedUsage.PromptTokensDetails == nil {
			billedUsage.PromptTokensDetails = &schemas.ChatPromptTokensDetails{}
		}
		if usageToProcess.CacheReadInputTokens > usage.InputTokensDetails.CachedReadTokens {
			usage.InputTokensDetails.CachedReadTokens = usageToProcess.CacheReadInputTokens
			if billedUsage != nil {
				billedUsage.PromptTokensDetails.CachedReadTokens = usageToProcess.CacheReadInputTokens
			}
		}
	}
	if usageToProcess.CacheWriteInputTokens > 0 {
		if usage.InputTokensDetails == nil {
			usage.InputTokensDetails = &schemas.ResponsesResponseInputTokens{}
		}
		if billedUsage != nil && billedUsage.PromptTokensDetails == nil {
			billedUsage.PromptTokensDetails = &schemas.ChatPromptTokensDetails{}
		}
		if usageToProcess.CacheWriteInputTokens > usage.InputTokensDetails.CachedWriteTokens {
			usage.InputTokensDetails.CachedWriteTokens = usageToProcess.CacheWriteInputTokens
			if billedUsage != nil {
				billedUsage.PromptTokensDetails.CachedWriteTokens = usageToProcess.CacheWriteInputTokens
			}
		}
		if usageToProcess.CacheDetails != nil {
			if usage.InputTokensDetails.CachedWriteTokenDetails == nil {
				usage.InputTokensDetails.CachedWriteTokenDetails = &schemas.ChatCachedWriteTokenDetails{}
			}
			if billedUsage != nil && billedUsage.PromptTokensDetails.CachedWriteTokenDetails == nil {
				billedUsage.PromptTokensDetails.CachedWriteTokenDetails = &schemas.ChatCachedWriteTokenDetails{}
			}
			for _, cacheDetail := range *usageToProcess.CacheDetails {
				if cacheDetail.TTL == BedrockCacheWriteTTL5m {
					usage.InputTokensDetails.CachedWriteTokenDetails.CachedWriteTokens5m = cacheDetail.InputTokens
					if billedUsage != nil {
						billedUsage.PromptTokensDetails.CachedWriteTokenDetails.CachedWriteTokens5m = cacheDetail.InputTokens
					}
				}
				if cacheDetail.TTL == BedrockCacheWriteTTL1h {
					usage.InputTokensDetails.CachedWriteTokenDetails.CachedWriteTokens1h = cacheDetail.InputTokens
					if billedUsage != nil {
						billedUsage.PromptTokensDetails.CachedWriteTokenDetails.CachedWriteTokens1h = cacheDetail.InputTokens
					}
				}
			}
		}
	}
}

// ChatCompletionStream performs a streaming chat completion request to Bedrock's API.
// OpenAI-family and Gemma 4 models route via the Bedrock Mantle OpenAI-compatible endpoint.
// All other models (including Anthropic/Claude) use the Bedrock Converse streaming API.
// Returns a channel for streaming BifrostStreamChunk objects or an error if the request fails.
func (provider *BedrockProvider) ChatCompletionStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, postHookSpanFinalizer func(context.Context), key schemas.Key, request *schemas.BifrostChatRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.Bedrock, provider.customProviderConfig, schemas.ChatCompletionStreamRequest); err != nil {
		return nil, err
	}

	if isMantleModel(ctx, request.Model) {
		return provider.mantleChatCompletionsStream(ctx, postHookRunner, postHookSpanFinalizer, key, request)
	}

	// Use Bedrock Converse streaming API for all other models
	jsonData, bifrostErr := providerUtils.CheckContextAndGetRequestBody(
		ctx,
		request,
		func() (providerUtils.RequestBodyWithExtraParams, error) {
			return ToBedrockChatCompletionRequest(ctx, request)
		})
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	startTime := time.Now()

	resp, bifrostErr := provider.makeStreamingRequest(ctx, jsonData, key, request.Model, "converse-stream")
	if bifrostErr != nil {
		return nil, providerUtils.EnrichError(ctx, bifrostErr, jsonData, nil, provider.sendBackRawRequest, provider.sendBackRawResponse)
	}

	ctx.SetValue(schemas.BifrostContextKeyProviderResponseHeaders, providerUtils.ExtractProviderResponseHeadersFromHTTP(resp))

	// Create response channel
	responseChan := make(chan *schemas.BifrostStreamChunk, schemas.DefaultStreamBufferSize)

	providerUtils.SetStreamIdleTimeoutIfEmpty(ctx, provider.networkConfig.StreamIdleTimeoutInSeconds)
	// Start streaming in a goroutine
	go func() {
		defer providerUtils.EnsureStreamFinalizerCalled(ctx, postHookSpanFinalizer)
		defer func() {
			if ctx.Err() == context.Canceled {
				providerUtils.HandleStreamCancellation(ctx, postHookRunner, responseChan, provider.logger, postHookSpanFinalizer, jsonData)
			} else if ctx.Err() == context.DeadlineExceeded {
				providerUtils.HandleStreamTimeout(ctx, postHookRunner, responseChan, provider.logger, postHookSpanFinalizer, jsonData)
			}
			providerUtils.CloseStream(ctx, responseChan)
		}()
		defer resp.Body.Close()

		// Wrap body with idle timeout to detect stalled streams.
		idleReader, stopIdleTimeout := providerUtils.NewIdleTimeoutReader(resp.Body, resp.Body, providerUtils.GetStreamIdleTimeout(ctx), ctx)
		defer stopIdleTimeout()

		// Setup cancellation handler to close body stream on ctx cancellation
		stopCancellation := providerUtils.SetupStreamCancellation(ctx, resp.Body, provider.logger)
		defer stopCancellation()

		// Process AWS Event Stream format
		usage := &schemas.BifrostLLMUsage{}
		// Register the accumulating usage handle so a mid-stream
		// cancel/timeout can bill for tokens the provider already processed.
		ctx.SetValue(schemas.BifrostContextKeyStreamAccumulatedUsage, usage)

		// Fold cached tokens into PromptTokens exactly once at stream end. The EOF
		// path calls normalizeUsage() after the loop; on a mid-stream cancel/timeout
		// the deferred call below runs first (LIFO, registered after the cancellation
		// handler at the top of the goroutine) so HandleStreamCancellation/Timeout
		// bills the normalized totals.
		usageNormalized := false
		normalizeUsage := func() {
			if usageNormalized {
				return
			}
			usageNormalized = true
			normalizeCachedUsage(usage)
		}
		defer func() {
			if ctx.Err() != nil {
				normalizeUsage()
			}
		}()
		var finishReason *string
		chunkIndex := 0

		// Process AWS Event Stream format using proper decoder
		lastChunkTime := startTime
		decoder := eventstream.NewDecoder()
		payloadBuf := make([]byte, 0, 1024*1024) // 1MB payload buffer

		// Bedrock does not provide a unique identifier for the stream, so we generate one ourselves
		id := uuid.New().String()

		// Check for structured output mode - if set, we need to intercept tool calls
		// and convert them to content instead of forwarding as tool calls
		var structuredOutputToolName string
		if toolName, ok := ctx.Value(schemas.BifrostContextKeyStructuredOutputToolName).(string); ok {
			structuredOutputToolName = toolName
		}

		streamState := NewBedrockStreamStateWithContext(ctx)
		var isAccumulatingStructuredOutput bool
		var structuredOutputBuilder strings.Builder

		for {
			if ctx.Err() != nil {
				return
			}
			// Decode a single EventStream message
			message, err := decoder.Decode(idleReader, payloadBuf)
			if err != nil {
				// If context was cancelled/timed out, let defer handle it
				if ctx.Err() != nil {
					return
				}
				// End of stream - this is normal
				if err == io.EOF {
					break
				}
				ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
				provider.logger.Warn("Error decoding EventStream message: %v", err)
				// Transport-level errors (stale/closed connection, unexpected EOF) are retryable.
				// Use IsBifrostError:false so the retry gate in executeRequestWithRetries can retry.
				if isStreamTransportError(err) {
					providerUtils.ProcessAndSendBifrostError(ctx, postHookRunner, &schemas.BifrostError{
						IsBifrostError: false,
						Error: &schemas.ErrorField{
							Message: schemas.ErrProviderNetworkError,
							Error:   err,
						},
					}, responseChan, provider.logger, postHookSpanFinalizer)
				} else {
					providerUtils.ProcessAndSendError(ctx, postHookRunner, err, responseChan, provider.logger, postHookSpanFinalizer)
				}
				return
			}

			// Process the decoded message payload (contains JSON for normal events)
			if len(message.Payload) > 0 {
				if msgTypeHeader := message.Headers.Get(":message-type"); msgTypeHeader != nil {
					if msgType := msgTypeHeader.String(); msgType != "event" {
						excType := msgType
						if excHeader := message.Headers.Get(":exception-type"); excHeader != nil {
							if v := excHeader.String(); v != "" {
								excType = v
							}
						}
						streamErr := newBedrockStreamException("", excType, message.Payload)
						providerUtils.ProcessAndSendBifrostError(ctx, postHookRunner, streamErr, responseChan, provider.logger, postHookSpanFinalizer)
						return
					}
				}

				// Converse API path: parse Bedrock Converse-specific stream events
				var streamEvent BedrockStreamEvent
				if err := sonic.Unmarshal(message.Payload, &streamEvent); err != nil {
					provider.logger.Debug("Failed to parse JSON from event buffer: %v, data: %s", err, string(message.Payload))
					providerUtils.ProcessAndSendError(ctx, postHookRunner, err, responseChan, provider.logger, postHookSpanFinalizer)
					return
				}

				if streamEvent.Usage != nil {
					// Accumulate usage information instead of overwriting
					// In some cases usage comes in multiple events, so we need to take the maximum values
					if streamEvent.Usage.InputTokens > usage.PromptTokens {
						usage.PromptTokens = streamEvent.Usage.InputTokens
					}
					if streamEvent.Usage.OutputTokens > usage.CompletionTokens {
						usage.CompletionTokens = streamEvent.Usage.OutputTokens
					}
					if streamEvent.Usage.TotalTokens > usage.TotalTokens {
						usage.TotalTokens = streamEvent.Usage.TotalTokens
					}
					// Handle cached tokens if present
					if streamEvent.Usage.CacheReadInputTokens > 0 {
						if usage.PromptTokensDetails == nil {
							usage.PromptTokensDetails = &schemas.ChatPromptTokensDetails{}
						}
						if streamEvent.Usage.CacheReadInputTokens > usage.PromptTokensDetails.CachedReadTokens {
							usage.PromptTokensDetails.CachedReadTokens = streamEvent.Usage.CacheReadInputTokens
						}
					}
					if streamEvent.Usage.CacheWriteInputTokens > 0 {
						if usage.PromptTokensDetails == nil {
							usage.PromptTokensDetails = &schemas.ChatPromptTokensDetails{}
						}
						if streamEvent.Usage.CacheWriteInputTokens > usage.PromptTokensDetails.CachedWriteTokens {
							usage.PromptTokensDetails.CachedWriteTokens = streamEvent.Usage.CacheWriteInputTokens
						}
						if streamEvent.Usage.CacheDetails != nil {
							if usage.PromptTokensDetails.CachedWriteTokenDetails == nil {
								usage.PromptTokensDetails.CachedWriteTokenDetails = &schemas.ChatCachedWriteTokenDetails{}
							}
							for _, cacheDetail := range *streamEvent.Usage.CacheDetails {
								if cacheDetail.TTL == BedrockCacheWriteTTL5m {
									usage.PromptTokensDetails.CachedWriteTokenDetails.CachedWriteTokens5m = cacheDetail.InputTokens
								}
								if cacheDetail.TTL == BedrockCacheWriteTTL1h {
									usage.PromptTokensDetails.CachedWriteTokenDetails.CachedWriteTokens1h = cacheDetail.InputTokens
								}
							}
						}
					}
				}

				if streamEvent.StopReason != nil {
					finishReason = schemas.Ptr(anthropic.ConvertAnthropicFinishReasonToBifrost(anthropic.AnthropicStopReason(*streamEvent.StopReason)))

					// Override finish reason for structured output
					// When structured output is used, tool_use stop reason should appear as "stop" to the client
					if structuredOutputToolName != "" && *finishReason == string(schemas.BifrostFinishReasonToolCalls) {
						finishReason = schemas.Ptr(string(schemas.BifrostFinishReasonStop))
					}
				}

				// Handle structured output: intercept tool calls for the structured output tool
				// and convert them to content instead of forwarding as tool calls
				if structuredOutputToolName != "" {
					// Check for tool use start event
					if streamEvent.Start != nil && streamEvent.Start.ToolUse != nil {
						if streamEvent.Start.ToolUse.Name == structuredOutputToolName {
							// This is the structured output tool - start accumulating, don't forward
							isAccumulatingStructuredOutput = true
							continue
						}
					}

					// Check for tool use delta event
					if streamEvent.Delta != nil && streamEvent.Delta.ToolUse != nil && isAccumulatingStructuredOutput {
						// Accumulate the input for tracking purposes
						structuredOutputBuilder.WriteString(streamEvent.Delta.ToolUse.Input)

						// Convert tool use delta to content delta
						content := streamEvent.Delta.ToolUse.Input
						response := &schemas.BifrostChatResponse{
							ID:     id,
							Model:  request.Model,
							Object: "chat.completion.chunk",
							Choices: []schemas.BifrostResponseChoice{
								{
									Index: 0,
									ChatStreamResponseChoice: &schemas.ChatStreamResponseChoice{
										Delta: &schemas.ChatStreamResponseChoiceDelta{
											Content: &content,
										},
									},
								},
							},
							ExtraFields: schemas.BifrostResponseExtraFields{
								ChunkIndex: chunkIndex,
								Latency:    time.Since(lastChunkTime).Milliseconds(),
							},
						}
						chunkIndex++
						lastChunkTime = time.Now()

						if providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse) {
							response.ExtraFields.RawResponse = string(message.Payload)
						}

						providerUtils.ProcessAndSendResponse(ctx, postHookRunner, providerUtils.GetBifrostResponseForStreamResponse(nil, response, nil, nil, nil, nil), responseChan, postHookSpanFinalizer)
						continue
					}

					// Suppress non-tool content events that would leak into the
					// assembled structured output (mirrors ResponsesStreamRequest).
					if streamEvent.Delta != nil && (streamEvent.Delta.Text != nil || streamEvent.Delta.ReasoningContent != nil) {
						continue
					}
					if streamEvent.Start != nil && streamEvent.Start.ToolUse == nil {
						continue // non-tool content-block start (text block) — drop
					}
				}

				response, bifrostErr, _ := streamEvent.ToBifrostChatCompletionStream(streamState)
				if bifrostErr != nil {
					ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
					providerUtils.ProcessAndSendBifrostError(ctx, postHookRunner, bifrostErr, responseChan, provider.logger, postHookSpanFinalizer)
					return
				}
				if response != nil {
					response.ID = id
					response.Model = request.Model
					response.ExtraFields = schemas.BifrostResponseExtraFields{
						ChunkIndex: chunkIndex,
						Latency:    time.Since(lastChunkTime).Milliseconds(),
					}
					chunkIndex++
					lastChunkTime = time.Now()

					if providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse) {
						response.ExtraFields.RawResponse = string(message.Payload)
					}

					providerUtils.ProcessAndSendResponse(ctx, postHookRunner, providerUtils.GetBifrostResponseForStreamResponse(nil, response, nil, nil, nil, nil), responseChan, postHookSpanFinalizer)
				}
			}
		}

		normalizeUsage()

		// Send final chunk with accumulated usage
		response := providerUtils.CreateBifrostChatCompletionChunkResponse(id, usage, finishReason, chunkIndex, request.Model, 0)
		// Set raw request if enabled
		if providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest) {
			providerUtils.ParseAndSetRawRequest(&response.ExtraFields, jsonData)
		}
		response.ExtraFields.Latency = time.Since(startTime).Milliseconds()
		ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
		providerUtils.ProcessAndSendResponse(ctx, postHookRunner, providerUtils.GetBifrostResponseForStreamResponse(nil, response, nil, nil, nil, nil), responseChan, postHookSpanFinalizer)
	}()

	return responseChan, nil
}

// Responses performs a responses request to Bedrock's API.
// OpenAI-family and Gemma 4 models route via the Bedrock Mantle OpenAI-compatible endpoint.
// All other models (including Anthropic/Claude) use the Bedrock Converse API.
// Returns a BifrostResponse containing the completion results or an error if the request fails.
func (provider *BedrockProvider) Responses(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostResponsesRequest) (*schemas.BifrostResponsesResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.Bedrock, provider.customProviderConfig, schemas.ResponsesRequest); err != nil {
		return nil, err
	}

	if isMantleModel(ctx, request.Model) {
		return provider.mantleResponses(ctx, key, request)
	}

	// Use Bedrock Converse API for all other models
	jsonData, bifrostErr := providerUtils.CheckContextAndGetRequestBody(
		ctx,
		request,
		func() (providerUtils.RequestBodyWithExtraParams, error) {
			return ToBedrockResponsesRequest(ctx, request)
		})
	if bifrostErr != nil {
		return nil, bifrostErr
	}
	path, _ := provider.getModelPathAndRegion(ctx, "converse", request.Model, key)

	// Create the signed request
	responseBody, latency, providerResponseHeaders, bifrostErr := provider.completeRequest(ctx, jsonData, path, key, request.Model)
	if providerResponseHeaders != nil {
		ctx.SetValue(schemas.BifrostContextKeyProviderResponseHeaders, providerResponseHeaders)
	}
	if bifrostErr != nil {
		return nil, providerUtils.EnrichError(ctx, bifrostErr, jsonData, nil, provider.sendBackRawRequest, provider.sendBackRawResponse, latency)
	}

	// Parse Bedrock Converse API response
	bedrockResponse := acquireBedrockChatResponse()
	defer releaseBedrockChatResponse(bedrockResponse)

	// Parse the response using the new Bedrock type
	if err := sonic.Unmarshal(responseBody, bedrockResponse); err != nil {
		return nil, providerUtils.EnrichError(ctx, providerUtils.NewBifrostOperationError("failed to parse bedrock response", err), jsonData, responseBody, provider.sendBackRawRequest, provider.sendBackRawResponse, latency)
	}

	// Convert using the new response converter
	bifrostResponse, err := bedrockResponse.ToBifrostResponsesResponse(ctx)
	if err != nil {
		return nil, providerUtils.EnrichError(ctx, providerUtils.NewBifrostOperationError("failed to convert bedrock response", err), jsonData, responseBody, provider.sendBackRawRequest, provider.sendBackRawResponse, latency)
	}

	bifrostResponse.Model = request.Model

	// Set ExtraFields
	bifrostResponse.ExtraFields.Latency = latency.Milliseconds()
	bifrostResponse.ExtraFields.ProviderResponseHeaders = providerResponseHeaders

	// Set raw request if enabled
	if providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest) {
		providerUtils.ParseAndSetRawRequest(&bifrostResponse.ExtraFields, jsonData)
	}

	// Set raw response if enabled
	if providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse) {
		var rawResponse interface{}
		if err := sonic.Unmarshal(responseBody, &rawResponse); err == nil {
			bifrostResponse.ExtraFields.RawResponse = rawResponse
		}
	}

	return bifrostResponse, nil
}

// ResponsesStream performs a streaming chat completion request to Bedrock's API.
// It formats the request, sends it to Bedrock, and processes the streaming response.
// Returns a channel for streaming BifrostResponse objects or an error if the request fails.
func (provider *BedrockProvider) ResponsesStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, postHookSpanFinalizer func(context.Context), key schemas.Key, request *schemas.BifrostResponsesRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.Bedrock, provider.customProviderConfig, schemas.ResponsesStreamRequest); err != nil {
		return nil, err
	}

	if isMantleModel(ctx, request.Model) {
		return provider.mantleResponsesStream(ctx, postHookRunner, postHookSpanFinalizer, key, request)
	}

	// Use Bedrock Converse streaming API for all other models
	jsonData, bifrostErr := providerUtils.CheckContextAndGetRequestBody(
		ctx,
		request,
		func() (providerUtils.RequestBodyWithExtraParams, error) {
			return ToBedrockResponsesRequest(ctx, request)
		})
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	startTime := time.Now()

	resp, bifrostErr := provider.makeStreamingRequest(ctx, jsonData, key, request.Model, "converse-stream")
	latency := time.Since(startTime)
	if bifrostErr != nil {
		return nil, providerUtils.EnrichError(ctx, bifrostErr, jsonData, nil, provider.sendBackRawRequest, provider.sendBackRawResponse, latency)
	}

	ctx.SetValue(schemas.BifrostContextKeyProviderResponseHeaders, providerUtils.ExtractProviderResponseHeadersFromHTTP(resp))

	// Create response channel
	responseChan := make(chan *schemas.BifrostStreamChunk, schemas.DefaultStreamBufferSize)

	providerUtils.SetStreamIdleTimeoutIfEmpty(ctx, provider.networkConfig.StreamIdleTimeoutInSeconds)

	// Start streaming in a goroutine
	go func() {
		defer providerUtils.EnsureStreamFinalizerCalled(ctx, postHookSpanFinalizer)
		defer func() {
			if ctx.Err() == context.Canceled {
				providerUtils.HandleStreamCancellation(ctx, postHookRunner, responseChan, provider.logger, postHookSpanFinalizer, jsonData)
			} else if ctx.Err() == context.DeadlineExceeded {
				providerUtils.HandleStreamTimeout(ctx, postHookRunner, responseChan, provider.logger, postHookSpanFinalizer, jsonData)
			}
			providerUtils.CloseStream(ctx, responseChan)
		}()
		// Always release response on exit; bodyStream close should prevent indefinite blocking.
		defer resp.Body.Close()

		// Wrap body with idle timeout to detect stalled streams.
		idleReader, stopIdleTimeout := providerUtils.NewIdleTimeoutReader(resp.Body, resp.Body, providerUtils.GetStreamIdleTimeout(ctx), ctx)
		defer stopIdleTimeout()

		// Setup cancellation handler to close body stream on ctx cancellation
		stopCancellation := providerUtils.SetupStreamCancellation(ctx, resp.Body, provider.logger)
		defer stopCancellation()

		// Process AWS Event Stream format
		usage := &schemas.ResponsesResponseUsage{}
		billedUsage := &schemas.BifrostLLMUsage{}
		// Register the accumulating usage handle so a mid-stream cancel/timeout
		// can bill for Bedrock Responses usage already reported by stream events
		// before the stream was interrupted.
		ctx.SetValue(schemas.BifrostContextKeyStreamAccumulatedUsage, billedUsage)

		usageNormalized := false
		normalizeUsage := func() {
			if usageNormalized {
				return
			}
			usageNormalized = true
			normalizeCachedUsage(billedUsage)
		}
		defer func() {
			if ctx.Err() != nil {
				normalizeUsage()
			}
		}()

		var streamTrace *BedrockConverseTrace
		chunkIndex := 0

		// Create stream state for stateful conversions (used by Converse API path)
		streamState := acquireBedrockResponsesStreamState()
		streamState.Model = &request.Model
		streamState.Ctx = ctx
		defer releaseBedrockResponsesStreamState(streamState)

		// Check for structured output mode - if set, we need to intercept tool calls
		// and convert them to content instead of forwarding as tool calls
		var structuredOutputToolName string
		if toolName, ok := ctx.Value(schemas.BifrostContextKeyStructuredOutputToolName).(string); ok {
			structuredOutputToolName = toolName
		}
		var isAccumulatingStructuredOutput bool

		// Process AWS Event Stream format using proper decoder
		lastChunkTime := startTime
		decoder := eventstream.NewDecoder()
		payloadBuf := make([]byte, 0, 1024*1024) // 1MB payload buffer
		for {
			// If context was cancelled/timed out, let defer handle it
			if ctx.Err() != nil {
				return
			}
			// Decode a single EventStream message
			message, err := decoder.Decode(idleReader, payloadBuf)
			if err != nil {
				// If context was cancelled/timed out, let defer handle it
				if ctx.Err() != nil {
					return
				}
				if err == io.EOF {
					// Converse API: finalize any open items at end of stream.
					finalResponses := FinalizeBedrockStream(streamState, chunkIndex, usage, streamTrace)
					for i, finalResponse := range finalResponses {
						finalResponse.ExtraFields = schemas.BifrostResponseExtraFields{
							ChunkIndex: chunkIndex,
							Latency:    time.Since(lastChunkTime).Milliseconds(),
						}
						chunkIndex++
						lastChunkTime = time.Now()

						if providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse) {
							finalResponse.ExtraFields.RawResponse = "{}" // Final event has no payload
						}

						if i == len(finalResponses)-1 {
							// Set raw request if enabled
							ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
							if providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest) {
								providerUtils.ParseAndSetRawRequest(&finalResponse.ExtraFields, jsonData)
							}
							finalResponse.ExtraFields.Latency = time.Since(startTime).Milliseconds()
						}

						providerUtils.ProcessAndSendResponse(ctx, postHookRunner, providerUtils.GetBifrostResponseForStreamResponse(nil, nil, finalResponse, nil, nil, nil), responseChan, postHookSpanFinalizer)
					}
					break
				}
				ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
				provider.logger.Warn("Error decoding EventStream message: %v", err)
				// Transport-level errors (stale/closed connection, unexpected EOF) are retryable.
				// Use IsBifrostError:false so the retry gate in executeRequestWithRetries can retry.
				if isStreamTransportError(err) {
					providerUtils.ProcessAndSendBifrostError(ctx, postHookRunner, &schemas.BifrostError{
						IsBifrostError: false,
						Error: &schemas.ErrorField{
							Message: schemas.ErrProviderNetworkError,
							Error:   err,
						},
					}, responseChan, provider.logger, postHookSpanFinalizer)
				} else {
					providerUtils.ProcessAndSendError(ctx, postHookRunner, err, responseChan, provider.logger, postHookSpanFinalizer)
				}
				return
			}

			// Process the decoded message payload (contains JSON for normal events)
			if len(message.Payload) > 0 {
				if msgTypeHeader := message.Headers.Get(":message-type"); msgTypeHeader != nil {
					if msgType := msgTypeHeader.String(); msgType != "event" {
						excType := msgType
						if excHeader := message.Headers.Get(":exception-type"); excHeader != nil {
							if v := excHeader.String(); v != "" {
								excType = v
							}
						}
						streamErr := newBedrockStreamException("", excType, message.Payload)
						providerUtils.ProcessAndSendBifrostError(ctx, postHookRunner, streamErr, responseChan, provider.logger, postHookSpanFinalizer)
						return
					}
				}

				// Converse API path: parse Bedrock Converse-specific stream events
				var streamEvent BedrockStreamEvent
				if err := sonic.Unmarshal(message.Payload, &streamEvent); err != nil {
					provider.logger.Debug("Failed to parse JSON from event buffer: %v, data: %s", err, string(message.Payload))
					providerUtils.ProcessAndSendError(ctx, postHookRunner, err, responseChan, provider.logger, postHookSpanFinalizer)
					return
				}

				if streamEvent.Trace != nil {
					streamTrace = streamEvent.Trace
				}

				if streamEvent.Usage != nil {
					// Accumulate usage information instead of overwriting
					// In some cases usage comes in multiple events, so we need to take the maximum values
					accumulateBedrockResponsesUsage(usage, billedUsage, streamEvent.Usage)
				}

				// Handle structured output: intercept tool calls for the structured output tool
				// and convert them to content instead of forwarding as tool calls
				if structuredOutputToolName != "" {
					// Check for tool use start event
					if streamEvent.Start != nil && streamEvent.Start.ToolUse != nil {
						if streamEvent.Start.ToolUse.Name == structuredOutputToolName {
							// This is the structured output tool - start accumulating, don't forward
							isAccumulatingStructuredOutput = true
							streamState.UsedStructuredOutputTool = true
							continue
						}
					}

					// Check for tool use delta event
					if streamEvent.Delta != nil && streamEvent.Delta.ToolUse != nil && isAccumulatingStructuredOutput {
						// Convert tool use delta to text delta
						content := streamEvent.Delta.ToolUse.Input
						response := &schemas.BifrostResponsesStreamResponse{
							Type:           schemas.ResponsesStreamResponseTypeOutputTextDelta,
							SequenceNumber: chunkIndex,
							Delta:          &content,
							ExtraFields: schemas.BifrostResponseExtraFields{
								ChunkIndex: chunkIndex,
								Latency:    time.Since(lastChunkTime).Milliseconds(),
							},
						}
						chunkIndex++
						lastChunkTime = time.Now()

						if providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse) {
							response.ExtraFields.RawResponse = string(message.Payload)
						}

						providerUtils.ProcessAndSendResponse(ctx, postHookRunner, providerUtils.GetBifrostResponseForStreamResponse(nil, nil, response, nil, nil, nil), responseChan, postHookSpanFinalizer)
						continue
					}

					// Suppress non-tool content events that would leak into the
					// assembled structured output. Bedrock Claude can emit prose
					// alongside the forced tool call (markdown, preambles, reasoning
					// blocks); forwarding those as text deltas corrupts the JSON
					// the client assembles from the structured-output stream.
					if streamEvent.Delta != nil && (streamEvent.Delta.Text != nil || streamEvent.Delta.ReasoningContent != nil) {
						continue
					}
					if streamEvent.Start != nil && streamEvent.Start.ToolUse == nil {
						continue // non-tool content-block start (text block) — drop
					}
				}

				responses, bifrostErr, _ := streamEvent.ToBifrostResponsesStream(chunkIndex, streamState)
				if bifrostErr != nil {
					ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
					providerUtils.ProcessAndSendBifrostError(ctx, postHookRunner, bifrostErr, responseChan, provider.logger, postHookSpanFinalizer)
					return
				}
				for _, response := range responses {
					if response != nil {
						response.ExtraFields = schemas.BifrostResponseExtraFields{
							ChunkIndex: chunkIndex,
							Latency:    time.Since(lastChunkTime).Milliseconds(),
						}
						chunkIndex++
						lastChunkTime = time.Now()

						if providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse) {
							response.ExtraFields.RawResponse = string(message.Payload)
						}

						providerUtils.ProcessAndSendResponse(ctx, postHookRunner, providerUtils.GetBifrostResponseForStreamResponse(nil, nil, response, nil, nil, nil), responseChan, postHookSpanFinalizer)
					}
				}
			}
		}
	}()

	return responseChan, nil
}

// Embedding generates embeddings for the given input text(s) using Amazon Bedrock.
// Supports Titan and Cohere embedding models. Returns a BifrostResponse containing the embedding(s) and any error that occurred.
func (provider *BedrockProvider) Embedding(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostEmbeddingRequest) (*schemas.BifrostEmbeddingResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.Bedrock, provider.customProviderConfig, schemas.EmbeddingRequest); err != nil {
		return nil, err
	}

	// Determine model type
	modelType, err := DetermineEmbeddingModelType(ctx, request.Model)
	if err != nil {
		return nil, providerUtils.NewConfigurationError(err.Error())
	}

	// Convert request and execute based on model type
	var rawResponse []byte
	var bifrostError *schemas.BifrostError
	var latency time.Duration
	var providerResponseHeaders map[string]string
	var path string
	var jsonData []byte

	switch modelType {
	case "titan":
		jsonData, bifrostError = providerUtils.CheckContextAndGetRequestBody(
			ctx,
			request,
			func() (providerUtils.RequestBodyWithExtraParams, error) {
				return ToBedrockTitanEmbeddingRequest(request)
			})
		if bifrostError != nil {
			return nil, bifrostError
		}
		path, _ = provider.getModelPathAndRegion(ctx, "invoke", request.Model, key)
		rawResponse, latency, providerResponseHeaders, bifrostError = provider.completeRequest(ctx, jsonData, path, key, request.Model)

	case "cohere":
		jsonData, bifrostError = providerUtils.CheckContextAndGetRequestBody(
			ctx,
			request,
			func() (providerUtils.RequestBodyWithExtraParams, error) {
				return ToBedrockCohereEmbeddingRequest(request)
			})
		if bifrostError != nil {
			return nil, bifrostError
		}
		path, _ = provider.getModelPathAndRegion(ctx, "invoke", request.Model, key)
		rawResponse, latency, providerResponseHeaders, bifrostError = provider.completeRequest(ctx, jsonData, path, key, request.Model)

	default:
		return nil, providerUtils.NewConfigurationError("unsupported embedding model type")
	}

	if providerResponseHeaders != nil {
		ctx.SetValue(schemas.BifrostContextKeyProviderResponseHeaders, providerResponseHeaders)
	}
	if bifrostError != nil {
		return nil, providerUtils.EnrichError(ctx, bifrostError, jsonData, nil, provider.sendBackRawRequest, provider.sendBackRawResponse, latency)
	}
	// Parse response based on model type
	var bifrostResponse *schemas.BifrostEmbeddingResponse
	switch modelType {
	case "titan":
		var titanResp BedrockTitanEmbeddingResponse
		if err := sonic.Unmarshal(rawResponse, &titanResp); err != nil {
			return nil, providerUtils.EnrichError(ctx, providerUtils.NewBifrostOperationError("error parsing Titan embedding response", err), jsonData, rawResponse, provider.sendBackRawRequest, provider.sendBackRawResponse, latency)
		}
		bifrostResponse = titanResp.ToBifrostEmbeddingResponse()
		bifrostResponse.Model = request.Model

	case "cohere":
		var cohereResp BedrockCohereEmbeddingResponse
		if err := sonic.Unmarshal(rawResponse, &cohereResp); err != nil {
			return nil, providerUtils.EnrichError(ctx, providerUtils.NewBifrostOperationError("error parsing Cohere embedding response", err), jsonData, rawResponse, provider.sendBackRawRequest, provider.sendBackRawResponse, latency)
		}
		converted, convErr := cohereResp.ToBifrostEmbeddingResponse()
		if convErr != nil {
			return nil, providerUtils.EnrichError(ctx, providerUtils.NewBifrostOperationError("error parsing Cohere embedding response", convErr), jsonData, rawResponse, provider.sendBackRawRequest, provider.sendBackRawResponse, latency)
		}
		bifrostResponse = converted
		bifrostResponse.Model = request.Model
		// For embeddings_by_type responses preserve the raw Bedrock payload so the
		// invoke-endpoint converter can return all encoding variants verbatim, since
		// the internal BifrostEmbeddingResponse only has float32 and string fields.
		if cohereResp.ResponseType == "embeddings_by_type" {
			var rawResponseData interface{}
			if err := sonic.Unmarshal(rawResponse, &rawResponseData); err == nil {
				bifrostResponse.ExtraFields.RawResponse = rawResponseData
			}
		}
	}

	// Bedrock Cohere embed models omit token usage from the response body and instead
	// return it in the X-Amzn-Bedrock-Input-Token-Count response header. Backfill Usage
	// from that header when the body did not provide it. (#3917)
	if bifrostResponse.Usage == nil {
		if inputTokens, ok := inputTokensFromHeaders(providerResponseHeaders); ok {
			bifrostResponse.Usage = &schemas.BifrostLLMUsage{
				PromptTokens: inputTokens,
				TotalTokens:  inputTokens,
			}
		}
	}

	// Set ExtraFields
	bifrostResponse.ExtraFields.Latency = latency.Milliseconds()
	bifrostResponse.ExtraFields.ProviderResponseHeaders = providerResponseHeaders

	// Set raw response if enabled
	if providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse) {
		var rawResponseData interface{}
		if err := sonic.Unmarshal(rawResponse, &rawResponseData); err == nil {
			bifrostResponse.ExtraFields.RawResponse = rawResponseData
		}
	}

	return bifrostResponse, nil
}

// Speech is not supported by the Bedrock provider.
func (provider *BedrockProvider) Speech(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostSpeechRequest) (*schemas.BifrostSpeechResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.SpeechRequest, schemas.Bedrock)
}

// Rerank performs a rerank request using the Bedrock Agent Runtime /rerank API.
func (provider *BedrockProvider) Rerank(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostRerankRequest) (*schemas.BifrostRerankResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.Bedrock, provider.customProviderConfig, schemas.RerankRequest); err != nil {
		return nil, err
	}

	if !strings.HasPrefix(request.Model, "arn:") {
		return nil, providerUtils.NewConfigurationError(fmt.Sprintf("bedrock rerank requires an ARN model identifier; got %q", request.Model))
	}

	jsonData, bifrostErr := providerUtils.CheckContextAndGetRequestBody(
		ctx,
		request,
		func() (providerUtils.RequestBodyWithExtraParams, error) {
			return ToBedrockRerankRequest(request, request.Model)
		},
	)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	rawResponseBody, latency, providerResponseHeaders, bifrostErr := provider.completeAgentRuntimeRequest(ctx, jsonData, "/rerank", key)
	if providerResponseHeaders != nil {
		ctx.SetValue(schemas.BifrostContextKeyProviderResponseHeaders, providerResponseHeaders)
	}
	if bifrostErr != nil {
		return nil, providerUtils.EnrichError(ctx, bifrostErr, jsonData, nil, provider.sendBackRawRequest, provider.sendBackRawResponse, latency)
	}

	response := &BedrockRerankResponse{}
	rawRequest, rawResponse, bifrostErr := providerUtils.HandleProviderResponse(rawResponseBody, response, jsonData, providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest), providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse))
	if bifrostErr != nil {
		return nil, providerUtils.EnrichError(ctx, bifrostErr, jsonData, rawResponseBody, provider.sendBackRawRequest, provider.sendBackRawResponse, latency)
	}

	returnDocuments := request.Params != nil && request.Params.ReturnDocuments != nil && *request.Params.ReturnDocuments
	bifrostResponse := response.ToBifrostRerankResponse(request.Documents, returnDocuments)
	bifrostResponse.Model = request.Model

	// Bedrock returns rerank input token usage only in the X-Amzn-Bedrock-Input-Token-Count
	// response header (it is absent from the body); backfill Usage from it. (#3917)
	if bifrostResponse.Usage == nil {
		if inputTokens, ok := inputTokensFromHeaders(providerResponseHeaders); ok {
			bifrostResponse.Usage = &schemas.BifrostLLMUsage{
				PromptTokens: inputTokens,
				TotalTokens:  inputTokens,
			}
		}
	}

	bifrostResponse.ExtraFields.Latency = latency.Milliseconds()
	bifrostResponse.ExtraFields.ProviderResponseHeaders = providerResponseHeaders

	if providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest) {
		bifrostResponse.ExtraFields.RawRequest = rawRequest
	}
	if providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse) {
		bifrostResponse.ExtraFields.RawResponse = rawResponse
	}

	return bifrostResponse, nil
}

// OCR is not supported by the Bedrock provider.
func (provider *BedrockProvider) OCR(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostOCRRequest) (*schemas.BifrostOCRResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.OCRRequest, provider.GetProviderKey())
}

// SpeechStream is not supported by the Bedrock provider.
func (provider *BedrockProvider) SpeechStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, postHookSpanFinalizer func(context.Context), key schemas.Key, request *schemas.BifrostSpeechRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.SpeechStreamRequest, schemas.Bedrock)
}

// Transcription is not supported by the Bedrock provider.
func (provider *BedrockProvider) Transcription(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostTranscriptionRequest) (*schemas.BifrostTranscriptionResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.TranscriptionRequest, schemas.Bedrock)
}

// TranscriptionStream is not supported by the Bedrock provider.
func (provider *BedrockProvider) TranscriptionStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, postHookSpanFinalizer func(context.Context), key schemas.Key, request *schemas.BifrostTranscriptionRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.TranscriptionStreamRequest, schemas.Bedrock)
}

// ImageGeneration generates images using Amazon Bedrock.
// Supports Titan Image Generator v1, Nova Canvas v1, Titan Image Generator v2, and Stability AI models.
// Returns a BifrostImageGenerationResponse containing the generated images and any error that occurred.
func (provider *BedrockProvider) ImageGeneration(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostImageGenerationRequest) (*schemas.BifrostImageGenerationResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.Bedrock, provider.customProviderConfig, schemas.ImageGenerationRequest); err != nil {
		return nil, err
	}

	var rawResponse []byte
	var jsonData []byte
	var bifrostError *schemas.BifrostError
	var latency time.Duration
	var providerResponseHeaders map[string]string
	var path string

	path, _ = provider.getModelPathAndRegion(ctx, "invoke", request.Model, key)

	jsonData, bifrostError = providerUtils.CheckContextAndGetRequestBody(
		ctx,
		request,
		func() (providerUtils.RequestBodyWithExtraParams, error) {
			if isStabilityAIModel(request.Model) {
				return ToStabilityAIImageGenerationRequest(request)
			}
			return ToBedrockImageGenerationRequest(request)
		})
	if bifrostError != nil {
		return nil, bifrostError
	}
	rawResponse, latency, providerResponseHeaders, bifrostError = provider.completeRequest(ctx, jsonData, path, key, request.Model)
	if providerResponseHeaders != nil {
		ctx.SetValue(schemas.BifrostContextKeyProviderResponseHeaders, providerResponseHeaders)
	}
	if bifrostError != nil {
		return nil, providerUtils.EnrichError(ctx, bifrostError, jsonData, nil, provider.sendBackRawRequest, provider.sendBackRawResponse, latency)
	}

	// Parse response based on model type
	var bifrostResponse *schemas.BifrostImageGenerationResponse
	var imageResp BedrockImageGenerationResponse
	if err := sonic.Unmarshal(rawResponse, &imageResp); err != nil {
		return nil, providerUtils.EnrichError(ctx, providerUtils.NewBifrostOperationError("error parsing image generation response", err), jsonData, rawResponse, provider.sendBackRawRequest, provider.sendBackRawResponse, latency)
	}

	if imageResp.Error != "" {
		return nil, providerUtils.EnrichError(ctx, providerUtils.NewBifrostOperationError(imageResp.Error, nil), jsonData, rawResponse, provider.sendBackRawRequest, provider.sendBackRawResponse, latency)
	}

	bifrostResponse = ToBifrostImageGenerationResponse(&imageResp)
	bifrostResponse.Model = request.Model
	bifrostResponse.ExtraFields.Latency = latency.Milliseconds()
	bifrostResponse.ExtraFields.ProviderResponseHeaders = providerResponseHeaders

	// Set raw request if enabled
	if providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest) {
		providerUtils.ParseAndSetRawRequest(&bifrostResponse.ExtraFields, jsonData)
	}

	if providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse) {
		var rawResponseData interface{}
		if err := sonic.Unmarshal(rawResponse, &rawResponseData); err == nil {
			bifrostResponse.ExtraFields.RawResponse = rawResponseData
		}
	}

	return bifrostResponse, nil
}

// ImageGenerationStream is not supported by the Bedrock provider.
func (provider *BedrockProvider) ImageGenerationStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, postHookSpanFinalizer func(context.Context), key schemas.Key, request *schemas.BifrostImageGenerationRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ImageGenerationStreamRequest, schemas.Bedrock)
}

// ImageEdit performs image editing using Amazon Bedrock.
// Supports Titan Image Generator v1, Nova Canvas v1, Titan Image Generator v2 (three edit types:
// INPAINTING, OUTPAINTING, BACKGROUND_REMOVAL), and Stability AI edit models (inpaint, outpaint,
// recolor, search-replace, erase-object, remove-bg, control-sketch, control-structure, style-guide,
// style-transfer, upscale-creative, upscale-conservative, upscale-fast).
// Returns a BifrostImageGenerationResponse containing the edited images and any error that occurred.
func (provider *BedrockProvider) ImageEdit(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostImageEditRequest) (*schemas.BifrostImageGenerationResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.Bedrock, provider.customProviderConfig, schemas.ImageEditRequest); err != nil {
		return nil, err
	}

	var jsonData []byte
	var bifrostError *schemas.BifrostError

	// Stability AI routing and task-type inference use the actual model ID.
	path, _ := provider.getModelPathAndRegion(ctx, "invoke", request.Model, key)

	jsonData, bifrostError = providerUtils.CheckContextAndGetRequestBody(
		ctx,
		request,
		func() (providerUtils.RequestBodyWithExtraParams, error) {
			if isStabilityAIModel(request.Model) {
				return ToStabilityAIImageEditRequest(request, request.Model)
			}
			return ToBedrockImageEditRequest(request)
		})
	if bifrostError != nil {
		return nil, bifrostError
	}

	// Make API request (same URL as image generation)
	rawResponse, latency, providerResponseHeaders, bifrostError := provider.completeRequest(ctx, jsonData, path, key, request.Model)
	if providerResponseHeaders != nil {
		ctx.SetValue(schemas.BifrostContextKeyProviderResponseHeaders, providerResponseHeaders)
	}
	if bifrostError != nil {
		return nil, providerUtils.EnrichError(ctx, bifrostError, jsonData, nil, provider.sendBackRawRequest, provider.sendBackRawResponse, latency)
	}

	// Parse response (reuse BedrockImageGenerationResponse)
	var imageResp BedrockImageGenerationResponse
	if err := sonic.Unmarshal(rawResponse, &imageResp); err != nil {
		return nil, providerUtils.EnrichError(ctx, providerUtils.NewBifrostOperationError("error parsing image edit response", err), jsonData, rawResponse, provider.sendBackRawRequest, provider.sendBackRawResponse, latency)
	}

	if imageResp.Error != "" {
		return nil, providerUtils.EnrichError(ctx, providerUtils.NewBifrostOperationError(imageResp.Error, nil), jsonData, rawResponse, provider.sendBackRawRequest, provider.sendBackRawResponse, latency)
	}

	// Convert response and set metadata
	bifrostResponse := ToBifrostImageGenerationResponse(&imageResp)
	bifrostResponse.Model = request.Model
	bifrostResponse.ExtraFields.Latency = latency.Milliseconds()
	bifrostResponse.ExtraFields.ProviderResponseHeaders = providerResponseHeaders

	// Set raw request/response if enabled
	if providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest) {
		providerUtils.ParseAndSetRawRequest(&bifrostResponse.ExtraFields, jsonData)
	}

	if providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse) {
		var rawResponseData interface{}
		if err := sonic.Unmarshal(rawResponse, &rawResponseData); err == nil {
			bifrostResponse.ExtraFields.RawResponse = rawResponseData
		}
	}

	return bifrostResponse, nil
}

// ImageEditStream is not supported by the Bedrock provider.
func (provider *BedrockProvider) ImageEditStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, postHookSpanFinalizer func(context.Context), key schemas.Key, request *schemas.BifrostImageEditRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ImageEditStreamRequest, provider.GetProviderKey())
}

// ImageVariation generates image variations using Amazon Bedrock.
// Supports Titan Image Generator v1, Nova Canvas v1, and Titan Image Generator v2.
// Returns a BifrostImageGenerationResponse containing the generated image variations and any error that occurred.
func (provider *BedrockProvider) ImageVariation(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostImageVariationRequest) (*schemas.BifrostImageGenerationResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.Bedrock, provider.customProviderConfig, schemas.ImageVariationRequest); err != nil {
		return nil, err
	}

	var jsonData []byte
	var bifrostError *schemas.BifrostError

	jsonData, bifrostError = providerUtils.CheckContextAndGetRequestBody(
		ctx,
		request,
		func() (providerUtils.RequestBodyWithExtraParams, error) {
			return ToBedrockImageVariationRequest(request)
		})
	if bifrostError != nil {
		return nil, bifrostError
	}

	// Make API request (same URL as image generation)
	path, _ := provider.getModelPathAndRegion(ctx, "invoke", request.Model, key)
	rawResponse, latency, providerResponseHeaders, bifrostError := provider.completeRequest(ctx, jsonData, path, key, request.Model)
	if providerResponseHeaders != nil {
		ctx.SetValue(schemas.BifrostContextKeyProviderResponseHeaders, providerResponseHeaders)
	}
	if bifrostError != nil {
		return nil, providerUtils.EnrichError(ctx, bifrostError, jsonData, nil, provider.sendBackRawRequest, provider.sendBackRawResponse, latency)
	}

	// Parse response (reuse BedrockImageGenerationResponse and ToBifrostImageGenerationResponse)
	var imageResp BedrockImageGenerationResponse
	if err := sonic.Unmarshal(rawResponse, &imageResp); err != nil {
		return nil, providerUtils.EnrichError(ctx, providerUtils.NewBifrostOperationError("error parsing image variation response", err), jsonData, rawResponse, provider.sendBackRawRequest, provider.sendBackRawResponse, latency)
	}

	if imageResp.Error != "" {
		return nil, providerUtils.EnrichError(ctx, providerUtils.NewBifrostOperationError(imageResp.Error, nil), jsonData, rawResponse, provider.sendBackRawRequest, provider.sendBackRawResponse, latency)
	}

	// Convert response and set metadata
	bifrostResponse := ToBifrostImageGenerationResponse(&imageResp)
	bifrostResponse.Model = request.Model
	bifrostResponse.ExtraFields.Latency = latency.Milliseconds()
	bifrostResponse.ExtraFields.ProviderResponseHeaders = providerResponseHeaders

	// Set raw request/response if enabled
	if providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest) {
		providerUtils.ParseAndSetRawRequest(&bifrostResponse.ExtraFields, jsonData)
	}

	if providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse) {
		var rawResponseData interface{}
		if err := sonic.Unmarshal(rawResponse, &rawResponseData); err == nil {
			bifrostResponse.ExtraFields.RawResponse = rawResponseData
		}
	}

	return bifrostResponse, nil
}

// VideoGeneration is not supported by the Bedrock provider.
func (provider *BedrockProvider) VideoGeneration(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostVideoGenerationRequest) (*schemas.BifrostVideoGenerationResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.VideoGenerationRequest, provider.GetProviderKey())
}

// VideoRetrieve is not supported by the Bedrock provider.
func (provider *BedrockProvider) VideoRetrieve(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostVideoRetrieveRequest) (*schemas.BifrostVideoGenerationResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.VideoRetrieveRequest, provider.GetProviderKey())
}

// VideoDownload is not supported by the Bedrock provider.
func (provider *BedrockProvider) VideoDownload(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostVideoDownloadRequest) (*schemas.BifrostVideoDownloadResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.VideoDownloadRequest, provider.GetProviderKey())
}

// VideoDelete is not supported by Bedrock provider.
func (provider *BedrockProvider) VideoDelete(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostVideoDeleteRequest) (*schemas.BifrostVideoDeleteResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.VideoDeleteRequest, provider.GetProviderKey())
}

// VideoList is not supported by Bedrock provider.
func (provider *BedrockProvider) VideoList(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostVideoListRequest) (*schemas.BifrostVideoListResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.VideoListRequest, provider.GetProviderKey())
}

// VideoRemix is not supported by Bedrock provider.
func (provider *BedrockProvider) VideoRemix(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostVideoRemixRequest) (*schemas.BifrostVideoGenerationResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.VideoRemixRequest, provider.GetProviderKey())
}

// FileUpload uploads a file to S3 for Bedrock batch processing.
func (provider *BedrockProvider) FileUpload(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostFileUploadRequest) (*schemas.BifrostFileUploadResponse, *schemas.BifrostError) {

	if err := providerUtils.CheckOperationAllowed(schemas.Bedrock, provider.customProviderConfig, schemas.FileUploadRequest); err != nil {
		if err.Error != nil {
			provider.logger.Error("file upload operation not allowed: %s", err.Error.Message)
		}
		return nil, err
	}

	// Get S3 bucket from storage config or extra params
	s3Bucket := ""
	s3Prefix := ""
	if request.StorageConfig != nil && request.StorageConfig.S3 != nil {
		if request.StorageConfig.S3.Bucket != "" {
			s3Bucket = request.StorageConfig.S3.Bucket
		}
		if request.StorageConfig.S3.Prefix != "" {
			s3Prefix = request.StorageConfig.S3.Prefix
		}
	} else if request.ExtraParams != nil {
		if bucket, ok := request.ExtraParams["s3_bucket"].(string); ok && bucket != "" {
			s3Bucket = bucket
		}
		if prefix, ok := request.ExtraParams["s3_prefix"].(string); ok && prefix != "" {
			s3Prefix = prefix
		}
	}

	if s3Bucket == "" {
		provider.logger.Error("s3_bucket is required for Bedrock file operations (provide in storage_config.s3 or extra_params)")
		return nil, providerUtils.NewBifrostOperationError("s3_bucket is required for Bedrock file operations (provide in storage_config.s3 or extra_params)", nil)
	}

	// Parse bucket name and optional prefix from s3Bucket (could be "bucket-name" or "s3://bucket-name/prefix/")
	bucketName, bucketPrefix := parseS3URI(s3Bucket)
	if bucketPrefix != "" {
		s3Prefix = bucketPrefix + s3Prefix
	}

	region := DefaultBedrockRegion
	if key.BedrockKeyConfig.Region != nil && key.BedrockKeyConfig.Region.GetValue() != "" {
		region = key.BedrockKeyConfig.Region.GetValue()
	}

	// Generate S3 key for the file
	filename := request.Filename
	if filename == "" {
		filename = fmt.Sprintf("file-%d.jsonl", time.Now().UnixNano())
	}

	cleanedPrefix := strings.Trim(s3Prefix, "/")
	s3Key := filename
	if cleanedPrefix != "" {
		s3Key = cleanedPrefix + "/" + filename
	}

	provider.logger.Debug("uploading file to s3: %s", s3Key)

	// Build S3 PUT request URL
	// Escape each path segment individually to handle special characters while preserving "/"
	reqURL := fmt.Sprintf("https://%s.s3.%s.amazonaws.com/%s", bucketName, region, escapeS3KeyForURL(s3Key))

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPut, reqURL, bytes.NewReader(request.File))
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError("error creating request", err)
	}

	httpReq.Header.Set("Content-Type", "application/octet-stream")
	httpReq.ContentLength = int64(len(request.File))

	// Sign request for S3
	if err := signAWSRequest(ctx, httpReq, key.BedrockKeyConfig, region, "s3"); err != nil {
		provider.logger.Error("error signing request: %s", err.Error.Message)
		return nil, err
	}

	// Execute request
	startTime := time.Now()
	resp, err := providerUtils.DoHTTPRequest(provider.client, httpReq)
	latency := time.Since(startTime)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return nil, &schemas.BifrostError{
				IsBifrostError: false,
				Error: &schemas.ErrorField{
					Type:    schemas.Ptr(schemas.RequestCancelled),
					Message: schemas.ErrRequestCancelled,
					Error:   err,
				},
			}
		}
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderDoRequest, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		provider.logger.Error("s3 upload failed: %d", resp.StatusCode)
		return nil, providerUtils.NewProviderAPIError(fmt.Sprintf("S3 upload failed: %s", string(body)), nil, resp.StatusCode, nil, nil)
	}

	// Return S3 URI as the file ID
	s3URI := fmt.Sprintf("s3://%s/%s", bucketName, s3Key)

	return &schemas.BifrostFileUploadResponse{
		ID:             s3URI,
		Object:         "file",
		Bytes:          int64(len(request.File)),
		CreatedAt:      time.Now().Unix(),
		Filename:       filename,
		Purpose:        request.Purpose,
		Status:         schemas.FileStatusProcessed,
		StorageBackend: schemas.FileStorageS3,
		StorageURI:     s3URI,
		ExtraFields: schemas.BifrostResponseExtraFields{
			Latency: latency.Milliseconds(),
		},
	}, nil
}

// FileList lists files in the S3 bucket used for Bedrock batch processing from all provided keys.
// FileList lists S3 files using serial pagination across keys.
// Exhausts all pages from one key before moving to the next.
func (provider *BedrockProvider) FileList(ctx *schemas.BifrostContext, keys []schemas.Key, request *schemas.BifrostFileListRequest) (*schemas.BifrostFileListResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.Bedrock, provider.customProviderConfig, schemas.FileListRequest); err != nil {
		return nil, err
	}

	// Get S3 bucket from storage config or extra params
	s3Bucket := ""
	s3Prefix := ""
	if request.StorageConfig != nil && request.StorageConfig.S3 != nil {
		if request.StorageConfig.S3.Bucket != "" {
			s3Bucket = request.StorageConfig.S3.Bucket
		}
		if request.StorageConfig.S3.Prefix != "" {
			s3Prefix = request.StorageConfig.S3.Prefix
		}
	}
	if request.ExtraParams != nil {
		if bucket, ok := request.ExtraParams["s3_bucket"].(string); ok && bucket != "" {
			s3Bucket = bucket
		}
		if prefix, ok := request.ExtraParams["s3_prefix"].(string); ok && prefix != "" {
			s3Prefix = prefix
		}
	}

	if s3Bucket == "" {
		return nil, providerUtils.NewBifrostOperationError("s3_bucket is required for Bedrock file operations (provide in storage_config.s3 or extra_params)", nil)
	}

	bucketName, bucketPrefix := parseS3URI(s3Bucket)
	if bucketPrefix != "" {
		s3Prefix = bucketPrefix + s3Prefix
	}

	// Initialize serial pagination helper
	helper, err := providerUtils.NewSerialListHelper(keys, request.After, provider.logger, true)
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError("invalid pagination cursor", err)
	}

	// Get current key to query
	key, nativeCursor, ok := helper.GetCurrentKey()
	if !ok {
		// All keys exhausted
		return &schemas.BifrostFileListResponse{
			Object:  "list",
			Data:    []schemas.FileObject{},
			HasMore: false,
		}, nil
	}

	region := DefaultBedrockRegion
	if key.BedrockKeyConfig != nil {
		if key.BedrockKeyConfig.Region != nil && key.BedrockKeyConfig.Region.GetValue() != "" {
			region = key.BedrockKeyConfig.Region.GetValue()
		}
	}

	// Build S3 ListObjectsV2 request
	params := url.Values{}
	params.Set("list-type", "2")
	params.Set("prefix", s3Prefix)
	if request.Limit > 0 {
		params.Set("max-keys", fmt.Sprintf("%d", request.Limit))
	}
	// Use native cursor from serial helper
	if nativeCursor != "" {
		params.Set("continuation-token", nativeCursor)
	}

	requestURL := fmt.Sprintf("https://%s.s3.%s.amazonaws.com/?%s", bucketName, region, params.Encode())

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError("error creating request", err)
	}

	// Sign request for S3
	if bifrostErr := signAWSRequest(ctx, httpReq, key.BedrockKeyConfig, region, "s3"); bifrostErr != nil {
		return nil, bifrostErr
	}

	// Execute request
	startTime := time.Now()
	resp, err := providerUtils.DoHTTPRequest(provider.client, httpReq)
	latency := time.Since(startTime)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return nil, &schemas.BifrostError{
				IsBifrostError: false,
				Error: &schemas.ErrorField{
					Type:    schemas.Ptr(schemas.RequestCancelled),
					Message: schemas.ErrRequestCancelled,
					Error:   err,
				},
			}
		}
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderDoRequest, err)
	}

	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError("error reading response", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, providerUtils.NewProviderAPIError(fmt.Sprintf("S3 list failed: %s", string(body)), nil, resp.StatusCode, nil, nil)
	}

	// Parse S3 ListObjectsV2 XML response
	var listResp S3ListObjectsResponse
	if err := parseS3ListResponse(body, &listResp); err != nil {
		return nil, providerUtils.NewBifrostOperationError("error parsing S3 response", err)
	}

	// Convert files to Bifrost format
	files := make([]schemas.FileObject, 0, len(listResp.Contents))
	for _, obj := range listResp.Contents {
		s3URI := fmt.Sprintf("s3://%s/%s", bucketName, obj.Key)
		filename := obj.Key
		if idx := strings.LastIndex(obj.Key, "/"); idx >= 0 {
			filename = obj.Key[idx+1:]
		}
		files = append(files, schemas.FileObject{
			ID:        s3URI,
			Object:    "file",
			Bytes:     obj.Size,
			CreatedAt: obj.LastModified.Unix(),
			Filename:  filename,
			Purpose:   schemas.FilePurposeBatch,
			Status:    schemas.FileStatusProcessed,
		})
	}

	// Build cursor for next request
	// S3 uses NextContinuationToken for pagination
	nextCursor, hasMore := helper.BuildNextCursor(listResp.IsTruncated, listResp.NextContinuationToken)

	// Convert to Bifrost response
	bifrostResp := &schemas.BifrostFileListResponse{
		Object:  "list",
		Data:    files,
		HasMore: hasMore,
		ExtraFields: schemas.BifrostResponseExtraFields{
			Latency: latency.Milliseconds(),
		},
	}
	if nextCursor != "" {
		bifrostResp.After = &nextCursor
	}

	return bifrostResp, nil
}

// FileRetrieve retrieves S3 object metadata for Bedrock batch processing by trying each key until found.
func (provider *BedrockProvider) FileRetrieve(ctx *schemas.BifrostContext, keys []schemas.Key, request *schemas.BifrostFileRetrieveRequest) (*schemas.BifrostFileRetrieveResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.Bedrock, provider.customProviderConfig, schemas.FileRetrieveRequest); err != nil {
		return nil, err
	}

	if request.FileID == "" {
		return nil, providerUtils.NewBifrostOperationError("file_id (S3 URI) is required", nil)
	}

	// Parse S3 URI
	bucketName, s3Key := parseS3URI(request.FileID)
	if bucketName == "" || s3Key == "" {
		return nil, providerUtils.NewBifrostOperationError("invalid S3 URI format, expected s3://bucket/key", nil)
	}

	var lastErr *schemas.BifrostError
	for _, key := range keys {
		region := DefaultBedrockRegion
		if key.BedrockKeyConfig.Region != nil && key.BedrockKeyConfig.Region.GetValue() != "" {
			region = key.BedrockKeyConfig.Region.GetValue()
		}

		// Build S3 HEAD request
		// Escape each path segment individually to handle special characters while preserving "/"
		reqURL := fmt.Sprintf("https://%s.s3.%s.amazonaws.com/%s", bucketName, region, escapeS3KeyForURL(s3Key))

		httpReq, err := http.NewRequestWithContext(ctx, http.MethodHead, reqURL, nil)
		if err != nil {
			lastErr = providerUtils.NewBifrostOperationError("error creating request", err)
			continue
		}

		// Sign request for S3
		if err := signAWSRequest(ctx, httpReq, key.BedrockKeyConfig, region, "s3"); err != nil {
			lastErr = err
			continue
		}

		// Execute request
		startTime := time.Now()
		resp, err := providerUtils.DoHTTPRequest(provider.client, httpReq)
		latency := time.Since(startTime)
		if err != nil {
			if errors.Is(err, context.Canceled) {
				return nil, &schemas.BifrostError{
					IsBifrostError: false,
					Error: &schemas.ErrorField{
						Type:    schemas.Ptr(schemas.RequestCancelled),
						Message: schemas.ErrRequestCancelled,
						Error:   err,
					},
				}
			}
			lastErr = providerUtils.NewBifrostOperationError(schemas.ErrProviderDoRequest, err)
			continue
		}

		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			lastErr = providerUtils.NewProviderAPIError(fmt.Sprintf("S3 HEAD failed with status %d", resp.StatusCode), nil, resp.StatusCode, nil, nil)
			continue
		}

		resp.Body.Close()

		// Extract metadata from headers
		filename := s3Key
		if idx := strings.LastIndex(s3Key, "/"); idx >= 0 {
			filename = s3Key[idx+1:]
		}

		var createdAt int64
		if lastMod := resp.Header.Get("Last-Modified"); lastMod != "" {
			if t, err := time.Parse(time.RFC1123, lastMod); err == nil {
				createdAt = t.Unix()
			}
		}

		return &schemas.BifrostFileRetrieveResponse{
			ID:             request.FileID,
			Object:         "file",
			Bytes:          resp.ContentLength,
			CreatedAt:      createdAt,
			Filename:       filename,
			Purpose:        schemas.FilePurposeBatch,
			Status:         schemas.FileStatusProcessed,
			StorageBackend: schemas.FileStorageS3,
			StorageURI:     request.FileID,
			ExtraFields: schemas.BifrostResponseExtraFields{
				Latency: latency.Milliseconds(),
			},
		}, nil
	}

	return nil, lastErr
}

// FileDelete deletes an S3 object used for Bedrock batch processing by trying each key until successful.
func (provider *BedrockProvider) FileDelete(ctx *schemas.BifrostContext, keys []schemas.Key, request *schemas.BifrostFileDeleteRequest) (*schemas.BifrostFileDeleteResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.Bedrock, provider.customProviderConfig, schemas.FileDeleteRequest); err != nil {
		return nil, err
	}

	if request.FileID == "" {
		return nil, providerUtils.NewBifrostOperationError("file_id (S3 URI) is required", nil)
	}

	// Parse S3 URI
	bucketName, s3Key := parseS3URI(request.FileID)
	if bucketName == "" || s3Key == "" {
		return nil, providerUtils.NewBifrostOperationError("invalid S3 URI format, expected s3://bucket/key", nil)
	}

	var lastErr *schemas.BifrostError
	for _, key := range keys {
		region := DefaultBedrockRegion
		if key.BedrockKeyConfig.Region != nil && key.BedrockKeyConfig.Region.GetValue() != "" {
			region = key.BedrockKeyConfig.Region.GetValue()
		}

		// Build S3 DELETE request
		// Escape each path segment individually to handle special characters while preserving "/"
		reqURL := fmt.Sprintf("https://%s.s3.%s.amazonaws.com/%s", bucketName, region, escapeS3KeyForURL(s3Key))

		httpReq, err := http.NewRequestWithContext(ctx, http.MethodDelete, reqURL, nil)
		if err != nil {
			lastErr = providerUtils.NewBifrostOperationError("error creating request", err)
			continue
		}

		// Sign request for S3
		if err := signAWSRequest(ctx, httpReq, key.BedrockKeyConfig, region, "s3"); err != nil {
			lastErr = err
			continue
		}

		// Execute request
		startTime := time.Now()
		resp, err := providerUtils.DoHTTPRequest(provider.client, httpReq)
		latency := time.Since(startTime)
		if err != nil {
			if errors.Is(err, context.Canceled) {
				return nil, &schemas.BifrostError{
					IsBifrostError: false,
					Error: &schemas.ErrorField{
						Type:    schemas.Ptr(schemas.RequestCancelled),
						Message: schemas.ErrRequestCancelled,
						Error:   err,
					},
				}
			}
			lastErr = providerUtils.NewBifrostOperationError(schemas.ErrProviderDoRequest, err)
			continue
		}

		// S3 DELETE returns 204 No Content on success
		if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			lastErr = providerUtils.NewProviderAPIError(fmt.Sprintf("S3 DELETE failed: %s", string(body)), nil, resp.StatusCode, nil, nil)
			continue
		}

		resp.Body.Close()

		return &schemas.BifrostFileDeleteResponse{
			ID:      request.FileID,
			Object:  "file",
			Deleted: true,
			ExtraFields: schemas.BifrostResponseExtraFields{
				Latency: latency.Milliseconds(),
			},
		}, nil
	}

	return nil, lastErr
}

// FileContent downloads S3 object content for Bedrock batch processing by trying each key until found.
func (provider *BedrockProvider) FileContent(ctx *schemas.BifrostContext, keys []schemas.Key, request *schemas.BifrostFileContentRequest) (*schemas.BifrostFileContentResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.Bedrock, provider.customProviderConfig, schemas.FileContentRequest); err != nil {
		return nil, err
	}

	if request.FileID == "" {
		return nil, providerUtils.NewBifrostOperationError("file_id (S3 URI) is required", nil)
	}

	// Parse S3 URI
	bucketName, s3Key := parseS3URI(request.FileID)
	if bucketName == "" || s3Key == "" {
		return nil, providerUtils.NewBifrostOperationError("invalid S3 URI format, expected s3://bucket/key", nil)
	}

	var lastErr *schemas.BifrostError
	for _, key := range keys {
		region := DefaultBedrockRegion
		if key.BedrockKeyConfig.Region != nil && key.BedrockKeyConfig.Region.GetValue() != "" {
			region = key.BedrockKeyConfig.Region.GetValue()
		}

		// Build S3 GET request
		// Escape each path segment individually to handle special characters while preserving "/"
		reqURL := fmt.Sprintf("https://%s.s3.%s.amazonaws.com/%s", bucketName, region, escapeS3KeyForURL(s3Key))

		httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
		if err != nil {
			lastErr = providerUtils.NewBifrostOperationError("error creating request", err)
			continue
		}

		// Sign request for S3
		if err := signAWSRequest(ctx, httpReq, key.BedrockKeyConfig, region, "s3"); err != nil {
			lastErr = err
			continue
		}

		// Execute request
		startTime := time.Now()
		resp, err := providerUtils.DoHTTPRequest(provider.client, httpReq)
		latency := time.Since(startTime)
		if err != nil {
			if errors.Is(err, context.Canceled) {
				return nil, &schemas.BifrostError{
					IsBifrostError: false,
					Error: &schemas.ErrorField{
						Type:    schemas.Ptr(schemas.RequestCancelled),
						Message: schemas.ErrRequestCancelled,
						Error:   err,
					},
				}
			}
			lastErr = providerUtils.NewBifrostOperationError(schemas.ErrProviderDoRequest, err)
			continue
		}

		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			lastErr = providerUtils.NewProviderAPIError(fmt.Sprintf("S3 GET failed: %s", string(body)), nil, resp.StatusCode, nil, nil)
			continue
		}

		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			lastErr = providerUtils.NewBifrostOperationError("error reading S3 object content", err)
			continue
		}

		contentType := resp.Header.Get("Content-Type")
		if contentType == "" {
			contentType = "application/octet-stream"
		}

		return &schemas.BifrostFileContentResponse{
			FileID:      request.FileID,
			Content:     body,
			ContentType: contentType,
			ExtraFields: schemas.BifrostResponseExtraFields{
				Latency: latency.Milliseconds(),
			},
		}, nil
	}

	return nil, lastErr
}

// BatchCreate creates a new batch inference job on AWS Bedrock.
func (provider *BedrockProvider) BatchCreate(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostBatchCreateRequest) (*schemas.BifrostBatchCreateResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.Bedrock, provider.customProviderConfig, schemas.BatchCreateRequest); err != nil {
		provider.logger.Error("batch create is not allowed for Bedrock provider", "error", err)
		return nil, err
	}

	// Resolve the batch service role
	roleArn := ""
	// Server-configured batch role takes priority over the client-sent value
	if key.BedrockKeyConfig.BatchRoleARN != nil {
		roleArn = key.BedrockKeyConfig.BatchRoleARN.GetValue()
	}
	// Then we will honor the role_arn coming from the client side if present
	if roleArn == "" && request.ExtraParams != nil {
		if r, ok := request.ExtraParams["role_arn"].(string); ok {
			roleArn = r
		}
	}
	// If its empty then we will honor the role_arn from the key config
	if roleArn == "" {
		if key.BedrockKeyConfig.RoleARN != nil {
			roleArn = key.BedrockKeyConfig.RoleARN.GetValue()
		}
	}
	// And if still we don't get role ARN
	if roleArn == "" {
		provider.logger.Error("role_arn is required for Bedrock batch API (provide in extra_params)")
		return nil, providerUtils.NewBifrostOperationError("role_arn is required for Bedrock batch API (provide in extra_params)", nil)
	}
	// Get output S3 URI from extra params
	outputS3Uri := ""
	if request.ExtraParams != nil {
		if o, ok := request.ExtraParams["output_s3_uri"].(string); ok {
			outputS3Uri = o
		}
	}
	if outputS3Uri == "" {
		provider.logger.Error("output_s3_uri is required for Bedrock batch API (provide in extra_params)")
		return nil, providerUtils.NewBifrostOperationError("output_s3_uri is required for Bedrock batch API (provide in extra_params)", nil)
	}

	if request.Model == nil {
		provider.logger.Error("model is required for Bedrock batch API")
		return nil, providerUtils.NewBifrostOperationError("model is required for Bedrock batch API", nil)
	}

	// Generate job name
	jobName := fmt.Sprintf("bifrost-batch-%d", time.Now().Unix())
	if request.Metadata != nil {
		if name, ok := request.Metadata["job_name"]; ok {
			jobName = name
		}
	}

	// Determine input file ID (S3 URI)
	inputFileID := request.InputFileID

	// If no S3 URI provided but inline requests are available, upload them to S3 first
	if inputFileID == "" && len(request.Requests) > 0 {
		// Get region for S3 upload
		region := DefaultBedrockRegion
		if key.BedrockKeyConfig.Region != nil && key.BedrockKeyConfig.Region.GetValue() != "" {
			region = key.BedrockKeyConfig.Region.GetValue()
		}

		var sessionKey *string
		if key.BedrockKeyConfig.SessionToken != nil && key.BedrockKeyConfig.SessionToken.GetValue() != "" {
			sessionKey = schemas.Ptr(key.BedrockKeyConfig.SessionToken.GetValue())
		}

		// Convert inline requests to Bedrock JSONL format
		jsonlData, err := ConvertBedrockRequestsToJSONL(request.Requests, request.Model)
		if err != nil {
			return nil, providerUtils.NewBifrostOperationError("failed to convert requests to JSONL", err)
		}

		// Generate S3 key for the input file
		inputKey := generateBatchInputS3Key(jobName)

		// Derive bucket from output S3 URI
		inputS3URI := deriveInputS3URIFromOutput(outputS3Uri, inputKey)
		bucket, s3Key := parseS3URI(inputS3URI)

		// Upload to S3 using Bedrock credentials
		if bifrostErr := uploadToS3(
			ctx,
			key.BedrockKeyConfig.AccessKey.GetValue(),
			key.BedrockKeyConfig.SecretKey.GetValue(),
			sessionKey,
			region,
			bucket,
			s3Key,
			jsonlData,
		); bifrostErr != nil {
			return nil, bifrostErr
		}

		inputFileID = inputS3URI
	}

	// Validate that we have an input file ID (either provided or uploaded)
	if inputFileID == "" {
		provider.logger.Error("either input_file_id (S3 URI) or requests array is required for Bedrock batch API")
		return nil, providerUtils.NewBifrostOperationError("either input_file_id (S3 URI) or requests array is required for Bedrock batch API", nil)
	}

	// Build request
	bedrockReq := &BedrockBatchJobRequest{
		JobName: jobName,
		ModelID: request.Model,
		RoleArn: roleArn,
		InputDataConfig: BedrockInputDataConfig{
			S3InputDataConfig: BedrockS3InputDataConfig{
				S3Uri:         inputFileID,
				S3InputFormat: "JSONL",
			},
		},
		OutputDataConfig: BedrockOutputDataConfig{
			S3OutputDataConfig: BedrockS3OutputDataConfig{
				S3Uri: outputS3Uri,
			},
		},
	}

	// Set timeout if provided
	if request.CompletionWindow != "" {
		// Parse completion window (e.g., "24h" -> 24)
		if d, err := time.ParseDuration(request.CompletionWindow); err == nil {
			bedrockReq.TimeoutDurationInHours = int(d.Hours())
		}
	}

	jsonData, err := providerUtils.MarshalSorted(bedrockReq)
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderRequestMarshal, err)
	}

	sendBackRawRequest := provider.sendBackRawRequest
	sendBackRawResponse := provider.sendBackRawResponse

	region := DefaultBedrockRegion
	if key.BedrockKeyConfig.Region != nil && key.BedrockKeyConfig.Region.GetValue() != "" {
		region = key.BedrockKeyConfig.Region.GetValue()
	}

	// Create HTTP request
	reqURL := fmt.Sprintf("https://bedrock.%s.amazonaws.com/model-invocation-job", region)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, reqURL, bytes.NewBuffer(jsonData))
	if err != nil {
		return nil, providerUtils.EnrichError(ctx, providerUtils.NewBifrostOperationError("error creating request", err), jsonData, nil, sendBackRawRequest, sendBackRawResponse)
	}

	// Sign request
	if err := signAWSRequest(ctx, httpReq, key.BedrockKeyConfig, region, bedrockSigningService); err != nil {
		return nil, providerUtils.EnrichError(ctx, err, jsonData, nil, sendBackRawRequest, sendBackRawResponse)
	}

	// Execute request
	startTime := time.Now()
	resp, err := providerUtils.DoHTTPRequest(provider.client, httpReq)
	latency := time.Since(startTime)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return nil, providerUtils.EnrichError(ctx, &schemas.BifrostError{
				IsBifrostError: false,
				Error: &schemas.ErrorField{
					Type:    schemas.Ptr(schemas.RequestCancelled),
					Message: schemas.ErrRequestCancelled,
					Error:   err,
				},
			}, jsonData, nil, sendBackRawRequest, sendBackRawResponse)
		}
		return nil, providerUtils.EnrichError(ctx, providerUtils.NewBifrostOperationError(schemas.ErrProviderDoRequest, err), jsonData, nil, sendBackRawRequest, sendBackRawResponse)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, providerUtils.EnrichError(ctx, providerUtils.NewBifrostOperationError("error reading response", err), jsonData, nil, sendBackRawRequest, sendBackRawResponse)
	}

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return nil, providerUtils.EnrichError(ctx, parseBedrockHTTPError(resp.StatusCode, resp.Header, body), jsonData, body, sendBackRawRequest, sendBackRawResponse)
	}

	var bedrockResp BedrockBatchJobResponse
	if err := sonic.Unmarshal(body, &bedrockResp); err != nil {
		return nil, providerUtils.EnrichError(ctx, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseUnmarshal, err), jsonData, body, sendBackRawRequest, sendBackRawResponse)
	}

	// AWS CreateModelInvocationJob only returns jobArn, not status or other details.
	// Retrieve the job to get full status details.
	retrieveResp, bifrostErr := provider.BatchRetrieve(ctx, []schemas.Key{key}, &schemas.BifrostBatchRetrieveRequest{
		Provider: request.Provider,
		BatchID:  bedrockResp.JobArn,
	})
	if bifrostErr != nil {
		// Return basic response if retrieve fails
		return &schemas.BifrostBatchCreateResponse{
			ID:          bedrockResp.JobArn,
			Object:      "batch",
			InputFileID: inputFileID,
			Status:      schemas.BatchStatusValidating,
			ExtraFields: schemas.BifrostResponseExtraFields{
				Latency: latency.Milliseconds(),
			},
		}, nil
	}

	// Use retrieved response for complete data
	result := &schemas.BifrostBatchCreateResponse{
		ID:          retrieveResp.ID,
		Object:      "batch",
		InputFileID: inputFileID,
		Status:      retrieveResp.Status,
		CreatedAt:   retrieveResp.CreatedAt,
		ExtraFields: schemas.BifrostResponseExtraFields{
			Latency: latency.Milliseconds(),
		},
	}

	if retrieveResp.ExpiresAt != nil {
		result.ExpiresAt = retrieveResp.ExpiresAt
	}

	return result, nil
}

// BatchList lists batch inference jobs using serial pagination across keys.
// Exhausts all pages from one key before moving to the next.
func (provider *BedrockProvider) BatchList(ctx *schemas.BifrostContext, keys []schemas.Key, request *schemas.BifrostBatchListRequest) (*schemas.BifrostBatchListResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.Bedrock, provider.customProviderConfig, schemas.BatchListRequest); err != nil {
		return nil, err
	}

	// Initialize serial pagination helper (Bedrock uses PageToken for pagination)
	helper, err := providerUtils.NewSerialListHelper(keys, request.PageToken, provider.logger, true)
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError("invalid pagination cursor", err)
	}

	// Get current key to query
	key, nativeCursor, ok := helper.GetCurrentKey()
	if !ok {
		// All keys exhausted
		return &schemas.BifrostBatchListResponse{
			Object:  "list",
			Data:    []schemas.BifrostBatchRetrieveResponse{},
			HasMore: false,
		}, nil
	}

	region := DefaultBedrockRegion
	if key.BedrockKeyConfig.Region != nil && key.BedrockKeyConfig.Region.GetValue() != "" {
		region = key.BedrockKeyConfig.Region.GetValue()
	}

	// Build URL with query params
	params := url.Values{}
	if request.Limit > 0 {
		params.Set("maxResults", fmt.Sprintf("%d", request.Limit))
	}
	// Use native cursor from serial helper
	if nativeCursor != "" {
		params.Set("nextToken", nativeCursor)
	}

	reqURL := fmt.Sprintf("https://bedrock.%s.amazonaws.com/model-invocation-jobs", region)
	if len(params) > 0 {
		reqURL += "?" + params.Encode()
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError("error creating request", err)
	}

	// Sign request
	if bifrostErr := signAWSRequest(ctx, httpReq, key.BedrockKeyConfig, region, bedrockSigningService); bifrostErr != nil {
		return nil, bifrostErr
	}

	// Execute request
	startTime := time.Now()
	resp, err := providerUtils.DoHTTPRequest(provider.client, httpReq)
	latency := time.Since(startTime)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return nil, &schemas.BifrostError{
				IsBifrostError: false,
				Error: &schemas.ErrorField{
					Type:    schemas.Ptr(schemas.RequestCancelled),
					Message: schemas.ErrRequestCancelled,
					Error:   err,
				},
			}
		}
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderDoRequest, err)
	}

	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError("error reading response", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, parseBedrockHTTPError(resp.StatusCode, resp.Header, body)
	}

	var bedrockResp BedrockBatchJobListResponse
	if err := sonic.Unmarshal(body, &bedrockResp); err != nil {
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseUnmarshal, err)
	}

	// Convert batches to Bifrost format
	batches := make([]schemas.BifrostBatchRetrieveResponse, 0, len(bedrockResp.InvocationJobSummaries))
	for _, job := range bedrockResp.InvocationJobSummaries {
		var createdAt int64
		if job.SubmitTime != nil {
			createdAt = job.SubmitTime.Unix()
		}

		// Store Bedrock-specific fields in Metadata for later conversion back to Bedrock format
		metadata := make(map[string]string)
		if job.JobName != "" {
			metadata["job_name"] = job.JobName
		}
		if job.ModelID != "" {
			metadata["model_id"] = job.ModelID
		}

		batches = append(batches, schemas.BifrostBatchRetrieveResponse{
			ID:        job.JobArn,
			Object:    "batch",
			Status:    ToBifrostBatchStatus(job.Status),
			CreatedAt: createdAt,
			Metadata:  metadata,
		})
	}

	// Build cursor for next request
	// Bedrock uses NextToken for pagination
	nativeNextToken := ""
	apiHasMore := false
	if bedrockResp.NextToken != nil && *bedrockResp.NextToken != "" {
		nativeNextToken = *bedrockResp.NextToken
		apiHasMore = true
	}
	nextCursor, hasMore := helper.BuildNextCursor(apiHasMore, nativeNextToken)

	// Convert to Bifrost response
	bifrostResp := &schemas.BifrostBatchListResponse{
		Object:  "list",
		Data:    batches,
		HasMore: hasMore,
		ExtraFields: schemas.BifrostResponseExtraFields{
			Latency: latency.Milliseconds(),
		},
	}
	if nextCursor != "" {
		bifrostResp.NextCursor = &nextCursor
	}

	return bifrostResp, nil
}

// fetchBatchManifest fetches the manifest.json.out from S3 to get record counts.
// Returns nil if manifest doesn't exist (job still in progress) or on error.
func (provider *BedrockProvider) fetchBatchManifest(ctx *schemas.BifrostContext, key schemas.Key, region, outputS3Uri string) *BedrockBatchManifest {
	if outputS3Uri == "" {
		return nil
	}

	// Parse the output S3 URI and construct manifest path
	bucketName, prefix := parseS3URI(outputS3Uri)
	if bucketName == "" {
		return nil
	}

	// Manifest is at: {output_s3_uri}/manifest.json.out
	base := strings.Trim(prefix, "/")
	manifestKey := "manifest.json.out"
	if base != "" {
		manifestKey = base + "/manifest.json.out"
	}

	// Build S3 GET request
	reqURL := fmt.Sprintf("https://%s.s3.%s.amazonaws.com/%s", bucketName, region, escapeS3KeyForURL(manifestKey))

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		provider.logger.Error("failed to create manifest request: %v", err)
		return nil
	}

	// Sign request for S3
	if err := signAWSRequest(ctx, httpReq, key.BedrockKeyConfig, region, "s3"); err != nil {
		provider.logger.Error("failed to sign manifest request: %v", err)
		return nil
	}

	resp, err := providerUtils.DoHTTPRequest(provider.client, httpReq)
	if err != nil {
		provider.logger.Error("failed to fetch manifest: %v", err)
		return nil
	}
	defer resp.Body.Close()

	// 404 is expected if job is still in progress
	if resp.StatusCode != http.StatusOK {
		return nil
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		provider.logger.Debug("failed to read manifest body: %v", err)
		return nil
	}

	var manifest BedrockBatchManifest
	if err := sonic.Unmarshal(body, &manifest); err != nil {
		provider.logger.Error("failed to parse manifest: %v", err)
		return nil
	}

	return &manifest
}

// BatchRetrieve retrieves a specific batch inference job from AWS Bedrock by trying each key until found.
func (provider *BedrockProvider) BatchRetrieve(ctx *schemas.BifrostContext, keys []schemas.Key, request *schemas.BifrostBatchRetrieveRequest) (*schemas.BifrostBatchRetrieveResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.Bedrock, provider.customProviderConfig, schemas.BatchRetrieveRequest); err != nil {
		return nil, err
	}

	if request.BatchID == "" {
		return nil, providerUtils.NewBifrostOperationError("batch_id (job ARN) is required", nil)
	}

	var lastErr *schemas.BifrostError
	for _, key := range keys {
		region := DefaultBedrockRegion
		if key.BedrockKeyConfig.Region != nil && key.BedrockKeyConfig.Region.GetValue() != "" {
			region = key.BedrockKeyConfig.Region.GetValue()
		}

		// URL encode the job ARN
		encodedJobArn := url.PathEscape(request.BatchID)
		reqURL := fmt.Sprintf("https://bedrock.%s.amazonaws.com/model-invocation-job/%s", region, encodedJobArn)

		httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
		if err != nil {
			lastErr = providerUtils.NewBifrostOperationError("error creating request", err)
			continue
		}

		// Sign request
		if err := signAWSRequest(ctx, httpReq, key.BedrockKeyConfig, region, bedrockSigningService); err != nil {
			lastErr = err
			continue
		}

		// Execute request
		startTime := time.Now()
		resp, err := providerUtils.DoHTTPRequest(provider.client, httpReq)
		latency := time.Since(startTime)
		if err != nil {
			if errors.Is(err, context.Canceled) {
				return nil, &schemas.BifrostError{
					IsBifrostError: false,
					Error: &schemas.ErrorField{
						Type:    schemas.Ptr(schemas.RequestCancelled),
						Message: schemas.ErrRequestCancelled,
						Error:   err,
					},
				}
			}
			lastErr = providerUtils.NewBifrostOperationError(schemas.ErrProviderDoRequest, err)
			continue
		}

		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			lastErr = providerUtils.NewBifrostOperationError("error reading response", err)
			continue
		}

		if resp.StatusCode != http.StatusOK {
			lastErr = parseBedrockHTTPError(resp.StatusCode, resp.Header, body)
			continue
		}

		var bedrockResp BedrockBatchJobResponse
		if err := sonic.Unmarshal(body, &bedrockResp); err != nil {
			lastErr = providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseUnmarshal, err)
			continue
		}

		// Store Bedrock-specific fields in Metadata for later conversion back to Bedrock format
		metadata := make(map[string]string)
		if bedrockResp.JobName != "" {
			metadata["job_name"] = bedrockResp.JobName
		}
		if bedrockResp.ModelID != "" {
			metadata["model_id"] = bedrockResp.ModelID
		}

		result := &schemas.BifrostBatchRetrieveResponse{
			ID:       bedrockResp.JobArn,
			Object:   "batch",
			Status:   ToBifrostBatchStatus(bedrockResp.Status),
			Metadata: metadata,
			ExtraFields: schemas.BifrostResponseExtraFields{
				Latency: latency.Milliseconds(),
			},
		}

		// Surface the AWS job message (e.g. a validation failure reason) so
		// callers see why a job failed without dropping to the AWS CLI.
		if bedrockResp.Message != "" {
			result.Errors = &schemas.BatchErrors{
				Object: "list",
				Data:   []schemas.BatchError{{Message: bedrockResp.Message}},
			}
		}

		if bedrockResp.InputDataConfig != nil {
			result.InputFileID = bedrockResp.InputDataConfig.S3InputDataConfig.S3Uri
		}

		if bedrockResp.OutputDataConfig != nil {
			outputURI := bedrockResp.OutputDataConfig.S3OutputDataConfig.S3Uri
			result.OutputFileID = &outputURI
			// Fetch manifest to get record counts (only available after job starts processing)
			manifest := provider.fetchBatchManifest(ctx, key, region, outputURI)
			if manifest != nil {
				result.RequestCounts = schemas.BatchRequestCounts{
					Total:     manifest.TotalRecordCount,
					Completed: manifest.ProcessedRecordCount - manifest.ErrorRecordCount,
					Failed:    manifest.ErrorRecordCount,
				}
			}
		}

		// Capture VPC config in metadata if present
		if bedrockResp.VpcConfig != nil {
			if len(bedrockResp.VpcConfig.SecurityGroupIds) > 0 {
				metadata["vpc_security_group_ids"] = strings.Join(bedrockResp.VpcConfig.SecurityGroupIds, ",")
			}
			if len(bedrockResp.VpcConfig.SubnetIds) > 0 {
				metadata["vpc_subnet_ids"] = strings.Join(bedrockResp.VpcConfig.SubnetIds, ",")
			}
		}

		if bedrockResp.SubmitTime != nil {
			result.CreatedAt = bedrockResp.SubmitTime.Unix()
		}
		if bedrockResp.EndTime != nil {
			completedAt := bedrockResp.EndTime.Unix()
			result.CompletedAt = &completedAt
		}
		if bedrockResp.JobExpirationTime != nil {
			expiresAt := bedrockResp.JobExpirationTime.Unix()
			result.ExpiresAt = &expiresAt
		}

		return result, nil
	}

	return nil, lastErr
}

// BatchCancel stops a batch inference job on AWS Bedrock by trying each key until successful.
func (provider *BedrockProvider) BatchCancel(ctx *schemas.BifrostContext, keys []schemas.Key, request *schemas.BifrostBatchCancelRequest) (*schemas.BifrostBatchCancelResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.Bedrock, provider.customProviderConfig, schemas.BatchCancelRequest); err != nil {
		return nil, err
	}

	if request.BatchID == "" {
		return nil, providerUtils.NewBifrostOperationError("batch_id (job ARN) is required", nil)
	}

	var lastErr *schemas.BifrostError
	for _, key := range keys {
		region := DefaultBedrockRegion
		if key.BedrockKeyConfig.Region != nil && key.BedrockKeyConfig.Region.GetValue() != "" {
			region = key.BedrockKeyConfig.Region.GetValue()
		}

		// URL encode the job ARN
		encodedJobArn := url.PathEscape(request.BatchID)
		reqURL := fmt.Sprintf("https://bedrock.%s.amazonaws.com/model-invocation-job/%s/stop", region, encodedJobArn)

		httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, reqURL, nil)
		if err != nil {
			lastErr = providerUtils.NewBifrostOperationError("error creating request", err)
			continue
		}

		// Sign request
		if err := signAWSRequest(ctx, httpReq, key.BedrockKeyConfig, region, bedrockSigningService); err != nil {
			lastErr = err
			continue
		}

		// Execute request
		startTime := time.Now()
		resp, err := providerUtils.DoHTTPRequest(provider.client, httpReq)
		latency := time.Since(startTime)
		if err != nil {
			if errors.Is(err, context.Canceled) {
				return nil, &schemas.BifrostError{
					IsBifrostError: false,
					Error: &schemas.ErrorField{
						Type:    schemas.Ptr(schemas.RequestCancelled),
						Message: schemas.ErrRequestCancelled,
						Error:   err,
					},
				}
			}
			lastErr = providerUtils.NewBifrostOperationError(schemas.ErrProviderDoRequest, err)
			continue
		}

		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			lastErr = providerUtils.NewBifrostOperationError("error reading response", err)
			continue
		}

		if resp.StatusCode != http.StatusOK {
			lastErr = parseBedrockHTTPError(resp.StatusCode, resp.Header, body)
			continue
		}

		// After stopping, retrieve the job to get updated status
		retrieveResp, bifrostErr := provider.BatchRetrieve(ctx, keys, &schemas.BifrostBatchRetrieveRequest{
			Provider: request.Provider,
			BatchID:  request.BatchID,
		})
		if bifrostErr != nil {
			// Return basic response if retrieve fails
			// Compute total latency including stop + failed retrieve
			totalLatency := time.Since(startTime)
			return &schemas.BifrostBatchCancelResponse{
				ID:     request.BatchID,
				Object: "batch",
				Status: schemas.BatchStatusCancelling,
				ExtraFields: schemas.BifrostResponseExtraFields{
					Latency: totalLatency.Milliseconds(),
				},
			}, nil
		}

		return &schemas.BifrostBatchCancelResponse{
			ID:     retrieveResp.ID,
			Object: "batch",
			Status: retrieveResp.Status,
			ExtraFields: schemas.BifrostResponseExtraFields{
				Latency: latency.Milliseconds(),
			},
		}, nil
	}

	return nil, lastErr
}

// BatchDelete is not supported by the Bedrock provider.
func (provider *BedrockProvider) BatchDelete(ctx *schemas.BifrostContext, keys []schemas.Key, request *schemas.BifrostBatchDeleteRequest) (*schemas.BifrostBatchDeleteResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.BatchDeleteRequest, provider.GetProviderKey())
}

// BatchResults retrieves batch results from AWS Bedrock by trying each key until successful.
// For Bedrock, results are stored in S3 at the output S3 URI prefix.
// The output includes JSONL files with results (*.jsonl.out) and a manifest file.
func (provider *BedrockProvider) BatchResults(ctx *schemas.BifrostContext, keys []schemas.Key, request *schemas.BifrostBatchResultsRequest) (*schemas.BifrostBatchResultsResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.Bedrock, provider.customProviderConfig, schemas.BatchResultsRequest); err != nil {
		return nil, err
	}

	// First, retrieve the batch to get the output S3 URI prefix (using all keys)
	batchResp, bifrostErr := provider.BatchRetrieve(ctx, keys, &schemas.BifrostBatchRetrieveRequest{
		Provider: request.Provider,
		BatchID:  request.BatchID,
	})
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	if batchResp.OutputFileID == nil || *batchResp.OutputFileID == "" {
		return nil, providerUtils.NewBifrostOperationError("batch results not available: output S3 URI is empty (batch may not be completed)", nil)
	}

	outputS3URI := *batchResp.OutputFileID
	var allResults []schemas.BatchResultItem
	var totalLatency int64
	// The output S3 URI is a prefix/folder. List files in that folder to find output JSONL files.
	var (
		listResp  *schemas.BifrostFileListResponse
		pageToken *string
		allFiles  []schemas.FileObject
	)
	for {
		listResp, bifrostErr = provider.FileList(ctx, keys, &schemas.BifrostFileListRequest{
			Provider: request.Provider,
			StorageConfig: &schemas.FileStorageConfig{
				S3: &schemas.S3StorageConfig{
					Bucket: outputS3URI,
				},
			},
			Limit: 100,
			After: pageToken,
		})
		if bifrostErr != nil {
			break
		}
		totalLatency += listResp.ExtraFields.Latency
		allFiles = append(allFiles, listResp.Data...)
		if !listResp.HasMore || listResp.After == nil {
			break
		}
		pageToken = listResp.After
	}
	if bifrostErr != nil {
		// If listing fails, try direct download (in case outputS3URI is already a file path)
		fileContentResp, directErr := provider.FileContent(ctx, keys, &schemas.BifrostFileContentRequest{
			Provider: request.Provider,
			FileID:   outputS3URI,
		})
		if directErr != nil {
			return nil, providerUtils.NewBifrostOperationError(
				fmt.Sprintf("failed to access batch results at %s: listing failed and direct access failed", outputS3URI),
				nil)
		}

		// Direct download succeeded, parse the content
		results, parseErrors := parseBatchResultsJSONL(fileContentResp.Content, provider)
		batchResultsResp := &schemas.BifrostBatchResultsResponse{
			BatchID: request.BatchID,
			Results: results,
			ExtraFields: schemas.BifrostResponseExtraFields{
				Latency: fileContentResp.ExtraFields.Latency,
			},
		}
		if len(parseErrors) > 0 {
			batchResultsResp.ExtraFields.ParseErrors = parseErrors
		}
		return batchResultsResp, nil
	}
	// Find and download JSONL output files (files ending with .jsonl.out or containing results)
	var allParseErrors []schemas.BatchError
	for _, file := range allFiles {
		// Skip manifest files, only process JSONL output files
		if strings.HasSuffix(file.ID, ".jsonl.out") || strings.HasSuffix(file.ID, ".jsonl") {
			fileContentResp, fileErr := provider.FileContent(ctx, keys, &schemas.BifrostFileContentRequest{
				Provider: request.Provider,
				FileID:   file.ID,
			})
			if fileErr != nil {
				provider.logger.Warn("failed to download batch result file %s: %v", file.ID, fileErr)
				continue
			}

			totalLatency += fileContentResp.ExtraFields.Latency
			results, parseErrors := parseBatchResultsJSONL(fileContentResp.Content, provider)
			allResults = append(allResults, results...)
			allParseErrors = append(allParseErrors, parseErrors...)
		}
	}

	batchResultsResp := &schemas.BifrostBatchResultsResponse{
		BatchID: request.BatchID,
		Results: allResults,
		ExtraFields: schemas.BifrostResponseExtraFields{
			Latency: totalLatency,
		},
	}

	if len(allParseErrors) > 0 {
		batchResultsResp.ExtraFields.ParseErrors = allParseErrors
	}

	return batchResultsResp, nil
}

// getModelPathAndRegion is a helper that calls parseBedrockRegionAndModel
// once and returns both the request path and the AWS signing region.
// Honors per-alias Region and BedrockAliasCfg.InferenceProfileARN overrides
// via the resolved alias in ctx.
func (provider *BedrockProvider) getModelPathAndRegion(ctx *schemas.BifrostContext, basePath, model string, key schemas.Key) (path, region string) {
	r, bareModel := parseBedrockRegionAndModel(model)
	if r == "" {
		if ra := schemas.GetResolvedAlias(ctx); ra != nil && ra.Config != nil && ra.Config.Region != nil {
			if v := ra.Config.Region.GetValue(); v != "" {
				r = v
			}
		}
		if r == "" {
			if key.BedrockKeyConfig != nil && key.BedrockKeyConfig.Region != nil && key.BedrockKeyConfig.Region.GetValue() != "" {
				r = key.BedrockKeyConfig.Region.GetValue()
			} else {
				r = DefaultBedrockRegion
			}
		}
	}
	p := fmt.Sprintf("%s/%s", bareModel, basePath)
	if arn := resolveBedrockARN(ctx, key); arn != "" {
		encodedModelIdentifier := url.PathEscape(fmt.Sprintf("%s/%s", arn, bareModel))
		p = fmt.Sprintf("%s/%s", encodedModelIdentifier, basePath)
	}
	return p, r
}

func (provider *BedrockProvider) CountTokens(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostResponsesRequest) (*schemas.BifrostCountTokensResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.Bedrock, provider.customProviderConfig, schemas.CountTokensRequest); err != nil {
		return nil, err
	}

	// Convert to Bedrock Converse format using the existing responses converter
	converseReq, convErr := ToBedrockResponsesRequest(ctx, request)
	if convErr != nil {
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderRequestMarshal, convErr)
	}

	// Wrap in the CountTokens request envelope
	countTokensReq := &BedrockCountTokensRequest{}
	countTokensReq.Input.Converse = converseReq

	jsonData, err := providerUtils.MarshalSorted(countTokensReq)
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderRequestMarshal, err)
	}

	// Format the path with proper model identifier
	path, _ := provider.getModelPathAndRegion(ctx, "count-tokens", request.Model, key)

	// Send the request
	responseBody, latency, providerResponseHeaders, bifrostErr := provider.completeRequest(ctx, jsonData, path, key, request.Model)
	if providerResponseHeaders != nil {
		ctx.SetValue(schemas.BifrostContextKeyProviderResponseHeaders, providerResponseHeaders)
	}
	if bifrostErr != nil {
		if isCountTokensUnsupported(bifrostErr) {
			estimated := estimateTokenCount(jsonData)
			return &schemas.BifrostCountTokensResponse{
				Model:       request.Model,
				InputTokens: estimated,
				TotalTokens: &estimated,
				Object:      "response.input_tokens",
				ExtraFields: schemas.BifrostResponseExtraFields{
					Latency:                 latency.Milliseconds(),
					ProviderResponseHeaders: providerResponseHeaders,
				},
			}, nil
		}
		return nil, providerUtils.EnrichError(ctx, bifrostErr, jsonData, responseBody, provider.sendBackRawRequest, provider.sendBackRawResponse, latency)
	}

	// Parse the response
	bedrockResponse := &BedrockCountTokensResponse{}
	rawRequest, rawResponse, bifrostErr := providerUtils.HandleProviderResponse(
		responseBody,
		bedrockResponse,
		jsonData,
		providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest),
		providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse),
	)
	if bifrostErr != nil {
		return nil, providerUtils.EnrichError(ctx, bifrostErr, jsonData, responseBody, provider.sendBackRawRequest, provider.sendBackRawResponse, latency)
	}

	// Convert to Bifrost format
	response := bedrockResponse.ToBifrostCountTokensResponse(request.Model)

	response.ExtraFields.Latency = latency.Milliseconds()
	response.ExtraFields.ProviderResponseHeaders = providerResponseHeaders
	if providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest) {
		response.ExtraFields.RawRequest = rawRequest
	}

	if providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse) {
		response.ExtraFields.RawResponse = rawResponse
	}

	return response, nil
}

// Compaction is not supported by the Bedrock provider.
func (provider *BedrockProvider) Compaction(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostCompactionRequest) (*schemas.BifrostCompactionResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.CompactionRequest, provider.GetProviderKey())
}

// ContainerCreate is not supported by the Bedrock provider.
func (provider *BedrockProvider) ContainerCreate(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostContainerCreateRequest) (*schemas.BifrostContainerCreateResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerCreateRequest, provider.GetProviderKey())
}

// ContainerList is not supported by the Bedrock provider.
func (provider *BedrockProvider) ContainerList(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostContainerListRequest) (*schemas.BifrostContainerListResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerListRequest, provider.GetProviderKey())
}

// ContainerRetrieve is not supported by the Bedrock provider.
func (provider *BedrockProvider) ContainerRetrieve(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostContainerRetrieveRequest) (*schemas.BifrostContainerRetrieveResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerRetrieveRequest, provider.GetProviderKey())
}

// ContainerDelete is not supported by the Bedrock provider.
func (provider *BedrockProvider) ContainerDelete(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostContainerDeleteRequest) (*schemas.BifrostContainerDeleteResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerDeleteRequest, provider.GetProviderKey())
}

// ContainerFileCreate is not supported by the Bedrock provider.
func (provider *BedrockProvider) ContainerFileCreate(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostContainerFileCreateRequest) (*schemas.BifrostContainerFileCreateResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerFileCreateRequest, provider.GetProviderKey())
}

// ContainerFileList is not supported by the Bedrock provider.
func (provider *BedrockProvider) ContainerFileList(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostContainerFileListRequest) (*schemas.BifrostContainerFileListResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerFileListRequest, provider.GetProviderKey())
}

// ContainerFileRetrieve is not supported by the Bedrock provider.
func (provider *BedrockProvider) ContainerFileRetrieve(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostContainerFileRetrieveRequest) (*schemas.BifrostContainerFileRetrieveResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerFileRetrieveRequest, provider.GetProviderKey())
}

// ContainerFileContent is not supported by the Bedrock provider.
func (provider *BedrockProvider) ContainerFileContent(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostContainerFileContentRequest) (*schemas.BifrostContainerFileContentResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerFileContentRequest, provider.GetProviderKey())
}

// ContainerFileDelete is not supported by the Bedrock provider.
func (provider *BedrockProvider) ContainerFileDelete(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostContainerFileDeleteRequest) (*schemas.BifrostContainerFileDeleteResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerFileDeleteRequest, provider.GetProviderKey())
}

// Passthrough is not supported by the Bedrock provider.
func (provider *BedrockProvider) Passthrough(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostPassthroughRequest) (*schemas.BifrostPassthroughResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.PassthroughRequest, provider.GetProviderKey())
}

func (provider *BedrockProvider) PassthroughStream(_ *schemas.BifrostContext, _ schemas.PostHookRunner, _ func(context.Context), _ schemas.Key, _ *schemas.BifrostPassthroughRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.PassthroughStreamRequest, provider.GetProviderKey())
}
