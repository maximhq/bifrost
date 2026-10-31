package rssfeed

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"sync/atomic"
	"testing"
	"time"
)

// newsListingFixture mirrors the card markup of anthropic.com/news: each entry
// is an anchor wrapping a heading, a <time> and a summary paragraph.
const newsListingFixture = `<!DOCTYPE html><html><body>
<nav><a href="/">Home</a><a href="/news">News</a><a href="/careers"><h3>Careers</h3></a></nav>
<a href="/news/older-post" class="card"><h3>Older post</h3><time>Oct 6, 2026</time><p>Older summary.</p></a>
<a href="/claude-haiku" class="card"><span>Announcements</span><time datetime="2026-10-07T09:00:00Z">Oct 7, 2026</time><h4>Introducing &amp; launching Haiku</h4><p>Fastest <b>model</b>.</p></a>
<a href="https://www.example.com/news/newest#top"><h2>Newest post</h2><time>October 13, 2026</time></a>
<a href="https://www.example.com/news/newest"><h2>Duplicate of newest</h2></a>
<a href="https://other.example.org/news/offsite"><h2>Offsite link</h2></a>
<a href="/news/undated"><h3>Undated post</h3></a>
<ul><li><a href="/news/list-row" class="listItem"><div><time>Sep 23, 2026</time><span>Science</span></div><span class="list__title body-3">List row post </span></a></li></ul>
<a href="/pricing"><span class="nav-title">Pricing</span></a>
</body></html>`

func TestParseHTMLPageExtractsCards(t *testing.T) {
	base, _ := url.Parse("https://www.example.com/news")
	items, err := ParseHTMLPage([]byte(newsListingFixture), base)
	if err != nil {
		t.Fatalf("ParseHTMLPage: %v", err)
	}
	want := []struct {
		title, link string
		date        time.Time
	}{
		{"Newest post", "https://www.example.com/news/newest", time.Date(2026, 10, 13, 0, 0, 0, 0, time.UTC)},
		{"Introducing & launching Haiku", "https://www.example.com/claude-haiku", time.Date(2026, 10, 7, 9, 0, 0, 0, time.UTC)},
		{"Older post", "https://www.example.com/news/older-post", time.Date(2026, 10, 6, 0, 0, 0, 0, time.UTC)},
		{"List row post", "https://www.example.com/news/list-row", time.Date(2026, 9, 23, 0, 0, 0, 0, time.UTC)},
		{"Careers", "https://www.example.com/careers", time.Time{}},
		{"Undated post", "https://www.example.com/news/undated", time.Time{}},
	}
	if len(items) != len(want) {
		t.Fatalf("expected %d items (offsite, duplicate and undated nav title dropped), got %d: %+v", len(want), len(items), items)
	}
	for i, w := range want {
		if items[i].Title != w.title || items[i].Link != w.link || !items[i].PublishedAt.Equal(w.date) {
			t.Errorf("item %d = {%q %q %v}, want {%q %q %v}", i, items[i].Title, items[i].Link, items[i].PublishedAt, w.title, w.link, w.date)
		}
	}
	if items[1].Summary != "Fastest model." {
		t.Errorf("summary not cleaned: %q", items[1].Summary)
	}
}

func TestParseHTMLPageRejectsNonListings(t *testing.T) {
	base, _ := url.Parse("https://www.example.com/")
	page := `<html><body><a href="/a"><h1>Only one headline</h1></a><a href="/b">plain link</a></body></html>`
	if _, err := ParseHTMLPage([]byte(page), base); !errors.Is(err, ErrNotAFeed) {
		t.Errorf("expected ErrNotAFeed for a page with too few cards, got %v", err)
	}
}

func TestManagerFallsBackToHTMLListing(t *testing.T) {
	var hits atomic.Int32
	srv := newFeedServer(t, &hits, func() (int, string) { return http.StatusOK, newsListingFixture })
	m := NewManagerWithClient(srv.Client())
	items, err := m.Fetch(context.Background(), srv.URL+"/news")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	// Absolute links to www.example.com are offsite relative to the test server.
	if len(items) != 5 || items[0].Link != srv.URL+"/claude-haiku" {
		t.Fatalf("expected the listing's same-host cards, got %+v", items)
	}
}
