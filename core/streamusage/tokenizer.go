// Package streamusage reconciles interrupted streams with the usage reported by
// their provider. Token counting is only performed when the stream is interrupted.
package streamusage

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"unicode/utf8"

	"github.com/tiktoken-go/tokenizer"
)

type counter interface{ Count(string) (int, error) }
type namedCounter struct {
	counter
	name    string
	chunked bool
}

// The cache is bounded by encoding/family, never by arbitrary user model names.
var counters sync.Map
var deepseekModel = regexp.MustCompile(`(?i)deepseek[-_.](?:chat[-_.])?v[34]|(?:^|/)deepseek-(?:chat|reasoner|flash)(?::free)?$`)

// family names the embedded vocabulary for a model, or "" when the model is
// counted with an OpenAI encoding. Claude 3 models are excluded from the
// Anthropic vocabulary, which does not match their tokenizer.
func family(model string) string {
	m := strings.ToLower(model)
	switch {
	case knownModel("anthropic", model) && !strings.Contains(m, "claude-3"):
		return "anthropic"
	case deepseekModel.MatchString(m):
		return "deepseek"
	case strings.Contains(m, "llama-2") || strings.Contains(m, "replicate"):
		return "llama2"
	case strings.Contains(m, "llama-3"):
		return "llama3"
	default:
		return ""
	}
}

type counterEntry struct {
	once  sync.Once
	value namedCounter
	err   error
}

// Families lists the embedded vocabularies Configure can enable.
var Families = []string{"anthropic", "deepseek", "llama2", "llama3"}

// FamilyAll enables every embedded vocabulary.
const FamilyAll = "all"

// enabled holds the families Configure turned on. A nil set (the default) means
// only the base tiktoken encodings are used.
var enabled atomic.Pointer[map[string]struct{}]

// Configure sets the embedded vocabularies estimation may use. A model whose
// family is not enabled is counted with o200k_base or cl100k_base. Enabled
// vocabularies start loading in the background, so the first cancelled stream
// does not pay the 40-220 ms parse; disabled ones are dropped from the cache to
// release their 4-18 MB. Names are matched case-insensitively and FamilyAll
// enables every family. It returns the names it did not recognise.
func Configure(families []string) (unknown []string) {
	set := make(map[string]struct{}, len(Families))
	for _, f := range families {
		switch f = strings.ToLower(strings.TrimSpace(f)); {
		case f == FamilyAll:
			for _, kind := range Families {
				set[kind] = struct{}{}
			}
		case slices.Contains(Families, f):
			set[f] = struct{}{}
		case f != "":
			unknown = append(unknown, f)
		}
	}
	enabled.Store(&set)
	var preload []string
	for _, kind := range Families {
		if _, ok := set[kind]; ok {
			preload = append(preload, kind)
		} else {
			counters.Delete(kind)
		}
	}
	// The entry is stored before the goroutine starts, so a cancel racing the
	// preload waits on the same once instead of parsing the vocabulary twice.
	entries := make([]*counterEntry, 0, len(preload))
	for _, kind := range preload {
		v, _ := counters.LoadOrStore(kind, &counterEntry{})
		entries = append(entries, v.(*counterEntry))
	}
	if len(entries) > 0 {
		go func() {
			for i, e := range entries {
				e.load(preload[i])
			}
		}()
	}
	return unknown
}

// familyEnabled reports whether Configure turned on the family's vocabulary.
func familyEnabled(kind string) bool {
	set := enabled.Load()
	if set == nil {
		return false
	}
	_, ok := (*set)[kind]
	return ok
}

// load parses the family's embedded vocabulary exactly once.
func (e *counterEntry) load(kind string) {
	e.once.Do(func() { e.value.counter, e.err = loadVocabularyCounter(kind); e.value.name = kind })
}

