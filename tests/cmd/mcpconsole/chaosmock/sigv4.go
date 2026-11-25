package chaosmock

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
)

// SigV4Auth makes a server accept only requests SigV4-signed with a credential of
// the fleet's fake AWS account (see aws.go), the way an Amazon Bedrock AgentCore
// Gateway with AWS_IAM inbound auth or an IAM-authorized API Gateway does: anything
// else is a 403 with no WWW-Authenticate challenge. It covers the MCP endpoints and
// the REST surface; the OAuth authorization server stays open. The verifier is
// written from the SigV4 specification rather than an SDK, so it checks a signer
// independently.
type SigV4Auth struct {
	Region  string `json:"region"`
	Service string `json:"service"`
	// Allow lists the identities IAM lets call this server ("pod", or
	// "assumed-role/<role>/<session>"); empty allows every identity of the account.
	Allow []string `json:"allow,omitempty"`
}

// SigV4Entry is one request the verifier saw.
type SigV4Entry struct {
	Method        string `json:"method"`
	Path          string `json:"path"`
	AccessKeyID   string `json:"access_key_id,omitempty"`
	Identity      string `json:"identity,omitempty"`
	Region        string `json:"region,omitempty"`
	Service       string `json:"service,omitempty"`
	SignedHeaders string `json:"signed_headers,omitempty"`
	SessionToken  bool   `json:"session_token"`
	// Result is "ok" or why the request was refused.
	Result string `json:"result"`
}

// SigV4State is the verifier's counters and log.
type SigV4State struct {
	Verified int64        `json:"verified"`
	Rejected int64        `json:"rejected"`
	Log      []SigV4Entry `json:"log"`
}

// sigv4State records what a server's verifier decided.
type sigv4State struct {
	mu sync.Mutex
	d  SigV4State
}

// sigv4LogCap bounds the request log, as restLogCap does the REST log; the counters keep counting.
const sigv4LogCap = restLogCap

func (st *sigv4State) record(e SigV4Entry) {
	st.mu.Lock()
	defer st.mu.Unlock()
	if e.Result == "ok" {
		st.d.Verified++
	} else {
		st.d.Rejected++
	}
	st.d.Log = append(st.d.Log, e)
	if len(st.d.Log) > sigv4LogCap {
		st.d.Log = st.d.Log[len(st.d.Log)-sigv4LogCap:]
	}
}

func (st *sigv4State) snapshot() *SigV4State {
	st.mu.Lock()
	defer st.mu.Unlock()
	d := st.d
	d.Log = append([]SigV4Entry{}, st.d.Log...)
	return &d
}

func (st *sigv4State) reset() {
	st.mu.Lock()
	st.d = SigV4State{}
	st.mu.Unlock()
}

// sigv4IdentityKey carries a verified caller from routeServer to authenticate.
type sigv4IdentityKey struct{}

// sigv4MaxSkew is how far X-Amz-Date may be from now, as AWS allows.
const sigv4MaxSkew = 5 * time.Minute

// verifySigV4 checks r against the server's SigV4 settings. It answers 403 itself
// and returns false on a refusal; on success the request carries the caller and its
// body is readable again.
func (s *Server) verifySigV4(w http.ResponseWriter, r *http.Request, cfg *SigV4Auth) (*http.Request, bool) {
	body, err := readAndRestoreBody(r)
	if errors.Is(err, errBodyTooLarge) {
		http.Error(w, err.Error(), http.StatusRequestEntityTooLarge)
		return nil, false
	}
	if err != nil {
		http.Error(w, "failed to read body", http.StatusBadRequest)
		return nil, false
	}
	entry := SigV4Entry{Method: r.Method, Path: r.URL.Path, SessionToken: r.Header.Get("X-Amz-Security-Token") != ""}
	identity, reason := checkSigV4(r, body, awsAccount.lookup, cfg.Region, cfg.Service, time.Now(), &entry)
	if reason == "" && len(cfg.Allow) > 0 && !slices.Contains(cfg.Allow, identity) {
		reason = "AccessDenied: " + identity + " is not authorized to invoke this server"
	}
	entry.Identity = identity
	if reason != "" {
		entry.Result = reason
		s.sigv4.record(entry)
		http.Error(w, reason, http.StatusForbidden)
		return nil, false
	}
	entry.Result = "ok"
	s.sigv4.record(entry)
	return r.WithContext(context.WithValue(r.Context(), sigv4IdentityKey{}, identity)), true
}

// sigv4Identity returns the caller verifySigV4 established for r, or "".
func sigv4Identity(r *http.Request) string {
	identity, _ := r.Context().Value(sigv4IdentityKey{}).(string)
	return identity
}

// maxSignedBody bounds the body a signature is checked over.
const maxSignedBody = 1 << 20

var errBodyTooLarge = fmt.Errorf("request body is larger than %d bytes", maxSignedBody)

// readAndRestoreBody reads r's body and leaves it readable from the start. A body
// over maxSignedBody is refused rather than hashed in part.
func readAndRestoreBody(r *http.Request) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxSignedBody+1))
	if err != nil {
		return nil, err
	}
	if len(body) > maxSignedBody {
		return nil, errBodyTooLarge
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	return body, nil
}

// awsKeyLookup returns a known access key's secret, identity and the session token
// issued with it ("" for a permanent key).
type awsKeyLookup func(accessKeyID string) (secret, identity, token string, ok bool)

