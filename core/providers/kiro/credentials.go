package kiro

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/bytedance/sonic"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

const (
	// defaultKiroRegion is the SSO region of every social login and of the Builder ID flow.
	defaultKiroRegion = "us-east-1"

	// builderIDServiceProfileArn is the request-scoped profile the CLI sends for AWS Builder ID
	// accounts, which have no profile of their own. It is never persisted and never used for
	// region inference.
	builderIDServiceProfileArn = "arn:aws:codewhisperer:us-east-1:638616132270:profile/AAAACCCCXXXX"

	// defaultAccessTokenLifetime applies when a token response omits expiresIn.
	defaultAccessTokenLifetime = time.Hour

	// maxAuthBodyBytes caps every auth-service response.
	maxAuthBodyBytes = 64 * 1024
	// refreshTimeout bounds one refresh round trip; callers queued behind the refresh wait at
	// most this long.
	refreshTimeout = 30 * time.Second
	// deviceTimeout bounds one device-login round trip.
	deviceTimeout = 20 * time.Second

	authMethodSocial    = "social"
	authMethodBuilderID = "builder-id"

	builderIDStartURL = "https://view.awsapps.com/start"
	kiroCLIClientID   = "kiro-cli"
)

// Auth hosts. Variables rather than constants so tests can point them at a local server; the
// region argument is always validated against regionPattern first, so it can never smuggle a
// different host into the URL.
var (
	kiroSocialAuthBaseURL = func(region string) string { return "https://prod." + region + ".auth.desktop.kiro.dev" }
	kiroOIDCBaseURL       = func(region string) string { return "https://oidc." + region + ".amazonaws.com" }
)

var (
	regionPattern     = regexp.MustCompile(`^[a-z]{2}(?:-[a-z]+)+-\d$`)
	profileArnPattern = regexp.MustCompile(`^arn:[a-z0-9-]+:codewhisperer:[a-z0-9-]+:\d{12}:profile/[A-Za-z0-9-]+$`)
	userCodePattern   = regexp.MustCompile(`^[A-Za-z0-9-]{4,32}$`)
)

// terminalOAuthErrors are the refresh error codes that mean the refresh token is dead and the
// account has to log in again.
var terminalOAuthErrors = map[string]bool{
	"invalid_grant":         true,
	"refresh_token_reused":  true,
	"revoked":               true,
	"revoked_token":         true,
	"refresh_token_revoked": true,
	"access_denied":         true,
	"expired_token":         true,
}

// Credentials is the Kiro credential stored as the key value. It is compatible with Kiro IDE's
// ~/.aws/sso/cache/kiro-auth-token.json. Region is the SSO region used for refresh; APIRegion is
// the runtime region. ClientID and ClientSecret are present for AWS SSO OIDC accounts (Builder
// ID and IAM Identity Center) and select the OIDC refresh flow.
type Credentials struct {
	AccessToken  string `json:"accessToken,omitempty"`
	RefreshToken string `json:"refreshToken"`
	ExpiresAt    string `json:"expiresAt,omitempty"`
	ProfileArn   string `json:"profileArn,omitempty"`
	Region       string `json:"region,omitempty"`
	APIRegion    string `json:"apiRegion,omitempty"`
	AuthMethod   string `json:"authMethod,omitempty"`
	ClientID     string `json:"clientId,omitempty"`
	ClientSecret string `json:"clientSecret,omitempty"`
}