// selectCounter picks the model family's vocabulary when that family is
// enabled, loading it on first use, and falls back to an OpenAI encoding
// otherwise.
func selectCounter(model string) (namedCounter, error) {
	if kind := family(model); kind != "" && familyEnabled(kind) {
		v, _ := counters.LoadOrStore(kind, &counterEntry{})
		e := v.(*counterEntry)
		e.load(kind)
		if e.err == nil {
			return e.value, nil
		}
	}
	if knownModel("azure", model) {
		model = strings.ReplaceAll(model, "-35", "-3.5")
	} else if !knownModel("openai", model) {
		model = ""
	}
	encoding := tokenizer.Cl100kBase
	// Newer OpenAI models use o200k_base; aliases and unknown models fall back
	// to cl100k_base. Provider names are not tokenizer names.
	if strings.HasPrefix(model, "gpt-4o") || strings.HasPrefix(model, "gpt-4.1") || strings.HasPrefix(model, "gpt-5") || strings.HasPrefix(model, "o1") || strings.HasPrefix(model, "o3") || strings.HasPrefix(model, "o4") {
		encoding = tokenizer.O200kBase
	}
	v, _ := counters.LoadOrStore(string(encoding), &counterEntry{})
	e := v.(*counterEntry)
	e.once.Do(func() {
		e.value.counter, e.err = tokenizer.Get(encoding)
		e.value.name = string(encoding)
		e.value.chunked = true
	})
	return e.value, e.err
}

// count encodes tiktoken input in 1024-codepoint segments and samples inputs over
// four million codepoints, bounding the cost. It is NOT per-SSE-chunk BPE.
func (c namedCounter) count(text string) (int, error) {
	if c.counter == nil {
		return 0, fmt.Errorf("tokenizer unavailable")
	}
	const limit = 4_000_000
	length := utf8.RuneCountInString(text)
	if length > limit {
		runes := []rune(text)
		const samples = 16
		const size = limit / samples
		total := 0
		for i := 0; i < samples; i++ {
			start := (length - size) * i / (samples - 1)
			n, err := c.exact(string(runes[start : start+size]))
			if err != nil {
				return 0, err
			}
			total += n
		}
		return int(math.RoundToEven(float64(total) * float64(length) / limit)), nil
	}
	return c.exact(text)
}

// exact counts text with no sampling. Tiktoken encodings are counted in
// 1024-codepoint segments to bound the cost of one encode call; embedded
// vocabularies count the whole text at once.
func (c namedCounter) exact(text string) (int, error) {
	if !c.chunked {
		if b, ok := c.counter.(*bpeCounter); ok {
			return b.countContent(text)
		}
		return c.Count(text)
	}
	total, start, runes := 0, 0, 0
	for i := range text {
		if runes == 1024 {
			n, err := c.Count(text[start:i])
			if err != nil {
				return 0, err
			}
			total += n
			start, runes = i, 0
		}
		runes++
	}
	if start < len(text) {
		n, err := c.Count(text[start:])
		if err != nil {
			return 0, err
		}
		total += n
	}
	return total, nil
}

//go:embed assets/models.json
var modelRegistryJSON []byte
var modelRegistry = func() map[string]map[string]bool {
	var lists map[string][]string
	if err := json.Unmarshal(modelRegistryJSON, &lists); err != nil {
		panic(err)
	}
	registry := make(map[string]map[string]bool, len(lists))
	for provider, models := range lists {
		registry[provider] = make(map[string]bool, len(models))
		for _, model := range models {
			registry[provider][model] = true
		}
	}
	return registry
}()

// knownModel reports whether model is listed for provider in assets/models.json.
func knownModel(provider, model string) bool { return modelRegistry[provider][model] }

// sequenceTokens is the number of special tokens (llama2's <s> BOS) the
// vocabulary adds once to each encoded sequence. count never includes them.
func (c namedCounter) sequenceTokens() int {
	if b, ok := c.counter.(*bpeCounter); ok {
		return b.templateTokens
	}
	return 0
}