// checkSigV4 returns the identity of the credential r was signed with, or why the
// signature is not valid for region and service. An empty region accepts any.
// It fills in what it parsed on entry.
func checkSigV4(r *http.Request, body []byte, lookup awsKeyLookup, region, service string, now time.Time, entry *SigV4Entry) (string, string) {
	rest, ok := strings.CutPrefix(r.Header.Get("Authorization"), "AWS4-HMAC-SHA256 ")
	if !ok {
		return "", "missing SigV4 Authorization header"
	}
	fields := map[string]string{}
	for _, part := range strings.Split(rest, ",") {
		k, v, _ := strings.Cut(strings.TrimSpace(part), "=")
		fields[k] = v
	}
	scope := strings.Split(fields["Credential"], "/")
	if len(scope) != 5 || scope[4] != "aws4_request" {
		return "", "malformed credential scope"
	}
	entry.AccessKeyID, entry.Region, entry.Service, entry.SignedHeaders = scope[0], scope[2], scope[3], fields["SignedHeaders"]
	secret, identity, token, known := lookup(scope[0])
	if !known {
		return "", "unknown access key " + scope[0]
	}
	if (region != "" && scope[2] != region) || scope[3] != service {
		return "", fmt.Sprintf("credential scope is for %s/%s, expected %s/%s", scope[2], scope[3], region, service)
	}
	amzDate := r.Header.Get("X-Amz-Date")
	signedAt, err := time.Parse("20060102T150405Z", amzDate)
	if err != nil {
		return "", "missing or malformed X-Amz-Date"
	}
	if d := now.Sub(signedAt); d > sigv4MaxSkew || d < -sigv4MaxSkew {
		return "", "signature expired or not yet valid"
	}
	if scope[1] != amzDate[:8] {
		return "", "credential scope date does not match X-Amz-Date"
	}
	signed := strings.Split(fields["SignedHeaders"], ";")
	if !sort.StringsAreSorted(signed) || !slices.Contains(signed, "host") || !slices.Contains(signed, "x-amz-date") {
		return "", "SignedHeaders must be sorted and include host and x-amz-date"
	}
	if r.Header.Get("X-Amz-Security-Token") != "" && !slices.Contains(signed, "x-amz-security-token") {
		return "", "x-amz-security-token is not signed"
	}
	// A temporary key is valid only with the token issued with it, and a permanent one with none.
	if got := r.Header.Get("X-Amz-Security-Token"); got != token {
		if token == "" {
			return "", "a session token was sent with a permanent access key"
		}
		return "", "the session token is missing or was not issued with access key " + scope[0]
	}

	var canonical strings.Builder
	canonical.WriteString(r.Method + "\n")
	canonical.WriteString(sigv4EscapePath(r.URL.EscapedPath()) + "\n")
	canonical.WriteString(sigv4CanonicalQuery(r.URL.RawQuery) + "\n")
	for _, name := range signed {
		var value string
		switch name {
		case "host":
			value = r.Host
		case "content-length":
			value = fmt.Sprint(r.ContentLength)
		default:
			// A copy: Values returns the request's own slice.
			values := slices.Clone(r.Header.Values(name))
			for i, v := range values {
				values[i] = strings.Join(strings.Fields(v), " ")
			}
			value = strings.Join(values, ",")
		}
		canonical.WriteString(name + ":" + value + "\n")
	}
	canonical.WriteString("\n" + fields["SignedHeaders"] + "\n")
	canonical.WriteString(hexSHA256(body))

	stringToSign := "AWS4-HMAC-SHA256\n" + amzDate + "\n" + strings.Join(scope[1:], "/") + "\n" + hexSHA256([]byte(canonical.String()))
	key := []byte("AWS4" + secret)
	for _, part := range scope[1:] {
		key = hmacSHA256(key, part)
	}
	want := hex.EncodeToString(hmacSHA256(key, stringToSign))
	if !hmac.Equal([]byte(want), []byte(fields["Signature"])) {
		return "", "signature does not match"
	}
	return identity, ""
}

// sigv4EscapePath URI-encodes an already escaped path once more, as SigV4 does for
// every service but S3.
func sigv4EscapePath(escaped string) string {
	if escaped == "" {
		return "/"
	}
	var b strings.Builder
	for i := 0; i < len(escaped); i++ {
		c := escaped[i]
		if c == '/' || isUnreserved(c) {
			b.WriteByte(c)
		} else {
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

// sigv4CanonicalQuery sorts the query by encoded name, then encoded value. Sorting
// whole "name=value" strings would put a1=2 before a=1, since '1' sorts below '='.
func sigv4CanonicalQuery(raw string) string {
	values, _ := url.ParseQuery(raw)
	type pair struct{ key, value string }
	var pairs []pair
	for k, vs := range values {
		for _, v := range vs {
			pairs = append(pairs, pair{rfc3986(k), rfc3986(v)})
		}
	}
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i].key != pairs[j].key {
			return pairs[i].key < pairs[j].key
		}
		return pairs[i].value < pairs[j].value
	})
	parts := make([]string, len(pairs))
	for i, p := range pairs {
		parts[i] = p.key + "=" + p.value
	}
	return strings.Join(parts, "&")
}

func rfc3986(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if c := s[i]; isUnreserved(c) {
			b.WriteByte(c)
		} else {
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

func isUnreserved(c byte) bool {
	return 'A' <= c && c <= 'Z' || 'a' <= c && c <= 'z' || '0' <= c && c <= '9' || c == '-' || c == '_' || c == '.' || c == '~'
}

func hexSHA256(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func hmacSHA256(key []byte, data string) []byte {
	h := hmac.New(sha256.New, key)
	h.Write([]byte(data))
	return h.Sum(nil)
}