// ParseCredentials parses a key value: either a bare Kiro social refresh token or a
// kiro-auth-token.json-compatible JSON object. snake_case aliases are accepted, and expiresAt may
// be an ISO-8601 string or an epoch in seconds or milliseconds. Regions and the profile ARN are
// validated because they are interpolated into request hosts and headers.
func ParseCredentials(value string) (*Credentials, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return nil, errors.New("kiro credential is empty")
	}
	if !strings.HasPrefix(trimmed, "{") {
		if strings.ContainsFunc(trimmed, isSpaceOrControl) {
			return nil, errors.New("kiro credential is neither a JSON object nor a bare refresh token")
		}
		return &Credentials{RefreshToken: trimmed}, nil
	}

	var raw map[string]any
	if err := sonic.UnmarshalString(trimmed, &raw); err != nil {
		// The parser's message quotes the input around the error, and the input is a secret.
		return nil, errors.New("kiro credential is not valid JSON")
	}
	creds := &Credentials{
		AccessToken:  firstString(raw, "accessToken", "access_token"),
		RefreshToken: firstString(raw, "refreshToken", "refresh_token"),
		ProfileArn:   firstString(raw, "profileArn", "profile_arn"),
		Region:       firstString(raw, "region", "ssoRegion", "sso_region"),
		APIRegion:    firstString(raw, "apiRegion", "api_region"),
		AuthMethod:   firstString(raw, "authMethod", "auth_method"),
		ClientID:     firstString(raw, "clientId", "client_id"),
		ClientSecret: firstString(raw, "clientSecret", "client_secret"),
	}
	if creds.RefreshToken == "" {
		return nil, errors.New("kiro credential is missing refreshToken")
	}
	if strings.ContainsFunc(creds.RefreshToken, isSpaceOrControl) || strings.ContainsFunc(creds.AccessToken, isSpaceOrControl) {
		return nil, errors.New("kiro credential tokens must not contain whitespace or control characters")
	}
	if creds.Region != "" && !regionPattern.MatchString(creds.Region) {
		return nil, fmt.Errorf("kiro credential region %q is not a valid AWS region", creds.Region)
	}
	if creds.APIRegion != "" && !regionPattern.MatchString(creds.APIRegion) {
		return nil, fmt.Errorf("kiro credential apiRegion %q is not a valid AWS region", creds.APIRegion)
	}
	if creds.ProfileArn != "" && (len(creds.ProfileArn) > 256 || !profileArnPattern.MatchString(creds.ProfileArn)) {
		return nil, errors.New("kiro credential profileArn is not a CodeWhisperer profile ARN")
	}
	if (creds.ClientID == "") != (creds.ClientSecret == "") {
		return nil, errors.New("kiro credential must carry both clientId and clientSecret, or neither")
	}
	if len(creds.ClientID) > 4096 || len(creds.ClientSecret) > 4096 ||
		strings.ContainsFunc(creds.ClientID, isControl) || strings.ContainsFunc(creds.ClientSecret, isControl) {
		return nil, errors.New("kiro credential clientId/clientSecret are malformed")
	}
	if expiresAt, present := raw["expiresAt"]; present {
		creds.ExpiresAt = normalizeExpiresAt(expiresAt)
	} else if expiresAt, present := raw["expires_at"]; present {
		creds.ExpiresAt = normalizeExpiresAt(expiresAt)
	}
	return creds, nil
}

// Encode serializes the credential as compact JSON, the canonical stored form.
func (c *Credentials) Encode() (string, error) {
	if c == nil || c.RefreshToken == "" {
		return "", errors.New("kiro credential has no refresh token")
	}
	encoded, err := sonic.Marshal(c)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

// usesOIDC reports whether the credential refreshes through AWS SSO OIDC.
func (c *Credentials) usesOIDC() bool {
	return c.ClientID != "" && c.ClientSecret != ""
}

// ssoRegion is the region used for token refresh.
func (c *Credentials) ssoRegion() string {
	if c.Region != "" {
		return c.Region
	}
	return defaultKiroRegion
}

// apiRegion is the runtime region: apiRegion, else the region of the profile ARN, else the SSO
// region.
func (c *Credentials) apiRegion() string {
	if c.APIRegion != "" {
		return c.APIRegion
	}
	if region := regionFromProfileArn(c.ProfileArn); region != "" {
		return region
	}
	return c.ssoRegion()
}

// requestProfile returns the profile ARN to send and whether it is the Builder ID fallback.
func (c *Credentials) requestProfile() (arn string, builderIDFallback bool) {
	if c.ProfileArn != "" {
		return c.ProfileArn, false
	}
	if c.usesOIDC() {
		return builderIDServiceProfileArn, true
	}
	return "", false
}

// accessTokenExpiry returns the stored access token expiry, or the zero time when unknown.
func (c *Credentials) accessTokenExpiry() time.Time {
	if c.ExpiresAt == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339Nano, c.ExpiresAt)
	if err != nil {
		return time.Time{}
	}
	return t
}

func regionFromProfileArn(arn string) string {
	parts := strings.Split(arn, ":")
	if len(parts) < 4 || !regionPattern.MatchString(parts[3]) {
		return ""
	}
	return parts[3]
}

