package githubcopilot

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bytedance/sonic"
	"github.com/golang-jwt/jwt/v5"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

const (
	// appJWTIssuedAtSkew backdates iat to absorb clock drift between us and GitHub.
	appJWTIssuedAtSkew = 60 * time.Second
	// appJWTLifetime is deliberately 8 minutes, not 10. GitHub rejects any App JWT whose
	// exp - iat exceeds 600 seconds. Because iat is already pushed 60s into the past, the
	// obvious "exp = now + 10m" yields a 660 second span and a blanket 401 with no other
	// signal. 8 minutes leaves 60 seconds of headroom.
	appJWTLifetime = 8 * time.Minute

	// tokenRefreshMargin is how long before expiry a token is considered stale.
	tokenRefreshMargin = 60 * time.Second
	// minRefreshIn floors the refresh_in hint Copilot returns, so a small or zero value
	// cannot turn into a refresh loop.
	minRefreshIn = 60 * time.Second
	// maxTokenLifetime rejects absurd expiry values rather than trusting them.
	maxTokenLifetime = 24 * time.Hour

	// maxExchangeBodyBytes caps every credential-exchange response.
	maxExchangeBodyBytes = 64 * 1024
	// exchangeTimeout bounds the whole refresh pipeline. Callers queued on the entry
	// mutex wait at most this long.
	exchangeTimeout = 20 * time.Second

	// Backoff for a cached failure. Permanent faults are configuration errors and there is
	// no point retrying them quickly; transient ones deserve a short pause.
	permanentBackoffBase = 2 * time.Second
	permanentBackoffCap  = 60 * time.Second
	transientBackoffBase = 500 * time.Millisecond
	transientBackoffCap  = 10 * time.Second

	// clockSkewWarnThreshold is when host clock drift becomes worth reporting. Drift is the
	// most common cause of App JWT 401s and is invisible from the error body.
	clockSkewWarnThreshold = 30 * time.Second

	defaultGithubAPIBase   = "https://api.github.com"
	copilotPublicAPIDomain = "githubcopilot.com"
	copilotTokenPath       = "/copilot_internal/v2/token"

	githubAcceptJSON     = "application/vnd.github+json"
	githubRESTAPIVersion = "2022-11-28"
	// bifrostUserAgent is sent on credential exchanges. api.github.com answers 403 with an
	// unhelpful body when a request has no User-Agent, and a stable one also gets us
	// rate-limited per App rather than per source IP.
	bifrostUserAgent = "Bifrost-Copilot/1.0"

	cacheKeyVersion = "ghcp-v1"
)

// copilotConfig is the validated form of schemas.GithubCopilotKeyConfig. Built per request;
// SecretVar.GetValue is a plain field read, so this is cheap.
type copilotConfig struct {
	appID          string
	installationID string
	repositoryID   int64
	privateKeyPEM  string
	githubDomain   string
	endpoints      copilotEndpoints
	cacheKey       string
}

// copilotEndpoints holds every URL that a GitHub Enterprise deployment changes.
type copilotEndpoints struct {
	apiBase              string
	installationTokenURL string
	copilotTokenURL      string
	allowedAPIDomains    []string
	isEnterprise         bool
}

// installationToken is layer 2. Immutable once stored.
type installationToken struct {
	token       string
	expiresAt   time.Time
	refreshAt   time.Time
	permissions map[string]string
}

// copilotToken is layer 3. Immutable once stored.
type copilotToken struct {
	token     string
	baseURL   string
	expiresAt time.Time
	refreshAt time.Time
}

// exchangeFailure is the negative cache. Without it, a permanently misconfigured key
// issues two api.github.com calls for every inbound request, and GitHub's penalty lands
// on the App rather than on the key.
type exchangeFailure struct {
	err        *schemas.BifrostError
	retryAfter time.Time
	attempts   int
}

// copilotTokenEntry is one cache slot holding both token layers behind one refresh lock.
//
// Layers 2 and 3 derive from a byte-identical cache key and form a strict pipeline, so
// splitting them across two maps would buy no reuse while making the invalidation ordering
// between them unspecifiable. Independent expiry lives in the two snapshot structs, not in
// two locks.
type copilotTokenEntry struct {
	mu           sync.Mutex
	installation atomic.Pointer[installationToken]
	copilot      atomic.Pointer[copilotToken]
	failure      atomic.Pointer[exchangeFailure]
}

