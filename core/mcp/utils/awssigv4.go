package utils

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/maximhq/bifrost/core/providers/utils/awsauth"
	"github.com/maximhq/bifrost/core/schemas"
)

// ErrAWSSigV4Credentials marks a request an aws_sigv4 client could not sign because
// Bifrost's AWS credentials could not be resolved (no identity, a refused AssumeRole).
var ErrAWSSigV4Credentials = errors.New("aws_sigv4: could not get AWS credentials")

// AWSSigV4RejectedError is a 401 or 403 an MCP server answered a signed request
// with: the signature, its scope or the identity's IAM permissions were refused.
type AWSSigV4RejectedError struct {
	StatusCode int
	Message    string // the server's response body, trimmed
}

func (e *AWSSigV4RejectedError) Error() string {
	return fmt.Sprintf("aws_sigv4: the MCP server refused the signed request with HTTP %d: %s", e.StatusCode, e.Message)
}

// awsSigV4RejectionBodyLimit bounds how much of a refusal's body is kept.
const awsSigV4RejectionBodyLimit = 1024

// AWSSigV4Credentials maps an aws_sigv4 block onto the credential source it signs with:
// the default chain, assuming the configured role on top when there is one.
func AWSSigV4Credentials(cfg *schemas.MCPAWSSigV4Config) awsauth.Config {
	if cfg == nil {
		return awsauth.Config{}
	}
	return awsauth.Config{
		RoleARN:     strings.TrimSpace(cfg.RoleARN.GetValue()),
		ExternalID:  strings.TrimSpace(cfg.ExternalID.GetValue()),
		SessionName: strings.TrimSpace(cfg.SessionName.GetValue()),
	}
}

// NewAWSSigV4Client returns a copy of base that SigV4-signs every request with cfg,
// after every other header is set and before base's TLS, proxy and dial policies run.
// It follows no redirect: a signature is bound to its host. With refuseAuthStatus, a
// 401 or 403 answer becomes an *AWSSigV4RejectedError carrying the server's reason:
// an MCP server never sends either as a JSON-RPC answer, and mcp-go would otherwise
// report a 403 on initialize as a legacy SSE server and drop the body. An OpenAPI
// upstream's 403 is the API's own answer, so it is passed through.
func NewAWSSigV4Client(base *http.Client, cfg *schemas.MCPAWSSigV4Config, refuseAuthStatus bool) *http.Client {
	next := base.Transport
	if next == nil {
		next = http.DefaultTransport
	}
	client := *base
	client.Transport = &awsSigV4Transport{
		next:             next,
		creds:            AWSSigV4Credentials(cfg),
		region:           strings.TrimSpace(cfg.Region.GetValue()),
		service:          cfg.SigningService(),
		refuseAuthStatus: refuseAuthStatus,
	}
	client.CheckRedirect = func(req *http.Request, _ []*http.Request) error {
		return fmt.Errorf("aws_sigv4: not following the redirect to %s; a signed request is not resent elsewhere", req.URL.Redacted())
	}
	return &client
}

// awsSigV4Transport signs each request with SigV4 before handing it to next.
type awsSigV4Transport struct {
	next             http.RoundTripper
	creds            awsauth.Config
	region           string
	service          string
	refuseAuthStatus bool
	provider         atomic.Pointer[aws.CredentialsProvider] // resolved on first use
}

func (t *awsSigV4Transport) RoundTrip(req *http.Request) (*http.Response, error) {
	signed := req.Clone(req.Context())
	// The signer owns these: a configured or forwarded one would be sent unsigned or
	// replace the signature.
	for name := range signed.Header {
		if schemas.IsAWSSigV4ReservedHeader(name) {
			delete(signed.Header, name)
		}
	}
	payloadHash, err := awsauth.HashBody(signed)
	if req.Body != nil && signed.Body != req.Body {
		// HashBody handed the clone a fresh body; the caller's is spent.
		_ = req.Body.Close()
	}
	if err != nil {
		closeBody(signed.Body)
		return nil, fmt.Errorf("aws_sigv4: %w", err)
	}
	creds, err := t.credentials(req)
	if err != nil {
		closeBody(signed.Body)
		return nil, fmt.Errorf("%w for region %s: %w", ErrAWSSigV4Credentials, t.region, err)
	}
	// Sent and signed: Lambda function URLs refuse a POST or PUT without the body hash.
	signed.Header.Set("X-Amz-Content-Sha256", payloadHash)
	if err := awsauth.Sign(req.Context(), signed, creds, payloadHash, t.service, t.region); err != nil {
		closeBody(signed.Body)
		return nil, fmt.Errorf("aws_sigv4: signing failed: %w", err)
	}
	resp, err := t.next.RoundTrip(signed)
	if err != nil || !t.refuseAuthStatus || (resp.StatusCode != http.StatusUnauthorized && resp.StatusCode != http.StatusForbidden) {
		return resp, err
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, awsSigV4RejectionBodyLimit))
	closeBody(resp.Body)
	return nil, &AWSSigV4RejectedError{StatusCode: resp.StatusCode, Message: strings.TrimSpace(string(body))}
}

// CloseIdleConnections closes next's idle connections, so dropping or replacing a
// signed client frees its sockets.
func (t *awsSigV4Transport) CloseIdleConnections() {
	if c, ok := t.next.(interface{ CloseIdleConnections() }); ok {
		c.CloseIdleConnections()
	}
}

// credentials returns the client's credentials, cached by awsauth until they near expiry.
func (t *awsSigV4Transport) credentials(req *http.Request) (aws.Credentials, error) {
	held := t.provider.Load()
	if held == nil {
		provider, err := awsauth.CredentialsProvider(req.Context(), t.creds, t.region)
		if err != nil {
			return aws.Credentials{}, err
		}
		held = &provider
		t.provider.Store(held)
	}
	creds, err := (*held).Retrieve(req.Context())
	if err != nil {
		// Look the provider up again next time, so a source that appeared since is used.
		t.provider.CompareAndSwap(held, nil)
	}
	return creds, err
}

func closeBody(body io.ReadCloser) {
	if body != nil {
		_ = body.Close()
	}
}
