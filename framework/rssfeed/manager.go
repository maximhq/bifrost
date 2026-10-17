package rssfeed

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/network"
	"golang.org/x/sync/singleflight"
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
)

// ErrFeedTooLarge is returned when a feed body exceeds maxFeedBytes.
var ErrFeedTooLarge = errors.New("feed exceeds the 2 MB size limit")

type cacheEntry struct {
	items     []Item
	err       error
	fetchedAt time.Time
	ttl       time.Duration
}

// Manager fetches feeds through an SSRF-safe client and caches the parsed
// result per URL. Memory retained is bounded by the number of distinct URLs
// fetched (at most MaxItemsPerFeed items each); Forget drops a URL's entry when
// its feed is deleted or repointed.
type Manager struct {
	client   *http.Client
	validate func(string) error
	now      func() time.Time
	ttl      time.Duration

	mu          sync.RWMutex
	cache       map[string]*cacheEntry
	generations map[string]uint64 // bumped by Forget; a fetch that started before the bump is not cached
	group       singleflight.Group
}

// NewManager returns a Manager that refuses private and link-local targets,
// both at validation time and at dial time (defeating DNS rebinding).
func NewManager() *Manager {
	return &Manager{
		client:      network.NewSSRFSafeHTTPClient(fetchTimeout),
		validate:    func(u string) error { return bifrost.ValidateExternalURL(u, false) },
		now:         time.Now,
		ttl:         DefaultTTL,
		cache:       make(map[string]*cacheEntry),
		generations: make(map[string]uint64),
	}
}

// NewManagerWithClient is the test seam: it uses the given client and skips
// URL validation, so feeds can be served from an httptest server on loopback.
func NewManagerWithClient(client *http.Client) *Manager {
	m := NewManager()
	m.client = client
	m.validate = func(string) error { return nil }
	return m
}

// Get returns the items for url, serving from cache while fresh. Concurrent
// callers for the same URL share one upstream fetch.
func (m *Manager) Get(ctx context.Context, url string) ([]Item, error) {
	m.mu.RLock()
	entry, ok := m.cache[url]
	m.mu.RUnlock()
	if ok && m.now().Sub(entry.fetchedAt) < entry.ttl {
		return entry.items, entry.err
	}
	// The fetch is shared and its result cached, so one caller's cancellation must
	// not fail it for the rest; Fetch still bounds it with fetchTimeout. A Forget
	// that lands mid-fetch wins: the result is returned but not cached.
	result, _, _ := m.group.Do(url, func() (any, error) {
		m.mu.RLock()
		generation := m.generations[url]
		m.mu.RUnlock()
		items, err := m.Fetch(context.WithoutCancel(ctx), url)
		ttl := m.ttl
		if err != nil {
			ttl = errorTTL
		}
		e := &cacheEntry{items: items, err: err, fetchedAt: m.now(), ttl: ttl}
		m.mu.Lock()
		if m.generations[url] == generation {
			m.cache[url] = e
		}
		m.mu.Unlock()
		return e, nil
	})
	e := result.(*cacheEntry)
	return e.items, e.err
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
	m.mu.Lock()
	m.generations[url]++
	delete(m.cache, url)
	m.mu.Unlock()
}
