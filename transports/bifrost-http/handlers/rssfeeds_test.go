package handlers

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/rssfeed"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

type memoryRSSFeedStore struct {
	feeds        []tables.TableRSSFeed
	nextID       uint
	newsDisabled bool
	writeErr     error // returned by UpdateRSSFeed and DeleteRSSFeed when set
}

func (s *memoryRSSFeedStore) GetRSSNewsEnabled(context.Context) (bool, error) {
	return !s.newsDisabled, nil
}

func (s *memoryRSSFeedStore) SetRSSNewsEnabled(_ context.Context, enabled bool) error {
	s.newsDisabled = !enabled
	return nil
}

func (s *memoryRSSFeedStore) ListRSSFeeds(context.Context) ([]tables.TableRSSFeed, error) {
	return append([]tables.TableRSSFeed(nil), s.feeds...), nil
}

func (s *memoryRSSFeedStore) GetRSSFeed(_ context.Context, id uint) (*tables.TableRSSFeed, error) {
	for i := range s.feeds {
		if s.feeds[i].ID == id {
			feed := s.feeds[i]
			return &feed, nil
		}
	}
	return nil, configstore.ErrRSSFeedNotFound
}

func (s *memoryRSSFeedStore) CreateRSSFeed(_ context.Context, feed *tables.TableRSSFeed) error {
	s.nextID++
	feed.ID = s.nextID
	s.feeds = append(s.feeds, *feed)
	return nil
}

func (s *memoryRSSFeedStore) UpdateRSSFeed(_ context.Context, feed *tables.TableRSSFeed) error {
	if s.writeErr != nil {
		return s.writeErr
	}
	for i := range s.feeds {
		if s.feeds[i].ID == feed.ID {
			s.feeds[i] = *feed
			return nil
		}
	}
	return configstore.ErrRSSFeedNotFound
}

func (s *memoryRSSFeedStore) DeleteRSSFeed(_ context.Context, id uint) error {
	if s.writeErr != nil {
		return s.writeErr
	}
	for i := range s.feeds {
		if s.feeds[i].ID == id {
			s.feeds = append(s.feeds[:i], s.feeds[i+1:]...)
			return nil
		}
	}
	return configstore.ErrRSSFeedNotFound
}

func (s *memoryRSSFeedStore) SeedBuiltinRSSFeeds(context.Context) error { return nil }

const testRSSFeedBody = `<rss version="2.0"><channel>
<item><title>Old</title><link>https://example.com/old</link><guid>old</guid><pubDate>Mon, 01 Jan 2024 00:00:00 GMT</pubDate></item>
<item><title>Script link</title><link>javascript:alert(1)</link><guid>bad</guid><pubDate>Mon, 01 Jan 2024 00:00:00 GMT</pubDate></item>
</channel></rss>`

const testAtomFeedBody = `<feed xmlns="http://www.w3.org/2005/Atom">
<entry><id>new</id><title>New</title><link href="https://example.com/new"/><updated>2025-01-01T00:00:00Z</updated></entry>
</feed>`

const testRDFFeedBody = `<rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#" xmlns="http://purl.org/rss/1.0/">
<channel><title>RDF</title></channel>
<item><title>RDF item</title><link>https://example.com/rdf</link></item>
</rdf:RDF>`

const testListingPageBody = `<!DOCTYPE html><html><body>
<a href="/news/one"><h2>One</h2><time datetime="2026-10-01T00:00:00Z">Oct 1</time></a>
<a href="/news/two"><h2>Two</h2><time datetime="2026-10-02T00:00:00Z">Oct 2</time></a>
<a href="/news/three"><h2>Three</h2><time datetime="2026-10-03T00:00:00Z">Oct 3</time></a>
</body></html>`

func newRSSFeedTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/rss", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(testRSSFeedBody)) })
	mux.HandleFunc("/atom", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(testAtomFeedBody)) })
	mux.HandleFunc("/rdf", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(testRDFFeedBody)) })
	mux.HandleFunc("/listing", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(testListingPageBody)) })
	mux.HandleFunc("/html", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("<html><body>hi</body></html>")) })
	mux.HandleFunc("/down", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusBadGateway) })
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func newTestRSSFeedsHandler(srv *httptest.Server, feeds ...tables.TableRSSFeed) (*RSSFeedsHandler, *memoryRSSFeedStore) {
	store := &memoryRSSFeedStore{feeds: feeds, nextID: uint(len(feeds))}
	return &RSSFeedsHandler{store: store, manager: rssfeed.NewManagerWithClient(srv.Client())}, store
}

func rssRequest(body string, id uint) *fasthttp.RequestCtx {
	ctx := &fasthttp.RequestCtx{}
	if body != "" {
		ctx.Request.SetBody([]byte(body))
	}
	if id != 0 {
		ctx.SetUserValue("id", strconv.FormatUint(uint64(id), 10))
	}
	return ctx
}

func TestRSSItemsMergesEnabledFeedsAndReportsFailures(t *testing.T) {
	srv := newRSSFeedTestServer(t)
	h, _ := newTestRSSFeedsHandler(srv,
		tables.TableRSSFeed{ID: 1, Slug: "rss", Name: "RSS", URL: srv.URL + "/rss", Icon: "openai", Source: tables.RSSFeedSourceBuiltin, Enabled: true},
		tables.TableRSSFeed{ID: 2, Slug: "atom", Name: "Atom", URL: srv.URL + "/atom", IconURL: "https://example.com/atom.png", Source: tables.RSSFeedSourceCustom, Enabled: true},
		tables.TableRSSFeed{ID: 3, Slug: "down", Name: "Down", URL: srv.URL + "/down", Source: tables.RSSFeedSourceCustom, Enabled: true},
		tables.TableRSSFeed{ID: 4, Slug: "off", Name: "Off", URL: srv.URL + "/atom", Source: tables.RSSFeedSourceCustom, Enabled: false},
		tables.TableRSSFeed{ID: 5, Slug: configstore.BifrostAnnouncementsFeedSlug, Name: "Bifrost", URL: srv.URL + "/down", Source: tables.RSSFeedSourceBuiltin, Enabled: true},
	)
	ctx := rssRequest("", 0)
	h.listItems(ctx)
	require.Equal(t, fasthttp.StatusOK, ctx.Response.StatusCode(), string(ctx.Response.Body()))

	var response rssItemsResponse
	require.NoError(t, sonic.Unmarshal(ctx.Response.Body(), &response))
	require.Len(t, response.Items, 2, "disabled feed skipped, javascript: link dropped")
	require.Equal(t, "atom:new", response.Items[0].ID, "newest first across feeds")
	require.Equal(t, "Atom", response.Items[0].FeedName)
	require.Equal(t, "rss:old", response.Items[1].ID)
	require.Equal(t, "openai", response.Items[1].FeedIcon)
	require.Equal(t, time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC), response.Items[1].PublishedAt)

	require.Len(t, response.Errors, 1, "the Bifrost announcements feed failing is not reported")
	require.Equal(t, uint(3), response.Errors[0].FeedID)
	require.Equal(t, rssFeedUnavailableMessage, response.Errors[0].Message, "upstream detail must not leak")
}

// TestRSSItemsBoundsConcurrentFetches pins that a gateway with many enabled
// feeds fetches at most rssItemsMaxConcurrentFetches of them at once, while
// still returning every feed's items.
func TestRSSItemsBoundsConcurrentFetches(t *testing.T) {
	var inFlight, peak atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		now := inFlight.Add(1)
		defer inFlight.Add(-1)
		for {
			old := peak.Load()
			if now <= old || peak.CompareAndSwap(old, now) {
				break
			}
		}
		time.Sleep(30 * time.Millisecond)
		_, _ = w.Write([]byte(testAtomFeedBody))
	}))
	t.Cleanup(srv.Close)

	const feedCount = 12
	feeds := make([]tables.TableRSSFeed, 0, feedCount)
	for i := 1; i <= feedCount; i++ {
		id := strconv.Itoa(i)
		feeds = append(feeds, tables.TableRSSFeed{ID: uint(i), Slug: "f" + id, Name: "F" + id, URL: srv.URL + "/" + id, Source: tables.RSSFeedSourceCustom, Enabled: true})
	}
	h, _ := newTestRSSFeedsHandler(srv, feeds...)
	ctx := rssRequest("", 0)
	h.listItems(ctx)
	require.Equal(t, fasthttp.StatusOK, ctx.Response.StatusCode(), string(ctx.Response.Body()))

	var response rssItemsResponse
	require.NoError(t, sonic.Unmarshal(ctx.Response.Body(), &response))
	require.Len(t, response.Items, feedCount, "every feed still contributes its item")
	require.Empty(t, response.Errors)
	require.LessOrEqual(t, peak.Load(), int32(rssItemsMaxConcurrentFetches), "fetches must be capped in flight")
}

