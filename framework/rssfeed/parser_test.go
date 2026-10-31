package rssfeed

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

const rss2Fixture = `<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0" xmlns:content="http://purl.org/rss/1.0/modules/content/" xmlns:dc="http://purl.org/dc/elements/1.1/">
<channel>
  <title>Example</title>
  <item>
    <title>Older post</title>
    <link>https://example.com/older</link>
    <guid isPermaLink="false">older-1</guid>
    <description>&lt;p&gt;Hello &amp;amp; &lt;b&gt;welcome&lt;/b&gt;&lt;/p&gt;</description>
    <pubDate>Mon, 02 Jan 2006 15:04:05 GMT</pubDate>
  </item>
  <item>
    <title><![CDATA[Newer <em>post</em>]]></title>
    <link>https://example.com/newer</link>
    <content:encoded><![CDATA[<p>Body only in content</p>]]></content:encoded>
    <dc:date>2024-05-01T10:00:00Z</dc:date>
  </item>
  <item>
    <title>No link is dropped</title>
  </item>
</channel>
</rss>`

const atomFixture = `<?xml version="1.0" encoding="utf-8"?>
<feed xmlns="http://www.w3.org/2005/Atom">
  <title>Releases</title>
  <entry>
    <id>tag:github.com,2008:v1</id>
    <title>v1.0.0</title>
    <link rel="alternate" type="text/html" href="https://example.com/v1"/>
    <updated>2024-01-01T00:00:00Z</updated>
    <content type="html">&lt;h2&gt;Changes&lt;/h2&gt;</content>
  </entry>
  <entry>
    <id>tag:github.com,2008:v2</id>
    <title>v2.0.0</title>
    <link rel="self" href="https://example.com/self"/>
    <link href="https://example.com/v2"/>
    <published>2024-06-01T00:00:00+02:00</published>
    <summary>Second release</summary>
  </entry>
</feed>`

func TestParseRSS2(t *testing.T) {
	items, err := Parse([]byte(rss2Fixture))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("expected 2 items (linkless one dropped), got %d", len(items))
	}
	newer, older := items[0], items[1]
	if newer.Title != "Newer post" || newer.Link != "https://example.com/newer" || newer.GUID != "https://example.com/newer" {
		t.Errorf("newer item not normalized: %+v", newer)
	}
	if newer.Summary != "Body only in content" {
		t.Errorf("expected content:encoded fallback, got %q", newer.Summary)
	}
	if !newer.PublishedAt.Equal(time.Date(2024, 5, 1, 10, 0, 0, 0, time.UTC)) {
		t.Errorf("dc:date not parsed: %v", newer.PublishedAt)
	}
	if older.GUID != "older-1" || older.Summary != "Hello & welcome" {
		t.Errorf("older item not normalized: %+v", older)
	}
	if !older.PublishedAt.Equal(time.Date(2006, 1, 2, 15, 4, 5, 0, time.UTC)) {
		t.Errorf("pubDate not parsed: %v", older.PublishedAt)
	}
}

func TestParseAtom(t *testing.T) {
	items, err := Parse([]byte(atomFixture))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("expected 2 items, got %d", len(items))
	}
	if items[0].Title != "v2.0.0" || items[0].Link != "https://example.com/v2" || items[0].Summary != "Second release" {
		t.Errorf("v2 entry not normalized (self link must be skipped): %+v", items[0])
	}
	if items[1].Link != "https://example.com/v1" || items[1].Summary != "Changes" || items[1].GUID != "tag:github.com,2008:v1" {
		t.Errorf("v1 entry not normalized: %+v", items[1])
	}
	if !items[0].PublishedAt.Equal(time.Date(2024, 5, 31, 22, 0, 0, 0, time.UTC)) {
		t.Errorf("published offset not normalized to UTC: %v", items[0].PublishedAt)
	}
}

func TestParseRejectsNonFeeds(t *testing.T) {
	if _, err := Parse([]byte(`<!DOCTYPE html><html><body>nope</body></html>`)); !errors.Is(err, ErrNotAFeed) {
		t.Errorf("html page: expected ErrNotAFeed, got %v", err)
	}
	if _, err := Parse([]byte(`not xml at all`)); err == nil {
		t.Error("garbage: expected an error")
	}
}

func TestParseCapsItemsAndTruncatesSummary(t *testing.T) {
	var b strings.Builder
	b.WriteString(`<rss version="2.0"><channel>`)
	for i := 0; i < MaxItemsPerFeed+5; i++ {
		fmt.Fprintf(&b, `<item><title>t%d</title><link>https://example.com/%d</link><description>%s</description></item>`, i, i, strings.Repeat("word ", 200))
	}
	b.WriteString(`</channel></rss>`)
	items, err := Parse([]byte(b.String()))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(items) != MaxItemsPerFeed {
		t.Errorf("expected cap of %d items, got %d", MaxItemsPerFeed, len(items))
	}
	if items[0].Title != "t0" {
		t.Errorf("undated items must keep document order, first was %q", items[0].Title)
	}
	if n := len([]rune(items[0].Summary)); n > maxSummaryRunes || !strings.HasSuffix(items[0].Summary, "…") {
		t.Errorf("summary not truncated: %d runes", n)
	}
}

func TestParseDateFormats(t *testing.T) {
	want := time.Date(2024, 3, 5, 8, 9, 10, 0, time.UTC)
	for _, value := range []string{
		"Tue, 05 Mar 2024 08:09:10 +0000",
		"Tue, 5 Mar 2024 08:09:10 +0000",
		"Tue, 05 Mar 2024 08:09:10 GMT",
		"2024-03-05T08:09:10Z",
		"2024-03-05T09:09:10+01:00",
	} {
		if got := parseDate(value); !got.Equal(want) {
			t.Errorf("parseDate(%q) = %v, want %v", value, got, want)
		}
	}
	if got := parseDate("sometime last week"); !got.IsZero() {
		t.Errorf("unparseable date should be zero, got %v", got)
	}
}
