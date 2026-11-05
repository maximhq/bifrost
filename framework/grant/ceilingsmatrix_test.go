package grant

import (
	"fmt"
	"slices"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The matrix below enumerates every shape an access can take with up to two caller permits, two
// ceiling layers of up to two permits each, and a scoping permit under either mode, with every
// permit either allowing or refusing what is asked. Each answer is checked against an oracle written
// from the rule alone - the caller's side is any base AND every layer (any of its permits); the
// scoping permit composes with that side - so the fold and the rule cannot drift apart unnoticed.

// matrixPermit is one permit of the matrix: whether it allows what is asked, and the keys it holds.
type matrixPermit struct {
	allows bool
	keys   []string
}

// build turns a matrix permit into a real one. An allowing permit grants the model, agent, relay
// and tool under test; a refusing one grants only something else on the same provider and client,
// so it holds the provider without serving the request.
func (m matrixPermit) build(id string) *Permit {
	model, agent, relay, tool := "other-model", "other-agent", "other-relay", "other_tool"
	if m.allows {
		model, agent, relay, tool = "gpt-4o", "agent-a", "relay-a", "read_file"
	}
	return NewPermit(PermitAccessProfile, id, id, true, false,
		[]schemas.ProviderPermit{{Provider: "openai", AllowedModels: []string{model}, KeyIDs: m.keys}},
		[]schemas.MCPPermit{{Client: "github-id", ClientName: "github", Tools: []string{tool}}},
		WithAgentPermits([]string{agent}), WithRelayPermits([]string{relay}))
}

// matrixCase is one access shape.
type matrixCase struct {
	bases    []matrixPermit
	ceilings [][]matrixPermit
	scoping  *matrixPermit
	mode     CompositionMode
}

// name labels a case so a failure says which shape broke.
func (c matrixCase) name() string {
	v := func(ps []matrixPermit) string {
		out := ""
		for _, p := range ps {
			if p.allows {
				out += "A"
			} else {
				out += "D"
			}
		}
		if out == "" {
			return "-"
		}
		return out
	}
	name := "bases=" + v(c.bases)
	for i, layer := range c.ceilings {
		name += fmt.Sprintf(",ceil%d=%s", i, v(layer))
	}
	if c.scoping != nil {
		name += fmt.Sprintf(",scope=%s/%s", v([]matrixPermit{*c.scoping}), c.mode)
	}
	return name
}

// anyAllows reports whether any permit in ps allows what is asked.
func anyAllows(ps []matrixPermit) bool {
	return slices.ContainsFunc(ps, func(p matrixPermit) bool { return p.allows })
}

// oracle is the rule, written independently of the fold.
func (c matrixCase) oracle() (callerAllows, allowed bool) {
	callerAllows = anyAllows(c.bases)
	for _, layer := range c.ceilings {
		callerAllows = callerAllows && anyAllows(layer)
	}
	if c.scoping == nil {
		return callerAllows, callerAllows
	}
	if c.mode == Union {
		return callerAllows, callerAllows || c.scoping.allows
	}
	return callerAllows, callerAllows && c.scoping.allows
}

// oracleKeys is the key restriction the rule hands load balancing: the caller's allowing permits'
// keys pooled and narrowed by every layer's allowing permits' keys, composed with the scope's.
func (c matrixCase) oracleKeys() []string {
	callerAllows, allowed := c.oracle()
	if !allowed {
		return nil
	}
	allowingKeys := func(ps []matrixPermit) []string {
		var keys []string
		for _, p := range ps {
			if !p.allows {
				continue
			}
			for _, k := range p.keys {
				if !slices.Contains(keys, k) {
					keys = append(keys, k)
				}
			}
		}
		return keys
	}
	var caller []string
	callerHolds := false
	if callerAllows {
		caller, callerHolds = allowingKeys(c.bases), true
		for _, layer := range c.ceilings {
			layerKeys := allowingKeys(layer)
			caller = slices.DeleteFunc(slices.Clone(caller), func(k string) bool { return !slices.Contains(layerKeys, k) })
		}
	}
	scopeHolds := c.scoping != nil && c.scoping.allows
	switch {
	case callerHolds && scopeHolds:
		if c.mode == Union {
			for _, k := range c.scoping.keys {
				if !slices.Contains(caller, k) {
					caller = append(caller, k)
				}
			}
			return caller
		}
		return slices.DeleteFunc(caller, func(k string) bool { return !slices.Contains(c.scoping.keys, k) })
	case callerHolds:
		return caller
	default:
		return slices.Clone(c.scoping.keys)
	}
}

// matrixCases enumerates every shape.
func matrixCases() []matrixCase {
	a, d := matrixPermit{allows: true}, matrixPermit{}
	baseSets := [][]matrixPermit{{}, {a}, {d}, {a, a}, {a, d}, {d, d}}
	layerSets := [][]matrixPermit{{a}, {d}, {a, d}, {d, d}}
	var ceilingSets [][][]matrixPermit
	ceilingSets = append(ceilingSets, nil)
	for _, l1 := range layerSets {
		ceilingSets = append(ceilingSets, [][]matrixPermit{l1})
		for _, l2 := range layerSets {
			ceilingSets = append(ceilingSets, [][]matrixPermit{l1, l2})
		}
	}
	type scope struct {
		permit *matrixPermit
		mode   CompositionMode
	}
	scopes := []scope{{nil, ""}, {&a, Intersect}, {&d, Intersect}, {&a, Union}, {&d, Union}}

	var cases []matrixCase
	for _, bases := range baseSets {
		for _, ceilings := range ceilingSets {
			for _, sc := range scopes {
				c := matrixCase{mode: sc.mode}
				// Every permit holds a key of its own, so pooling is observable. A layer's permit also
				// holds the base key of its own position and the scope holds base0, so narrowing keeps
				// some keys and drops others instead of always emptying the set.
				for i, p := range bases {
					p.keys = []string{fmt.Sprintf("base%d", i)}
					c.bases = append(c.bases, p)
				}
				for li, layer := range ceilings {
					var built []matrixPermit
					for pi, p := range layer {
						p.keys = []string{fmt.Sprintf("base%d", pi), fmt.Sprintf("ceil%d-%d", li, pi)}
						built = append(built, p)
					}
					c.ceilings = append(c.ceilings, built)
				}
				if sc.permit != nil {
					p := *sc.permit
					p.keys = []string{"base0", "scope"}
					c.scoping = &p
				}
				cases = append(cases, c)
			}
		}
	}
	return cases
}

// TestAccessCeilingsMatrix checks every verdict of every shape against the oracle.
func TestAccessCeilingsMatrix(t *testing.T) {
	cases := matrixCases()
	require.Len(t, cases, 6*21*5)
	for _, c := range cases {
		t.Run(c.name(), func(t *testing.T) {
			bases := make([]schemas.Permit, 0, len(c.bases))
			var names []string
			for i, p := range c.bases {
				id := fmt.Sprintf("base%d", i)
				bases = append(bases, p.build(id))
				names = append(names, id)
			}
			var layers [][]schemas.Permit
			for li, layer := range c.ceilings {
				var built []schemas.Permit
				for pi, p := range layer {
					built = append(built, p.build(fmt.Sprintf("ceil%d-%d", li, pi)))
				}
				layers = append(layers, built)
			}
			var scoping schemas.Permit
			if c.scoping != nil {
				scoping = c.scoping.build("scope")
			}
			access := NewAccess(bases, scoping, c.mode, nil, WithCeilings(layers...))
			callerAllows, want := c.oracle()

			// Every dimension answers to the same rule.
			assert.Equal(t, want, access.IsModelAllowed("openai", "gpt-4o"), "model")
			assert.Equal(t, want, access.IsAgentAllowed("agent-a"), "agent")
			assert.Equal(t, want, access.IsRelayAllowed("relay-a"), "relay")
			assert.Equal(t, want, access.IsMCPToolAllowed("github-read_file"), "tool")
			assert.Equal(t, want, slices.Contains(access.MCPToolIncludeList(), "github-read_file"), "tool include list")

			// A refusal names someone exactly when the request is refused and someone holds a permit.
			denied := access.DeniedPermitsForModel("openai", "gpt-4o")
			// A caller holding no permit has nothing of their own to name; only a union scope that
			// refused is named then.
			namesSomeone := len(bases) > 0 || (c.scoping != nil && c.mode == Union)
			assert.Equal(t, !want && namesSomeone, len(denied) > 0, "denied permits named: %v", permitIDs(denied))
			// When the caller's permits allow and a layer refuses (and no union scope rescues it), the
			// refusal names exactly the refusing layers, never the caller's own permits.
			if !want && anyAllows(c.bases) && !callerAllows && (c.scoping == nil || c.scoping.allows) {
				var refusing []string
				for li, layer := range c.ceilings {
					if !anyAllows(layer) {
						for pi := range layer {
							refusing = append(refusing, fmt.Sprintf("ceil%d-%d", li, pi))
						}
					}
				}
				assert.Equal(t, refusing, permitIDs(denied), "the refusing layers are named")
			}

			// Attribution and candidates follow the verdict.
			permits := access.PermitsForModel("openai", "gpt-4o")
			assert.Equal(t, want, len(permits) > 0, "permits for the model")
			if want && callerAllows {
				for i, p := range c.bases {
					assert.Equal(t, p.allows, slices.Contains(permitIDs(permits), names[i]), "base%d attributed", i)
				}
			}
			candidates := access.ProvidersForModel("gpt-4o")
			assert.Equal(t, want, len(candidates) > 0, "provider candidates")
			granted := access.GrantedProvidersForModel("gpt-4o")
			assert.Equal(t, want, slices.Contains(granted, "openai"), "granted providers")

			// The key restriction is the rule's.
			keyIDs, restricted := access.KeysForModel("openai", "gpt-4o")
			wantKeys := c.oracleKeys()
			if want {
				assert.True(t, restricted, "every permit restricts keys")
				assert.ElementsMatch(t, wantKeys, keyIDs, "keys")
			} else {
				assert.False(t, restricted)
				assert.Nil(t, keyIDs)
			}
		})
	}
}

// permitIDs lists permit ids, in order.
func permitIDs(permits []schemas.Permit) []string {
	ids := make([]string, 0, len(permits))
	for _, p := range permits {
		ids = append(ids, p.ID())
	}
	return ids
}
