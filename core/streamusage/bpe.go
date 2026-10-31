package streamusage

import (
	"bytes"
	"compress/gzip"
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/dlclark/regexp2/v2"
	"golang.org/x/text/unicode/norm"
)

// Vocabularies are the pinned tokenizer.json files listed in assets/manifest.json,
// gzipped and compiled in so that selecting a family never touches the network.
//
//go:embed assets/*.json.gz
var vocabularies embed.FS

// gpt2Split is the pattern a ByteLevel pre-tokenizer applies when use_regex is set.
const gpt2Split = `'s|'t|'re|'ve|'m|'ll|'d| ?\p{L}+| ?\p{N}+| ?[^\s\p{L}\p{N}]+|\s+(?!\S)|\s+`

// bpeCounter counts tokens for a Hugging Face tokenizer.json BPE model in pure
// Go. It implements only the components the bundled vocabularies use and rejects
// anything else at load time, so an unsupported file falls back to tiktoken
// rather than producing silently different counts.
type bpeCounter struct {
	vocab           map[string]uint32 // raw-byte keys when byteLevel is set
	merges          map[uint64]uint64 // left<<32|right -> rank<<32|merged
	byteTokens      [256]uint32       // byte-level alphabet or <0xXX> fallback ids
	hasByteTokens   bool
	byteLevel       bool
	byteFallback    bool
	ignoreMerges    bool
	unk             uint32
	hasUnk          bool
	fuseUnk         bool
	normalizers     []func(string) string
	splits          []*regexp2.Regexp // sequential Isolated splits
	rawAdded        addedTokens       // matched before normalization
	normalizedAdded addedTokens       // matched on normalized text
	templateTokens  int               // special tokens the post-processor adds
}

type tokenizerFile struct {
	AddedTokens []struct {
		Content    string `json:"content"`
		SingleWord bool   `json:"single_word"`
		LStrip     bool   `json:"lstrip"`
		RStrip     bool   `json:"rstrip"`
		Normalized bool   `json:"normalized"`
	} `json:"added_tokens"`
	Normalizer    json.RawMessage `json:"normalizer"`
	PreTokenizer  json.RawMessage `json:"pre_tokenizer"`
	PostProcessor json.RawMessage `json:"post_processor"`
	Model         struct {
		Type                    string            `json:"type"`
		Vocab                   map[string]uint32 `json:"vocab"`
		Merges                  json.RawMessage   `json:"merges"`
		Dropout                 *float64          `json:"dropout"`
		UnkToken                *string           `json:"unk_token"`
		ContinuingSubwordPrefix *string           `json:"continuing_subword_prefix"`
		EndOfWordSuffix         *string           `json:"end_of_word_suffix"`
		FuseUnk                 bool              `json:"fuse_unk"`
		ByteFallback            bool              `json:"byte_fallback"`
		IgnoreMerges            bool              `json:"ignore_merges"`
	} `json:"model"`
}

type component struct {
	Type          string          `json:"type"`
	Normalizers   json.RawMessage `json:"normalizers"`
	Pretokenizers json.RawMessage `json:"pretokenizers"`
	Prepend       string          `json:"prepend"`
	Content       string          `json:"content"`
	Pattern       struct {
		String *string `json:"String"`
		Regex  *string `json:"Regex"`
	} `json:"pattern"`
	Behavior       string `json:"behavior"`
	Invert         bool   `json:"invert"`
	AddPrefixSpace bool   `json:"add_prefix_space"`
	UseRegex       *bool  `json:"use_regex"`
	Single         []struct {
		SpecialToken *struct {
			ID string `json:"id"`
		} `json:"SpecialToken"`
	} `json:"single"`
	SpecialTokens map[string]struct {
		IDs []uint32 `json:"ids"`
	} `json:"special_tokens"`
}

func loadVocabularyCounter(kind string) (counter, error) {
	compressed, err := vocabularies.ReadFile("assets/" + kind + ".json.gz")
	if err != nil {
		return nil, fmt.Errorf("unknown tokenizer %q", kind)
	}
	data, err := gunzip(compressed)
	if err != nil {
		return nil, err
	}
	return parseBPE(data)
}

func gunzip(compressed []byte) ([]byte, error) {
	r, err := gzip.NewReader(bytes.NewReader(compressed))
	if err != nil {
		return nil, err
	}
	defer r.Close()
	return io.ReadAll(r)
}