// TestRSSItemsHonorsFetchDeadline pins that a request with more hanging feeds
// than fetch slots returns at the deadline, reporting every unfinished feed as
// failed, instead of waiting out each fetch's own timeout in turn.
func TestRSSItemsHonorsFetchDeadline(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		<-release
		_, _ = w.Write([]byte(testAtomFeedBody))
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(func() { close(release) })
	previous := rssItemsFetchTimeout
	rssItemsFetchTimeout = 300 * time.Millisecond
	t.Cleanup(func() { rssItemsFetchTimeout = previous })

	const feedCount = rssItemsMaxConcurrentFetches + 1
	feeds := make([]tables.TableRSSFeed, 0, feedCount)
	for i := 1; i <= feedCount; i++ {
		id := strconv.Itoa(i)
		feeds = append(feeds, tables.TableRSSFeed{ID: uint(i), Slug: "f" + id, Name: "F" + id, URL: srv.URL + "/" + id, Source: tables.RSSFeedSourceCustom, Enabled: true})
	}
	h, _ := newTestRSSFeedsHandler(srv, feeds...)
	ctx := rssRequest("", 0)
	start := time.Now()
	h.listItems(ctx)
	elapsed := time.Since(start)
	require.Equal(t, fasthttp.StatusOK, ctx.Response.StatusCode(), string(ctx.Response.Body()))
	require.Less(t, elapsed, 2*time.Second, "the request must return at the deadline")

	var response rssItemsResponse
	require.NoError(t, sonic.Unmarshal(ctx.Response.Body(), &response))
	require.Empty(t, response.Items)
	require.Len(t, response.Errors, feedCount, "every unfinished feed is reported as failed")
}

// TestRSSItemsSendsEachFeedLogoOnce pins that a feed's logo (up to 128 KB as a
// data: URI) is returned once in feed_icons rather than copied into every item.
func TestRSSItemsSendsEachFeedLogoOnce(t *testing.T) {
	var body strings.Builder
	body.WriteString(`<rss version="2.0"><channel>`)
	for i := 0; i < 5; i++ {
		fmt.Fprintf(&body, `<item><title>t%d</title><link>https://example.com/%d</link></item>`, i, i)
	}
	body.WriteString(`</channel></rss>`)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(body.String())) }))
	t.Cleanup(srv.Close)
	const logo = "data:image/png;base64,iVBORw0KGgoAAAANSUhEUg=="
	h, _ := newTestRSSFeedsHandler(srv,
		tables.TableRSSFeed{ID: 7, Slug: "custom-logo", Name: "Logo", URL: srv.URL + "/feed", IconURL: logo, Source: tables.RSSFeedSourceCustom, Enabled: true},
		tables.TableRSSFeed{ID: 8, Slug: "custom-plain", Name: "Plain", URL: srv.URL + "/plain", Source: tables.RSSFeedSourceCustom, Enabled: true},
	)
	ctx := rssRequest("", 0)
	h.listItems(ctx)
	require.Equal(t, fasthttp.StatusOK, ctx.Response.StatusCode(), string(ctx.Response.Body()))

	raw := string(ctx.Response.Body())
	require.Equal(t, 1, strings.Count(raw, logo), "the logo must appear once, not once per item")
	var decoded struct {
		Items     []map[string]any  `json:"items"`
		FeedIcons map[string]string `json:"feed_icons"`
	}
	require.NoError(t, sonic.Unmarshal(ctx.Response.Body(), &decoded))
	require.Len(t, decoded.Items, 10)
	require.Equal(t, map[string]string{"7": logo}, decoded.FeedIcons, "only feeds with a logo are listed, keyed by feed id")
}