// copilotTokenPool maps cache key to *copilotTokenEntry.
//
// Entries are never deleted, which is a deliberate departure from vertexTokenSourcePool
// (core/providers/vertex/vertex.go:48) where removeVertexClient deletes. That is safe
// there because the cached value is a Google oauth2.TokenSource that locks internally.
// Here the entry is the lock, and it also holds the negative cache. Deleting it on
// invalidation would split goroutines across two mutexes for as long as any of them still
// holds the old pointer, and would reset the failure backoff every time, so a permanently
// misconfigured key would retry on every request. Eviction stores nil into the atomic
// pointers and leaves the entry in place.
//
// The cost is that a rotated credential leaks its old entry, a couple of hundred bytes,
// bounded by the number of distinct historical credentials.
var copilotTokenPool sync.Map

// invalidationScope selects how far back up the chain to discard.
type invalidationScope uint8

const (
	scopeCopilotOnly invalidationScope = iota
	scopeAll
)

// installationTokenResponse is the layer 2 wire shape. Note expires_at is an RFC3339
// string here and a UNIX integer in copilotTokenResponse; they are genuinely different.
type installationTokenResponse struct {
	Token       string            `json:"token"`
	ExpiresAt   string            `json:"expires_at"`
	Permissions map[string]string `json:"permissions"`
}

// copilotTokenResponse is the layer 3 wire shape.
type copilotTokenResponse struct {
	Token     string `json:"token"`
	ExpiresAt int64  `json:"expires_at"`
	RefreshIn int64  `json:"refresh_in"`
	Endpoints struct {
		API string `json:"api"`
	} `json:"endpoints"`
}

// githubErrorBody is the bare error shape api.github.com returns.
type githubErrorBody struct {
	Message string `json:"message"`
}

// mintCredentials returns Copilot credentials for one request, minting and caching them
// as needed.
func mintCredentials(
	ctx *schemas.BifrostContext,
	keyConfig *schemas.GithubCopilotKeyConfig,
	client *fasthttp.Client,
	configuredBaseURL string,
	logger schemas.Logger,
) (*copilotCredentials, *schemas.BifrostError) {
	cfg, bErr := validateKeyConfig(keyConfig)
	if bErr != nil {
		return nil, bErr
	}

	// WithoutCancel: the caller that happens to trigger a refresh may hang up, but others
	// are blocked on the entry mutex waiting for its result. Killing the refresh because
	// one client disconnected would turn one cancellation into many failures. Context
	// values survive, so latency accounting still lands correctly.
	parent := context.Background()
	if ctx != nil {
		parent = context.WithoutCancel(ctx)
	}
	return mintWithConfig(parent, cfg, client, configuredBaseURL, logger)
}

// mintWithConfig is the cache and exchange pipeline, split from mintCredentials so the
// endpoints can be pointed somewhere other than api.github.com under test.
func mintWithConfig(
	parent context.Context,
	cfg *copilotConfig,
	client *fasthttp.Client,
	configuredBaseURL string,
	logger schemas.Logger,
) (*copilotCredentials, *schemas.BifrostError) {
	entry := loadOrCreateEntry(cfg.cacheKey)
	now := time.Now()

	// Fast path: one atomic load and one comparison, never blocking. Snapshots are
	// immutable and replaced wholesale, so a reader sees either the whole old token or
	// the whole new one.
	if tok := entry.copilot.Load(); tok != nil && now.Before(tok.refreshAt) {
		return credentialsFrom(tok, configuredBaseURL), nil
	}
	// Checked before the lock, so a misconfigured key does not queue every in-flight
	// request behind a mutex just to be told no.
	if f := entry.failure.Load(); f != nil && now.Before(f.retryAfter) {
		return nil, f.err
	}

	entry.mu.Lock()
	defer entry.mu.Unlock()

	// Double-checked read. Every goroutine that queued behind the one that actually
	// refreshed exits here with the fresh token, having issued no HTTP request.
	now = time.Now()
	if tok := entry.copilot.Load(); tok != nil && now.Before(tok.refreshAt) {
		return credentialsFrom(tok, configuredBaseURL), nil
	}
	if f := entry.failure.Load(); f != nil && now.Before(f.retryAfter) {
		return nil, f.err
	}

	exchangeCtx, cancel := context.WithTimeout(parent, exchangeTimeout)
	defer cancel()

	inst := entry.installation.Load()
	if inst == nil || !now.Before(inst.refreshAt) {
		// The App JWT is not cached. It is minted only inside a layer 2 refresh, roughly
		// once an hour per credential, so an RSA sign is never on a hot path.
		appJWT, err := signAppJWT(cfg, time.Now())
		if err != nil {
			bErr := configurationError(
				"github copilot: could not sign the GitHub App JWT: " + err.Error())
			entry.recordFailure(bErr, true)
			return nil, bErr
		}

		var bErr *schemas.BifrostError
		inst, bErr = exchangeInstallationToken(exchangeCtx, client, cfg, appJWT, logger)
		if bErr != nil {
			entry.recordFailure(bErr, isPermanentError(bErr))
			entry.installation.Store(nil)
			entry.copilot.Store(nil)
			return nil, bErr
		}
		entry.installation.Store(inst)

		warnOnMissingCopilotPermission(inst, logger)
	}

	tok, bErr := exchangeCopilotToken(exchangeCtx, client, cfg, inst.token, logger)
	if bErr != nil {
		entry.recordFailure(bErr, isPermanentError(bErr))
		entry.copilot.Store(nil)
		// Layer 3's only input was this installation token, so discard it too.
		entry.installation.Store(nil)
		return nil, bErr
	}

	entry.copilot.Store(tok)
	entry.failure.Store(nil)
	return credentialsFrom(tok, configuredBaseURL), nil
}