func parseBPE(data []byte) (*bpeCounter, error) {
	var f tokenizerFile
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, err
	}
	m := f.Model
	if m.Type != "BPE" || m.Dropout != nil || m.ContinuingSubwordPrefix != nil || m.EndOfWordSuffix != nil {
		return nil, fmt.Errorf("unsupported tokenizer model")
	}
	c := &bpeCounter{byteFallback: m.ByteFallback, ignoreMerges: m.IgnoreMerges, fuseUnk: m.FuseUnk}
	if m.UnkToken != nil {
		c.unk, c.hasUnk = m.Vocab[*m.UnkToken]
	}
	if err := c.parseNormalizer(f.Normalizer); err != nil {
		return nil, err
	}
	if err := c.parsePreTokenizer(f.PreTokenizer, false); err != nil {
		return nil, err
	}
	if err := c.parsePostProcessor(f.PostProcessor); err != nil {
		return nil, err
	}
	if err := c.parseMerges(m.Merges, m.Vocab); err != nil {
		return nil, err
	}
	if c.byteLevel {
		// Byte-level vocabularies spell bytes as printable runes; keying by the
		// raw bytes lets words be looked up without re-encoding them.
		decode := make(map[rune]byte, 256)
		for b, r := range byteLevelAlphabet() {
			decode[r] = byte(b)
			id, ok := m.Vocab[string(r)]
			if !ok {
				return nil, fmt.Errorf("byte-level vocabulary is missing byte %d", b)
			}
			c.byteTokens[b] = id
		}
		c.hasByteTokens = true
		c.vocab = make(map[string]uint32, len(m.Vocab))
		buf := make([]byte, 0, 64)
		for token, id := range m.Vocab {
			buf = buf[:0]
			valid := true
			for _, r := range token {
				b, ok := decode[r]
				if !ok {
					valid = false // added tokens outside the byte alphabet
					break
				}
				buf = append(buf, b)
			}
			if valid {
				c.vocab[string(buf)] = id
			}
		}
	} else {
		c.vocab = m.Vocab
		if c.byteFallback {
			c.hasByteTokens = true
			for b := 0; b < 256; b++ {
				id, ok := m.Vocab[fmt.Sprintf("<0x%02X>", b)]
				if !ok {
					c.hasByteTokens = false
					break
				}
				c.byteTokens[b] = id
			}
		}
	}
	for _, t := range f.AddedTokens {
		if t.SingleWord || t.LStrip || t.RStrip {
			return nil, fmt.Errorf("unsupported added token options for %q", t.Content)
		}
		if t.Normalized {
			c.normalizedAdded.add(c.normalize(t.Content))
		} else {
			c.rawAdded.add(t.Content)
		}
	}
	return c, nil
}

func (c *bpeCounter) parseNormalizer(raw json.RawMessage) error {
	if isNull(raw) {
		return nil
	}
	var n component
	if err := json.Unmarshal(raw, &n); err != nil {
		return err
	}
	switch n.Type {
	case "Sequence":
		var items []json.RawMessage
		if err := json.Unmarshal(n.Normalizers, &items); err != nil {
			return err
		}
		for _, item := range items {
			if err := c.parseNormalizer(item); err != nil {
				return err
			}
		}
	case "NFKC":
		c.normalizers = append(c.normalizers, norm.NFKC.String)
	case "Prepend":
		prefix := n.Prepend
		c.normalizers = append(c.normalizers, func(s string) string {
			if s == "" {
				return s
			}
			return prefix + s
		})
	case "Replace":
		if n.Pattern.String == nil {
			return fmt.Errorf("unsupported Replace pattern")
		}
		from, to := *n.Pattern.String, n.Content
		c.normalizers = append(c.normalizers, func(s string) string { return strings.ReplaceAll(s, from, to) })
	default:
		return fmt.Errorf("unsupported normalizer %q", n.Type)
	}
	return nil
}

func (c *bpeCounter) parsePreTokenizer(raw json.RawMessage, nested bool) error {
	if isNull(raw) {
		return nil
	}
	var p component
	if err := json.Unmarshal(raw, &p); err != nil {
		return err
	}
	if c.byteLevel {
		return fmt.Errorf("pre-tokenizer after ByteLevel is unsupported")
	}
	switch p.Type {
	case "Sequence":
		var items []json.RawMessage
		if err := json.Unmarshal(p.Pretokenizers, &items); err != nil {
			return err
		}
		for _, item := range items {
			if err := c.parsePreTokenizer(item, true); err != nil {
				return err
			}
		}
	case "Split":
		if p.Pattern.Regex == nil || p.Behavior != "Isolated" || p.Invert {
			return fmt.Errorf("unsupported Split pre-tokenizer")
		}
		// regexp2's default (non-RE2) mode gives \s and \p{..} the Unicode
		// semantics of the Rust implementation, including U+00A0 as whitespace.
		re, err := regexp2.Compile(*p.Pattern.Regex, regexp2.None)
		if err != nil {
			return err
		}
		c.splits = append(c.splits, re)
	case "ByteLevel":
		if p.AddPrefixSpace {
			return fmt.Errorf("unsupported ByteLevel add_prefix_space")
		}
		if p.UseRegex == nil || *p.UseRegex {
			c.splits = append(c.splits, regexp2.MustCompile(gpt2Split, regexp2.None))
		}
		c.byteLevel = true
	default:
		return fmt.Errorf("unsupported pre-tokenizer %q", p.Type)
	}
	return nil
}