// TestRSSItemsClampsLimit pins the documented limit contract: values above
// rssItemsMaxLimit are clamped rather than rejected, and non-positive values
// are a 400.
func TestRSSItemsClampsLimit(t *testing.T) {
	var body strings.Builder
	body.WriteString(`<rss version="2.0"><channel>`)
	for i := 0; i < rssfeed.MaxItemsPerFeed; i++ {
		fmt.Fprintf(&body, `<item><title>t%d</title><link>https://example.com/%d</link></item>`, i, i)
	}
	body.WriteString(`</channel></rss>`)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(body.String())) }))
	t.Cleanup(srv.Close)
	var feeds []tables.TableRSSFeed
	for i := 1; i <= 6; i++ {
		id := strconv.Itoa(i)
		feeds = append(feeds, tables.TableRSSFeed{ID: uint(i), Slug: "f" + id, Name: "F" + id, URL: srv.URL + "/" + id, Source: tables.RSSFeedSourceCustom, Enabled: true})
	}
	h, _ := newTestRSSFeedsHandler(srv, feeds...)

	ctx := rssRequest("", 0)
	ctx.Request.SetRequestURI("/api/rss/items?limit=101")
	h.listItems(ctx)
	require.Equal(t, fasthttp.StatusOK, ctx.Response.StatusCode(), string(ctx.Response.Body()))
	var response rssItemsResponse
	require.NoError(t, sonic.Unmarshal(ctx.Response.Body(), &response))
	require.Len(t, response.Items, rssItemsMaxLimit, "limit above the max is clamped, not rejected")

	ctx = rssRequest("", 0)
	ctx.Request.SetRequestURI("/api/rss/items?limit=0")
	h.listItems(ctx)
	require.Equal(t, fasthttp.StatusBadRequest, ctx.Response.StatusCode())
}

func TestRSSFeedCreateValidatesAndProbes(t *testing.T) {
	srv := newRSSFeedTestServer(t)
	h, store := newTestRSSFeedsHandler(srv)

	for name, body := range map[string]string{
		"missing name": `{"name":"  ","url":"` + srv.URL + `/rss"}`,
		"bad scheme":   `{"name":"x","url":"ftp://example.com/feed"}`,
		"not a feed":   `{"name":"x","url":"` + srv.URL + `/html"}`,
		"unreachable":  `{"name":"x","url":"` + srv.URL + `/down"}`,
	} {
		ctx := rssRequest(body, 0)
		h.createFeed(ctx)
		require.Equal(t, fasthttp.StatusBadRequest, ctx.Response.StatusCode(), name)
	}
	require.Empty(t, store.feeds, "rejected feeds must not be stored")

	ctx := rssRequest(`{"name":"x","url":"`+srv.URL+`/atom","icon_url":"javascript:alert(1)"}`, 0)
	h.createFeed(ctx)
	require.Equal(t, fasthttp.StatusBadRequest, ctx.Response.StatusCode(), "non-image icon_url rejected")

	ctx = rssRequest(`{"name":" My Releases ","url":"`+srv.URL+`/atom","icon_url":"https://example.com/logo.png"}`, 0)
	h.createFeed(ctx)
	require.Equal(t, fasthttp.StatusCreated, ctx.Response.StatusCode(), string(ctx.Response.Body()))
	require.Len(t, store.feeds, 1)
	created := store.feeds[0]
	require.Equal(t, "My Releases", created.Name)
	require.Equal(t, tables.RSSFeedSourceCustom, created.Source)
	require.True(t, created.Enabled, "custom feeds default to enabled")
	require.Equal(t, "https://example.com/logo.png", created.IconURL)
	require.Regexp(t, `^custom-my-releases-`, created.Slug)
}