// credentialsFrom converts a cached token into per-request credentials. An explicitly
// configured base URL always wins over the one Copilot advertised, which is what lets an
// operator point at a recording proxy or a pinned enterprise host.
func credentialsFrom(tok *copilotToken, configuredBaseURL string) *copilotCredentials {
	baseURL := tok.baseURL
	if configuredBaseURL != "" {
		baseURL = strings.TrimRight(configuredBaseURL, "/")
	}
	return &copilotCredentials{Token: tok.token, BaseURL: baseURL}
}

func loadOrCreateEntry(cacheKey string) *copilotTokenEntry {
	if existing, ok := copilotTokenPool.Load(cacheKey); ok {
		return existing.(*copilotTokenEntry)
	}
	actual, _ := copilotTokenPool.LoadOrStore(cacheKey, &copilotTokenEntry{})
	return actual.(*copilotTokenEntry)
}

// recordFailure caches an error so repeated requests do not hammer api.github.com.
func (e *copilotTokenEntry) recordFailure(bErr *schemas.BifrostError, permanent bool) {
	attempts := 1
	if prev := e.failure.Load(); prev != nil {
		attempts = prev.attempts + 1
	}
	e.failure.Store(&exchangeFailure{
		err:        bErr,
		retryAfter: time.Now().Add(backoffFor(attempts, permanent)),
		attempts:   attempts,
	})
}

// backoffFor grows the retry delay geometrically and clamps it.
func backoffFor(attempts int, permanent bool) time.Duration {
	base, cap := transientBackoffBase, transientBackoffCap
	if permanent {
		base, cap = permanentBackoffBase, permanentBackoffCap
	}
	delay := base
	for i := 1; i < attempts && delay < cap; i++ {
		delay *= 2
	}
	if delay > cap {
		delay = cap
	}
	return delay
}

// isPermanentError reports whether a failure is a configuration fault rather than a
// transient one. Rate limits and server errors are transient; everything else on the
// credential path means something is misconfigured.
func isPermanentError(bErr *schemas.BifrostError) bool {
	if bErr == nil || bErr.StatusCode == nil {
		return false
	}
	switch status := *bErr.StatusCode; {
	case status == http.StatusTooManyRequests, status >= 500:
		return false
	default:
		return true
	}
}

// invalidateCredentials discards cached tokens for a credential after the inference call
// rejected them.
//
// Callers pass the config they used, so the cache key is recomputed rather than trusted.
// This is intentionally coarser than a generation-scoped CAS: the provider does not hold
// the snapshot pointer, so a same-key retry after a 401 discards whatever is cached. The
// negative cache and the refresh margin together keep that from becoming a loop.
func invalidateCredentials(keyConfig *schemas.GithubCopilotKeyConfig, scope invalidationScope) {
	if keyConfig == nil {
		return
	}
	entry, ok := copilotTokenPool.Load(copilotCacheKey(keyConfig))
	if !ok {
		return
	}
	e := entry.(*copilotTokenEntry)
	e.copilot.Store(nil)
	if scope == scopeAll {
		e.installation.Store(nil)
	}
}

