package handlers

import (
	"context"
	"errors"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bytedance/sonic"
	"github.com/fasthttp/router"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/rssfeed"
	"github.com/maximhq/bifrost/transports/v2/bifrost-http/lib"
	"github.com/valyala/fasthttp"
)

const (
	rssItemsDefaultLimit = 50
	rssItemsMaxLimit     = 100
	rssFeedNameMaxLen    = 120
	rssFeedURLMaxLen     = 2048
	// A data: URI logo is stored inline in the row, so it is kept small: 128 KB
	// of URI is roughly a 96 KB image, far more than a tray icon needs.
	rssFeedIconURLMaxLen = 128 << 10
	// Custom feeds are unbounded in number, so the items fan-out is capped.
	rssItemsMaxConcurrentFetches = 8
	// The opaque per-feed error surfaced to the UI. The underlying error stays
	// in the server log so the items endpoint cannot be used as a port-scan or
	// banner oracle against arbitrary hosts.
	rssFeedUnavailableMessage = "feed could not be loaded"
)

var errRSSFeedsUnavailable = errors.New("rss feed storage is unavailable")

// rssItemsFetchTimeout bounds one items request and one probe; a var so tests can shorten it.
var rssItemsFetchTimeout = 15 * time.Second

// RSSFeedsHandler serves the news sources shown in the notification tray:
// feed CRUD backed by configstore, and a merged item list fetched server-side
// (vendor feeds do not send CORS headers, so the browser cannot read them).
// None of this is on the inference path.
type RSSFeedsHandler struct {
	store   configstore.RSSFeedStore
	manager *rssfeed.Manager
}

func NewRSSFeedsHandler(store configstore.ConfigStore, manager *rssfeed.Manager) *RSSFeedsHandler {
	feedStore, _ := store.(configstore.RSSFeedStore)
	if manager == nil {
		manager = rssfeed.NewManager()
	}
	return &RSSFeedsHandler{store: feedStore, manager: manager}
}

func (h *RSSFeedsHandler) RegisterRoutes(r *router.Router, middlewares ...schemas.BifrostHTTPMiddleware) {
	r.GET("/api/rss/feeds", lib.ChainMiddlewares(h.listFeeds, middlewares...))
	r.POST("/api/rss/feeds", lib.ChainMiddlewares(h.createFeed, middlewares...))
	r.PUT("/api/rss/feeds/{id}", lib.ChainMiddlewares(h.updateFeed, middlewares...))
	r.DELETE("/api/rss/feeds/{id}", lib.ChainMiddlewares(h.deleteFeed, middlewares...))
	r.GET("/api/rss/items", lib.ChainMiddlewares(h.listItems, middlewares...))
	r.GET("/api/rss/settings", lib.ChainMiddlewares(h.getSettings, middlewares...))
	r.PUT("/api/rss/settings", lib.ChainMiddlewares(h.updateSettings, middlewares...))
}

// Start reseeds the builtin catalog so feeds added to it after the table was
// created reach existing deployments.
func (h *RSSFeedsHandler) Start(ctx context.Context) {
	if h.store == nil {
		return
	}
	if err := h.store.SeedBuiltinRSSFeeds(ctx); err != nil {
		logger.Warn("failed to seed builtin rss feeds: %v", err)
	}
}

type rssFeedListResponse struct {
	Feeds []tables.TableRSSFeed `json:"feeds"`
}

func (h *RSSFeedsHandler) listFeeds(ctx *fasthttp.RequestCtx) {
	if h.store == nil {
		SendError(ctx, fasthttp.StatusServiceUnavailable, errRSSFeedsUnavailable.Error())
		return
	}
	feeds, err := h.store.ListRSSFeeds(ctx)
	if err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, "Failed to list rss feeds")
		return
	}
	if feeds == nil {
		feeds = []tables.TableRSSFeed{}
	}
	SendJSON(ctx, rssFeedListResponse{Feeds: feeds})
}

type rssFeedCreateRequest struct {
	Name    string `json:"name"`
	URL     string `json:"url"`
	IconURL string `json:"icon_url,omitempty"`
	Enabled *bool  `json:"enabled,omitempty"`
}

