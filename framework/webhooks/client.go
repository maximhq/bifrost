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
// applies network.PrivateNetworkDialContext's policy instead: loopback and
// private receivers are dialable, link-local (including its IPv6 transition
// forms), the cloud metadata endpoints, and unspecified addresses never are.
// With an HTTP client factory, both go through the global proxy when it is
// enabled for API traffic; the policy then judges each target before the
// request is proxied (network.PolicyTransport). Both refuse redirects and
// require TLS >= 1.2. The clients carry no timeout state at all — every phase
// (DNS, dial, TLS, body) is bounded by the per-attempt context, which carries
// the endpoint's own attempt timeout — so endpoints sharing a policy can share
// connection pools safely.
type deliveryClient struct {
	strict  *http.Client
	private *http.Client
}

func newDeliveryClient(factory *network.HTTPClientFactory) *deliveryClient {
	build := func(policy *network.DialPolicy, dial func(ctx context.Context, netw, addr string) (net.Conn, error)) *http.Client {
		var transport http.RoundTripper = &http.Transport{
			DialContext:     dial,
			TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12},
		}
		if factory != nil {
			transport = factory.PolicyTransport(network.ClientPurposeAPI, policy)
		}
		return &http.Client{
			Transport: transport,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				return http.ErrUseLastResponse
			},
		}
	}
	// A zero dial timeout defers entirely to the per-attempt context.
	strictDial := network.SSRFSafeDialContext(0)
	privateDial := network.PrivateNetworkDialContext(0)
	return &deliveryClient{
		strict:  build(network.SSRFPolicy(nil), strictDial),
		private: build(network.NewDialPolicy(privateDial, privateTargetCheck), privateDial),
	}
}

// privateTargetCheck is the allow_private_network rule for a proxied delivery:
// the receiver is resolved here and every address must pass the same
// PrivateNetworkDialContext policy a direct delivery applies at dial time.
func privateTargetCheck(ctx context.Context, host string) error {
	_, err := network.ResolvePrivateNetworkTarget(ctx, host)
	return err
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
