package registry

import (
	"crypto/sha256"
	"encoding/binary"
	"hash"
	"io"
	"math"

	"github.com/maximhq/bifrost/core/schemas"
)

// GrantSetID identifies a grant set by its content: owners granted the same things hold the same
// ID.
type GrantSetID [16]byte

// GrantSet is what an owner may use: providers and their models and keys, MCP tools, virtual
// MCPs, agents and relays. It has no row of its own. A read of an owner builds it from the owner's
// grant rows, and the live store keeps one copy per content, shared by every owner granted the
// same things. It is never changed once built.
type GrantSet struct {
	// ID is the set's content hash, set when the set is built.
	ID                GrantSetID
	AllowAllProviders bool
	// Providers are the owner's provider configs' grants, in the order the configs were read.
	Providers []ProviderGrant
	// MCP are the owner's MCP configs' grants, in the order the configs were read.
	MCP []MCPGrant
	// VirtualMCPIDs are the virtual MCPs assigned to the owner, ascending.
	VirtualMCPIDs []uint
	Agents        []string
	Relays        []string
}

// ProviderGrant is one provider config's grant: the provider, its weight, which models it allows
// and blocks, and which of the provider's keys it may use. A key is named by its key_id; the key
// itself lives on its provider's record, so a grant holds no copy of it.
type ProviderGrant struct {
	Provider          string
	Weight            *float64
	AllowedModels     schemas.WhiteList
	BlacklistedModels schemas.BlackList
	// KeyIDs lists the key_ids the grant allows, or ["*"] when it allows every key of the provider.
	KeyIDs schemas.WhiteList
}

// MCPGrant is one MCP config's grant: the client, as its row was read when the owner was, and the
// tools it may execute.
type MCPGrant struct {
	// ClientRowID is the client's row ID, the config's mcp_client_id.
	ClientRowID uint
	// ClientID and ClientName are the client's client_id and name.
	ClientID   string
	ClientName string
	Tools      schemas.WhiteList
}

// ProviderLimit is what funds one of an owner's provider configs: its row ID and the budgets and
// rate limit it holds. These differ per owner, so they stay on the owner's record.
type ProviderLimit struct {
	ConfigID    uint
	Provider    string
	BudgetIDs   []string
	RateLimitID string
}

// PoolKey is the set's key in the shared pool of grant sets: its content hash, set when the set is
// sealed.
func (g *GrantSet) PoolKey() [16]byte { return g.ID }

// SealGrantSet sets g's ID from its content and returns g. Every field is written length-prefixed,
// with nil apart from empty, so two sets that differ in anything differ in what is hashed. A set is
// sealed once, after it is built and before it is interned.
func SealGrantSet(g *GrantSet) *GrantSet {
	w := NewContentHasher()
	w.Flag(g.AllowAllProviders)
	w.Count(len(g.Providers), g.Providers == nil)
	for i := range g.Providers {
		p := &g.Providers[i]
		w.Str(p.Provider)
		w.Flag(p.Weight != nil)
		if p.Weight != nil {
			w.Num(math.Float64bits(*p.Weight))
		}
		w.Strs(p.AllowedModels)
		w.Strs(p.BlacklistedModels)
		w.Strs(p.KeyIDs)
	}
	w.Count(len(g.MCP), g.MCP == nil)
	for i := range g.MCP {
		m := &g.MCP[i]
		w.Num(uint64(m.ClientRowID))
		w.Str(m.ClientID)
		w.Str(m.ClientName)
		w.Strs(m.Tools)
	}
	w.Count(len(g.VirtualMCPIDs), g.VirtualMCPIDs == nil)
	for _, id := range g.VirtualMCPIDs {
		w.Num(uint64(id))
	}
	w.Strs(g.Agents)
	w.Strs(g.Relays)
	g.ID = w.Sum()
	return g
}

// ContentHasher writes a value's fields into a SHA-256 hash for interning by content. Every field
// is written length-prefixed, and a nil list apart from an empty one, so two values that differ in
// anything differ in what is hashed. Any value interned by content can use it, so each only lists
// the fields it hashes.
type ContentHasher struct{ h hash.Hash }

// NewContentHasher returns an empty hasher.
func NewContentHasher() ContentHasher { return ContentHasher{h: sha256.New()} }

// Num writes n.
func (w ContentHasher) Num(n uint64) {
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], n)
	w.h.Write(b[:])
}

// Flag writes b.
func (w ContentHasher) Flag(b bool) {
	if b {
		w.Num(1)
		return
	}
	w.Num(0)
}

// Count writes a list's length, or a marker no length can take when the list is nil.
func (w ContentHasher) Count(n int, isNil bool) {
	if isNil {
		w.Num(math.MaxUint64)
		return
	}
	w.Num(uint64(n))
}

// Str writes s, length first.
func (w ContentHasher) Str(s string) {
	w.Num(uint64(len(s)))
	io.WriteString(w.h, s)
}

// Strs writes a list of strings.
func (w ContentHasher) Strs(ss []string) {
	w.Count(len(ss), ss == nil)
	for _, s := range ss {
		w.Str(s)
	}
}

// Sum returns the first 128 bits of the hash, the size of an interned value's ID.
func (w ContentHasher) Sum() [16]byte {
	var out [16]byte
	copy(out[:], w.h.Sum(nil))
	return out
}
