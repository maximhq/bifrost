package handlers

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/network"
	configtables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/oauth2"
	"github.com/maximhq/bifrost/transports/v2/bifrost-http/lib"
	"golang.org/x/sync/singleflight"
)

// Client ID Metadata Documents (MCP 2025-11-25+): a client_id that is an https URL is resolved by
// fetching the document there, instead of a DCR registration.

const (
	maxClientMetadataDocumentBytes = 64 << 10
	clientMetadataFetchTimeout     = 5 * time.Second
	minClientMetadataTTL           = time.Minute
	maxClientMetadataTTL           = 24 * time.Hour
	defaultClientMetadataTTL       = time.Hour
	failedClientMetadataTTL        = time.Minute
	maxClientMetadataEntries       = 1024
	maxClientMetadataFetches       = 16 // concurrent outbound fetches across all client_ids
)

// defaultClientMetadataResolver refuses private and loopback addresses, and through a proxy any
// destination it cannot resolve itself: the URL is chosen by an unauthenticated caller.
var defaultClientMetadataResolver = newOAuth2ClientMetadataResolver(network.NewStrictSSRFSafeHTTPClient(clientMetadataFetchTimeout))

// clientMetadataPrivateHosts are private hosts documents may also be fetched from, over http too. Only a
// binary built with the harness tag sets it (mcpoauth2cimdharness.go); nil, always in a release, permits none.
var clientMetadataPrivateHosts atomic.Pointer[network.Allowlist]

// privateClientMetadataHTTP fetches documents from clientMetadataPrivateHosts; nil while unset.
var privateClientMetadataHTTP atomic.Pointer[http.Client]

// setClientMetadataPrivateAllowlist lets documents be fetched from allow's private hosts, over http as
// well as https: anyone who can reach /oauth2/authorize can then make Bifrost GET those hosts, so only
// tests and the harness build call it. Build allow with network.ValidatePrivateTargetAllowlist; nil
// restores the default.
func setClientMetadataPrivateAllowlist(allow *network.Allowlist) {
	clientMetadataPrivateHosts.Store(allow)
	if allow == nil {
		privateClientMetadataHTTP.Store(nil)
		return
	}
	privateClientMetadataHTTP.Store(&http.Client{
		Timeout:       clientMetadataFetchTimeout,
		Transport:     &http.Transport{DialContext: network.AllowlistedPrivateDialContext(clientMetadataFetchTimeout, clientMetadataPrivateHosts.Load)},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	})
}

// isClientMetadataDocumentID reports whether clientID names a document: an https URL, or an http URL
// on an allow-listed private host.
func isClientMetadataDocumentID(clientID string) bool {
	if oauth2.IsClientMetadataURL(clientID) {
		return true
	}
	rest, ok := strings.CutPrefix(clientID, "http://")
	return ok && privateClientMetadataHost(clientID) && oauth2.IsClientMetadataURL("https://"+rest)
}

// privateClientMetadataHost reports whether the document at clientID is on an allow-listed private host.
func privateClientMetadataHost(clientID string) bool {
	allow := clientMetadataPrivateHosts.Load()
	if allow == nil {
		return false
	}
	host := urlHost(clientID)
	return allow.Permits(host, net.ParseIP(host))
}

// oauth2ClientMetadataResolver fetches, validates and caches Client ID Metadata Documents.
type oauth2ClientMetadataResolver struct {
	client     *http.Client
	now        func() time.Time
	inflight   singleflight.Group
	fetchSlots chan struct{}

	mu    sync.Mutex
	cache map[string]clientMetadataEntry
}

type clientMetadataEntry struct {
	client  *configtables.TableOAuth2Client
	err     *oauth2ClientMetadataError
	expires time.Time
}