// TestRSSFeedCreateAcceptsRDFAndListingPages pins the url contract documented
// in the OpenAPI schema: RSS 1.0 feeds and news listing pages are accepted too.
func TestRSSFeedCreateAcceptsRDFAndListingPages(t *testing.T) {
	srv := newRSSFeedTestServer(t)
	h, store := newTestRSSFeedsHandler(srv)
	for _, path := range []string{"/rdf", "/listing"} {
		ctx := rssRequest(`{"name":"feed `+path+`","url":"`+srv.URL+path+`"}`, 0)
		h.createFeed(ctx)
		require.Equal(t, fasthttp.StatusCreated, ctx.Response.StatusCode(), "%s: %s", path, ctx.Response.Body())
	}
	require.Len(t, store.feeds, 2)
}

// TestRSSFeedCreateProbesWhileNewsIsOff pins what the docs say about the news
// switch: it stops item fetching, but adding a feed still fetches its URL once.
func TestRSSFeedCreateProbesWhileNewsIsOff(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte(testAtomFeedBody))
	}))
	t.Cleanup(srv.Close)
	h, store := newTestRSSFeedsHandler(srv)
	store.newsDisabled = true

	ctx := rssRequest(`{"name":"x","url":"`+srv.URL+`/atom"}`, 0)
	h.createFeed(ctx)
	require.Equal(t, fasthttp.StatusCreated, ctx.Response.StatusCode(), string(ctx.Response.Body()))
	require.Equal(t, int32(1), hits.Load(), "the probe runs even while news is off")
}

func TestRSSFeedBuiltinGuards(t *testing.T) {
	srv := newRSSFeedTestServer(t)
	h, store := newTestRSSFeedsHandler(srv,
		tables.TableRSSFeed{ID: 1, Slug: "openai", Name: "OpenAI", URL: srv.URL + "/rss", Source: tables.RSSFeedSourceBuiltin},
	)

	ctx := rssRequest(`{"url":"`+srv.URL+`/atom"}`, 1)
	h.updateFeed(ctx)
	require.Equal(t, fasthttp.StatusBadRequest, ctx.Response.StatusCode(), "builtin URL is fixed")

	ctx = rssRequest(`{"icon_url":"https://example.com/logo.png"}`, 1)
	h.updateFeed(ctx)
	require.Equal(t, fasthttp.StatusBadRequest, ctx.Response.StatusCode(), "builtin logo is fixed")

	ctx = rssRequest("", 1)
	h.deleteFeed(ctx)
	require.Equal(t, fasthttp.StatusBadRequest, ctx.Response.StatusCode(), "builtin cannot be deleted")

	ctx = rssRequest(`{"enabled":true}`, 1)
	h.updateFeed(ctx)
	require.Equal(t, fasthttp.StatusOK, ctx.Response.StatusCode(), string(ctx.Response.Body()))
	require.True(t, store.feeds[0].Enabled)

	ctx = rssRequest(`{"enabled":true}`, 99)
	h.updateFeed(ctx)
	require.Equal(t, fasthttp.StatusNotFound, ctx.Response.StatusCode())
}

func TestRSSFeedCustomUpdateAndDelete(t *testing.T) {
	srv := newRSSFeedTestServer(t)
	h, store := newTestRSSFeedsHandler(srv,
		tables.TableRSSFeed{ID: 1, Slug: "custom-x", Name: "X", URL: srv.URL + "/rss", Source: tables.RSSFeedSourceCustom, Enabled: true},
	)
	// Warm the cache so a URL change has an entry to drop.
	_, err := h.manager.Get(context.Background(), srv.URL+"/rss")
	require.NoError(t, err)

	ctx := rssRequest(`{"url":"`+srv.URL+`/html"}`, 1)
	h.updateFeed(ctx)
	require.Equal(t, fasthttp.StatusBadRequest, ctx.Response.StatusCode(), "new URL is probed")
	require.Equal(t, srv.URL+"/rss", store.feeds[0].URL)

	ctx = rssRequest(`{"name":"Renamed","url":"`+srv.URL+`/atom"}`, 1)
	h.updateFeed(ctx)
	require.Equal(t, fasthttp.StatusOK, ctx.Response.StatusCode(), string(ctx.Response.Body()))
	require.Equal(t, "Renamed", store.feeds[0].Name)
	require.Equal(t, srv.URL+"/atom", store.feeds[0].URL)

	const pngLogo = "data:image/png;base64,iVBORw0KGgo="
	ctx = rssRequest(`{"icon_url":"`+pngLogo+`"}`, 1)
	h.updateFeed(ctx)
	require.Equal(t, fasthttp.StatusOK, ctx.Response.StatusCode(), string(ctx.Response.Body()))
	require.Equal(t, pngLogo, store.feeds[0].IconURL)

	ctx = rssRequest(`{"icon_url":""}`, 1)
	h.updateFeed(ctx)
	require.Equal(t, fasthttp.StatusOK, ctx.Response.StatusCode())
	require.Empty(t, store.feeds[0].IconURL, "empty icon_url clears the logo")

	ctx = rssRequest("", 1)
	h.deleteFeed(ctx)
	require.Equal(t, fasthttp.StatusOK, ctx.Response.StatusCode())
	require.Empty(t, store.feeds)
}

