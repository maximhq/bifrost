// Package rssfeed fetches and parses RSS 2.0 and Atom feeds for the dashboard
// news tray. It uses encoding/xml only: the feeds it reads are small, and the
// subset of either format the tray renders (title, link, summary, date, id) is
// narrow enough that a full feed library would be dead weight.
package rssfeed

import (
	"bytes"
	"encoding/xml"
	"errors"
	"html"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/net/html/charset"
)

const (
	// MaxItemsPerFeed caps what one feed contributes to the tray.
	MaxItemsPerFeed = 20
	maxSummaryRunes = 280
	maxTitleRunes   = 300
)

// ErrNotAFeed is returned when the document parses as XML but is neither an
// RSS nor an Atom feed (for example an HTML page served at the feed URL).
var ErrNotAFeed = errors.New("document is not an RSS or Atom feed")

// Item is one normalized entry from a feed.
type Item struct {
	GUID        string    `json:"guid"`
	Title       string    `json:"title"`
	Link        string    `json:"link"`
	Summary     string    `json:"summary,omitempty"`
	PublishedAt time.Time `json:"published_at"`
}

type rssDocument struct {
	XMLName xml.Name
	Channel struct {
		Items []rssItem `xml:"item"`
	} `xml:"channel"`
	// RSS 1.0 (RDF) puts items beside the channel rather than inside it.
	Items []rssItem `xml:"item"`
	// Atom entries, when the root element is <feed>.
	Entries []atomEntry `xml:"entry"`
}

type rssItem struct {
	Title       string `xml:"title"`
	Link        string `xml:"link"`
	GUID        string `xml:"guid"`
	Description string `xml:"description"`
	Content     string `xml:"http://purl.org/rss/1.0/modules/content/ encoded"`
	PubDate     string `xml:"pubDate"`
	DCDate      string `xml:"http://purl.org/dc/elements/1.1/ date"`
}

type atomEntry struct {
	ID        string     `xml:"id"`
	Title     string     `xml:"title"`
	Links     []atomLink `xml:"link"`
	Summary   string     `xml:"summary"`
	Content   string     `xml:"content"`
	Published string     `xml:"published"`
	Updated   string     `xml:"updated"`
}

type atomLink struct {
	Href string `xml:"href,attr"`
	Rel  string `xml:"rel,attr"`
}

// Parse decodes an RSS 2.0, RSS 1.0 or Atom document into at most
// MaxItemsPerFeed items, newest first. Entries without a title and a link are
// dropped, since the tray can neither label nor open them.
func Parse(data []byte) ([]Item, error) {
	decoder := xml.NewDecoder(bytes.NewReader(data))
	decoder.CharsetReader = charset.NewReaderLabel
	decoder.Strict = false
	decoder.Entity = xml.HTMLEntity
	var doc rssDocument
	if err := decoder.Decode(&doc); err != nil {
		return nil, err
	}
	var items []Item
	switch strings.ToLower(doc.XMLName.Local) {
	case "rss", "rdf":
		raw := doc.Channel.Items
		if len(raw) == 0 {
			raw = doc.Items
		}
		items = make([]Item, 0, len(raw))
		for i := range raw {
			if item, ok := normalizeRSSItem(&raw[i]); ok {
				items = append(items, item)
			}
		}
	case "feed":
		items = make([]Item, 0, len(doc.Entries))
		for i := range doc.Entries {
			if item, ok := normalizeAtomEntry(&doc.Entries[i]); ok {
				items = append(items, item)
			}
		}
	default:
		return nil, ErrNotAFeed
	}
	// Stable so undated items keep document order, which feeds publish newest first.
	sort.SliceStable(items, func(i, j int) bool { return items[i].PublishedAt.After(items[j].PublishedAt) })
	if len(items) > MaxItemsPerFeed {
		items = items[:MaxItemsPerFeed]
	}
	return items, nil
}

func normalizeRSSItem(raw *rssItem) (Item, bool) {
	link := strings.TrimSpace(raw.Link)
	title := cleanText(raw.Title, maxTitleRunes)
	if link == "" || title == "" {
		return Item{}, false
	}
	summary := raw.Description
	if strings.TrimSpace(summary) == "" {
		summary = raw.Content
	}
	guid := strings.TrimSpace(raw.GUID)
	if guid == "" {
		guid = link
	}
	date := raw.PubDate
	if strings.TrimSpace(date) == "" {
		date = raw.DCDate
	}
	return Item{GUID: guid, Title: title, Link: link, Summary: cleanText(summary, maxSummaryRunes), PublishedAt: parseDate(date)}, true
}

func normalizeAtomEntry(raw *atomEntry) (Item, bool) {
	link := ""
	for _, l := range raw.Links {
		if l.Rel == "" || l.Rel == "alternate" {
			link = strings.TrimSpace(l.Href)
			break
		}
	}
	if link == "" && len(raw.Links) > 0 {
		link = strings.TrimSpace(raw.Links[0].Href)
	}
	title := cleanText(raw.Title, maxTitleRunes)
	if link == "" || title == "" {
		return Item{}, false
	}
	summary := raw.Summary
	if strings.TrimSpace(summary) == "" {
		summary = raw.Content
	}
	guid := strings.TrimSpace(raw.ID)
	if guid == "" {
		guid = link
	}
	date := raw.Published
	if strings.TrimSpace(date) == "" {
		date = raw.Updated
	}
	return Item{GUID: guid, Title: title, Link: link, Summary: cleanText(summary, maxSummaryRunes), PublishedAt: parseDate(date)}, true
}

var (
	tagPattern        = regexp.MustCompile(`(?s)<[^>]*>`)
	whitespacePattern = regexp.MustCompile(`\s+`)
)

// cleanText strips markup and entities from a feed field, collapses whitespace
// and truncates to limit runes. Feed summaries are routinely full HTML
// articles; the tray renders plain text only.
func cleanText(value string, limit int) string {
	value = tagPattern.ReplaceAllString(value, " ")
	value = html.UnescapeString(value)
	value = strings.TrimSpace(whitespacePattern.ReplaceAllString(value, " "))
	if utf8.RuneCountInString(value) <= limit {
		return value
	}
	runes := []rune(value)
	return strings.TrimSpace(string(runes[:limit-1])) + "…"
}

var dateLayouts = []string{
	time.RFC1123Z,
	time.RFC1123,
	time.RFC3339,
	time.RFC3339Nano,
	"Mon, 2 Jan 2006 15:04:05 -0700",
	"Mon, 2 Jan 2006 15:04:05 MST",
	"Mon, 02 Jan 2006 15:04:05 GMT",
	"2 Jan 2006 15:04:05 -0700",
	"2006-01-02T15:04:05",
	"2006-01-02",
	// Human-readable dates on HTML news listing pages (see ParseHTMLPage).
	"Jan 2, 2006",
	"January 2, 2006",
	"2 Jan 2006",
	"2 January 2006",
}

// parseDate accepts the RFC 822 variants RSS uses in practice, the RFC 3339
// forms Atom requires, and the plain dates HTML listing pages print. An unparseable date yields the zero time, which sorts
// last rather than failing the whole feed.
func parseDate(value string) time.Time {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}
	}
	for _, layout := range dateLayouts {
		if t, err := time.Parse(layout, value); err == nil {
			return t.UTC()
		}
	}
	return time.Time{}
}