func newOAuth2ClientMetadataResolver(client *http.Client) *oauth2ClientMetadataResolver {
	noRedirects := *client
	// The document must come from the client_id URL itself.
	noRedirects.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &oauth2ClientMetadataResolver{client: &noRedirects, now: time.Now, cache: map[string]clientMetadataEntry{}, fetchSlots: make(chan struct{}, maxClientMetadataFetches)}
}

// resolve returns the public client the document at clientID describes. Concurrent misses share
// one fetch.
func (r *oauth2ClientMetadataResolver) resolve(ctx context.Context, store *lib.Config, clientID string) (*configtables.TableOAuth2Client, *oauth2ClientMetadataError) {
	if entry, ok := r.cached(clientID); ok {
		return entry.client, entry.err
	}
	v, _, _ := r.inflight.Do(clientID, func() (any, error) {
		if entry, ok := r.cached(clientID); ok {
			return entry, nil
		}
		// A flood of distinct client_ids gets no more than maxClientMetadataFetches outbound fetches;
		// the refusal is not cached, so a retry once a slot frees succeeds.
		select {
		case r.fetchSlots <- struct{}{}:
			defer func() { <-r.fetchSlots }()
		default:
			return clientMetadataEntry{err: &oauth2ClientMetadataError{code: "temporarily_unavailable", description: "too many client metadata documents are being fetched; retry shortly"}}, nil
		}
		// Detached from the caller so its cancellation does not fail the joined callers.
		fetchCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), clientMetadataFetchTimeout)
		defer cancel()
		client, ttl, err := r.fetch(fetchCtx, store, clientID)
		if err != nil {
			ttl = failedClientMetadataTTL
		}
		now := r.now()
		entry := clientMetadataEntry{client: client, err: err, expires: now.Add(ttl)}
		r.mu.Lock()
		r.store(clientID, entry, now)
		r.mu.Unlock()
		return entry, nil
	})
	entry := v.(clientMetadataEntry)
	return entry.client, entry.err
}

// cached returns clientID's entry while it is fresh.
func (r *oauth2ClientMetadataResolver) cached(clientID string) (clientMetadataEntry, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	entry, ok := r.cache[clientID]
	return entry, ok && r.now().Before(entry.expires)
}

// store records entry, evicting expired entries, then failed lookups, then arbitrary ones when full.
// Callers hold r.mu.
func (r *oauth2ClientMetadataResolver) store(clientID string, entry clientMetadataEntry, now time.Time) {
	if len(r.cache) >= maxClientMetadataEntries {
		for id, e := range r.cache {
			if !now.Before(e.expires) {
				delete(r.cache, id)
			}
		}
		for id, e := range r.cache {
			if len(r.cache) < maxClientMetadataEntries {
				break
			}
			if e.err != nil {
				delete(r.cache, id)
			}
		}
		for id := range r.cache {
			if len(r.cache) < maxClientMetadataEntries {
				break
			}
			delete(r.cache, id)
		}
	}
	r.cache[clientID] = entry
}

