package utils

import (
	"bytes"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// TestRedactURLForError pins that nothing a caller could authenticate with survives into
// an error string. AWS documents pre-signed URLs as bearer tokens valid for up to 7 days
// (docs.aws.amazon.com/AmazonS3/latest/userguide/using-presigned-url.html), so the query
// half is the credential, not a detail.
func TestRedactURLForError(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    string
		secrets []string
	}{
		{
			name:    "s3 sigv4 pre-signed url",
			input:   "https://amzn-s3-demo-bucket.s3.us-east-1.amazonaws.com/reports/q3.pdf?X-Amz-Algorithm=AWS4-HMAC-SHA256&X-Amz-Credential=AKIAIOSFODNN7EXAMPLE%2F20260812%2Fus-east-1%2Fs3%2Faws4_request&X-Amz-Signature=deadbeefcafe",
			want:    "https://amzn-s3-demo-bucket.s3.us-east-1.amazonaws.com/reports/q3.pdf",
			secrets: []string{"X-Amz-Signature", "deadbeefcafe", "AKIAIOSFODNN7EXAMPLE"},
		},
		{
			name:    "azure sas token",
			input:   "https://acct.blob.core.windows.net/c/doc.pdf?sv=2022-11-02&sig=Zm9vYmFyc2ln&se=2026-08-13T00%3A00%3A00Z",
			want:    "https://acct.blob.core.windows.net/c/doc.pdf",
			secrets: []string{"sig=", "Zm9vYmFyc2ln"},
		},
		{
			name:    "userinfo credentials",
			input:   "https://alice:hunter2@files.example.com/private/doc.pdf",
			want:    "https://files.example.com/private/doc.pdf",
			secrets: []string{"hunter2", "alice"},
		},
		{
			name:    "fragment is dropped",
			input:   "https://files.example.com/doc.pdf#token=abc123",
			want:    "https://files.example.com/doc.pdf",
			secrets: []string{"abc123"},
		},
		{
			name:    "plain url is unchanged",
			input:   "https://files.example.com/doc.pdf",
			want:    "https://files.example.com/doc.pdf",
			secrets: nil,
		},
		{
			// No host means nothing safe is identifiable, so nothing is echoed. Better a
			// useless-but-safe placeholder than a guess at which half was the secret.
			name:    "unparseable input yields a placeholder",
			input:   "://not a url?sig=leaked",
			want:    "[redacted url]",
			secrets: []string{"leaked"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := RedactURLForError(tt.input)
			if got != tt.want {
				t.Errorf("RedactURLForError(%q) = %q, want %q", tt.input, got, tt.want)
			}
			for _, secret := range tt.secrets {
				if strings.Contains(got, secret) {
					t.Errorf("redacted URL still contains %q: %s", secret, got)
				}
			}
		})
	}
}

// TestSanitizeFetchError covers the half a clean format string cannot reach. net/http
// wraps transport failures in *url.Error, whose Error() prints the request URL verbatim
// apart from the password, so wrapping the cause with %w re-leaks the signed query even
// when the caller redacted its own copy of the URL.
func TestSanitizeFetchError(t *testing.T) {
	signed := "https://amzn-s3-demo-bucket.s3.amazonaws.com/q3.pdf?X-Amz-Signature=deadbeefcafe"
	redacted := RedactURLForError(signed)

	t.Run("rewrites the URL inside a *url.Error", func(t *testing.T) {
		cause := errors.New("dial tcp 203.0.113.10:443: i/o timeout")
		sanitized := sanitizeFetchError(&url.Error{Op: "Get", URL: signed, Err: cause}, redacted)

		msg := sanitized.Error()
		if strings.Contains(msg, "deadbeefcafe") || strings.Contains(msg, "X-Amz-Signature") {
			t.Errorf("sanitized error still leaks the signature: %s", msg)
		}
		if !strings.Contains(msg, "i/o timeout") {
			t.Errorf("expected the underlying cause to survive for diagnostics, got %s", msg)
		}
		if !errors.Is(sanitized, cause) {
			t.Error("expected errors.Is to still reach the original cause")
		}
	})

	t.Run("passes through a non-url error unchanged", func(t *testing.T) {
		cause := errors.New("unexpected EOF")
		if got := sanitizeFetchError(cause, redacted); got != cause {
			t.Errorf("expected the original error to be returned, got %v", got)
		}
	})
}

// TestFetchAndEncodeURL_ErrorsAreRedacted covers the paths reachable without a dial.
// FetchAndEncodeURL routes through network.SSRFSafeDialContext, which rejects loopback
// unconditionally. The scheme and parse guards run before any dial, and the dial
// rejection itself is reachable.
func TestFetchAndEncodeURL_ErrorsAreRedacted(t *testing.T) {
	secrets := []string{"X-Amz-Signature", "deadbeefcafe", "hunter2"}

	tests := []struct {
		name string
		url  string
	}{
		{
			name: "unsupported scheme",
			url:  "ftp://alice:hunter2@files.example.com/doc.pdf?X-Amz-Signature=deadbeefcafe",
		},
		{
			name: "blocked by the SSRF dialer",
			url:  "https://alice:hunter2@127.0.0.1/doc.pdf?X-Amz-Signature=deadbeefcafe",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := FetchAndEncodeURL(t.Context(), tt.url)
			if err == nil {
				t.Fatal("expected an error")
			}
			for _, secret := range secrets {
				if strings.Contains(err.Error(), secret) {
					t.Errorf("error leaks %q: %s", secret, err.Error())
				}
			}
		})
	}
}