// validateKeyConfig parses and checks the stored credential before anything touches the
// network or the CPU.
func validateKeyConfig(keyConfig *schemas.GithubCopilotKeyConfig) (*copilotConfig, *schemas.BifrostError) {
	if keyConfig == nil {
		return nil, configurationError("github copilot: github_copilot_key_config is required")
	}

	appID := strings.TrimSpace(keyConfig.AppID.GetValue())
	if appID == "" {
		return nil, configurationError("github copilot: github_copilot_key_config.app_id is required")
	}

	// installation_id is interpolated into an api.github.com path using our own App JWT,
	// so a value like "1/../../../user" would be a path injection against GitHub.
	installationID := strings.TrimSpace(keyConfig.InstallationID.GetValue())
	if !isASCIIDigits(installationID) || len(installationID) > 20 {
		return nil, configurationError(
			"github copilot: github_copilot_key_config.installation_id must be a numeric installation ID")
	}

	// repository_id must be a JSON number in the token request body. A string there is a
	// silent 422 from GitHub.
	repositoryID, err := strconv.ParseInt(strings.TrimSpace(keyConfig.RepositoryID.GetValue()), 10, 64)
	if err != nil || repositoryID <= 0 {
		return nil, configurationError(
			"github copilot: github_copilot_key_config.repository_id must be a numeric repository ID")
	}

	privateKey := keyConfig.PrivateKey.GetValue()
	if strings.TrimSpace(privateKey) == "" {
		return nil, configurationError("github copilot: github_copilot_key_config.private_key is required")
	}

	domain := normalizeGithubDomain(keyConfig.GithubDomain.GetValue())
	endpoints := resolveCopilotEndpoints(domain)
	endpoints.installationTokenURL = endpoints.apiBase + "/app/installations/" + installationID + "/access_tokens"

	return &copilotConfig{
		appID:          appID,
		installationID: installationID,
		repositoryID:   repositoryID,
		privateKeyPEM:  privateKey,
		githubDomain:   domain,
		endpoints:      endpoints,
		cacheKey:       copilotCacheKey(keyConfig),
	}, nil
}

func isASCIIDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// copilotCacheKey derives the pool key from every input that changes the identity of the
// minted token.
//
// Fields are length-framed rather than joined with a separator. With a plain "|",
// {app:"a|b", install:"c"} and {app:"a", install:"b|c"} collide, and a collision here
// means one tenant's Copilot token served on another tenant's request.
func copilotCacheKey(keyConfig *schemas.GithubCopilotKeyConfig) string {
	h := sha256.New()
	field := func(s string) {
		var n [8]byte
		binary.BigEndian.PutUint64(n[:], uint64(len(s)))
		_, _ = h.Write(n[:])
		_, _ = h.Write([]byte(s))
	}
	field(cacheKeyVersion)
	field(keyConfig.AppID.GetValue())
	field(keyConfig.InstallationID.GetValue())
	field(keyConfig.RepositoryID.GetValue())
	field(keyConfig.PrivateKey.GetValue())
	field(keyConfig.GithubDomain.GetValue())
	return hex.EncodeToString(h.Sum(nil))
}

// normalizeGithubDomain strips scheme, path, port, whitespace and case from an
// operator-supplied Enterprise domain, and rejects anything too broad to be a safe
// allowlist entry. A domain of "com" would otherwise authorize every .com host.
func normalizeGithubDomain(raw string) string {
	d := strings.ToLower(strings.TrimSpace(raw))
	d = strings.TrimPrefix(strings.TrimPrefix(d, "https://"), "http://")
	if i := strings.IndexAny(d, "/?#"); i >= 0 {
		d = d[:i]
	}
	if host, _, err := net.SplitHostPort(d); err == nil {
		d = host
	}
	d = strings.TrimSuffix(d, ".")
	if d == "" || net.ParseIP(d) != nil || strings.Count(d, ".") < 1 {
		return ""
	}
	return d
}

