// Package streamusage reconciles interrupted streams with the usage reported by
// their provider. Token counting is only performed when the stream is interrupted.
package streamusage

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"strings"
	"sync"
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

// selectCounter picks the model family's vocabulary, loading it on first use,
// and falls back to an OpenAI encoding when there is none.
func selectCounter(model string) (namedCounter, error) {
	if kind := family(model); kind != "" {
		v, _ := counters.LoadOrStore(kind, &counterEntry{})
		e := v.(*counterEntry)
		e.once.Do(func() { e.value.counter, e.err = loadVocabularyCounter(kind); e.value.name = kind })
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

func (c namedCounter) exact(text string) (int, error) {
	if !c.chunked {
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

func knownModel(provider, model string) bool { return modelRegistry[provider][model] }