func (r *oauth2ClientMetadataResolver) fetch(ctx context.Context, store *lib.Config, clientID string) (*configtables.TableOAuth2Client, time.Duration, *oauth2ClientMetadataError) {
	invalid := func(format string, args ...any) *oauth2ClientMetadataError {
		return &oauth2ClientMetadataError{code: "invalid_client", description: "client metadata document " + fmt.Sprintf(format, args...)}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, clientID, nil)
	if err != nil {
		return nil, 0, invalid("URL is invalid")
	}
	req.Header.Set("Accept", "application/json")
	httpClient := r.client
	if private := privateClientMetadataHTTP.Load(); private != nil && privateClientMetadataHost(clientID) {
		httpClient = private
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, 0, invalid("could not be fetched")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, 0, invalid("fetch returned HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxClientMetadataDocumentBytes+1))
	if err != nil {
		return nil, 0, invalid("could not be read")
	}
	if len(body) > maxClientMetadataDocumentBytes {
		return nil, 0, invalid("exceeds %d bytes", maxClientMetadataDocumentBytes)
	}

	var doc struct {
		dcrRequest
		ClientID                          string   `json:"client_id"`
		TokenEndpointAuthMethodsSupported []string `json:"token_endpoint_auth_methods_supported"` // not standard; ChatGPT and Codex publish it
	}
	if err := sonic.Unmarshal(body, &doc); err != nil {
		return nil, 0, invalid("is not valid JSON")
	}
	if doc.ClientID != clientID {
		return nil, 0, invalid("client_id does not match the URL it was fetched from")
	}
	if strings.TrimSpace(doc.ClientName) == "" {
		return nil, 0, invalid("has no client_name")
	}
	// A client publishes one document for every authorization server it talks to, so it can list
	// grants this server does not offer (claude.ai lists the jwt-bearer grant). Keep the offered
	// ones, the way a server narrows a registration (RFC 7591 §3.2.1), and refuse the document
	// only when none are left.
	metadata := doc.dcrRequest
	if len(metadata.GrantTypes) > 0 {
		metadata.GrantTypes = offeredOAuth2GrantTypes(metadata.GrantTypes)
		if len(metadata.GrantTypes) == 0 {
			return nil, 0, invalid("unsupported grant_type")
		}
	}
	// Likewise a client can prefer an auth method this server lacks (ChatGPT: private_key_jwt) while
	// also supporting none; it then uses none here, the only method this server advertises.
	if slices.Contains(doc.TokenEndpointAuthMethodsSupported, "none") {
		metadata.TokenEndpointAuthMethod = "none"
	}
	grantTypes, _, scope, metadataErr := validateOAuth2ClientMetadata(store, metadata)
	if metadataErr != nil {
		return nil, 0, invalid("%s", metadataErr.description)
	}
	return &configtables.TableOAuth2Client{
		ClientID:     clientID,
		ClientName:   doc.ClientName,
		RedirectURIs: doc.RedirectURIs,
		GrantTypes:   grantTypes,
		Scope:        scope,
	}, clientMetadataTTL(resp.Header.Get("Cache-Control")), nil
}

// clientMetadataTTL is the document's Cache-Control max-age, clamped to [min, max]TTL.
func clientMetadataTTL(cacheControl string) time.Duration {
	ttl := defaultClientMetadataTTL
	for directive := range strings.SplitSeq(strings.ToLower(cacheControl), ",") {
		directive = strings.TrimSpace(directive)
		switch {
		case directive == "no-store" || directive == "no-cache":
			return minClientMetadataTTL
		case strings.HasPrefix(directive, "max-age="):
			if seconds, err := strconv.Atoi(strings.TrimPrefix(directive, "max-age=")); err == nil {
				ttl = time.Duration(seconds) * time.Second
			}
		}
	}
	return min(max(ttl, minClientMetadataTTL), maxClientMetadataTTL)
}

// lookupOAuth2Client resolves clientID as a Client ID Metadata Document URL or a registered client.
func lookupOAuth2Client(ctx context.Context, store *lib.Config, resolver *oauth2ClientMetadataResolver, clientID string) (*configtables.TableOAuth2Client, *oauth2ClientMetadataError, error) {
	if isClientMetadataDocumentID(clientID) {
		client, metadataErr := resolver.resolve(ctx, store, clientID)
		return client, metadataErr, nil
	}
	client, err := store.ConfigStore.GetOAuth2ClientByClientID(ctx, clientID)
	return client, nil, err
}

// urlHost returns raw's hostname, or "".
func urlHost(raw string) string {
	if u, err := url.Parse(raw); err == nil {
		return u.Hostname()
	}
	return ""
}

// allLoopbackRedirects reports whether every redirect URI is loopback.
func allLoopbackRedirects(redirectURIs []string) bool {
	for _, uri := range redirectURIs {
		if !isLoopbackRedirectHost(urlHost(uri)) {
			return false
		}
	}
	return len(redirectURIs) > 0
}
