package toolsearch

import (
	"math"
	"regexp"
	"sort"
	"strings"
	"unicode"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/tidwall/gjson"
)

// BM25 parameters, the usual defaults.
const (
	bm25K1 = 1.2
	bm25B  = 0.75
)

// parseArguments reads the query and limit from a bifrost_tool_search call.
func parseArguments(arguments string) (string, int) {
	args := gjson.Parse(arguments)
	query := strings.TrimSpace(args.Get("query").String())
	limit := int(args.Get("limit").Int())
	if limit <= 0 {
		limit = defaultLimit
	}
	return query, min(limit, maxLimit)
}

// search returns up to limit deferred tools matching query. "select:a,b" picks tools
// by exact name. Any other query is ranked with BM25 over each tool's name,
// description and parameter names; when nothing scores, the query is tried as a
// case-insensitive regular expression over name and description.
func search(tools []schemas.ResponsesTool, query string, limit int) []schemas.ResponsesTool {
	if query == "" || len(tools) == 0 {
		return nil
	}
	if names, ok := strings.CutPrefix(query, "select:"); ok {
		var picked []schemas.ResponsesTool
		for _, name := range strings.Split(names, ",") {
			name = strings.TrimSpace(name)
			for _, tool := range tools {
				if *tool.Name == name {
					picked = append(picked, tool)
					break
				}
			}
			if len(picked) == limit {
				break
			}
		}
		return picked
	}
	if ranked := rankBM25(tools, tokenize(query), limit); len(ranked) > 0 {
		return ranked
	}
	re, err := regexp.Compile("(?i)" + query)
	if err != nil {
		return nil
	}
	var matched []schemas.ResponsesTool
	for _, tool := range tools {
		if re.MatchString(*tool.Name) || (tool.Description != nil && re.MatchString(*tool.Description)) {
			matched = append(matched, tool)
			if len(matched) == limit {
				break
			}
		}
	}
	return matched
}

// rankBM25 scores each tool's document against the query terms.
func rankBM25(tools []schemas.ResponsesTool, terms []string, limit int) []schemas.ResponsesTool {
	if len(terms) == 0 {
		return nil
	}
	docs := make([][]string, len(tools))
	totalLength := 0
	docFreq := make(map[string]int)
	for i, tool := range tools {
		docs[i] = toolDocument(&tool)
		totalLength += len(docs[i])
		seen := make(map[string]bool, len(docs[i]))
		for _, term := range docs[i] {
			if !seen[term] {
				seen[term] = true
				docFreq[term]++
			}
		}
	}
	avgLength := float64(totalLength) / float64(len(docs))
	n := float64(len(docs))

	type scored struct {
		index int
		score float64
	}
	var results []scored
	for i, doc := range docs {
		termFreq := make(map[string]int, len(doc))
		for _, term := range doc {
			termFreq[term]++
		}
		score := 0.0
		for _, term := range terms {
			tf := float64(termFreq[term])
			if tf == 0 {
				continue
			}
			df := float64(docFreq[term])
			idf := math.Log(1 + (n-df+0.5)/(df+0.5))
			score += idf * tf * (bm25K1 + 1) / (tf + bm25K1*(1-bm25B+bm25B*float64(len(doc))/avgLength))
		}
		if score > 0 {
			results = append(results, scored{i, score})
		}
	}
	sort.SliceStable(results, func(a, b int) bool { return results[a].score > results[b].score })
	if len(results) > limit {
		results = results[:limit]
	}
	ranked := make([]schemas.ResponsesTool, 0, len(results))
	for _, r := range results {
		ranked = append(ranked, tools[r.index])
	}
	return ranked
}

// toolDocument is the term list BM25 indexes for a tool. The name counts twice,
// since it is the strongest signal of what the tool does.
func toolDocument(tool *schemas.ResponsesTool) []string {
	nameTerms := tokenize(*tool.Name)
	doc := append(append([]string{}, nameTerms...), nameTerms...)
	if tool.Description != nil {
		doc = append(doc, tokenize(*tool.Description)...)
	}
	if tool.ResponsesToolFunction != nil && tool.ResponsesToolFunction.Parameters != nil &&
		tool.ResponsesToolFunction.Parameters.Properties != nil {
		for _, key := range tool.ResponsesToolFunction.Parameters.Properties.Keys() {
			doc = append(doc, tokenize(key)...)
		}
	}
	return doc
}

// tokenize lowercases s and splits it into words at non-alphanumeric characters
// and camelCase boundaries, so "getWeather_report" yields get, weather, report.
func tokenize(s string) []string {
	var terms []string
	var current []rune
	flush := func() {
		if len(current) > 0 {
			terms = append(terms, strings.ToLower(string(current)))
			current = current[:0]
		}
	}
	runes := []rune(s)
	for i, r := range runes {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			if unicode.IsUpper(r) && i > 0 && unicode.IsLower(runes[i-1]) {
				flush()
			}
			current = append(current, r)
		default:
			flush()
		}
	}
	flush()
	return terms
}