type rssFeedUpdateRequest struct {
	Name    *string `json:"name,omitempty"`
	URL     *string `json:"url,omitempty"`
	IconURL *string `json:"icon_url,omitempty"`
	Enabled *bool   `json:"enabled,omitempty"`
}

func (h *RSSFeedsHandler) createFeed(ctx *fasthttp.RequestCtx) {
	if h.store == nil {
		SendError(ctx, fasthttp.StatusServiceUnavailable, errRSSFeedsUnavailable.Error())
		return
	}
	var req rssFeedCreateRequest
	if err := sonic.Unmarshal(ctx.PostBody(), &req); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, "Invalid request payload")
		return
	}
	name, feedURL, err := validateRSSFeedInput(req.Name, req.URL)
	if err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, err.Error())
		return
	}
	iconURL, err := validateRSSFeedIconURL(req.IconURL)
	if err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, err.Error())
		return
	}
	if msg := h.probeFeed(feedURL); msg != "" {
		SendError(ctx, fasthttp.StatusBadRequest, msg)
		return
	}
	now := time.Now().UTC()
	feed := &tables.TableRSSFeed{
		Slug: customRSSFeedSlug(name, now), Name: name, URL: feedURL, IconURL: iconURL,
		Source: tables.RSSFeedSourceCustom, Enabled: req.Enabled == nil || *req.Enabled,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := h.store.CreateRSSFeed(ctx, feed); err != nil {
		if errors.Is(err, configstore.ErrAlreadyExists) {
			SendError(ctx, fasthttp.StatusConflict, "A feed with this name already exists")
			return
		}
		logger.Warn("failed to create rss feed: %v", err)
		SendError(ctx, fasthttp.StatusInternalServerError, "Failed to create rss feed")
		return
	}
	SendJSONWithStatus(ctx, feed, fasthttp.StatusCreated)
}

func (h *RSSFeedsHandler) updateFeed(ctx *fasthttp.RequestCtx) {
	if h.store == nil {
		SendError(ctx, fasthttp.StatusServiceUnavailable, errRSSFeedsUnavailable.Error())
		return
	}
	id, ok := rssFeedIDParam(ctx)
	if !ok {
		SendError(ctx, fasthttp.StatusBadRequest, "invalid feed id")
		return
	}
	var req rssFeedUpdateRequest
	if err := sonic.Unmarshal(ctx.PostBody(), &req); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, "Invalid request payload")
		return
	}
	feed, err := h.store.GetRSSFeed(ctx, id)
	if err != nil {
		h.sendStoreError(ctx, err)
		return
	}
	if feed.Source == tables.RSSFeedSourceBuiltin && (req.Name != nil || req.URL != nil || req.IconURL != nil) {
		SendError(ctx, fasthttp.StatusBadRequest, "Built-in feeds can only be enabled or disabled")
		return
	}
	previousURL := feed.URL
	if req.Name != nil || req.URL != nil {
		name, feedURL := feed.Name, feed.URL
		if req.Name != nil {
			name = *req.Name
		}
		if req.URL != nil {
			feedURL = *req.URL
		}
		name, feedURL, err = validateRSSFeedInput(name, feedURL)
		if err != nil {
			SendError(ctx, fasthttp.StatusBadRequest, err.Error())
			return
		}
		if feedURL != previousURL {
			if msg := h.probeFeed(feedURL); msg != "" {
				SendError(ctx, fasthttp.StatusBadRequest, msg)
				return
			}
		}
		feed.Name, feed.URL = name, feedURL
	}
	if req.IconURL != nil {
		iconURL, iconErr := validateRSSFeedIconURL(*req.IconURL)
		if iconErr != nil {
			SendError(ctx, fasthttp.StatusBadRequest, iconErr.Error())
			return
		}
		feed.IconURL = iconURL
	}
	if req.Enabled != nil {
		feed.Enabled = *req.Enabled
	}
	if err := h.store.UpdateRSSFeed(ctx, feed); err != nil {
		h.sendStoreError(ctx, err)
		return
	}
	if feed.URL != previousURL {
		h.manager.Forget(previousURL)
	}
	feed.UpdatedAt = time.Now().UTC()
	SendJSON(ctx, feed)
}