func (c *bpeCounter) parsePostProcessor(raw json.RawMessage) error {
	if isNull(raw) {
		return nil
	}
	var p component
	if err := json.Unmarshal(raw, &p); err != nil {
		return err
	}
	switch p.Type {
	case "ByteLevel":
	case "TemplateProcessing":
		for _, piece := range p.Single {
			if piece.SpecialToken != nil {
				c.templateTokens += len(p.SpecialTokens[piece.SpecialToken.ID].IDs)
			}
		}
	default:
		return fmt.Errorf("unsupported post-processor %q", p.Type)
	}
	return nil
}

func (c *bpeCounter) parseMerges(raw json.RawMessage, vocab map[string]uint32) error {
	var pairs [][2]string
	var joined []string
	if err := json.Unmarshal(raw, &joined); err == nil {
		pairs = make([][2]string, len(joined))
		for i, merge := range joined {
			left, right, ok := strings.Cut(merge, " ")
			if !ok {
				return fmt.Errorf("malformed merge %q", merge)
			}
			pairs[i] = [2]string{left, right}
		}
	} else if err := json.Unmarshal(raw, &pairs); err != nil {
		return err
	}
	c.merges = make(map[uint64]uint64, len(pairs))
	for rank, pair := range pairs {
		left, okLeft := vocab[pair[0]]
		right, okRight := vocab[pair[1]]
		merged, okMerged := vocab[pair[0]+pair[1]]
		if !okLeft || !okRight || !okMerged {
			return fmt.Errorf("merge %q references unknown tokens", pair[0]+" "+pair[1])
		}
		key := uint64(left)<<32 | uint64(right)
		if _, seen := c.merges[key]; !seen {
			c.merges[key] = uint64(rank)<<32 | uint64(merged)
		}
	}
	return nil
}

func isNull(raw json.RawMessage) bool {
	return len(raw) == 0 || string(raw) == "null"
}

// byteLevelAlphabet is GPT-2's bytes_to_unicode table.
func byteLevelAlphabet() [256]rune {
	var table [256]rune
	next := rune(256)
	for b := 0; b < 256; b++ {
		if (b >= '!' && b <= '~') || (b >= 0xA1 && b <= 0xAC) || (b >= 0xAE && b <= 0xFF) {
			table[b] = rune(b)
		} else {
			table[b] = next
			next++
		}
	}
	return table
}

func (c *bpeCounter) normalize(s string) string {
	for _, n := range c.normalizers {
		s = n(s)
	}
	return s
}

// Count mirrors Tokenizer.encode(text, add_special_tokens=True) and returns the
// number of ids it would produce.
func (c *bpeCounter) Count(text string) (int, error) {
	s := &bpeScratch{}
	total := c.templateTokens
	var err error
	c.rawAdded.each(text, func(segment string) {
		c.normalizedAdded.each(c.normalize(segment), func(normalized string) {
			if err == nil {
				err = c.split(0, normalized, func(word string) { total += c.word(word, s) })
			}
		}, func() { total++ })
	}, func() { total++ })
	return total, err
}

// split applies the Isolated split patterns in order: matches and the text
// between them both become pieces for the next pattern.
func (c *bpeCounter) split(level int, text string, emit func(string)) error {
	if text == "" {
		return nil
	}
	if level == len(c.splits) {
		emit(text)
		return nil
	}
	m, err := c.splits[level].FindStringMatch(text)
	last := 0
	for err == nil && m != nil {
		start, length := m.ByteRange()
		if start > last {
			if err = c.split(level+1, text[last:start], emit); err != nil {
				return err
			}
		}
		if err = c.split(level+1, text[start:start+length], emit); err != nil {
			return err
		}
		last = start + length
		m, err = c.splits[level].FindNextMatch(m)
	}
	if err != nil {
		return err
	}
	return c.split(level+1, text[last:], emit)
}

type bpeSymbol struct {
	id         uint32
	prev, next int32
}

type bpePair struct {
	rank, merged    uint32
	left            int32
	leftID, rightID uint32
}

type bpeScratch struct {
	symbols []bpeSymbol
	heap    []bpePair
}

const deadSymbol = ^uint32(0)

