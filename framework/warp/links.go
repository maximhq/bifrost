package warp

import (
	"net/url"
	"regexp"
	"slices"
	"strings"

	"github.com/maximhq/bifrost/framework/mcptools"
)

// Links in an answer are checked against what the tools handed out, because a
// model does not reliably leave a root-relative path alone. One "fixes" what
// looks like a URL with no domain by adding a scheme ("https://workspace/logs"),
// another writes it protocol-relative ("//workspace/logs", which a browser reads
// as a host named workspace), promotes the path to a host ("https://logs?..."),
// invents a domain outright, or escapes the slashes. A pattern per shape kept
// missing the next one: of the links in saved answers, a third were still broken
// after a three-shape repair had run over them. What models do reproduce
// faithfully is the query string - the window's unix seconds, a row's id - so
// that is what identifies a link, not whatever was put in front of it.

// issuedLinks is every Logs link the tools returned in a conversation, keyed by
// its query string in canonical order.
type issuedLinks map[string]string

// issuedLogsLink finds Logs links in a tool result or an earlier answer. The
// path is matched without its leading context on purpose: an earlier answer's
// "https://workspace/logs?..." still carries the query a tool built.
var issuedLogsLink = regexp.MustCompile(`/workspace/logs\?([^\s"'()<>\\]+)`)

// collect records the Logs links in text. The first spelling of a query wins,
// which is the tool's own.
func (issued issuedLinks) collect(text string) {
	for _, match := range issuedLogsLink.FindAllStringSubmatch(text, -1) {
		key, ok := canonicalQuery(match[1])
		if !ok {
			continue
		}
		if _, seen := issued[key]; !seen {
			issued[key] = mcptools.LogsViewPath + "?" + match[1]
		}
	}
}

// canonicalQuery puts a query string in one order and one escaping, so a link
// the model reordered still matches the one it was given.
func canonicalQuery(raw string) (string, bool) {
	values, err := url.ParseQuery(raw)
	if err != nil {
		return "", false
	}
	return values.Encode(), true
}

// logsPageParams is every URL parameter the Logs page reads (its useQueryStates
// block in ui/app/workspace/logs/page.tsx). A Logs link carrying any other
// parameter opens, looks filtered, and shows a wider set than the number beside
// it, so it is treated as invented.
var logsPageParams = map[string]struct{}{
	"parent_request_id": {}, "providers": {}, "models": {}, "aliases": {}, "status": {}, "stop_reasons": {},
	"tool_call_names": {}, "objects": {}, "selected_key_ids": {}, "virtual_key_ids": {}, "routing_rule_ids": {},
	"routing_engine_used": {}, "apps": {}, "user_agents": {}, "complexity_tiers": {}, "complexity_mechanisms": {},
	"session_id": {}, "user_ids": {}, "team_ids": {}, "customer_ids": {}, "business_unit_ids": {}, "project_ids": {},
	"content_search": {}, "request_id": {}, "min_latency": {}, "max_latency": {}, "min_cost": {}, "max_cost": {},
	"min_tokens": {}, "max_tokens": {},
	"start_time": {}, "end_time": {}, "limit": {}, "offset": {}, "sort_by": {}, "order": {}, "polling": {},
	"period": {}, "missing_cost_only": {}, "cache_hit_types": {}, "metadata_filters": {}, "selected_log": {},
	"grouped": {},
}

// markdownLink matches an inline link, allowing one level of brackets in its
// text ("[the [Warp] row](...)").
var markdownLink = regexp.MustCompile(`(\[(?:[^\[\]]|\[[^\[\]]*\])*\])\(([^)]*)\)`)

// linkTitle matches the optional title after a link target.
var linkTitle = regexp.MustCompile(`\s+["'(].*$`)

