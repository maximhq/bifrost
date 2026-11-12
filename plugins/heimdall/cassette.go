package heimdall

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"sort"
)

// Outcomes of a cassette lookup.
const (
	OutcomeHit            = "hit"
	OutcomeMissArgs       = "miss_args"       // the tool was recorded, with other arguments
	OutcomeMissExhausted  = "miss_exhausted"  // every recording for these arguments is used up
	OutcomeMissUnrecorded = "miss_unrecorded" // the tool is declared but was never called in the session
	OutcomeMissUndeclared = "miss_undeclared" // the session never declared the tool
)

// recordedCall is one tool call and its result as logged.
type recordedCall struct {
	Name    string `json:"name"`
	Args    string `json:"args"` // raw arguments as the model sent them
	Result  string `json:"result"`
	IsError bool   `json:"is_error,omitempty"`
}

type cassetteEntry struct {
	name    string
	result  string
	isError bool
}

// Cassette answers tool calls from a recording and never from anything else. A lookup consumes the
// answer it returns, so one replay owns one clone and must not share it across goroutines.
type Cassette struct {
	entries  []cassetteEntry // immutable, shared by clones
	byKey    map[[32]byte][]int
	byName   map[string][]int
	declared map[string]bool
	rules    ToolRules
	used     []bool
}

// NewCassette records calls in the order given. declared holds the names of the tools the session
// offered the model.
func NewCassette(recorded []recordedCall, declared map[string]bool, rules ToolRules) *Cassette {
	c := &Cassette{
		entries:  make([]cassetteEntry, len(recorded)),
		byKey:    make(map[[32]byte][]int),
		byName:   make(map[string][]int),
		declared: declared,
		rules:    rules,
		used:     make([]bool, len(recorded)),
	}
	for i, rc := range recorded {
		c.entries[i] = cassetteEntry{name: rc.Name, result: rc.Result, isError: rc.IsError}
		k := c.key(rc.Name, rc.Args)
		c.byKey[k] = append(c.byKey[k], i)
		c.byName[rc.Name] = append(c.byName[rc.Name], i)
	}
	return c
}

// key hashes the name and canonical arguments, each length-prefixed, so no name or argument text can
// be crafted to collide with another pair.
func (c *Cassette) key(name, args string) [32]byte {
	canonical := CanonicalArgs(args, c.rules.rule(name).IgnoreArgs)
	h := sha256.New()
	var n [8]byte
	binary.BigEndian.PutUint64(n[:], uint64(len(name)))
	h.Write(n[:])
	h.Write([]byte(name))
	binary.BigEndian.PutUint64(n[:], uint64(len(canonical)))
	h.Write(n[:])
	h.Write([]byte(canonical))
	var out [32]byte
	copy(out[:], h.Sum(nil))
	return out
}

// Clone returns a cassette with the same recordings and nothing consumed.
func (c *Cassette) Clone() *Cassette {
	clone := *c
	clone.used = make([]bool, len(c.used))
	return &clone
}

// Lookup returns the next recorded result for a call and consumes it, or the reason there is none.
func (c *Cassette) Lookup(name, args string) (result string, isError bool, outcome string) {
	candidates := c.byName[name]
	if c.rules.rule(name).Match != MatchNameOnly {
		candidates = c.byKey[c.key(name, args)]
	}
	for _, i := range candidates {
		if !c.used[i] {
			c.used[i] = true
			return c.entries[i].result, c.entries[i].isError, OutcomeHit
		}
	}
	switch {
	case len(candidates) > 0:
		return "", false, OutcomeMissExhausted
	case len(c.byName[name]) > 0:
		return "", false, OutcomeMissArgs
	case c.declared[name]:
		return "", false, OutcomeMissUnrecorded
	default:
		return "", false, OutcomeMissUndeclared
	}
}

// CassetteTool is a tool's name and how many calls were recorded for it.
type CassetteTool struct {
	Name  string `json:"name"`
	Calls int    `json:"calls"`
}

// Summary lists the recorded tools by name. It holds no results.
func (c *Cassette) Summary() []CassetteTool {
	out := make([]CassetteTool, 0, len(c.byName))
	for name, idx := range c.byName {
		out = append(out, CassetteTool{Name: name, Calls: len(idx)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// MarshalJSON writes the summary, so results never leave through a trace.
func (c *Cassette) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Tools []CassetteTool `json:"tools"`
	}{c.Summary()})
}
