package webhooks

import (
	"bytes"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/maximhq/bifrost/core/network"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/logstore"
)

// maxErrorBodyBytes caps how much of a failing receiver's response body is
// read for the delivery history's error text.
const maxErrorBodyBytes = 4 * 1024

// deliveryClient performs one signed POST per delivery attempt. It holds one
// HTTP client per security policy: the strict client re-resolves and
// re-validates every IP at dial time (rebinding-safe), while the private
// client — used only for endpoints registered with allow_private_network —
// skips only the public-IP requirement. Both refuse redirects and require
// TLS >= 1.2. The clients carry no timeout state at all — every phase (DNS,
// dial, TLS, body) is bounded by the per-attempt context, which carries the
// endpoint's own attempt timeout — so endpoints sharing a policy can share
// connection pools safely.
type deliveryClient struct {
	strict  *http.Client
	private *http.Client
}

func newDeliveryClient() *deliveryClient {
	build := func(dial func(ctx context.Context, netw, addr string) (net.Conn, error)) *http.Client {
		return &http.Client{
			Transport: &http.Transport{
				DialContext:     dial,
				TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12},
			},
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				return http.ErrUseLastResponse
			},
		}
	}
	return &deliveryClient{
		// A zero dial timeout defers entirely to the per-attempt context.
		strict:  build(network.SSRFSafeDialContext(0)),
		private: build(newPrivateDialContext()),
	}
}

// newPrivateDialContext returns the dialer for allow_private_network
// endpoints. Loopback, RFC1918, and public receivers are the point of the
// flag, but unspecified and link-local (cloud metadata) addresses stay
// blocked at dial time — the same gate the provider clients apply — so a
// DNS record that flips to 169.254.169.254 after registration still cannot
// be reached. Dial time is bounded by the caller's context.
func newPrivateDialContext() func(ctx context.Context, netw, addr string) (net.Conn, error) {
	dialer := &net.Dialer{}
	return func(ctx context.Context, netw, addr string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, err
		}
		ips, err := net.DefaultResolver.LookupIP(ctx, "ip", host)
		if err != nil {
			return nil, err
		}
		if len(ips) == 0 {
			return nil, fmt.Errorf("no addresses found for %s", host)
		}
		for _, ip := range ips {
			if ip.IsUnspecified() {
				return nil, fmt.Errorf("connection to unspecified IP %s is not allowed", ip)
			}
			if network.IsLinkLocal(ip) {
				return nil, fmt.Errorf("connection to link-local IP %s is not allowed", ip)
			}
		}
		return dialer.DialContext(ctx, netw, net.JoinHostPort(ips[0].String(), port))
	}
}

// attemptResult captures the observable outcome of one delivery attempt.
type attemptResult struct {
	// statusCode is the receiver's HTTP status, or 0 when no response was
	// obtained (network error, signing failure, invalid URL).
	statusCode int
	// errText is a truncated human-readable failure reason for the delivery
	// history; empty on success.
	errText string
}

// deliver signs body for the given delivery id and POSTs it to the endpoint.
func (c *deliveryClient) deliver(ctx context.Context, endpoint *tables.TableWebhookEndpoint, event tables.WebhookEvent, webhookID string, body []byte, timestamp time.Time) attemptResult {
	parsed, err := url.Parse(endpoint.URL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return attemptResult{errText: "invalid webhook URL scheme"}
	}
	// Endpoint validation already ties plaintext HTTP to the private-network
	// opt-in; re-check here so a row that bypassed it (older data, direct
	// writes) can never send signed payloads and custom headers in cleartext.
	if parsed.Scheme == "http" && !endpoint.AllowPrivateNetwork {
		return attemptResult{errText: "https is required unless allow_private_network is set"}
	}
	secret := ""
	if endpoint.Secret != nil {
		secret = endpoint.Secret.GetValue()
	}
	signature, err := Sign(secret, webhookID, timestamp, body)
	if err != nil {
		return attemptResult{errText: fmt.Sprintf("cannot sign delivery: %v", err)}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.URL, bytes.NewReader(body))
	if err != nil {
		return attemptResult{errText: err.Error()}
	}
	// Custom endpoint headers go first; the reserved delivery headers are set
	// after them, so they always win even if validation was bypassed.
	for name, value := range endpoint.Headers {
		if tables.IsProtectedWebhookHeader(name) {
			continue
		}
		req.Header.Set(name, value.GetValue())
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "Bifrost-Webhooks/1.0")
	req.Header.Set("webhook-id", webhookID)
	req.Header.Set("webhook-timestamp", strconv.FormatInt(timestamp.Unix(), 10))
	req.Header.Set("webhook-signature", signature)
	req.Header.Set("X-Bifrost-Event", string(event))

	client := c.strict
	if endpoint.AllowPrivateNetwork {
		client = c.private
	}
	resp, err := client.Do(req)
	if err != nil {
		return attemptResult{errText: err.Error()}
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		// Drain (bounded) so the keep-alive connection can be reused for the
		// next delivery; closing an unread body discards the connection.
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxErrorBodyBytes))
		return attemptResult{statusCode: resp.StatusCode}
	}
	snippet, readErr := io.ReadAll(io.LimitReader(resp.Body, maxErrorBodyBytes))
	if readErr != nil && len(snippet) == 0 {
		snippet = []byte(fmt.Sprintf("(failed to read response body: %v)", readErr))
	}
	return attemptResult{
		statusCode: resp.StatusCode,
		errText:    fmt.Sprintf("receiver responded %d: %s", resp.StatusCode, snippet),
	}
}

// classify maps an attempt result to its delivery outcome, before the
// attempt-budget check promotes retryable failures to exhausted. Any 2xx is
// success; 408, 429, 5xx, and transport-level failures are worth retrying;
// everything else — including 3xx, since redirects are never followed — is a
// permanent receiver-side rejection.
func classify(result attemptResult) logstore.WebhookDeliveryOutcome {
	switch {
	case result.statusCode >= 200 && result.statusCode < 300:
		return logstore.WebhookDeliveryOutcomeDelivered
	case result.statusCode == 0,
		result.statusCode == http.StatusRequestTimeout,
		result.statusCode == http.StatusTooManyRequests,
		result.statusCode >= 500:
		return logstore.WebhookDeliveryOutcomeRetryableFailure
	default:
		return logstore.WebhookDeliveryOutcomePermanentFailure
	}
}