// normalizeExpiresAt converts an ISO string or an epoch (seconds below 1e10, else milliseconds)
// into RFC3339. An unparseable value yields "", which treats the access token as expired so the
// first request refreshes.
func normalizeExpiresAt(value any) string {
	var epoch float64
	switch v := value.(type) {
	case float64:
		epoch = v
	case int64:
		epoch = float64(v)
	case string:
		s := strings.TrimSpace(v)
		if s == "" {
			return ""
		}
		if n, err := strconv.ParseFloat(s, 64); err == nil {
			epoch = n
			break
		}
		for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05.999999999", "2006-01-02 15:04:05Z07:00"} {
			if t, err := time.Parse(layout, s); err == nil {
				return t.UTC().Format(time.RFC3339Nano)
			}
		}
		return ""
	default:
		return ""
	}
	if math.IsNaN(epoch) || math.IsInf(epoch, 0) || epoch <= 0 {
		return ""
	}
	var t time.Time
	if epoch < 1e10 {
		t = time.Unix(0, int64(epoch*float64(time.Second)))
	} else {
		t = time.UnixMilli(int64(epoch))
	}
	return t.UTC().Format(time.RFC3339Nano)
}

func firstString(raw map[string]any, keys ...string) string {
	for _, key := range keys {
		if s, ok := raw[key].(string); ok {
			if trimmed := strings.TrimSpace(s); trimmed != "" {
				return trimmed
			}
		}
	}
	return ""
}

func isControl(r rune) bool {
	return r < 0x20 || (r >= 0x7f && r <= 0x9f)
}

func isSpaceOrControl(r rune) bool {
	return r == ' ' || isControl(r)
}

// tokenGrant is a successful token response from either auth service.
type tokenGrant struct {
	accessToken  string
	refreshToken string
	profileArn   string
	expiresAt    time.Time
}

// tokenResponse is the union of the social and OIDC token response shapes.
type tokenResponse struct {
	AccessToken       string  `json:"accessToken"`
	AccessTokenSnake  string  `json:"access_token"`
	RefreshToken      string  `json:"refreshToken"`
	RefreshTokenSnake string  `json:"refresh_token"`
	ExpiresIn         float64 `json:"expiresIn"`
	ExpiresInSnake    float64 `json:"expires_in"`
	ProfileArn        string  `json:"profileArn"`
	Error             string  `json:"error"`
	Status            string  `json:"status"`
}

func (r *tokenResponse) access() string {
	if r.AccessToken != "" {
		return r.AccessToken
	}
	return r.AccessTokenSnake
}

func (r *tokenResponse) refresh() string {
	if r.RefreshToken != "" {
		return r.RefreshToken
	}
	return r.RefreshTokenSnake
}

func (r *tokenResponse) expiresIn() float64 {
	if r.ExpiresIn > 0 {
		return r.ExpiresIn
	}
	return r.ExpiresInSnake
}

// refreshAccessToken exchanges the credential's refresh token: AWS SSO OIDC CreateToken when the
// credential carries a client registration, the Kiro desktop auth service otherwise. The rotated
// refresh token (if any) and a profile ARN learned from a social refresh are returned so the
// caller can persist them.
func refreshAccessToken(ctx context.Context, client *fasthttp.Client, creds *Credentials) (*tokenGrant, *schemas.BifrostError) {
	region := creds.ssoRegion()
	var url string
	var body map[string]string
	if creds.usesOIDC() {
		url = kiroOIDCBaseURL(region) + "/token"
		body = map[string]string{
			"grantType":    "refresh_token",
			"clientId":     creds.ClientID,
			"clientSecret": creds.ClientSecret,
			"refreshToken": creds.RefreshToken,
		}
	} else {
		url = kiroSocialAuthBaseURL(region) + "/refreshToken"
		body = map[string]string{"refreshToken": creds.RefreshToken}
	}

	reply, err := postJSON(ctx, client, url, body, refreshTimeout)
	if err != nil {
		return nil, providerUtils.NewBifrostUpstreamConnectionError("kiro: could not reach the Kiro auth service to refresh the access token", err)
	}
	if reply.status != http.StatusOK {
		bErr := classifyRefreshFailure(reply.status, reply.body)
		providerUtils.ApplyRetryAfter(bErr, reply.header)
		return nil, bErr
	}

	var parsed tokenResponse
	if err := sonic.Unmarshal(reply.body, &parsed); err != nil || parsed.access() == "" {
		return nil, newKiroError(http.StatusBadGateway, "server_error", "upstream_server_error",
			"kiro: the auth service returned an unreadable token response")
	}
	grant := &tokenGrant{
		accessToken:  parsed.access(),
		refreshToken: parsed.refresh(),
		expiresAt:    time.Now().Add(lifetimeFrom(parsed.expiresIn(), defaultAccessTokenLifetime)),
	}
	if grant.refreshToken == "" {
		grant.refreshToken = creds.RefreshToken
	}
	if !creds.usesOIDC() && parsed.ProfileArn != "" && len(parsed.ProfileArn) <= 256 && profileArnPattern.MatchString(parsed.ProfileArn) {
		grant.profileArn = parsed.ProfileArn
	}
	return grant, nil
}