func TestFetchAndEncodeImageURL_AcceptsImagesBySniffedBytes(t *testing.T) {
	png := []byte("\x89PNG\r\n\x1a\npng image bytes")
	jpeg := []byte("\xff\xd8\xff\xe0jpeg image bytes")

	tests := []struct {
		name        string
		contentType string
		body        []byte
		wantType    string
	}{
		{
			name:        "content type parameters are stripped and bytes win",
			contentType: "image/jpeg; charset=binary",
			body:        png,
			wantType:    "image/png",
		},
		{
			name:        "generic content type is allowed only when bytes are image",
			contentType: "application/octet-stream",
			body:        png,
			wantType:    "image/png",
		},
		{
			name:        "missing content type is allowed when bytes are image",
			contentType: "",
			body:        jpeg,
			wantType:    "image/jpeg",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotType, gotEncoded, err := fetchAndEncodeImageURL(t.Context(), "https://cdn.example.com/photo", stubFetchResponse(tt.contentType, http.StatusOK, bytes.NewReader(tt.body)))
			if err != nil {
				t.Fatalf("fetchAndEncodeImageURL: %v", err)
			}
			if gotType != tt.wantType {
				t.Errorf("media type = %q, want %q", gotType, tt.wantType)
			}
			if gotEncoded != base64.StdEncoding.EncodeToString(tt.body) {
				t.Errorf("encoded body = %q, want fetched bytes", gotEncoded)
			}
		})
	}
}

func TestFetchAndEncodeImageURL_RejectsNonImages(t *testing.T) {
	tests := []struct {
		name        string
		contentType string
		body        []byte
		want        string
	}{
		{
			name:        "declared non-image type",
			contentType: "text/html; charset=utf-8",
			body:        []byte("\x89PNG\r\n\x1a\npng image bytes"),
			want:        `content-type "text/html"`,
		},
		{
			name:        "image header with non-image bytes",
			contentType: "image/png",
			body:        []byte("<html>not an image</html>"),
			want:        "detected as",
		},
		{
			name:        "empty body",
			contentType: "image/png",
			body:        nil,
			want:        "is empty",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := fetchAndEncodeImageURL(t.Context(), "https://cdn.example.com/photo.png", stubFetchResponse(tt.contentType, http.StatusOK, bytes.NewReader(tt.body)))
			if err == nil {
				t.Fatal("expected an error")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error = %q, want it to contain %q", err.Error(), tt.want)
			}
		})
	}
}

func TestFetchAndEncodeImageURL_RejectsBadStatusAndTooLarge(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   io.Reader
		want   string
	}{
		{
			name:   "bad status",
			status: http.StatusNotFound,
			body:   bytes.NewReader([]byte("missing")),
			want:   "non-2xx status 404",
		},
		{
			name:   "too large",
			status: http.StatusOK,
			body:   bytes.NewReader(bytes.Repeat([]byte{0}, int(fetchMaxBytes+1))),
			want:   "exceeds 26214400-byte limit",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			signed := "https://cdn.example.com/private.png?X-Amz-Signature=deadbeefcafe"
			_, _, err := fetchAndEncodeImageURL(t.Context(), signed, stubFetchResponse("image/png", tt.status, tt.body))
			if err == nil {
				t.Fatal("expected an error")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error = %q, want it to contain %q", err.Error(), tt.want)
			}
			if strings.Contains(err.Error(), "deadbeefcafe") || strings.Contains(err.Error(), "X-Amz-Signature") {
				t.Errorf("error leaks signed URL credentials: %s", err.Error())
			}
		})
	}
}

func TestFetchAndEncodeImageURL_ErrorsAreRedacted(t *testing.T) {
	secrets := []string{"X-Amz-Signature", "deadbeefcafe", "hunter2"}

	tests := []struct {
		name string
		url  string
	}{
		{
			name: "unsupported scheme",
			url:  "ftp://alice:hunter2@files.example.com/image.png?X-Amz-Signature=deadbeefcafe",
		},
		{
			name: "blocked by the SSRF dialer",
			url:  "https://alice:hunter2@127.0.0.1/image.png?X-Amz-Signature=deadbeefcafe",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := FetchAndEncodeImageURL(t.Context(), tt.url)
			if err == nil {
				t.Fatal("expected an error")
			}
			for _, secret := range secrets {
				if strings.Contains(err.Error(), secret) {
					t.Errorf("error leaks %q: %s", secret, err.Error())
				}
			}
		})
	}
}

func stubFetchResponse(contentType string, status int, body io.Reader) httpDoer {
	return func(_ *http.Client, _ *http.Request) (*http.Response, error) {
		header := http.Header{}
		if contentType != "" {
			header.Set("Content-Type", contentType)
		}
		return &http.Response{
			StatusCode: status,
			Header:     header,
			Body:       io.NopCloser(body),
		}, nil
	}
}
