package rssfeed

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/network"
	"github.com/maximhq/bifrost/framework/livestore"
)

const (
	// DefaultTTL is how long a fetched feed is served from cache.
	DefaultTTL = 15 * time.Minute
	// errorTTL is shorter so a feed that was briefly down recovers quickly
	// without every tray open hammering it.
	errorTTL       = 2 * time.Minute
	fetchTimeout   = 10 * time.Second
	maxFeedBytes   = 2 << 20
	userAgentValue = "Bifrost-RSS/1.0 (+https://getbifrost.ai)"
	// itemsCache names the live-store cache of fetched feeds; one manager per process.
	itemsCache = "rss_feed_items"
)

// ErrFeedTooLarge is returned when a feed body exceeds maxFeedBytes.
var ErrFeedTooLarge = errors.New("feed exceeds the 2 MB size limit")

// feedResult is one fetch of a feed: its items, or the error the fetch failed with. Errors are
// cached too, briefly, so a feed that is down is not fetched on every tray open.
type feedResult struct {
	items []Item
	err   error
}

// Manager fetches feeds through an SSRF-safe client and caches the parsed
// result per URL in the live store. Memory retained is bounded by the number of
// distinct URLs fetched (at most MaxItemsPerFeed items each) and each entry
// expires after its TTL; Forget drops a URL's entry when its feed is deleted or
// repointed.
type Manager struct {
	client   *http.Client
	validate func(string) error
	// ttl and errTTL are how long a fetched feed and a failed fetch are served
	// from cache.
	ttl    time.Duration
	errTTL time.Duration
	cache  *livestore.Cache[*feedResult]
}

// NewManager returns a Manager that refuses private and link-local targets,
// both at validation time and at dial time (defeating DNS rebinding), and keeps
// fetched feeds in live.
func NewManager(live *livestore.Store) (*Manager, error) {
	if live == nil {
		return nil, fmt.Errorf("rss feed manager: %w", livestore.ErrNoLiveStore)
	}
	m := &Manager{
		client:   network.NewSSRFSafeHTTPClient(fetchTimeout),
		validate: func(u string) error { return bifrost.ValidateExternalURL(u, false) },
		ttl:      DefaultTTL,
		errTTL:   errorTTL,
	}
	// Concurrent readers of one feed cause one fetch and share its result.
	cache, err := live.NewCache[*feedResult](context.Background(), itemsCache, livestore.WithSharedFills[*feedResult](), livestore.WithCacheTTL(func(r *feedResult) time.Duration {
		if r.err != nil {
			return m.errTTL
		}
		return m.ttl
	}))
	if err != nil {
		return nil, err
	}
	m.cache = cache
	return m, nil
}

// NewManagerWithClient is the test seam: it uses the given client and skips
// URL validation, so feeds can be served from an httptest server on loopback.
func NewManagerWithClient(live *livestore.Store, client *http.Client) (*Manager, error) {
	m, err := NewManager(live)
	if err != nil {
		return nil, err
	}
	m.client = client
	m.validate = func(string) error { return nil }
	return m, nil
}

// Get returns the items for url, serving from cache while fresh. Concurrent
// callers for the same URL share one upstream fetch.
func (m *Manager) Get(ctx context.Context, url string) ([]Item, error) {
	if r, ok := m.cache.Get(url); ok {
		return r.items, r.err
	}
	// The fetch is shared and its result cached, so one caller's cancellation must
	// not fail it for the rest, and waiters wait for it as before; Fetch still
	// bounds it with fetchTimeout. A Forget that lands mid-fetch wins: the result
	// is returned but not cached.
	detached := context.WithoutCancel(ctx)
	r, err := m.cache.Fill(detached, url, func() (*feedResult, string, error) {
		items, err := m.Fetch(detached, url)
		return &feedResult{items: items, err: err}, "", nil
	})
	if err != nil {
		return nil, err
	}
	return r.items, r.err
}

// Fetch downloads and parses url without touching the cache. Used directly to
// validate a feed before it is saved.
func (m *Manager) Fetch(ctx context.Context, url string) ([]Item, error) {
	if err := m.validate(url); err != nil {
		return nil, fmt.Errorf("invalid feed url: %w", err)
	}
	fetchCtx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(fetchCtx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("invalid feed url: %w", err)
	}
	req.Header.Set("User-Agent", userAgentValue)
	req.Header.Set("Accept", "application/rss+xml, application/atom+xml, application/xml;q=0.9, text/xml;q=0.8, */*;q=0.5")
	resp, err := m.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch feed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch feed: unexpected status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxFeedBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read feed: %w", err)
	}
	if len(body) > maxFeedBytes {
		return nil, ErrFeedTooLarge
	}
	items, err := Parse(body)
	if err != nil && looksLikeHTML(resp.Header.Get("Content-Type"), body) {
		// No feed at this URL, but it may be a news listing page (anthropic.com/news
		// publishes no RSS). Links resolve against the final URL after redirects.
		items, err = ParseHTMLPage(body, resp.Request.URL)
	}
	if err != nil {
		return nil, fmt.Errorf("parse feed: %w", err)
	}
	return items, nil
}

// Forget drops the cached entry for url, including the result of a fetch
// still in flight for it.
func (m *Manager) Forget(url string) {
	m.cache.Evict(url)
}