// sanitizeAnswerLinks makes every link in an answer one that opens, so a broken
// link doesn't reach the client or get persisted to conversation history. A
// link whose query a tool issued becomes the issued link; any other link to the
// Logs page gets its path repaired and its query kept; a link into the
// dashboard that no tool could have returned loses its target and keeps its
// text. External links are left alone. issued may be nil, which skips only the
// first of those.
func sanitizeAnswerLinks(answer string, issued issuedLinks) string {
	answer = fencedIssueLink.ReplaceAllString(answer, "[Request this in Bifrost's issue tracker]($1)\n")
	matches := markdownLink.FindAllStringSubmatchIndex(answer, -1)
	if len(matches) == 0 {
		return answer
	}
	var builder strings.Builder
	last := 0
	for _, match := range matches {
		// An image is not a link, and its source is not the dashboard's.
		if match[0] > 0 && answer[match[0]-1] == '!' {
			continue
		}
		text, target := answer[match[2]:match[3]], answer[match[4]:match[5]]
		builder.WriteString(answer[last:match[0]])
		last = match[1]
		switch resolved, verdict := resolveLinkTarget(target, issued); verdict {
		case linkRewritten:
			builder.WriteString(text + "(" + resolved + ")")
		case linkInvented:
			builder.WriteString(text[1 : len(text)-1])
		default:
			builder.WriteString(answer[match[0]:match[1]])
		}
	}
	builder.WriteString(answer[last:])
	return builder.String()
}

type linkVerdict int

const (
	// linkUntouched is a link that is not the dashboard's to judge.
	linkUntouched linkVerdict = iota
	linkRewritten
	linkInvented
)

// resolveLinkTarget decides what one link target becomes.
//
// "workspace" and "logs" as a host are the path's own segments, reinterpreted
// by a model that wanted a domain, so those count as the dashboard. Any other
// host is somebody else's site unless the query is one a tool issued: a
// genuinely external "https://example.com/logs" is left alone.
func resolveLinkTarget(target string, issued issuedLinks) (string, linkVerdict) {
	raw := strings.TrimSpace(target)
	raw = strings.TrimSuffix(strings.TrimPrefix(raw, "<"), ">")
	raw = linkTitle.ReplaceAllString(raw, "")
	raw = strings.ReplaceAll(raw, `\`, "")
	raw = strings.ReplaceAll(raw, "&amp;", "&")
	// A space the model decoded back out of a search term.
	raw = strings.ReplaceAll(raw, " ", "+")
	if raw == "" || strings.HasPrefix(raw, "#") {
		return "", linkUntouched
	}
	parsed, err := url.Parse(raw)
	if err != nil || (parsed.Scheme != "" && parsed.Host == "") {
		// Unparseable, or a scheme with no host (mailto:, tel:).
		return "", linkUntouched
	}
	host := strings.ToLower(parsed.Host)
	foreign := host != "" && host != "workspace" && host != "logs"

	segments := strings.FieldsFunc(parsed.Path, func(r rune) bool { return r == '/' })
	if !foreign && host != "" {
		segments = append([]string{host}, segments...)
	}
	key, parseable := canonicalQuery(parsed.RawQuery)

	if foreign {
		if len(segments) > 0 && segments[len(segments)-1] == "logs" && parsed.RawQuery != "" && parseable {
			if link, ok := issued[key]; ok {
				return link, linkRewritten
			}
		}
		return "", linkUntouched
	}
	if !slices.Equal(segments, []string{"workspace", "logs"}) && !slices.Equal(segments, []string{"logs"}) {
		return "", linkInvented
	}
	if parsed.RawQuery == "" {
		return mcptools.LogsViewPath, linkRewritten
	}
	if !parseable {
		return "", linkInvented
	}
	if link, ok := issued[key]; ok {
		return link, linkRewritten
	}
	values, _ := url.ParseQuery(parsed.RawQuery)
	for param := range values {
		if _, known := logsPageParams[param]; !known {
			return "", linkInvented
		}
	}
	return mcptools.LogsViewPath + "?" + parsed.RawQuery, linkRewritten
}

// fencedIssueLink matches a fenced code block whose only content is the
// feature-request link from the prompt. The model wrapped it in a block
// labelled "github issue link placeholder", and the dashboard rendered a code
// viewer with a scrollbar where a link belonged. A fence holding anything else
// - real code that happens to contain the URL - does not match.
var fencedIssueLink = regexp.MustCompile(`\x60{3}[^\n\x60]*\n\s*(https://github\.com/maximhq/bifrost/issues/new\S*)\s*\n\x60{3}\n?`)
