package suggest

import (
	"strings"
)

// Prompt templates and pasted material.
//
// Real traffic repeats templates: an agent framework's fixed system text, a
// "write a prompt for an image generator" scaffold, a role-play setup, each
// sent again and again with a different fill. Such a cluster is one request
// type, but the medoid the suggestion is written from carries one fill, and a
// minimal edit of it keeps that fill. Finding the wording the shown requests
// share lets the prompt say which part is the template, and lets a phrase that
// copies one request's own fill be refused as pasted material.

const (
	// templateMinWords is the shortest shared opening that counts as a
	// template; shorter ones ("summarise this:") are an instruction, not a
	// scaffold.
	templateMinWords = 6
	// templateMaxWords bounds the template quoted to the LLM.
	templateMaxWords = 40
	// templateMinRequests is how many distinct shown requests must share it.
	templateMinRequests = 3
	// pastedRunWords is the longest run of words a phrase may copy from a
	// single request when no other shown request has it: past that, the
	// phrase carries that request's own material rather than its type's.
	pastedRunWords = 8
)

// words splits text the way phrases are compared: lowercased, on whitespace.
func words(s string) []string {
	return strings.Fields(strings.ToLower(s))
}

// sharedTemplate returns the opening words the shown requests have in common,
// or "" when fewer than templateMinRequests distinct ones share at least
// templateMinWords.
func sharedTemplate(reps []Member) string {
	seen := map[string]bool{}
	var texts [][]string
	for _, r := range reps {
		w := words(r.Text)
		key := strings.Join(w, " ")
		if len(w) == 0 || seen[key] {
			continue
		}
		seen[key] = true
		texts = append(texts, w)
	}
	if len(texts) < templateMinRequests {
		return ""
	}
	n := len(texts[0])
	for _, w := range texts[1:] {
		n = min(n, len(w))
		for i := 0; i < n; i++ {
			if w[i] != texts[0][i] {
				n = i
				break
			}
		}
	}
	if n < templateMinWords {
		return ""
	}
	// Every distinct request equal to the shared opening means they differ
	// only past what was shown: a repeated request, not a template with a fill.
	whole := 0
	for _, w := range texts {
		if len(w) == n {
			whole++
		}
	}
	if whole == len(texts) {
		return ""
	}
	return strings.Join(texts[0][:min(n, templateMaxWords)], " ")
}

// pastedFrom reports whether a phrase copies a run of more than
// pastedRunWords words from the fill of exactly one request in a template
// cluster: the part after the shared template, which is that request's own
// material. Outside a template there is no telling a request's material from
// its type's wording (a phrase is meant to be a minimal edit of one request),
// so nothing is refused.
func pastedFrom(phrase, template string, reps []Member) bool {
	p := words(phrase)
	if template == "" || len(p) <= pastedRunWords {
		return false
	}
	skip := len(words(template))
	seen := map[string]bool{}
	var fills []string
	for _, r := range reps {
		w := words(r.Text)
		if len(w) <= skip {
			continue
		}
		f := " " + strings.Join(w[skip:], " ") + " "
		if !seen[f] {
			seen[f] = true
			fills = append(fills, f)
		}
	}
	for i := 0; i+pastedRunWords < len(p); i++ {
		run := " " + strings.Join(p[i:i+pastedRunWords+1], " ") + " "
		holders := 0
		for _, f := range fills {
			if strings.Contains(f, run) {
				holders++
			}
		}
		if holders == 1 {
			return true
		}
	}
	return false
}

// templateNote tells the LLM which part of the requests is fixed.
func templateNote(template string) string {
	return "These requests share a fixed template that starts: \"" + template + "\". Write the phrases from the template's fixed wording and what it asks for; do not copy the parts that change from one request to the next."
}

// withoutPasted moves phrases that copy one request's own material from kept
// to leaked, so they get the same feedback and fallback as any other leak.
func withoutPasted(kept, leaked []string, template string, reps []Member) ([]string, []string) {
	out := kept[:0:0]
	for _, p := range kept {
		if pastedFrom(p, template, reps) {
			leaked = append(leaked, p)
			continue
		}
		out = append(out, p)
	}
	return out, leaked
}
