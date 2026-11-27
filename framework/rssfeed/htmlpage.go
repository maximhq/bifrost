package rssfeed

import (
	"bytes"
	"net/url"
	"sort"
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// minHTMLPageItems is how many article cards a page must yield before it is
// treated as a news listing. Below it, the page is more likely a landing page
// whose few headline links are navigation, so it stays "not a feed".
const minHTMLPageItems = 3

// ParseHTMLPage extracts entries from a news listing page that publishes no
// feed (anthropic.com/news is the motivating case). An entry is an <a href>
// that wraps either a heading, or a <time> plus an element whose class names a
// title (the compact list rows such pages use). The heading or title element
// is the title, a <time> inside the card is the date (its datetime attribute,
// else its text), and the first <p> is the summary. Relative links resolve against base. Links that leave base's host
// are ignored, as are duplicates.
func ParseHTMLPage(data []byte, base *url.URL) ([]Item, error) {
	doc, err := html.Parse(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	seen := make(map[string]struct{})
	var items []Item
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && n.DataAtom == atom.A {
			if item, ok := htmlCardItem(n, base); ok {
				if _, dup := seen[item.Link]; !dup {
					seen[item.Link] = struct{}{}
					items = append(items, item)
				}
			}
			return
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	if len(items) < minHTMLPageItems {
		return nil, ErrNotAFeed
	}
	sort.SliceStable(items, func(i, j int) bool { return items[i].PublishedAt.After(items[j].PublishedAt) })
	if len(items) > MaxItemsPerFeed {
		items = items[:MaxItemsPerFeed]
	}
	return items, nil
}

func htmlCardItem(a *html.Node, base *url.URL) (Item, bool) {
	href := strings.TrimSpace(htmlAttr(a, "href"))
	if href == "" || strings.HasPrefix(href, "#") {
		return Item{}, false
	}
	ref, err := url.Parse(href)
	if err != nil {
		return Item{}, false
	}
	link := base.ResolveReference(ref)
	if (link.Scheme != "http" && link.Scheme != "https") || !strings.EqualFold(link.Hostname(), base.Hostname()) {
		return Item{}, false
	}
	// The listing page itself (and its own anchors) is never an entry.
	if strings.TrimSuffix(link.Path, "/") == strings.TrimSuffix(base.Path, "/") {
		return Item{}, false
	}
	link.Fragment = ""

	timeNode := findFirst(a, func(n *html.Node) bool { return n.DataAtom == atom.Time })
	heading := findFirst(a, isHeading)
	if heading == nil && timeNode != nil {
		// Requiring a <time> keeps plain navigation links with a "title" class out.
		heading = findFirst(a, hasTitleClass)
	}
	if heading == nil {
		return Item{}, false
	}
	title := cleanText(nodeText(heading), maxTitleRunes)
	if title == "" {
		return Item{}, false
	}
	item := Item{GUID: link.String(), Title: title, Link: link.String()}
	if t := timeNode; t != nil {
		if dt := htmlAttr(t, "datetime"); dt != "" {
			item.PublishedAt = parseDate(dt)
		}
		if item.PublishedAt.IsZero() {
			item.PublishedAt = parseDate(nodeText(t))
		}
	}
	if p := findFirst(a, func(n *html.Node) bool { return n.DataAtom == atom.P }); p != nil {
		item.Summary = cleanText(nodeText(p), maxSummaryRunes)
	}
	return item, true
}

func isHeading(n *html.Node) bool {
	switch n.DataAtom {
	case atom.H1, atom.H2, atom.H3, atom.H4, atom.H5, atom.H6:
		return true
	}
	return false
}

func hasTitleClass(n *html.Node) bool {
	for _, class := range strings.Fields(htmlAttr(n, "class")) {
		if strings.Contains(strings.ToLower(class), "title") {
			return true
		}
	}
	return false
}

func findFirst(n *html.Node, match func(*html.Node) bool) *html.Node {
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if c.Type == html.ElementNode && match(c) {
			return c
		}
		if found := findFirst(c, match); found != nil {
			return found
		}
	}
	return nil
}

func nodeText(n *html.Node) string {
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.TextNode {
			b.WriteString(n.Data)
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return b.String()
}

func htmlAttr(n *html.Node, key string) string {
	for _, attr := range n.Attr {
		if attr.Key == key {
			return attr.Val
		}
	}
	return ""
}

// looksLikeHTML reports whether a body that failed feed parsing is an HTML
// document worth trying ParseHTMLPage on.
func looksLikeHTML(contentType string, body []byte) bool {
	if strings.Contains(strings.ToLower(contentType), "html") {
		return true
	}
	head := bytes.ToLower(bytes.TrimSpace(body[:min(len(body), 512)]))
	return bytes.HasPrefix(head, []byte("<!doctype html")) || bytes.HasPrefix(head, []byte("<html"))
}