// classifyRefreshFailure maps a failed refresh to a BifrostError. A recognised OAuth error on a
// 400/401 means the refresh token is dead; any other 4xx also leaves the key unusable. Both are
// reported as 401 so key rotation treats the key as a dead credential. 429 and 5xx stay retryable.
func classifyRefreshFailure(status int, body []byte) *schemas.BifrostError {
	var parsed struct {
		Error            string `json:"error"`
		ErrorDescription string `json:"error_description"`
		Message          string `json:"message"`
	}
	_ = sonic.Unmarshal(body, &parsed)
	oauthError := strings.TrimSpace(parsed.Error)
	switch {
	case (status == http.StatusBadRequest || status == http.StatusUnauthorized) && terminalOAuthErrors[oauthError]:
		bErr := newKiroError(http.StatusUnauthorized, "authentication_error", "invalid_api_key",
			fmt.Sprintf("kiro: the refresh token was rejected (%s); log in to Kiro again and update this key", oauthError))
		return bErr
	case status == http.StatusTooManyRequests:
		return newKiroError(http.StatusTooManyRequests, "rate_limit_error", "rate_limit_exceeded",
			"kiro: the auth service rate limited the token refresh")
	case status >= 500:
		return newKiroError(http.StatusBadGateway, "server_error", "upstream_server_error",
			fmt.Sprintf("kiro: the auth service failed the token refresh (HTTP %d)", status))
	default:
		detail := oauthError
		if detail == "" {
			detail = strings.TrimSpace(parsed.Message)
		}
		if detail == "" {
			detail = fmt.Sprintf("HTTP %d", status)
		}
		return newKiroError(http.StatusUnauthorized, "authentication_error", "invalid_api_key",
			"kiro: the token refresh failed: "+truncateText(detail, 200))
	}
}

// isTerminalRefreshError reports whether a refresh failure means the credential is dead rather
// than temporarily unrefreshable.
func isTerminalRefreshError(bErr *schemas.BifrostError) bool {
	return bErr != nil && bErr.StatusCode != nil && *bErr.StatusCode == http.StatusUnauthorized
}

func lifetimeFrom(seconds float64, fallback time.Duration) time.Duration {
	if seconds <= 0 || math.IsNaN(seconds) || math.IsInf(seconds, 0) {
		return fallback
	}
	// Bound absurd values so a hostile response cannot pin a token forever.
	return time.Duration(min(seconds, 7*24*3600) * float64(time.Second))
}

// authReply is one auth-service response, detached from fasthttp's pooled objects.
type authReply struct {
	status int
	body   []byte
	header *fasthttp.ResponseHeader
}

// postJSON performs one bounded JSON POST to an auth endpoint.
func postJSON(ctx context.Context, client *fasthttp.Client, url string, body any, timeout time.Duration) (*authReply, error) {
	payload, err := sonic.Marshal(body)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	req.SetRequestURI(url)
	req.Header.SetMethod(http.MethodPost)
	req.Header.SetContentType("application/json")
	req.Header.Set("Accept", "application/json")
	req.SetBody(payload)

	deadline := time.Now().Add(timeout)
	if ctxDeadline, ok := ctx.Deadline(); ok && ctxDeadline.Before(deadline) {
		deadline = ctxDeadline
	}
	if err := client.DoDeadline(req, resp, deadline); err != nil {
		return nil, err
	}
	respBody, err := providerUtils.CheckAndDecodeBody(resp)
	if err != nil {
		return nil, err
	}
	reply := &authReply{
		status: resp.StatusCode(),
		body:   append([]byte(nil), respBody...),
		header: &fasthttp.ResponseHeader{},
	}
	resp.Header.CopyTo(reply.header)
	return reply, nil
}

