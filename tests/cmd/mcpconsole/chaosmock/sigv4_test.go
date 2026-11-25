package chaosmock

import (
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// awsTestSuiteLookup knows the credential of AWS's published SigV4 test suite, a
// permanent key with no session token.
func awsTestSuiteLookup(accessKeyID string) (string, string, string, bool) {
	if accessKeyID != "AKIDEXAMPLE" {
		return "", "", "", false
	}
	return "wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY", "example", "", true
}

var awsTestSuiteTime = time.Date(2015, 8, 30, 12, 36, 0, 0, time.UTC)

const getVanilla = "5fa00fa31553b73ebf1942676e86291e8372ff2a2260956d9b8aae1d763fbf31"

func suiteRequest(method, signature string) *http.Request {
	r := httptest.NewRequest(method, "http://example.amazonaws.com/", nil)
	r.Host = "example.amazonaws.com"
	r.Header.Set("X-Amz-Date", "20150830T123600Z")
	r.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential=AKIDEXAMPLE/20150830/us-east-1/service/aws4_request, SignedHeaders=host;x-amz-date, Signature="+signature)
	return r
}

// TestCheckSigV4_AWSTestSuite: the verifier accepts the signatures of AWS's own
// get-vanilla and post-vanilla vectors, so it is a reference for Bifrost's signer
// rather than a copy of it.
func TestCheckSigV4_AWSTestSuite(t *testing.T) {
	for name, tc := range map[string]struct{ method, signature string }{
		"get-vanilla":  {http.MethodGet, getVanilla},
		"post-vanilla": {http.MethodPost, "5da7c1a2acd57cee7505fc6676e4e544621c30862966e37dddb68e92efbe5d6b"},
	} {
		t.Run(name, func(t *testing.T) {
			var entry SigV4Entry
			identity, reason := checkSigV4(suiteRequest(tc.method, tc.signature), nil, awsTestSuiteLookup, "us-east-1", "service", awsTestSuiteTime, &entry)
			if reason != "" {
				t.Fatalf("AWS's vector was refused: %s", reason)
			}
			if identity != "example" || entry.AccessKeyID != "AKIDEXAMPLE" || entry.SignedHeaders != "host;x-amz-date" {
				t.Fatalf("identity %q, entry %+v", identity, entry)
			}
		})
	}
}

// TestCheckSigV4_Refusals: everything a real AWS endpoint refuses is refused here.
func TestCheckSigV4_Refusals(t *testing.T) {
	wrongSecret := func(string) (string, string, string, bool) { return "other", "example", "", true }
	// The suite's key treated as temporary, issued with token "issued".
	temporary := func(id string) (string, string, string, bool) {
		secret, identity, _, ok := awsTestSuiteLookup(id)
		return secret, identity, "issued", ok
	}
	// withToken carries a signed session token; the signature itself is not reached.
	withToken := func(token string) *http.Request {
		r := suiteRequest(http.MethodGet, getVanilla)
		r.Header.Set("Authorization", strings.Replace(r.Header.Get("Authorization"), "SignedHeaders=host;x-amz-date", "SignedHeaders=host;x-amz-date;x-amz-security-token", 1))
		r.Header.Set("X-Amz-Security-Token", token)
		return r
	}
	for name, tc := range map[string]struct {
		request         *http.Request
		body            []byte
		lookup          awsKeyLookup
		region, service string
		now             time.Time
		want            string
	}{
		"bearer token": {func() *http.Request {
			r := suiteRequest(http.MethodGet, "")
			r.Header.Set("Authorization", "Bearer x")
			return r
		}(), nil, awsTestSuiteLookup, "us-east-1", "service", awsTestSuiteTime, "missing SigV4 Authorization header"},
		"tampered body": {suiteRequest(http.MethodGet, getVanilla), []byte("x"), awsTestSuiteLookup, "us-east-1", "service", awsTestSuiteTime, "signature does not match"},
		"wrong secret":  {suiteRequest(http.MethodGet, getVanilla), nil, wrongSecret, "us-east-1", "service", awsTestSuiteTime, "signature does not match"},
		"unknown key":   {suiteRequest(http.MethodGet, getVanilla), nil, func(string) (string, string, string, bool) { return "", "", "", false }, "us-east-1", "service", awsTestSuiteTime, "unknown access key AKIDEXAMPLE"},
		"other region":  {suiteRequest(http.MethodGet, getVanilla), nil, awsTestSuiteLookup, "us-gov-west-1", "service", awsTestSuiteTime, "credential scope is for us-east-1/service, expected us-gov-west-1/service"},
		"other service": {suiteRequest(http.MethodGet, getVanilla), nil, awsTestSuiteLookup, "us-east-1", "bedrock-agentcore", awsTestSuiteTime, "credential scope is for us-east-1/service, expected us-east-1/bedrock-agentcore"},
		"expired":       {suiteRequest(http.MethodGet, getVanilla), nil, awsTestSuiteLookup, "us-east-1", "service", awsTestSuiteTime.Add(6 * time.Minute), "signature expired or not yet valid"},
		"other method":  {suiteRequest(http.MethodDelete, getVanilla), nil, awsTestSuiteLookup, "us-east-1", "service", awsTestSuiteTime, "signature does not match"},
		"unsigned session token": {func() *http.Request {
			r := suiteRequest(http.MethodGet, getVanilla)
			r.Header.Set("X-Amz-Security-Token", "token")
			return r
		}(), nil, awsTestSuiteLookup, "us-east-1", "service", awsTestSuiteTime, "x-amz-security-token is not signed"},
		"temporary key without its token":  {suiteRequest(http.MethodGet, getVanilla), nil, temporary, "us-east-1", "service", awsTestSuiteTime, "the session token is missing or was not issued with access key AKIDEXAMPLE"},
		"temporary key with another token": {withToken("forged"), nil, temporary, "us-east-1", "service", awsTestSuiteTime, "the session token is missing or was not issued with access key AKIDEXAMPLE"},
		"permanent key with a token":       {withToken("issued"), nil, awsTestSuiteLookup, "us-east-1", "service", awsTestSuiteTime, "a session token was sent with a permanent access key"},
	} {
		t.Run(name, func(t *testing.T) {
			var entry SigV4Entry
			if _, got := checkSigV4(tc.request, tc.body, tc.lookup, tc.region, tc.service, tc.now, &entry); got != tc.want {
				t.Fatalf("reason = %q, want %q", got, tc.want)
			}
		})
	}
}

// testSign signs r as the fake account's accessKeyID, with the session token
// issued with it, using the verifier's own primitives, which
// TestCheckSigV4_AWSTestSuite anchors to AWS's vectors.
func testSign(r *http.Request, body []byte, accessKeyID, region, service string) {
	now := time.Now().UTC()
	amzDate := now.Format("20060102T150405Z")
	token := awsTokenFor(accessKeyID)
	r.Header.Set("X-Amz-Date", amzDate)
	r.Header.Set("X-Amz-Security-Token", token)
	scope := []string{accessKeyID, amzDate[:8], region, service, "aws4_request"}
	canonical := r.Method + "\n" + sigv4EscapePath(r.URL.EscapedPath()) + "\n" + sigv4CanonicalQuery(r.URL.RawQuery) + "\n" +
		"host:" + r.Host + "\nx-amz-date:" + amzDate + "\nx-amz-security-token:" + token + "\n\nhost;x-amz-date;x-amz-security-token\n" + hexSHA256(body)
	stringToSign := "AWS4-HMAC-SHA256\n" + amzDate + "\n" + strings.Join(scope[1:], "/") + "\n" + hexSHA256([]byte(canonical))
	key := []byte("AWS4" + awsSecretFor(accessKeyID))
	for _, part := range scope[1:] {
		key = hmacSHA256(key, part)
	}
	r.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential="+strings.Join(scope, "/")+", SignedHeaders=host;x-amz-date;x-amz-security-token, Signature="+hex.EncodeToString(hmacSHA256(key, stringToSign)))
}

// TestFleet_SigV4ProtectsMCPAndREST: with SigV4 on, unsigned MCP and REST requests
// are refused with 403 and no challenge; a signed one from an allowed identity is
// served, and one IAM does not allow is refused.
func TestFleet_SigV4ProtectsMCPAndREST(t *testing.T) {
	f := NewFleet()
	ts := httptest.NewServer(f.Handler())
	t.Cleanup(ts.Close)
	s := f.Add("gateway")
	s.auth.set(Auth{SigV4: &SigV4Auth{Region: "us-east-1", Service: "bedrock-agentcore", Allow: []string{AWSPodIdentity}}})

	ping := `{"jsonrpc":"2.0","id":1,"method":"ping"}`
	for _, path := range []string{"/s/gateway/mcp", "/s/gateway/rest/pets"} {
		resp, err := http.Post(ts.URL+path, "application/json", strings.NewReader(ping))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden || resp.Header.Get("WWW-Authenticate") != "" {
			t.Fatalf("%s: status %d, challenge %q; want 403 with none", path, resp.StatusCode, resp.Header.Get("WWW-Authenticate"))
		}
	}

	req := httptest.NewRequest(http.MethodGet, ts.URL+"/s/gateway/rest/pets", nil)
	req.RequestURI = ""
	testSign(req, nil, AWSPodAccessKeyID, "us-east-1", "bedrock-agentcore")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var echo map[string]string
	_ = json.NewDecoder(resp.Body).Decode(&echo)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || echo["method"] != http.MethodGet || echo["path"] != "/pets" {
		t.Fatalf("a request the pod signed must reach the REST surface: status %d, echo %v", resp.StatusCode, echo)
	}

	s.auth.set(Auth{SigV4: &SigV4Auth{Region: "us-east-1", Service: "bedrock-agentcore", Allow: []string{"assumed-role/gateway/bifrost"}}})
	req = httptest.NewRequest(http.MethodGet, ts.URL+"/s/gateway/rest/pets", nil)
	req.RequestURI = ""
	testSign(req, nil, AWSPodAccessKeyID, "us-east-1", "bedrock-agentcore")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("an identity IAM does not allow must be refused, got %d", resp.StatusCode)
	}

	state := s.State()
	if state.SigV4 == nil || state.SigV4.Verified != 1 || state.SigV4.Rejected != 3 {
		t.Fatalf("sigv4 state = %+v", state.SigV4)
	}
	if got := state.SigV4.Log[3].Result; !strings.HasPrefix(got, "AccessDenied: pod is not authorized") {
		t.Fatalf("log result = %q", got)
	}
	raw, _ := json.Marshal(state)
	if !strings.Contains(string(raw), `"sigv4":{"verified":1,"rejected":3`) {
		t.Fatalf("state JSON lacks the sigv4 block: %s", raw)
	}
}