func (h *RSSFeedsHandler) deleteFeed(ctx *fasthttp.RequestCtx) {
	if h.store == nil {
		SendError(ctx, fasthttp.StatusServiceUnavailable, errRSSFeedsUnavailable.Error())
		return
	}
	id, ok := rssFeedIDParam(ctx)
	if !ok {
		SendError(ctx, fasthttp.StatusBadRequest, "invalid feed id")
		return
	}
	feed, err := h.store.GetRSSFeed(ctx, id)
	if err != nil {
		h.sendStoreError(ctx, err)
		return
	}
	if feed.Source == tables.RSSFeedSourceBuiltin {
		SendError(ctx, fasthttp.StatusBadRequest, "Built-in feeds cannot be deleted; disable them instead")
		return
	}
	if err := h.store.DeleteRSSFeed(ctx, id); err != nil {
		h.sendStoreError(ctx, err)
		return
	}
	h.manager.Forget(feed.URL)
	SendJSON(ctx, map[string]string{"message": "Feed deleted"})
}

// RSSItem is one news entry as returned to the tray, tagged with its source.
type RSSItem struct {
	ID          string    `json:"id"`
	FeedID      uint      `json:"feed_id"`
	FeedSlug    string    `json:"feed_slug"`
	FeedName    string    `json:"feed_name"`
	FeedIcon    string    `json:"feed_icon,omitempty"`
	FeedURL     string    `json:"feed_url"`
	Title       string    `json:"title"`
	Link        string    `json:"link"`
	Summary     string    `json:"summary,omitempty"`
	PublishedAt time.Time `json:"published_at"`
}

type rssFeedError struct {
	FeedID   uint   `json:"feed_id"`
	FeedName string `json:"feed_name"`
	Message  string `json:"message"`
}

type rssItemsResponse struct {
	Enabled   bool            `json:"enabled"`
	Items     []RSSItem       `json:"items"`
	Errors    []rssFeedError  `json:"errors"`
	FeedIcons map[uint]string `json:"feed_icons,omitempty"` // custom logo per feed id, sent once instead of on every item
}

type rssSettings struct {
	Enabled bool `json:"enabled"`
}

type rssSettingsUpdateRequest struct {
	Enabled *bool `json:"enabled"`
}

func (h *RSSFeedsHandler) getSettings(ctx *fasthttp.RequestCtx) {
	if h.store == nil {
		SendError(ctx, fasthttp.StatusServiceUnavailable, errRSSFeedsUnavailable.Error())
		return
	}
	enabled, err := h.store.GetRSSNewsEnabled(ctx)
	if err != nil {
		logger.Warn("failed to read rss news setting: %v", err)
		SendError(ctx, fasthttp.StatusInternalServerError, "Failed to read news settings")
		return
	}
	SendJSON(ctx, rssSettings{Enabled: enabled})
}

// updateSettings flips the gateway-wide news switch. While it is off,
// listItems fetches nothing and the dashboard hides the News tab for everyone;
// feed configuration is kept, so turning it back on restores the same sources.
func (h *RSSFeedsHandler) updateSettings(ctx *fasthttp.RequestCtx) {
	if h.store == nil {
		SendError(ctx, fasthttp.StatusServiceUnavailable, errRSSFeedsUnavailable.Error())
		return
	}
	var req rssSettingsUpdateRequest
	if err := sonic.Unmarshal(ctx.PostBody(), &req); err != nil || req.Enabled == nil {
		SendError(ctx, fasthttp.StatusBadRequest, "enabled is required")
		return
	}
	if err := h.store.SetRSSNewsEnabled(ctx, *req.Enabled); err != nil {
		logger.Warn("failed to store rss news setting: %v", err)
		SendError(ctx, fasthttp.StatusInternalServerError, "Failed to update news settings")
		return
	}
	SendJSON(ctx, rssSettings{Enabled: *req.Enabled})
}