// deviceHTTPClient serves device-login calls, which run outside any provider instance. Every
// host it talks to is a fixed AWS or Kiro endpoint.
var deviceHTTPClient = &fasthttp.Client{
	ReadTimeout:         deviceTimeout,
	WriteTimeout:        deviceTimeout,
	MaxConnWaitTimeout:  deviceTimeout,
	MaxResponseBodySize: maxAuthBodyBytes,
	MaxIdleConnDuration: 30 * time.Second,
}

// DeviceAuthorization is a started device login. Method is "builder-id", "google" or "github".
// ClientID/ClientSecret are set for builder-id (the SSO OIDC client registration). Interval is
// the minimum poll interval; callers add 5s to it whenever PollDeviceLogin reports "slow_down".
type DeviceAuthorization struct {
	Method                  string
	DeviceCode              string
	UserCode                string
	VerificationURI         string
	VerificationURIComplete string
	ClientID                string
	ClientSecret            string
	ExpiresAt               time.Time
	Interval                time.Duration
}

// deviceAuthorizationResponse is the union of the OIDC (seconds) and Kiro (milliseconds) device
// authorization response shapes.
type deviceAuthorizationResponse struct {
	DeviceCode              string  `json:"deviceCode"`
	UserCode                string  `json:"userCode"`
	VerificationURI         string  `json:"verificationUri"`
	VerificationURIComplete string  `json:"verificationUriComplete"`
	ExpiresIn               float64 `json:"expiresIn"`
	Interval                float64 `json:"interval"`
	ExpiresInMilliseconds   float64 `json:"expiresInMilliseconds"`
	IntervalInMilliseconds  float64 `json:"intervalInMilliseconds"`
}

// StartDeviceLogin starts a Kiro device login. "builder-id" registers a public SSO OIDC client
// and starts an AWS Builder ID device authorization; "google" and "github" start a Kiro auth
// service device authorization for that identity provider. All flows run in us-east-1.
func StartDeviceLogin(ctx context.Context, method string) (*DeviceAuthorization, error) {
	switch method {
	case authMethodBuilderID:
		return startBuilderIDDeviceLogin(ctx)
	case "google", "github":
		return startSocialDeviceLogin(ctx, method)
	default:
		return nil, fmt.Errorf("kiro: unsupported device login method %q (want builder-id, google or github)", method)
	}
}

func startBuilderIDDeviceLogin(ctx context.Context) (*DeviceAuthorization, error) {
	oidc := kiroOIDCBaseURL(defaultKiroRegion)
	reply, err := postJSON(ctx, deviceHTTPClient, oidc+"/client/register", map[string]any{
		"clientName": kiroCLIClientID,
		"clientType": "public",
		"scopes":     []string{"codewhisperer:completions", "codewhisperer:analysis", "codewhisperer:conversations"},
	}, deviceTimeout)
	if err != nil {
		return nil, fmt.Errorf("kiro: client registration failed: %w", err)
	}
	var registration struct {
		ClientID     string `json:"clientId"`
		ClientSecret string `json:"clientSecret"`
	}
	if reply.status < 200 || reply.status >= 300 || sonic.Unmarshal(reply.body, &registration) != nil ||
		!isStorableClientPart(registration.ClientID) || !isStorableClientPart(registration.ClientSecret) {
		return nil, fmt.Errorf("kiro: client registration failed (HTTP %d)", reply.status)
	}

	reply, err = postJSON(ctx, deviceHTTPClient, oidc+"/device_authorization", map[string]string{
		"clientId":     registration.ClientID,
		"clientSecret": registration.ClientSecret,
		"startUrl":     builderIDStartURL,
	}, deviceTimeout)
	if err != nil {
		return nil, fmt.Errorf("kiro: device authorization failed: %w", err)
	}
	if reply.status < 200 || reply.status >= 300 {
		return nil, fmt.Errorf("kiro: device authorization failed (HTTP %d)", reply.status)
	}
	var parsed deviceAuthorizationResponse
	if err := sonic.Unmarshal(reply.body, &parsed); err != nil {
		return nil, errors.New("kiro: device authorization returned an unreadable response")
	}
	auth, err := deviceAuthorizationFrom(authMethodBuilderID, &parsed,
		lifetimeFrom(parsed.ExpiresIn, 600*time.Second), lifetimeFrom(parsed.Interval, 5*time.Second))
	if err != nil {
		return nil, err
	}
	auth.ClientID = registration.ClientID
	auth.ClientSecret = registration.ClientSecret
	return auth, nil
}

