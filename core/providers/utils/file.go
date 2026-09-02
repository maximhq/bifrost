package utils

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"sync/atomic"
	"testing"
	"time"
)

// FileBytesToBase64DataURL converts raw file bytes to base64 data URL format
func FileBytesToBase64DataURL(fileBytes []byte) string {
	mimeType := http.DetectContentType(fileBytes)
	b64Data := base64.StdEncoding.EncodeToString(fileBytes)
	return fmt.Sprintf("data:%s;base64,%s", mimeType, b64Data)
}

const maxAudioDownloadSize = 25 * 1024 * 1024

type contextDownloadClient struct {
	*http.Client
	Dial func(string) (net.Conn, error)
}

var audioDownloadDialer = &net.Dialer{Timeout: 10 * time.Second}

func dialValidatedAudioURL(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil || len(ips) == 0 {
		return nil, fmt.Errorf("failed to resolve %q", host)
	}
	if !allowPrivateAudioURLs.Load() {
		for _, ip := range ips {
			if isPrivateOrInternalIP(ip.IP) {
				return nil, fmt.Errorf("resolved to private/internal address %s", ip.IP)
			}
		}
	}

	var dialErr error
	for _, ip := range ips {
		conn, err := audioDownloadDialer.DialContext(ctx, network, net.JoinHostPort(ip.IP.String(), port))
		if err == nil {
			return conn, nil
		}
		dialErr = err
	}
	return nil, dialErr
}

// downloadClient is process-wide so TCP/TLS connections are reused across
// audio downloads. DialContext validates and pins the resolved address used by
// the connection, preventing a second DNS lookup from bypassing the SSRF guard.
var downloadClient = &contextDownloadClient{
	Client: &http.Client{
		Timeout: 20 * time.Second,
		Transport: &http.Transport{
			DialContext: dialValidatedAudioURL,
		},
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	},
	Dial: func(addr string) (net.Conn, error) {
		return dialValidatedAudioURL(context.Background(), "tcp", addr)
	},
}

// allowPrivateAudioURLs is a test-only override. Production code never sets it.
var allowPrivateAudioURLs atomic.Bool

// AllowPrivateAudioURLsForTest disables the SSRF guard so httptest servers on
// loopback can drive the download path in tests. Returns a cleanup function
// the caller MUST defer to restore the guard.
//
// Guarded by testing.Testing() so a non-test binary that reaches this function
// crashes immediately instead of silently flipping the SSRF bypass.
func AllowPrivateAudioURLsForTest() func() {
	if !testing.Testing() {
		panic("utils.AllowPrivateAudioURLsForTest: must not be called outside tests")
	}
	allowPrivateAudioURLs.Store(true)
	return func() { allowPrivateAudioURLs.Store(false) }
}

// validateRequestURL refuses URLs that would let a user-supplied audio URL
// reach internal services. The audio URL comes straight from the request
// body (ChatInputAudio.URL / ResponsesInputAudio.URL), so a naive fetch
// would let a caller probe the AWS IMDS endpoint (169.254.169.254), Redis
// on localhost, RFC 1918 ranges, etc.
func validateRequestURL(rawURL string) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("invalid URL: %w", err)
	}
	switch u.Scheme {
	case "https":
		// ok
	case "http":
		if !allowPrivateAudioURLs.Load() {
			return errors.New("plaintext http audio URLs are not allowed; use https")
		}
	default:
		return fmt.Errorf("unsupported URL scheme %q; only http(s) is allowed", u.Scheme)
	}
	host := u.Hostname()
	if host == "" {
		return errors.New("URL must include a host")
	}
	return nil
}

// isPrivateOrInternalIP returns true for any address the audio downloader
// must refuse: loopback, link-local (covers AWS IMDS 169.254.x and IPv6
// fe80::/10), multicast / unspecified, and RFC 1918 / ULA private ranges.
func isPrivateOrInternalIP(ip net.IP) bool {
	return ip.IsLoopback() ||
		ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() ||
		ip.IsInterfaceLocalMulticast() ||
		ip.IsMulticast() ||
		ip.IsUnspecified() ||
		ip.IsPrivate()
}

// DownloadURLToBase64 downloads content from a URL and returns it as a
// base64-encoded string. URLs are validated to reject non-https schemes and
// private/internal targets, and redirects are NOT followed (a redirect would
// target a host the guard never validated).
func DownloadURLToBase64(ctx context.Context, fileURL string) (string, error) {
	if ctx == nil {
		return "", fmt.Errorf("context is required")
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := validateRequestURL(fileURL); err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fileURL, nil)
	if err != nil {
		return "", fmt.Errorf("failed to create download request: %w", err)
	}
	req.Header.Set("User-Agent", "bifrost-fetch/1")

	resp, err := downloadClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to download URL: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= http.StatusMultipleChoices && resp.StatusCode < http.StatusBadRequest {
		return "", fmt.Errorf("redirect not followed (status=%d, Location=%q); resolve the redirect server-side or supply the final URL", resp.StatusCode, resp.Header.Get("Location"))
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return "", fmt.Errorf("failed to download URL: status=%d", resp.StatusCode)
	}
	if contentLength := resp.Header.Get("Content-Length"); contentLength != "" {
		size, parseErr := strconv.ParseInt(contentLength, 10, 64)
		if parseErr == nil && size > maxAudioDownloadSize {
			return "", fmt.Errorf("audio URL response exceeds %d byte limit", maxAudioDownloadSize)
		}
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxAudioDownloadSize+1))
	if err != nil {
		return "", fmt.Errorf("failed to read URL response: %w", err)
	}
	if len(body) > maxAudioDownloadSize {
		return "", fmt.Errorf("audio URL response exceeds %d byte limit", maxAudioDownloadSize)
	}
	return base64.StdEncoding.EncodeToString(body), nil
}
