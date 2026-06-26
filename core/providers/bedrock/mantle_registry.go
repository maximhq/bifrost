package bedrock

import (
	"regexp"
	"strings"
	"sync"
)

// mantleRegistry caches, per AWS region, the set of models that are served ONLY
// by the Bedrock Mantle endpoint (i.e. present in the mantle /v1/models catalog
// but absent from ListFoundationModels). Membership is the authoritative signal
// for routing a request to mantle, replacing the name-substring heuristic for any
// model that has been observed via ListModels.
//
// Keyed by AWS region because mantle availability is region-specific: a model can
// be mantle-only in one region and absent (or Converse-served) in another, so the
// routing decision must match the region the request will actually hit.
//
// Why "mantle-only" and not merely "in the mantle catalog": some models (e.g.
// Gemma 3) appear on mantle for Chat but also have a Converse fallback that
// serves both Chat and Responses. Routing those to mantle would break Responses.
// A model that is mantle-only has no Converse fallback, so it must go to mantle.
// See shouldRouteToMantle for how this composes with the name heuristic.
type mantleRegistry struct {
	mu       sync.RWMutex
	byRegion map[string]map[string]struct{} // region -> set of bare mantle-only model names
}

func newMantleRegistry() *mantleRegistry {
	return &mantleRegistry{byRegion: make(map[string]map[string]struct{})}
}

// addRegion records the given catalog IDs as mantle-only for a region. IDs are
// normalized to bare form so lookups are insensitive to region/vendor prefixes.
// The set only grows (union, not replace): ListModels populates per key
// concurrently and different keys may surface different filtered subsets for the
// same region, so unioning avoids one key's write clobbering another's. Stale
// entries aren't worth evicting — the mantle-only catalog is small and stable,
// and a routing miss harmlessly falls back to the Converse path.
func (r *mantleRegistry) addRegion(awsRegion string, ids []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	set := r.byRegion[awsRegion]
	if set == nil {
		set = make(map[string]struct{}, len(ids))
		r.byRegion[awsRegion] = set
	}
	for _, id := range ids {
		if bare := bareMantleID(id); bare != "" {
			set[bare] = struct{}{}
		}
	}
}

// isMantleOnly reports whether model is known to be mantle-only in the given
// region. Returns false before ListModels has populated the region, in which case
// routing falls back to the legacy name heuristic (never worse than the
// pre-registry behavior).
func (r *mantleRegistry) isMantleOnly(awsRegion, model string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	set := r.byRegion[awsRegion]
	if set == nil {
		return false
	}
	_, ok := set[bareMantleID(model)]
	return ok
}

// mantlePrefixRegex matches the leading prefixes of a Bedrock model ID that carry
// no identity of their own and must be stripped before comparison:
//   - an optional "region/" path prefix (e.g. "us-east-1/"), reusing the shared
//     awsRegionPattern so the definition of a region lives in exactly one place;
//   - the namespace segments — runs of pure ASCII letters each followed by '.' —
//     which cover both the vendor prefix ("openai.", "deepseek.", "zai.", ...) and
//     any cross-region inference-profile prefix ("us.", "eu.", "global.").
//
// The namespace rule is structural rather than a hardcoded vendor list: real model
// tokens always carry a digit or dash ("gpt-5.5", "v3.1", "glm-4.6"), so a version
// dot is never matched and new vendors need no code change.
var mantlePrefixRegex = regexp.MustCompile(`^(?:` + awsRegionPattern + `/)?(?:[a-z]+\.)*`)

// bareMantleID normalizes a model identifier to a prefix-insensitive form so that
// the registry (populated from catalog IDs) and routing lookups (using the
// requested model) agree regardless of region-path, geo, or vendor prefixes.
//
// Examples:
//
//	"us-east-1/openai.gpt-oss-120b" -> "gpt-oss-120b"
//	"us.openai.gpt-oss-120b"        -> "gpt-oss-120b"
//	"deepseek.v3.1"                 -> "v3.1"
//	"gpt-5.5"                       -> "gpt-5.5"  (version dot preserved)
func bareMantleID(model string) string {
	s := strings.ToLower(strings.TrimSpace(model))
	return mantlePrefixRegex.ReplaceAllString(s, "")
}