// listItems fans out to every enabled feed in parallel through the manager's
// cache and merges the results newest first. A feed that fails contributes an
// entry to errors instead of failing the response.
func (h *RSSFeedsHandler) listItems(ctx *fasthttp.RequestCtx) {
	if h.store == nil {
		SendError(ctx, fasthttp.StatusServiceUnavailable, errRSSFeedsUnavailable.Error())
		return
	}
	limit := rssItemsDefaultLimit
	if raw := string(ctx.QueryArgs().Peek("limit")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 {
			SendError(ctx, fasthttp.StatusBadRequest, "limit must be a positive integer")
			return
		}
		limit = min(parsed, rssItemsMaxLimit)
	}
	newsEnabled, err := h.store.GetRSSNewsEnabled(ctx)
	if err != nil {
		logger.Warn("failed to read rss news setting: %v", err)
		SendError(ctx, fasthttp.StatusInternalServerError, "Failed to read news settings")
		return
	}
	if !newsEnabled {
		// Switched off gateway-wide: no outbound fetches at all.
		SendJSON(ctx, rssItemsResponse{Enabled: false, Items: []RSSItem{}, Errors: []rssFeedError{}})
		return
	}
	feeds, err := h.store.ListRSSFeeds(ctx)
	if err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, "Failed to list rss feeds")
		return
	}
	enabled := make([]tables.TableRSSFeed, 0, len(feeds))
	for _, feed := range feeds {
		if feed.Enabled {
			enabled = append(enabled, feed)
		}
	}
	fetchCtx, cancel := context.WithTimeout(context.Background(), rssItemsFetchTimeout)
	defer cancel()
	type result struct {
		items []rssfeed.Item
		err   error
	}
	// A feed still waiting for a slot or still fetching when fetchCtx expires is
	// reported as failed. Manager.Get detaches its shared fetch from fetchCtx, so
	// the wait itself must stop at the deadline; an abandoned fetch still finishes
	// in the background and fills the cache for the next request.
	var mu sync.Mutex
	results := make([]result, len(enabled))
	finished := make([]bool, len(enabled))
	var wg sync.WaitGroup
	slots := make(chan struct{}, rssItemsMaxConcurrentFetches)
	for i := range enabled {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			select {
			case slots <- struct{}{}:
			case <-fetchCtx.Done():
				return
			}
			defer func() { <-slots }()
			items, fetchErr := h.manager.Get(fetchCtx, enabled[i].URL)
			mu.Lock()
			results[i] = result{items: items, err: fetchErr}
			finished[i] = true
			mu.Unlock()
		}(i)
	}
	allDone := make(chan struct{})
	go func() {
		wg.Wait()
		close(allDone)
	}()
	select {
	case <-allDone:
	case <-fetchCtx.Done():
	}
	// Copied under the lock: abandoned goroutines may still write to results.
	collected := make([]result, len(enabled))
	mu.Lock()
	for i := range enabled {
		collected[i] = results[i]
		if !finished[i] {
			collected[i] = result{err: fetchCtx.Err()}
		}
	}
	mu.Unlock()
	response := rssItemsResponse{Enabled: true, Items: []RSSItem{}, Errors: []rssFeedError{}, FeedIcons: map[uint]string{}}
	for i, feed := range enabled {
		if collected[i].err != nil {
			logger.Debug("rss feed %q (%s) failed: %v", feed.Name, feed.URL, collected[i].err)
			// Bifrost's own feed failing is nothing an admin can fix, so it is only logged.
			if feed.Slug == configstore.BifrostAnnouncementsFeedSlug {
				continue
			}
			response.Errors = append(response.Errors, rssFeedError{FeedID: feed.ID, FeedName: feed.Name, Message: rssFeedUnavailableMessage})
			continue
		}
		if feed.IconURL != "" {
			response.FeedIcons[feed.ID] = feed.IconURL
		}
		for _, item := range collected[i].items {
			if !isWebURL(item.Link) {
				continue
			}
			response.Items = append(response.Items, RSSItem{
				ID: feed.Slug + ":" + item.GUID, FeedID: feed.ID, FeedSlug: feed.Slug, FeedName: feed.Name,
				FeedIcon: feed.Icon, FeedURL: feed.URL, Title: item.Title, Link: item.Link,
				Summary: item.Summary, PublishedAt: item.PublishedAt,
			})
		}
	}
	sort.SliceStable(response.Items, func(i, j int) bool {
		return response.Items[i].PublishedAt.After(response.Items[j].PublishedAt)
	})
	if len(response.Items) > limit {
		response.Items = response.Items[:limit]
	}
	SendJSON(ctx, response)
}

