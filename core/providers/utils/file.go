package utils

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"time"

	"github.com/valyala/fasthttp"
)

// FileBytesToBase64DataURL converts raw file bytes to base64 data URL format
func FileBytesToBase64DataURL(fileBytes []byte) string {
	mimeType := http.DetectContentType(fileBytes)
	b64Data := base64.StdEncoding.EncodeToString(fileBytes)
	return fmt.Sprintf("data:%s;base64,%s", mimeType, b64Data)
}

// downloadClient is a process-wide fasthttp.Client used for audio URL fetches.
// Reusing a single client allows TCP/TLS connection pooling across requests
// instead of paying the connect cost per audio block.
var downloadClient = &fasthttp.Client{
	ReadTimeout:         20 * time.Second,
	WriteTimeout:        10 * time.Second,
	MaxResponseBodySize: 25 * 1024 * 1024,
	Dial: func(addr string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, err
		}
		ips, err := net.LookupIP(host)
		if err != nil || len(ips) == 0 {
			return nil, fmt.Errorf("failed to resolve %q", host)
		}
		if !allowPrivateAudioURLs {
			for _, ip := range ips {
				if isPrivateOrInternalIP(ip) {
					return nil, fmt.Errorf("resolved to private/internal address %s", ip)
				}
			}
		}
		return net.DialTimeout("tcp", net.JoinHostPort(ips[0].String(), port), 10*time.Second)
	},
}

// allowPrivateAudioURLs is a test-only override. Production code never sets it.
var allowPrivateAudioURLs bool

// AllowPrivateAudioURLsForTest disables the SSRF guard so httptest servers on
// loopback can drive the download path in tests. Returns a cleanup function
// the caller MUST defer to restore the guard.
//
// This MUST NOT be called from non-test code.
func AllowPrivateAudioURLsForTest() func() {
	allowPrivateAudioURLs = true
	return func() { allowPrivateAudioURLs = false }
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
		if !allowPrivateAudioURLs {
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
	if err := validateRequestURL(fileURL); err != nil {
		return "", err
	}

	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	req.SetRequestURI(fileURL)
	req.Header.SetMethod(http.MethodGet)
	req.Header.SetUserAgent("bifrost-fetch/1")

	type downloadResult struct {
		data string
		err  error
	}
	resultCh := make(chan downloadResult, 1)

	go func() {
		defer fasthttp.ReleaseRequest(req)
		defer fasthttp.ReleaseResponse(resp)

		if err := downloadClient.Do(req, resp); err != nil {
			resultCh <- downloadResult{err: fmt.Errorf("failed to download URL: %w", err)}
			return
		}
		sc := resp.StatusCode()
		if sc >= fasthttp.StatusMultipleChoices && sc < fasthttp.StatusBadRequest {
			loc := string(resp.Header.Peek("Location"))
			resultCh <- downloadResult{err: fmt.Errorf("redirect not followed (status=%d, Location=%q); resolve the redirect server-side or supply the final URL", sc, loc)}
			return
		}
		if sc < fasthttp.StatusOK || sc >= fasthttp.StatusMultipleChoices {
			resultCh <- downloadResult{err: fmt.Errorf("failed to download URL: status=%d", sc)}
			return
		}
		body := resp.Body()
		resultCh <- downloadResult{data: base64.StdEncoding.EncodeToString(body)}
	}()

	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case result := <-resultCh:
		return result.data, result.err
	}
}