// resolveCopilotEndpoints maps the operator-supplied domain onto the exchange URLs and the
// inference allowlist.
//
//	""  or "github.com" -> https://api.github.com          (github.com)
//	"*.ghe.com"         -> https://api.{domain}            (GHE Cloud, data residency)
//	anything else       -> https://{domain}/api/v3         (GHE Server, self-hosted)
func resolveCopilotEndpoints(domain string) copilotEndpoints {
	var base string
	// dataResidency marks a GHE Cloud tenant, whose whole purpose is keeping traffic in
	// region. Those must not accept the public Copilot host.
	dataResidency := false

	switch {
	case domain == "" || domain == "github.com":
		base, domain = defaultGithubAPIBase, ""
	case strings.HasSuffix(domain, ".ghe.com"):
		base = "https://api." + domain
		dataResidency = true
	default:
		base = "https://" + domain + "/api/v3"
	}

	endpoints := copilotEndpoints{
		apiBase:         base,
		copilotTokenURL: base + copilotTokenPath,
		isEnterprise:    domain != "",
	}

	switch {
	case domain == "":
		endpoints.allowedAPIDomains = []string{copilotPublicAPIDomain}
	case dataResidency:
		// Allowing api.githubcopilot.com here would route a data-residency tenant's prompts
		// out of its region, which is the thing it is paying to avoid.
		endpoints.allowedAPIDomains = []string{domain}
	default:
		// Self-hosted GHES commonly proxies Copilot through GitHub's public service, so the
		// public host stays reachable there.
		endpoints.allowedAPIDomains = []string{copilotPublicAPIDomain, domain}
	}
	return endpoints
}

// signAppJWT mints the layer 1 GitHub App JWT.
func signAppJWT(cfg *copilotConfig, now time.Time) (string, error) {
	key, err := jwt.ParseRSAPrivateKeyFromPEM([]byte(normalizePEM(cfg.privateKeyPEM)))
	if err != nil {
		return "", fmt.Errorf("private_key could not be parsed as an RSA PEM (PKCS#1 or PKCS#8): %w", err)
	}

	token := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.RegisteredClaims{
		IssuedAt:  jwt.NewNumericDate(now.Add(-appJWTIssuedAtSkew)),
		ExpiresAt: jwt.NewNumericDate(now.Add(appJWTLifetime)),
		Issuer:    cfg.appID,
	})
	return token.SignedString(key)
}

// normalizePEM repairs the most common deployment mistake: a PEM pasted into an
// environment variable or a JSON config where the newlines survived as the two characters
// backslash and n. pem.Decode returns nil for that input, and the operator gets a parse
// failure with nothing to act on.
func normalizePEM(raw string) string {
	s := strings.TrimSpace(raw)
	if !strings.Contains(s, "\n") && strings.Contains(s, `\n`) {
		s = strings.ReplaceAll(s, `\n`, "\n")
	}
	return strings.ReplaceAll(s, "\r\n", "\n")
}

// exchangeInstallationToken performs layer 2: App JWT in, ghs_ token out.
func exchangeInstallationToken(
	ctx context.Context,
	client *fasthttp.Client,
	cfg *copilotConfig,
	appJWT string,
	logger schemas.Logger,
) (*installationToken, *schemas.BifrostError) {
	body, err := sonic.Marshal(map[string]any{
		"repository_ids": []int64{cfg.repositoryID},
		"permissions":    map[string]string{"copilot_requests": "write"},
	})
	if err != nil {
		return nil, configurationError(
			"github copilot: could not build the installation token request: " + err.Error())
	}

	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	req.SetRequestURI(cfg.endpoints.installationTokenURL)
	req.Header.SetMethod(http.MethodPost)
	req.Header.Set("Authorization", "Bearer "+appJWT)
	req.Header.Set("Accept", githubAcceptJSON)
	req.Header.Set("X-GitHub-Api-Version", githubRESTAPIVersion)
	req.Header.SetUserAgent(bifrostUserAgent)
	req.Header.SetContentType("application/json")
	req.SetBody(body)

	if err := doRequest(ctx, client, req, resp); err != nil {
		return nil, providerUtils.NewProviderAPIError(
			"github copilot: could not reach GitHub to mint an installation token", err, 0, nil, nil)
	}

	warnOnClockSkew(resp, logger)

	if status := resp.StatusCode(); status != http.StatusCreated && status != http.StatusOK {
		return nil, classifyInstallationTokenError(status, resp.Body(), cfg)
	}

	var parsed installationTokenResponse
	if err := sonic.Unmarshal(resp.Body(), &parsed); err != nil {
		return nil, providerUtils.NewProviderAPIError(
			"github copilot: could not parse GitHub's installation token response", err,
			resp.StatusCode(), nil, nil)
	}
	if parsed.Token == "" {
		return nil, blockingError("github copilot: GitHub returned an empty installation token", resp.StatusCode())
	}

	expiresAt, err := time.Parse(time.RFC3339, parsed.ExpiresAt)
	if err != nil {
		return nil, blockingError(
			"github copilot: GitHub returned an unparseable installation token expiry: "+parsed.ExpiresAt,
			resp.StatusCode())
	}
	if bErr := checkExpiry(expiresAt, "installation token"); bErr != nil {
		return nil, bErr
	}

	return &installationToken{
		token:       parsed.Token,
		expiresAt:   expiresAt,
		refreshAt:   expiresAt.Add(-tokenRefreshMargin),
		permissions: parsed.Permissions,
	}, nil
}

