package awsauth

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
)

// emptyBodyHash is the SHA-256 of an empty payload.
const emptyBodyHash = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"

// unsignableHeaders must not be covered by the SigV4 signature: hop-by-hop and
// proxy-managed headers that an intermediary may rewrite in transit, which invalidates the
// signature. AWS's signing guide requires only host and x-amz-* to be signed and warns
// against "volatile transport headers that are mutated by proxies, load balancers, and the
// nodes in a distributed system". The AWS SDK itself skips only authorization, user-agent,
// x-amzn-trace-id, expect and transfer-encoding, so the rest is on us. These are removed for
// signing and restored afterwards, so what goes on the wire is unchanged.
var unsignableHeaders = map[string]struct{}{
	"connection":          {},
	"keep-alive":          {},
	"te":                  {},
	"trailer":             {},
	"upgrade":             {},
	"proxy-authorization": {},
	"proxy-authenticate":  {},
	"x-real-ip":           {},
	"x-request-id":        {},
}

// InternalHeaderPrefix marks Bifrost's own headers. They carry the caller's virtual key, so
// they are dropped outright rather than restored — they must never reach AWS.
const InternalHeaderPrefix = "x-bf-"

// restoredHeader is a header lifted off the request for signing, to be put back after.
type restoredHeader struct {
	name   string
	values []string
}

// stripHeadersForSigning removes headers that must not be signed and returns the ones to put
// back after signing. Bifrost-internal headers are dropped and never returned. A request with
// no volatile headers — the common case — allocates nothing and leaves the request untouched.
func stripHeadersForSigning(header http.Header) []restoredHeader {
	var restore []restoredHeader
	for name, values := range header {
		lower := strings.ToLower(name)
		internal := strings.HasPrefix(lower, InternalHeaderPrefix)
		_, volatile := unsignableHeaders[lower]
		if !internal && !volatile && !strings.HasPrefix(lower, "x-forwarded-") {
			continue
		}
		if !internal {
			restore = append(restore, restoredHeader{name: name, values: values})
		}
		// The name came straight off the map, so skip Del's redundant canonicalization.
		delete(header, name)
	}
	return restore
}

// HashBody returns the hex SHA-256 of req's body, leaving the body readable from the start.
// A body that can be re-read (every request built from a byte slice) is hashed as a stream and
// then rewound, so signing a large upload never holds a second copy of it.
func HashBody(req *http.Request) (string, error) {
	if req.Body == nil || req.Body == http.NoBody {
		return emptyBodyHash, nil
	}
	if req.GetBody != nil {
		h := sha256.New()
		if _, err := io.Copy(h, req.Body); err != nil {
			return "", fmt.Errorf("reading request body: %w", err)
		}
		body, err := req.GetBody()
		if err != nil {
			return "", fmt.Errorf("rewinding request body: %w", err)
		}
		req.Body = body
		return hex.EncodeToString(h.Sum(nil)), nil
	}
	bodyBytes, err := io.ReadAll(req.Body)
	if err != nil {
		return "", fmt.Errorf("reading request body: %w", err)
	}
	req.Body = io.NopCloser(bytes.NewReader(bodyBytes))
	hash := sha256.Sum256(bodyBytes)
	return hex.EncodeToString(hash[:]), nil
}

// Sign signs req with SigV4 using already resolved credentials. The SDK signs every header
// still on the request, so the volatile ones are lifted out while it signs.
func Sign(ctx context.Context, req *http.Request, creds aws.Credentials, payloadHash, service, region string) error {
	restoreHeaders := stripHeadersForSigning(req.Header)
	// S3 rejects a path escaped a second time (an object key holding %20 would be signed as
	// %2520), so the SDK's own S3 client turns the extra escaping off; a relay signing for s3 does
	// the same, and every other service keeps the default.
	signer := v4.NewSigner(func(o *v4.SignerOptions) {
		o.DisableURIPathEscaping = service == "s3"
	})
	err := signer.SignHTTP(ctx, creds, req, payloadHash, service, region, time.Now())
	for _, h := range restoreHeaders {
		req.Header[h.name] = h.values
	}
	return err
}