func startSocialDeviceLogin(ctx context.Context, method string) (*DeviceAuthorization, error) {
	loginProvider := "Google"
	if method == "github" {
		loginProvider = "Github"
	}
	reply, err := postJSON(ctx, deviceHTTPClient, kiroSocialAuthBaseURL(defaultKiroRegion)+"/oauth/device/authorization", map[string]string{
		"clientId":      kiroCLIClientID,
		"loginProvider": loginProvider,
	}, deviceTimeout)
	if err != nil {
		return nil, fmt.Errorf("kiro: device authorization failed: %w", err)
	}
	if reply.status < 200 || reply.status >= 300 {
		return nil, fmt.Errorf("kiro: device authorization failed (HTTP %d)", reply.status)
	}
	var parsed deviceAuthorizationResponse
	if err := sonic.Unmarshal(reply.body, &parsed); err != nil {
		return nil, errors.New("kiro: device authorization returned an unreadable response")
	}
	return deviceAuthorizationFrom(method, &parsed,
		lifetimeFrom(parsed.ExpiresInMilliseconds/1000, 300*time.Second),
		lifetimeFrom(parsed.IntervalInMilliseconds/1000, 5*time.Second))
}

func deviceAuthorizationFrom(method string, parsed *deviceAuthorizationResponse, lifetime, interval time.Duration) (*DeviceAuthorization, error) {
	if parsed.DeviceCode == "" || !userCodePattern.MatchString(parsed.UserCode) ||
		!isSafeVerificationURI(parsed.VerificationURI) ||
		(parsed.VerificationURIComplete != "" && !isSafeVerificationURI(parsed.VerificationURIComplete)) {
		return nil, errors.New("kiro: device authorization returned an invalid response")
	}
	if interval < time.Second {
		interval = time.Second
	}
	return &DeviceAuthorization{
		Method:                  method,
		DeviceCode:              parsed.DeviceCode,
		UserCode:                parsed.UserCode,
		VerificationURI:         parsed.VerificationURI,
		VerificationURIComplete: parsed.VerificationURIComplete,
		ExpiresAt:               time.Now().Add(lifetime),
		Interval:                interval,
	}, nil
}

// PollDeviceLogin polls a started device login once. status is "pending", "slow_down" (the caller
// must add 5s to Interval), "expired", or "complete" with creds set. err is non-nil only for a
// failed login, in which case status is empty.
func PollDeviceLogin(ctx context.Context, auth *DeviceAuthorization) (*Credentials, string, error) {
	if auth == nil || auth.DeviceCode == "" {
		return nil, "", errors.New("kiro: device login is not started")
	}
	if !auth.ExpiresAt.IsZero() && time.Now().After(auth.ExpiresAt) {
		return nil, "expired", nil
	}
	if auth.Method == authMethodBuilderID {
		return pollBuilderIDDeviceLogin(ctx, auth)
	}
	return pollSocialDeviceLogin(ctx, auth)
}