// TestRSSFeedWriteStoreFailureIs500 pins the documented 500 response for
// update and delete when the config store write fails.
func TestRSSFeedWriteStoreFailureIs500(t *testing.T) {
	srv := newRSSFeedTestServer(t)
	h, store := newTestRSSFeedsHandler(srv,
		tables.TableRSSFeed{ID: 1, Slug: "custom-x", Name: "X", URL: srv.URL + "/rss", Source: tables.RSSFeedSourceCustom, Enabled: true},
	)
	store.writeErr = errors.New("database is locked")

	ctx := rssRequest(`{"enabled":false}`, 1)
	h.updateFeed(ctx)
	require.Equal(t, fasthttp.StatusInternalServerError, ctx.Response.StatusCode(), string(ctx.Response.Body()))

	ctx = rssRequest("", 1)
	h.deleteFeed(ctx)
	require.Equal(t, fasthttp.StatusInternalServerError, ctx.Response.StatusCode(), string(ctx.Response.Body()))
}

func TestRSSFeedsUnavailableWithoutStore(t *testing.T) {
	h := NewRSSFeedsHandler(nil, nil)
	ctx := rssRequest("", 0)
	h.listItems(ctx)
	require.Equal(t, fasthttp.StatusServiceUnavailable, ctx.Response.StatusCode())
}

func TestRSSNewsSwitchStopsFetching(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits++
		_, _ = w.Write([]byte(testRSSFeedBody))
	}))
	t.Cleanup(srv.Close)
	h, store := newTestRSSFeedsHandler(srv,
		tables.TableRSSFeed{ID: 1, Slug: "rss", Name: "RSS", URL: srv.URL + "/rss", Source: tables.RSSFeedSourceCustom, Enabled: true},
	)

	ctx := rssRequest(`{}`, 0)
	h.updateSettings(ctx)
	require.Equal(t, fasthttp.StatusBadRequest, ctx.Response.StatusCode(), "enabled is required")

	ctx = rssRequest(`{"enabled":false}`, 0)
	h.updateSettings(ctx)
	require.Equal(t, fasthttp.StatusOK, ctx.Response.StatusCode(), string(ctx.Response.Body()))
	require.True(t, store.newsDisabled)

	ctx = rssRequest("", 0)
	h.getSettings(ctx)
	require.JSONEq(t, `{"enabled":false}`, string(ctx.Response.Body()))

	ctx = rssRequest("", 0)
	h.listItems(ctx)
	require.Equal(t, fasthttp.StatusOK, ctx.Response.StatusCode())
	require.JSONEq(t, `{"enabled":false,"items":[],"errors":[]}`, string(ctx.Response.Body()))
	require.Zero(t, hits, "no feed is fetched while news is off")

	ctx = rssRequest(`{"enabled":true}`, 0)
	h.updateSettings(ctx)
	ctx = rssRequest("", 0)
	h.listItems(ctx)
	var response rssItemsResponse
	require.NoError(t, sonic.Unmarshal(ctx.Response.Body(), &response))
	require.True(t, response.Enabled)
	require.Len(t, response.Items, 1)
	require.Equal(t, 1, hits)
}