// exchangeCopilotToken performs layer 3: ghs_ token in, Copilot API token and host out.
func exchangeCopilotToken(
	ctx context.Context,
	client *fasthttp.Client,
	cfg *copilotConfig,
	installationTok string,
	logger schemas.Logger,
) (*copilotToken, *schemas.BifrostError) {
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	req.SetRequestURI(cfg.endpoints.copilotTokenURL)
	req.Header.SetMethod(http.MethodGet)
	req.Header.Set("Authorization", "token "+installationTok)
	req.Header.Set("Accept", githubAcceptJSON)
	// Every shipping Copilot client sends these on the exchange. Their necessity is not
	// documented by GitHub, but omitting them is a known source of unexplained rejections.
	req.Header.Set("Editor-Version", editorVersion)
	req.Header.Set("Editor-Plugin-Version", editorPluginVersion)
	req.Header.SetUserAgent(bifrostUserAgent)

	if err := doRequest(ctx, client, req, resp); err != nil {
		return nil, providerUtils.NewProviderAPIError(
			"github copilot: could not reach GitHub to exchange the Copilot token", err, 0, nil, nil)
	}

	if status := resp.StatusCode(); status != http.StatusOK {
		return nil, classifyCopilotTokenError(status, resp.Body(), cfg)
	}

	var parsed copilotTokenResponse
	if err := sonic.Unmarshal(resp.Body(), &parsed); err != nil {
		return nil, providerUtils.NewProviderAPIError(
			"github copilot: could not parse GitHub's Copilot token response", err,
			resp.StatusCode(), nil, nil)
	}
	if parsed.Token == "" {
		return nil, blockingError(
			"github copilot: no active Copilot entitlement for this installation "+
				"(GitHub returned an empty token)", resp.StatusCode())
	}

	// Unlike layer 2, this expiry is UNIX seconds rather than an RFC3339 string.
	expiresAt := time.Unix(parsed.ExpiresAt, 0)
	if bErr := checkExpiry(expiresAt, "Copilot token"); bErr != nil {
		return nil, bErr
	}

	baseURL, err := validateCopilotAPIBaseURL(parsed.Endpoints.API, cfg.endpoints.allowedAPIDomains)
	if err != nil {
		// On an Enterprise deployment, falling back to the public host would ship the
		// customer's prompts to api.githubcopilot.com, which is the exact thing running
		// GitHub Enterprise is meant to prevent. Fail instead.
		if cfg.endpoints.isEnterprise {
			return nil, blockingError(
				"github copilot: refusing to use the Copilot host GitHub returned for this "+
					"enterprise deployment: "+err.Error(), resp.StatusCode())
		}
		if logger != nil {
			logger.Warn("[github-copilot] ignoring the advertised Copilot host and falling back to %s: %v",
				defaultCopilotAPIBaseURL, err)
		}
		baseURL = defaultCopilotAPIBaseURL
	}

	refreshAt := expiresAt.Add(-tokenRefreshMargin)
	if parsed.RefreshIn > 0 {
		hinted := time.Duration(parsed.RefreshIn) * time.Second
		if hinted < minRefreshIn {
			hinted = minRefreshIn
		}
		// The hint must never outlive the token it refreshes.
		if candidate := time.Now().Add(hinted); candidate.Before(refreshAt) {
			refreshAt = candidate
		}
	}

	return &copilotToken{
		token:     parsed.Token,
		baseURL:   baseURL,
		expiresAt: expiresAt,
		refreshAt: refreshAt,
	}, nil
}