func pollBuilderIDDeviceLogin(ctx context.Context, auth *DeviceAuthorization) (*Credentials, string, error) {
	if !isStorableClientPart(auth.ClientID) || !isStorableClientPart(auth.ClientSecret) {
		return nil, "", errors.New("kiro: builder-id device login has no client registration")
	}
	reply, err := postJSON(ctx, deviceHTTPClient, kiroOIDCBaseURL(defaultKiroRegion)+"/token", map[string]string{
		"clientId":     auth.ClientID,
		"clientSecret": auth.ClientSecret,
		"deviceCode":   auth.DeviceCode,
		"grantType":    "urn:ietf:params:oauth:grant-type:device_code",
	}, deviceTimeout)
	if err != nil {
		return nil, "", fmt.Errorf("kiro: device token poll failed: %w", err)
	}
	var parsed tokenResponse
	_ = sonic.Unmarshal(reply.body, &parsed)
	oauthError := parsed.Error
	if oauthError == "" {
		errorType, _, _ := strings.Cut(string(reply.header.Peek("x-amzn-errortype")), ":")
		oauthError = strings.TrimSpace(errorType)
	}
	if reply.status == http.StatusBadRequest {
		switch oauthError {
		case "authorization_pending", "AuthorizationPendingException":
			return nil, "pending", nil
		case "slow_down", "SlowDownException":
			return nil, "slow_down", nil
		case "expired_token", "ExpiredTokenException":
			return nil, "expired", nil
		}
	}
	if reply.status != http.StatusOK || oauthError != "" || parsed.Status != "" || parsed.expiresIn() <= 0 ||
		parsed.access() == "" || parsed.refresh() == "" {
		return nil, "", fmt.Errorf("kiro: device login failed (HTTP %d)", reply.status)
	}
	return &Credentials{
		AccessToken:  parsed.access(),
		RefreshToken: parsed.refresh(),
		ExpiresAt:    time.Now().Add(lifetimeFrom(parsed.expiresIn(), defaultAccessTokenLifetime)).UTC().Format(time.RFC3339Nano),
		Region:       defaultKiroRegion,
		APIRegion:    defaultKiroRegion,
		AuthMethod:   authMethodBuilderID,
		ClientID:     auth.ClientID,
		ClientSecret: auth.ClientSecret,
	}, "complete", nil
}

func pollSocialDeviceLogin(ctx context.Context, auth *DeviceAuthorization) (*Credentials, string, error) {
	reply, err := postJSON(ctx, deviceHTTPClient, kiroSocialAuthBaseURL(defaultKiroRegion)+"/oauth/device/poll", map[string]string{
		"clientId":   kiroCLIClientID,
		"deviceCode": auth.DeviceCode,
	}, deviceTimeout)
	if err != nil {
		return nil, "", fmt.Errorf("kiro: device token poll failed: %w", err)
	}
	var parsed tokenResponse
	_ = sonic.Unmarshal(reply.body, &parsed)
	if reply.status == http.StatusOK {
		switch parsed.Status {
		case "authorization_pending":
			return nil, "pending", nil
		case "slow_down":
			return nil, "slow_down", nil
		case "expired_token", "expired":
			return nil, "expired", nil
		}
	}
	if reply.status != http.StatusOK || parsed.Error != "" || (parsed.Status != "" && parsed.Status != "approved") ||
		parsed.access() == "" || parsed.refresh() == "" ||
		len(parsed.ProfileArn) > 256 || !profileArnPattern.MatchString(parsed.ProfileArn) {
		return nil, "", fmt.Errorf("kiro: device login failed (HTTP %d)", reply.status)
	}
	return &Credentials{
		AccessToken:  parsed.access(),
		RefreshToken: parsed.refresh(),
		ExpiresAt:    time.Now().Add(lifetimeFrom(parsed.expiresIn(), defaultAccessTokenLifetime)).UTC().Format(time.RFC3339Nano),
		ProfileArn:   parsed.ProfileArn,
		Region:       defaultKiroRegion,
		APIRegion:    defaultKiroRegion,
		AuthMethod:   authMethodSocial,
	}, "complete", nil
}

// isStorableClientPart mirrors OpenCodex's check on OIDC client id/secret values.
func isStorableClientPart(value string) bool {
	return value != "" && len(value) <= 4096 && value == strings.TrimSpace(value) && !strings.ContainsFunc(value, isControl)
}

// isSafeVerificationURI accepts only https URLs without embedded credentials or characters that
// could disguise the destination.
func isSafeVerificationURI(value string) bool {
	if value == "" || len(value) > 2048 || strings.ContainsFunc(value, isDisguisingRune) {
		return false
	}
	uri := fasthttp.AcquireURI()
	defer fasthttp.ReleaseURI(uri)
	if err := uri.Parse(nil, []byte(value)); err != nil {
		return false
	}
	return string(uri.Scheme()) == "https" && len(uri.Host()) > 0 && len(uri.Username()) == 0 && len(uri.Password()) == 0
}

func isDisguisingRune(r rune) bool {
	return isControl(r) || (r >= 0x200b && r <= 0x200f) || (r >= 0x202a && r <= 0x202e) ||
		(r >= 0x2060 && r <= 0x2069) || r == 0xfeff
}

func truncateText(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}