// word runs BPE over one pre-tokenized word, applying the lowest-ranked merge
// (leftmost on ties) until none applies, and returns the resulting token count.
func (c *bpeCounter) word(w string, s *bpeScratch) int {
	if c.ignoreMerges {
		if _, ok := c.vocab[w]; ok {
			return 1
		}
	}
	s.symbols = s.symbols[:0]
	if c.byteLevel {
		for i := 0; i < len(w); i++ {
			s.symbols = append(s.symbols, bpeSymbol{id: c.byteTokens[w[i]]})
		}
	} else {
		unkRun := false
		for i := 0; i < len(w); {
			_, size := utf8.DecodeRuneInString(w[i:])
			ch := w[i : i+size]
			i += size
			if id, ok := c.vocab[ch]; ok {
				s.symbols = append(s.symbols, bpeSymbol{id: id})
				unkRun = false
				continue
			}
			if c.byteFallback && c.hasByteTokens {
				for j := 0; j < len(ch); j++ {
					s.symbols = append(s.symbols, bpeSymbol{id: c.byteTokens[ch[j]]})
				}
				unkRun = false
				continue
			}
			if c.hasUnk && !(c.fuseUnk && unkRun) {
				s.symbols = append(s.symbols, bpeSymbol{id: c.unk})
			}
			unkRun = true
		}
	}
	n := len(s.symbols)
	for i := range s.symbols {
		s.symbols[i].prev, s.symbols[i].next = int32(i-1), int32(i+1)
	}
	if n > 0 {
		s.symbols[n-1].next = -1
	}
	s.heap = s.heap[:0]
	for i := 0; i+1 < n; i++ {
		c.pushPair(s, int32(i))
	}
	for len(s.heap) > 0 {
		p := s.pop()
		left := &s.symbols[p.left]
		if left.id != p.leftID || left.next < 0 || s.symbols[left.next].id != p.rightID {
			continue
		}
		right := &s.symbols[left.next]
		left.id = p.merged
		right.id = deadSymbol
		left.next = right.next
		if left.next >= 0 {
			s.symbols[left.next].prev = p.left
		}
		n--
		if left.prev >= 0 {
			c.pushPair(s, left.prev)
		}
		c.pushPair(s, p.left)
	}
	return n
}

func (c *bpeCounter) pushPair(s *bpeScratch, left int32) {
	l := s.symbols[left]
	if l.next < 0 {
		return
	}
	r := s.symbols[l.next]
	v, ok := c.merges[uint64(l.id)<<32|uint64(r.id)]
	if !ok {
		return
	}
	s.heap = append(s.heap, bpePair{rank: uint32(v >> 32), merged: uint32(v), left: left, leftID: l.id, rightID: r.id})
	for i := len(s.heap) - 1; i > 0; {
		parent := (i - 1) / 2
		if !s.heap[i].before(s.heap[parent]) {
			break
		}
		s.heap[i], s.heap[parent] = s.heap[parent], s.heap[i]
		i = parent
	}
}

func (s *bpeScratch) pop() bpePair {
	top := s.heap[0]
	last := len(s.heap) - 1
	s.heap[0] = s.heap[last]
	s.heap = s.heap[:last]
	for i := 0; ; {
		smallest, l, r := i, 2*i+1, 2*i+2
		if l < last && s.heap[l].before(s.heap[smallest]) {
			smallest = l
		}
		if r < last && s.heap[r].before(s.heap[smallest]) {
			smallest = r
		}
		if smallest == i {
			break
		}
		s.heap[i], s.heap[smallest] = s.heap[smallest], s.heap[i]
		i = smallest
	}
	return top
}

func (p bpePair) before(q bpePair) bool {
	return p.rank < q.rank || (p.rank == q.rank && p.left < q.left)
}

// addedTokens finds added tokens leftmost-longest, as the Rust AddedVocabulary does.
type addedTokens struct {
	byFirst [256][]string // longest first
}

func (a *addedTokens) add(content string) {
	if content == "" {
		return
	}
	bucket := append(a.byFirst[content[0]], content)
	for i := len(bucket) - 1; i > 0 && len(bucket[i]) > len(bucket[i-1]); i-- {
		bucket[i], bucket[i-1] = bucket[i-1], bucket[i]
	}
	a.byFirst[content[0]] = bucket
}

// each calls segment for the text between matches and token once per match.
func (a *addedTokens) each(text string, segment func(string), token func()) {
	last := 0
	for i := 0; i < len(text); i++ {
		for _, candidate := range a.byFirst[text[i]] {
			if strings.HasPrefix(text[i:], candidate) {
				segment(text[last:i])
				token()
				i += len(candidate) - 1
				last = i + 1
				break
			}
		}
	}
	segment(text[last:])
}