// checkExpiry rejects an expiry that is already past or implausibly far out. Caching an
// expired token means every subsequent request takes the slow path, which builds an
// unbounded request amplifier pointed at api.github.com.
func checkExpiry(expiresAt time.Time, what string) *schemas.BifrostError {
	now := time.Now()
	if !expiresAt.After(now) {
		return configurationError(
			"github copilot: GitHub returned an already-expired " + what)
	}
	if expiresAt.Sub(now) > maxTokenLifetime {
		return configurationError(
			"github copilot: GitHub returned an implausible " + what + " expiry")
	}
	return nil
}

// validateCopilotAPIBaseURL vets the endpoints.api value before it becomes the base URL of
// every inference request. It is response-controlled and decides where prompts and a
// bearer token are sent, so it is treated as untrusted even over TLS.
//
// Returns a rebuilt URL rather than the input, so nothing in the input survives
// unexamined. DNS is deliberately not resolved here: ConfigureDialer already resolves and
// rejects private and link-local addresses at connect time and then dials the IP literal,
// which closes the rebinding window a validate-then-connect design would open.
func validateCopilotAPIBaseURL(rawURL string, allowedDomains []string) (string, error) {
	if strings.TrimSpace(rawURL) == "" {
		return "", fmt.Errorf("endpoints.api is empty")
	}

	u, err := url.Parse(rawURL)
	if err != nil {
		return "", fmt.Errorf("endpoints.api is not a valid URL: %w", err)
	}
	if u.Opaque != "" {
		return "", fmt.Errorf("endpoints.api must not be an opaque URL")
	}
	if !strings.EqualFold(u.Scheme, "https") {
		return "", fmt.Errorf("endpoints.api scheme %q is not https", u.Scheme)
	}
	// https://api.githubcopilot.com@evil.com parses with Host=evil.com. Rejecting the
	// shape outright is clearer than relying on the host check alone.
	if u.User != nil {
		return "", fmt.Errorf("endpoints.api must not contain userinfo")
	}
	if port := u.Port(); port != "" && port != "443" {
		return "", fmt.Errorf("endpoints.api port %q is not allowed", port)
	}

	host := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	if host == "" {
		return "", fmt.Errorf("endpoints.api has no host")
	}
	if net.ParseIP(host) != nil {
		return "", fmt.Errorf("endpoints.api host %q is an IP literal", host)
	}

	for _, domain := range allowedDomains {
		if domain == "" {
			continue
		}
		// The "." prefix is the entire security property here. A bare
		// strings.HasSuffix(host, domain) also accepts evilgithubcopilot.com.
		if host == domain || strings.HasSuffix(host, "."+domain) {
			return "https://" + host + strings.TrimRight(u.EscapedPath(), "/"), nil
		}
	}

	return "", fmt.Errorf("endpoints.api host %q is not %s or a subdomain of it",
		host, strings.Join(allowedDomains, " / "))
}

// doRequest issues one exchange request, honouring the context deadline.
func doRequest(ctx context.Context, client *fasthttp.Client, req *fasthttp.Request, resp *fasthttp.Response) error {
	if deadline, ok := ctx.Deadline(); ok {
		return client.DoDeadline(req, resp, deadline)
	}
	return client.DoTimeout(req, resp, exchangeTimeout)
}

// warnOnClockSkew reports host clock drift, the most common cause of App JWT 401s and one
// that is invisible from the error body. Warn only; never adjust iat from a response
// header.
func warnOnClockSkew(resp *fasthttp.Response, logger schemas.Logger) {
	if logger == nil {
		return
	}
	raw := string(resp.Header.Peek("Date"))
	if raw == "" {
		return
	}
	serverTime, err := time.Parse(time.RFC1123, raw)
	if err != nil {
		return
	}
	skew := time.Since(serverTime)
	if skew < 0 {
		skew = -skew
	}
	if skew > clockSkewWarnThreshold {
		logger.Warn("[github-copilot] this host's clock differs from GitHub's by %s. "+
			"App JWTs are only valid for %s, so drift beyond that causes blanket 401s.",
			skew.Round(time.Second), appJWTLifetime)
	}
}