// TestFakeAWS_PodIdentityAndSTS: the credentials endpoint needs the container
// token; STS assumes a role only for a request the pod signed, applies the trust
// policies, and issues credentials the SigV4 servers then accept.
func TestFakeAWS_PodIdentityAndSTS(t *testing.T) {
	ts := httptest.NewServer(NewFleet().Handler())
	t.Cleanup(ts.Close)
	post := func(r *http.Request) *http.Response {
		r.RequestURI = ""
		resp, err := http.DefaultClient.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { resp.Body.Close() })
		return resp
	}

	unauth := httptest.NewRequest(http.MethodGet, ts.URL+"/aws/credentials", nil)
	if resp := post(unauth); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("credentials without the token: %d", resp.StatusCode)
	}
	authed := httptest.NewRequest(http.MethodGet, ts.URL+"/aws/credentials", nil)
	authed.Header.Set("Authorization", AWSContainerToken)
	var creds map[string]string
	if err := json.NewDecoder(post(authed).Body).Decode(&creds); err != nil || creds["AccessKeyId"] != AWSPodAccessKeyID {
		t.Fatalf("credentials = %v, %v", creds, err)
	}

	assume := func(role, externalID string, sign bool) *http.Response {
		form := url.Values{"Action": {"AssumeRole"}, "Version": {"2011-06-15"}, "RoleArn": {role}, "RoleSessionName": {"bifrost"}}
		if externalID != "" {
			form.Set("ExternalId", externalID)
		}
		body := form.Encode()
		r := httptest.NewRequest(http.MethodPost, ts.URL+"/aws/sts", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if sign {
			testSign(r, []byte(body), AWSPodAccessKeyID, "us-east-1", "sts")
		}
		return post(r)
	}
	for name, tc := range map[string]struct {
		role, externalID string
		sign             bool
		status           int
	}{
		"plain role":                  {"arn:aws:iam::123456789012:role/gateway", "", true, http.StatusOK},
		"external id given":           {"arn:aws:iam::123456789012:role/external-gateway", AWSExternalID, true, http.StatusOK},
		"external id missing":         {"arn:aws:iam::123456789012:role/external-gateway", "", true, http.StatusForbidden},
		"role that cannot be assumed": {"arn:aws:iam::123456789012:role/denied-gateway", "", true, http.StatusForbidden},
		"unsigned":                    {"arn:aws:iam::123456789012:role/gateway", "", false, http.StatusForbidden},
	} {
		if got := assume(tc.role, tc.externalID, tc.sign).StatusCode; got != tc.status {
			t.Errorf("%s: status %d, want %d", name, got, tc.status)
		}
	}

	var resp struct {
		AccessKeyID string `xml:"AssumeRoleResult>Credentials>AccessKeyId"`
	}
	if err := xml.NewDecoder(assume("arn:aws:iam::123456789012:role/gateway", "", true).Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	secret, identity, _, ok := awsAccount.lookup(resp.AccessKeyID)
	if !ok || identity != "assumed-role/gateway/bifrost" || secret != awsSecretFor(resp.AccessKeyID) {
		t.Fatalf("issued key %s: identity %q, known %v", resp.AccessKeyID, identity, ok)
	}
}

// TestSigV4CanonicalQuery_SortsByKeyThenValue: pairs sort by encoded name, then
// value, so a name that prefixes a longer one comes first.
func TestSigV4CanonicalQuery_SortsByKeyThenValue(t *testing.T) {
	for raw, want := range map[string]string{
		"a1=2&a=1":          "a=1&a1=2",
		"limit-x=2&limit=1": "limit=1&limit-x=2",
		"b=2&b=1&a=3":       "a=3&b=1&b=2",
		"q=a b&p=%2F":       "p=%2F&q=a%20b",
	} {
		if got := sigv4CanonicalQuery(raw); got != want {
			t.Errorf("sigv4CanonicalQuery(%q) = %q, want %q", raw, got, want)
		}
	}
}

// TestFleet_SigV4VerifierEdges: a body over the limit is refused as too large
// rather than as a bad signature, the verifier leaves the request's headers as
// sent, the log keeps only recent entries while the counters keep counting, and
// a request SigV4 was turned on for after routing is refused.
func TestFleet_SigV4VerifierEdges(t *testing.T) {
	f := NewFleet()
	ts := httptest.NewServer(f.Handler())
	t.Cleanup(ts.Close)
	s := f.Add("gateway")
	s.auth.set(Auth{SigV4: &SigV4Auth{Region: "us-east-1", Service: "bedrock-agentcore"}})

	big := strings.Repeat("x", maxSignedBody+1)
	req := httptest.NewRequest(http.MethodPost, ts.URL+"/s/gateway/rest/pets", strings.NewReader(big))
	req.RequestURI = ""
	testSign(req, []byte(big), AWSPodAccessKeyID, "us-east-1", "bedrock-agentcore")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("an oversized body: status %d, want 413", resp.StatusCode)
	}

	signed := httptest.NewRequest(http.MethodGet, ts.URL+"/s/gateway/rest/pets", nil)
	signed.Host = "example.com"
	signed.Header.Set("X-Custom", "a  b")
	testSign(signed, nil, AWSPodAccessKeyID, "us-east-1", "bedrock-agentcore")
	signed.Header.Set("Authorization", strings.Replace(signed.Header.Get("Authorization"), "SignedHeaders=host;x-amz-date;x-amz-security-token,", "SignedHeaders=host;x-amz-date;x-amz-security-token;x-custom,", 1))
	var entry SigV4Entry
	if _, reason := checkSigV4(signed, nil, awsAccount.lookup, "us-east-1", "bedrock-agentcore", time.Now(), &entry); reason != "signature does not match" {
		t.Fatalf("a header added after signing: reason %q, want a signature mismatch", reason)
	}
	if got := signed.Header.Get("X-Custom"); got != "a  b" {
		t.Fatalf("the verifier rewrote a signed header to %q", got)
	}

	for range sigv4LogCap + 5 {
		s.sigv4.record(SigV4Entry{Result: "ok"})
	}
	if state := s.sigv4.snapshot(); len(state.Log) != sigv4LogCap || state.Verified != int64(sigv4LogCap+5) {
		t.Fatalf("log %d entries, verified %d", len(state.Log), state.Verified)
	}

	rec := httptest.NewRecorder()
	if _, ok := s.authenticate(rec, httptest.NewRequest(http.MethodPost, "/s/gateway/mcp", nil)); ok || rec.Code != http.StatusUnauthorized {
		t.Fatalf("a request with no verified identity: ok %v, status %d", ok, rec.Code)
	}
}