// probeFeed fetches and parses url once before it is saved, so a typo or an
// HTML page is rejected at the form instead of failing silently in the tray.
// It returns an empty string on success.
func (h *RSSFeedsHandler) probeFeed(feedURL string) string {
	probeCtx, cancel := context.WithTimeout(context.Background(), rssItemsFetchTimeout)
	defer cancel()
	if _, err := h.manager.Fetch(probeCtx, feedURL); err != nil {
		logger.Debug("rss feed probe for %s failed: %v", feedURL, err)
		if errors.Is(err, rssfeed.ErrNotAFeed) || strings.HasPrefix(err.Error(), "parse feed:") {
			return "URL did not return a valid RSS or Atom feed"
		}
		if strings.HasPrefix(err.Error(), "invalid feed url:") || strings.Contains(err.Error(), "non-public address") {
			return "URL must be a public http or https address"
		}
		return "Feed could not be fetched from this URL"
	}
	return ""
}

func (h *RSSFeedsHandler) sendStoreError(ctx *fasthttp.RequestCtx, err error) {
	if errors.Is(err, configstore.ErrRSSFeedNotFound) {
		SendError(ctx, fasthttp.StatusNotFound, "Feed not found")
		return
	}
	logger.Warn("rss feed store operation failed: %v", err)
	SendError(ctx, fasthttp.StatusInternalServerError, "Failed to update rss feed")
}

func validateRSSFeedInput(name, feedURL string) (string, string, error) {
	name = strings.TrimSpace(name)
	feedURL = strings.TrimSpace(feedURL)
	if name == "" || len(name) > rssFeedNameMaxLen {
		return "", "", errors.New("name is required and must not exceed 120 characters")
	}
	if feedURL == "" || len(feedURL) > rssFeedURLMaxLen || !isWebURL(feedURL) {
		return "", "", errors.New("url must be an http or https URL")
	}
	return name, feedURL, nil
}

var rssFeedIconDataURIPattern = regexp.MustCompile(`^data:image/(png|jpeg|gif|webp|svg\+xml|x-icon|vnd\.microsoft\.icon);base64,[A-Za-z0-9+/]+={0,2}$`)

// validateRSSFeedIconURL accepts an empty value (no custom logo), an http(s)
// image URL, or a base64 data:image URI for an uploaded logo. The dashboard
// only ever renders it as an <img> src, where SVG scripts do not run.
func validateRSSFeedIconURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	switch {
	case raw == "":
		return "", nil
	case strings.HasPrefix(raw, "data:"):
		if len(raw) > rssFeedIconURLMaxLen || !rssFeedIconDataURIPattern.MatchString(raw) {
			return "", errors.New("icon_url must be a base64 PNG, JPEG, GIF, WebP, SVG or ICO image under 96 KB")
		}
		return raw, nil
	case len(raw) <= rssFeedURLMaxLen && isWebURL(raw):
		return raw, nil
	default:
		return "", errors.New("icon_url must be an http or https image URL or an uploaded image")
	}
}

func isWebURL(raw string) bool {
	parsed, err := url.Parse(raw)
	if err != nil {
		return false
	}
	return (parsed.Scheme == "http" || parsed.Scheme == "https") && parsed.Host != ""
}

var slugUnsafe = regexp.MustCompile(`[^a-z0-9]+`)

// customRSSFeedSlug derives a slug from the name, suffixed with a timestamp so
// it never collides with a builtin slug or another custom feed of the same name.
func customRSSFeedSlug(name string, now time.Time) string {
	base := strings.Trim(slugUnsafe.ReplaceAllString(strings.ToLower(name), "-"), "-")
	if len(base) > 40 {
		base = strings.Trim(base[:40], "-")
	}
	if base == "" {
		base = "feed"
	}
	return "custom-" + base + "-" + strconv.FormatInt(now.UnixNano()%1e9, 36)
}

func rssFeedIDParam(ctx *fasthttp.RequestCtx) (uint, bool) {
	raw, _ := ctx.UserValue("id").(string)
	id, err := strconv.ParseUint(raw, 10, 32)
	if err != nil || id == 0 {
		return 0, false
	}
	return uint(id), true
}
