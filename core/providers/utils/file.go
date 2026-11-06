package utils

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/valyala/fasthttp"
)

// FileBytesToBase64DataURL converts raw file bytes to base64 data URL format
func FileBytesToBase64DataURL(fileBytes []byte) string {
	mimeType := http.DetectContentType(fileBytes)
	b64Data := base64.StdEncoding.EncodeToString(fileBytes)
	return fmt.Sprintf("data:%s;base64,%s", mimeType, b64Data)
}

const maxAudioDownloadSize = 25 * 1024 * 1024

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
			return connClosedOnCancel(ctx, conn), nil
		}
		dialErr = err
	}
	return nil, dialErr
}

// connClosedOnCancel closes conn when ctx is canceled so an in-flight fasthttp
// read unblocks instead of waiting out ReadTimeout. Close stops the watch so a
// finished download does not close the conn again when ctx ends later.
func connClosedOnCancel(ctx context.Context, conn net.Conn) net.Conn {
	if ctx == nil || ctx.Done() == nil {
		return conn
	}
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	return &cancelAudioConn{Conn: conn, stop: stop}
}

type cancelAudioConn struct {
	net.Conn
	stop func() bool
}

func (c *cancelAudioConn) Close() error {
	c.stop()
	return c.Conn.Close()
}

// audioDownloadClient holds the shared download timeouts and the dial-time
// SSRF check. Tests call Dial directly. Downloads do not use this client:
// fasthttp's dial hook cannot take a context, and keying one on the calling
// goroutine is unsound because fasthttp may dial elsewhere. Each download
// builds its own HostClient whose Dial closure captures that request's ctx.
var audioDownloadClient = &fasthttp.Client{
	ReadTimeout:         20 * time.Second,
	WriteTimeout:        20 * time.Second,
	MaxResponseBodySize: maxAudioDownloadSize + 1,
	Dial: func(addr string) (net.Conn, error) {
		return dialValidatedAudioURL(context.Background(), "tcp", addr)
	},
}

// newAudioDownloadClient returns a per-request client. The dial closure
// captures ctx, and dialValidatedAudioURL closes the conn when ctx is canceled
// so an in-flight read unblocks instead of waiting out ReadTimeout.
func newAudioDownloadClient(ctx context.Context, fileURL string) (*fasthttp.HostClient, error) {
	u, err := url.Parse(fileURL)
	if err != nil {
		return nil, fmt.Errorf("invalid URL: %w", err)
	}
	isTLS := u.Scheme == "https"
	return &fasthttp.HostClient{
		Addr:                fasthttp.AddMissingPort(u.Host, isTLS),
		IsTLS:               isTLS,
		ReadTimeout:         audioDownloadClient.ReadTimeout,
		WriteTimeout:        audioDownloadClient.WriteTimeout,
		MaxResponseBodySize: audioDownloadClient.MaxResponseBodySize,
		Dial: func(addr string) (net.Conn, error) {
			return dialValidatedAudioURL(ctx, "tcp", addr)
		},
	}, nil
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

	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	req.SetRequestURI(fileURL)
	req.Header.SetMethod(fasthttp.MethodGet)
	req.Header.SetUserAgent("bifrost-fetch/1")
	req.Header.Set(fasthttp.HeaderConnection, "close")

	client, err := newAudioDownloadClient(ctx, fileURL)
	if err != nil {
		fasthttp.ReleaseRequest(req)
		fasthttp.ReleaseResponse(resp)
		return "", err
	}

	// The goroutine is the sole owner of the pooled req/resp. Releasing them
	// on the cancel path would race with DoDeadline/DoTimeout still writing.
	resultCh := make(chan audioDownloadResult, 1)
	go func() {
		defer fasthttp.ReleaseRequest(req)
		defer fasthttp.ReleaseResponse(resp)
		resultCh <- performAudioDownload(ctx, client, req, resp)
	}()

	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case result := <-resultCh:
		if err := ctx.Err(); err != nil {
			return "", err
		}
		return result.data, result.err
	}
}

type audioDownloadResult struct {
	data string
	err  error
}

func performAudioDownload(ctx context.Context, client *fasthttp.HostClient, req *fasthttp.Request, resp *fasthttp.Response) audioDownloadResult {
	var err error
	if deadline, ok := ctx.Deadline(); ok {
		err = client.DoDeadline(req, resp, deadline)
	} else {
		err = client.DoTimeout(req, resp, client.ReadTimeout)
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return audioDownloadResult{err: ctxErr}
	}
	if err != nil {
		if errors.Is(err, fasthttp.ErrBodyTooLarge) {
			return audioDownloadResult{err: fmt.Errorf("audio URL response exceeds %d byte limit", maxAudioDownloadSize)}
		}
		return audioDownloadResult{err: fmt.Errorf("failed to download URL: %w", err)}
	}

	statusCode := resp.StatusCode()
	if statusCode >= fasthttp.StatusMultipleChoices && statusCode < fasthttp.StatusBadRequest {
		return audioDownloadResult{err: fmt.Errorf("redirect not followed (status=%d, Location=%q); resolve the redirect server-side or supply the final URL", statusCode, resp.Header.Peek("Location"))}
	}
	if statusCode < fasthttp.StatusOK || statusCode >= fasthttp.StatusMultipleChoices {
		return audioDownloadResult{err: fmt.Errorf("failed to download URL: status=%d", statusCode)}
	}
	if contentLength := string(resp.Header.Peek("Content-Length")); contentLength != "" {
		size, parseErr := strconv.ParseInt(contentLength, 10, 64)
		if parseErr == nil && size > maxAudioDownloadSize {
			return audioDownloadResult{err: fmt.Errorf("audio URL response exceeds %d byte limit", maxAudioDownloadSize)}
		}
	}

	body := resp.Body()
	if len(body) > maxAudioDownloadSize {
		return audioDownloadResult{err: fmt.Errorf("audio URL response exceeds %d byte limit", maxAudioDownloadSize)}
	}
	return audioDownloadResult{data: base64.StdEncoding.EncodeToString(body)}
}