// blockingError builds a configuration-fault error that must not drain onto a fallback
// provider.
func blockingError(message string, statusCode int) *schemas.BifrostError {
	bErr := providerUtils.NewProviderAPIError(message, nil, statusCode, nil, nil)
	bErr.AllowFallbacks = schemas.Ptr(false)
	return bErr
}

// githubMessage pulls GitHub's own error text out of a response body, for appending to a
// Bifrost message.
func githubMessage(body []byte) string {
	var parsed githubErrorBody
	if err := sonic.Unmarshal(body, &parsed); err == nil && parsed.Message != "" {
		return parsed.Message
	}
	return "(no detail)"
}

// classifyInstallationTokenError maps a layer 2 failure. A failure here is about the App
// JWT or the installation, and has nothing to do with Copilot yet.
func classifyInstallationTokenError(status int, body []byte, cfg *copilotConfig) *schemas.BifrostError {
	detail := githubMessage(body)

	switch status {
	case http.StatusUnauthorized:
		return blockingError("github copilot: GitHub rejected the App JWT (401). Verify that "+
			"app_id belongs to the App that owns this private_key, that private_key is current, "+
			"and that this host's clock is accurate. GitHub said: "+detail, status)
	case http.StatusForbidden:
		if strings.Contains(strings.ToLower(detail), errNotAccessibleByIntegration) {
			return blockingError("github copilot: the App JWT was accepted but the App cannot mint "+
				"installation tokens (403). app_id and private_key must belong to a GitHub App, "+
				"not an OAuth app or a personal access token.", status)
		}
		return blockingError("github copilot: GitHub blocked the installation token request (403). "+
			"GitHub said: "+detail, status)
	case http.StatusNotFound:
		return blockingError(fmt.Sprintf("github copilot: installation %s was not found for app %s (404). "+
			"The App is not installed on that account, or installation_id belongs to a different App.",
			cfg.installationID, cfg.appID), status)
	case http.StatusUnprocessableEntity:
		return blockingError(fmt.Sprintf("github copilot: GitHub rejected the installation token request "+
			"(422). repository_id %d is most likely not one this installation can access. GitHub said: %s",
			cfg.repositoryID, detail), status)
	default:
		return providerUtils.NewProviderAPIError(
			fmt.Sprintf("github copilot: installation token request failed (%d): %s", status, detail),
			nil, status, nil, nil)
	}
}

// classifyCopilotTokenError maps a layer 3 failure, using GitHub's own troubleshooting
// vocabulary so an operator can match the message to the documented fix.
func classifyCopilotTokenError(status int, body []byte, cfg *copilotConfig) *schemas.BifrostError {
	detail := githubMessage(body)

	switch status {
	case http.StatusUnauthorized:
		return blockingError("github copilot: the organization does not support GitHub App "+
			"installation authentication for Copilot (401). An organization owner must enable "+
			"Copilot requests from GitHub App installations. GitHub said: "+detail, status)
	case http.StatusForbidden:
		return blockingError("github copilot: Copilot rejected the installation token (403). The "+
			"App installation most likely lacks the Copilot Requests permission or All repositories "+
			"access. GitHub said: "+detail, status)
	case http.StatusNotFound:
		return blockingError("github copilot: the Copilot token endpoint is unavailable at "+
			cfg.endpoints.copilotTokenURL+" (404). Copilot is not enabled for this account, or this "+
			"GitHub Enterprise Server version does not expose it.", status)
	default:
		return providerUtils.NewProviderAPIError(
			fmt.Sprintf("github copilot: Copilot token exchange failed (%d): %s", status, detail),
			nil, status, nil, nil)
	}
}

// warnOnMissingCopilotPermission flags an installation token minted without the permission
// the Copilot exchange requires. GitHub still returns 201 in that case, so the failure
// would otherwise only surface one layer later as an opaque 403.
func warnOnMissingCopilotPermission(inst *installationToken, logger schemas.Logger) {
	if logger == nil || inst == nil {
		return
	}
	if perm, ok := inst.permissions["copilot_requests"]; ok && perm == "write" {
		return
	}
	logger.Warn("[github-copilot] the installation token was issued without the " +
		"copilot_requests permission, so the Copilot token exchange will fail. Approve the " +
		"App's permission request under Settings > Applications.")
}
