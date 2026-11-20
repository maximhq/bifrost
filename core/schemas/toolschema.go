package schemas

import (
	"errors"
	"fmt"
	"net/url"
	"reflect"
	"strings"
)

// ErrToolSchemaTooComplex reports a root composition FlattenRootComposition will not expand.
var ErrToolSchemaTooComplex = errors.New("tool input schema is too complex")

const (
	// Keep composition flattening bounded. A compact schema can otherwise
	// produce exponentially many intermediate branches through nested unions.
	maxCompositionExpansionDepth = 32
	maxCompositionBranches       = 64
	// maxCompositionSteps bounds the total work: branch and depth caps alone let repeated $refs
	// inside nested allOf members expand exponentially while each step yields one branch.
	maxCompositionSteps = 4096
)

// compositionBranch is one object-shaped alternative extracted from a
// top-level JSON Schema composition.
type compositionBranch struct {
	properties                *OrderedMap
	required                  []string
	additionalPropertiesFalse bool
}

// FlattenRootComposition merges a root-level oneOf, anyOf or allOf into one
// object schema: properties are unioned, conflicting definitions become a
// property-level anyOf, and only fields every alternative requires stay
// required. Providers that reject a root composition and code-mode stubs use
// it. It returns ErrToolSchemaTooComplex past its depth, branch and work limits.
//
// The result approximates the composition: it is what a provider or a model is shown, not a
// validator. It covers what schema generators emit (local $defs/definitions references, $ref
// siblings, boolean members); exact validation stays with whoever receives the arguments.
// Schemas without a root composition take a zero-allocation fast path.
// Rewrites use a new root and property map while sharing immutable nested
// definitions, so the caller-owned schema is not mutated and large $defs
// trees are not copied twice.
func FlattenRootComposition(input *ToolFunctionParameters) (*ToolFunctionParameters, error) {
	if input == nil {
		return nil, nil
	}
	if len(input.OneOf) == 0 && len(input.AnyOf) == 0 && len(input.AllOf) == 0 {
		return input, nil
	}

	out := new(*input)

	base := compositionBranch{
		properties:                out.Properties,
		required:                  append([]string(nil), out.Required...),
		additionalPropertiesFalse: hasFalseAdditionalProperties(out.AdditionalProperties),
	}
	branches := []compositionBranch{base}
	steps := maxCompositionSteps

	// Multiple composition keywords at the root are cumulative constraints.
	// Expanding each group in turn preserves their object fields before the
	// alternatives are broadened into a single root object.
	for _, alternatives := range [][]OrderedMap{out.OneOf, out.AnyOf} {
		if len(alternatives) == 0 {
			continue
		}
		if len(alternatives) > maxCompositionBranches {
			return nil, compositionComplexityError("expanded branch count exceeds %d", maxCompositionBranches)
		}
		expanded := make([]compositionBranch, 0, len(alternatives))
		for i := range alternatives {
			if neverMatches(&alternatives[i]) {
				continue
			}
			alternativeBranches, err := expandCompositionBranch(&alternatives[i], out.Defs, out.Definitions, nil, 1, &steps)
			if err != nil {
				return nil, err
			}
			expanded, err = appendCompositionBranches(expanded, alternativeBranches)
			if err != nil {
				return nil, err
			}
		}
		var err error
		branches, err = combineCompositionBranches(branches, expanded)
		if err != nil {
			return nil, err
		}
	}

	// Every allOf member applies, so combine each member with the accumulated
	// alternatives rather than treating the members as alternatives themselves.
	for i := range out.AllOf {
		expanded, err := expandCompositionBranch(&out.AllOf[i], out.Defs, out.Definitions, nil, 1, &steps)
		if err != nil {
			return nil, err
		}
		branches, err = combineCompositionBranches(branches, expanded)
		if err != nil {
			return nil, err
		}
	}

	out.Type = "object"
	out.Properties = mergeCompositionBranchProperties(branches)
	out.Required = intersectCompositionBranchRequired(branches)
	if allCompositionBranchesDisallowAdditionalProperties(branches) {
		allow := false
		out.AdditionalProperties = &AdditionalPropertiesStruct{AdditionalPropertiesBool: &allow}
	}
	out.OneOf = nil
	out.AnyOf = nil
	out.AllOf = nil
	return out, nil
}

// expandCompositionBranch resolves local root references and recursively
// expands root-level compositions into object-shaped alternatives.
func expandCompositionBranch(schema *OrderedMap, defs, definitions *OrderedMap, seen map[string]bool, depth int, steps *int) ([]compositionBranch, error) {
	if depth > maxCompositionExpansionDepth {
		return nil, compositionComplexityError("composition depth exceeds %d", maxCompositionExpansionDepth)
	}
	if *steps--; *steps < 0 {
		return nil, compositionComplexityError("expansion work exceeds %d steps", maxCompositionSteps)
	}
	if schema == nil {
		return []compositionBranch{{properties: NewOrderedMap()}}, nil
	}
	// Keywords beside a $ref apply together with its target (draft 2020-12), so expand the two as an allOf.
	if siblings := refSiblings(schema); siblings != nil {
		ref, _ := schema.Get("$ref")
		refBranches, err := expandCompositionBranch(NewOrderedMapFromPairs(KV("$ref", ref)), defs, definitions, cloneCompositionSeenRefs(seen), depth+1, steps)
		if err != nil {
			return nil, err
		}
		siblingBranches, err := expandCompositionBranch(siblings, defs, definitions, cloneCompositionSeenRefs(seen), depth+1, steps)
		if err != nil {
			return nil, err
		}
		return combineCompositionBranches(refBranches, siblingBranches)
	}
	// The resolver records each reference it follows; keep that history for a resolved target's
	// own expansion, or a self-reference beside sibling keywords recurses to the depth limit.
	if seen == nil {
		seen = make(map[string]bool)
	}
	resolved, err := resolveCompositionRootRef(schema, defs, definitions, seen, depth, steps)
	if err != nil {
		return nil, err
	}
	if resolved != schema && refSiblings(resolved) != nil {
		return expandCompositionBranch(resolved, defs, definitions, seen, depth+1, steps)
	}
	base := compositionBranch{properties: NewOrderedMap()}
	if properties, ok := resolved.Get("properties"); ok {
		if object := compositionOrderedMap(properties); object != nil {
			base.properties = object
		}
	}
	if required, ok := resolved.Get("required"); ok {
		base.required = compositionStringSlice(required)
	}
	if additional, ok := resolved.Get("additionalProperties"); ok {
		if value, ok := additional.(bool); ok && !value {
			base.additionalPropertiesFalse = true
		}
	}

	branches := []compositionBranch{base}
	for _, keyword := range []string{"oneOf", "anyOf"} {
		value, ok := resolved.Get(keyword)
		if !ok {
			continue
		}
		alternatives := compositionOrderedMapSlice(value)
		if len(alternatives) > maxCompositionBranches {
			return nil, compositionComplexityError("expanded branch count exceeds %d", maxCompositionBranches)
		}
		expanded := make([]compositionBranch, 0, len(alternatives))
		for _, alternative := range alternatives {
			if neverMatches(alternative) {
				continue
			}
			alternativeBranches, err := expandCompositionBranch(alternative, defs, definitions, cloneCompositionSeenRefs(seen), depth+1, steps)
			if err != nil {
				return nil, err
			}
			expanded, err = appendCompositionBranches(expanded, alternativeBranches)
			if err != nil {
				return nil, err
			}
		}
		branches, err = combineCompositionBranches(branches, expanded)
		if err != nil {
			return nil, err
		}
	}
	if value, ok := resolved.Get("allOf"); ok {
		for _, member := range compositionOrderedMapSlice(value) {
			memberBranches, err := expandCompositionBranch(member, defs, definitions, cloneCompositionSeenRefs(seen), depth+1, steps)
			if err != nil {
				return nil, err
			}
			branches, err = combineCompositionBranches(branches, memberBranches)
			if err != nil {
				return nil, err
			}
		}
	}
	return branches, nil
}

func appendCompositionBranches(destination, source []compositionBranch) ([]compositionBranch, error) {
	if len(source) > maxCompositionBranches-len(destination) {
		return nil, compositionComplexityError("expanded branch count exceeds %d", maxCompositionBranches)
	}
	return append(destination, source...), nil
}

func compositionComplexityError(format string, args ...any) error {
	return fmt.Errorf("%w: "+format, append([]any{ErrToolSchemaTooComplex}, args...)...)
}

// cloneCompositionSeenRefs isolates reference-cycle tracking between sibling
// alternatives while retaining the reference chain that led to their parent.
func cloneCompositionSeenRefs(seen map[string]bool) map[string]bool {
	if seen == nil {
		return nil
	}
	cloned := make(map[string]bool, len(seen))
	for ref, visited := range seen {
		cloned[ref] = visited
	}
	return cloned
}

// resolveCompositionRootRef follows a local $defs or definitions reference
// used as a composition branch. Cycles stop at the last resolvable schema.
func resolveCompositionRootRef(schema *OrderedMap, defs, definitions *OrderedMap, seen map[string]bool, depth int, steps *int) (*OrderedMap, error) {
	if depth > maxCompositionExpansionDepth {
		return nil, compositionComplexityError("reference depth exceeds %d", maxCompositionExpansionDepth)
	}
	if *steps--; *steps < 0 {
		return nil, compositionComplexityError("expansion work exceeds %d steps", maxCompositionSteps)
	}
	refValue, ok := schema.Get("$ref")
	if !ok {
		return schema, nil
	}
	ref, ok := refValue.(string)
	if !ok {
		return schema, nil
	}
	if seen == nil {
		seen = make(map[string]bool)
	}
	if seen[ref] {
		return schema, nil
	}
	seen[ref] = true

	// A $ref is a URI fragment: percent-decode it once, then read it as a JSON Pointer.
	pointer := ref
	if unescaped, err := url.PathUnescape(ref); err == nil {
		pointer = unescaped
	}
	var collection *OrderedMap
	var name string
	switch {
	case strings.HasPrefix(pointer, "#/$defs/"):
		collection = defs
		name = strings.TrimPrefix(pointer, "#/$defs/")
	case strings.HasPrefix(pointer, "#/definitions/"):
		collection = definitions
		name = strings.TrimPrefix(pointer, "#/definitions/")
	default:
		return schema, nil
	}
	name, ok = refPointerToken(name)
	if !ok {
		return schema, nil
	}
	value, ok := collection.Get(name)
	if !ok {
		return schema, nil
	}
	resolved := compositionOrderedMap(value)
	if resolved == nil {
		return schema, nil
	}
	// A target that is itself a $ref with constraining siblings stops here, so the caller expands
	// it through the sibling-aware path instead of skipping to the end of the chain.
	if refSiblings(resolved) != nil {
		return resolved, nil
	}
	return resolveCompositionRootRef(resolved, defs, definitions, seen, depth+1, steps)
}

// combineCompositionBranches computes the combinations imposed by two
// schema groups while retaining every property seen in either side.
func combineCompositionBranches(left, right []compositionBranch) ([]compositionBranch, error) {
	if len(right) == 0 {
		return left, nil
	}
	if len(left) > maxCompositionBranches/len(right) {
		return nil, compositionComplexityError("expanded branch count exceeds %d", maxCompositionBranches)
	}
	combined := make([]compositionBranch, 0, len(left)*len(right))
	for _, lhs := range left {
		for _, rhs := range right {
			properties := NewOrderedMapWithCapacity(lhs.properties.Len() + rhs.properties.Len())
			mergeCompositionPropertiesInto(properties, lhs.properties, "allOf")
			mergeCompositionPropertiesInto(properties, rhs.properties, "allOf")
			combined = append(combined, compositionBranch{
				properties:                properties,
				required:                  unionCompositionStrings(lhs.required, rhs.required),
				additionalPropertiesFalse: lhs.additionalPropertiesFalse || rhs.additionalPropertiesFalse,
			})
		}
	}
	return combined, nil
}

// mergeCompositionBranchProperties creates a deterministic union of all branch
// properties. Conflicting definitions become a property-level anyOf.
func mergeCompositionBranchProperties(branches []compositionBranch) *OrderedMap {
	merged := NewOrderedMap()
	for _, branch := range branches {
		mergeCompositionPropertiesInto(merged, branch.properties, "anyOf")
	}
	return merged
}

// mergeCompositionPropertiesInto appends source properties to destination and
// combines conflicting definitions under the requested composition keyword.
func mergeCompositionPropertiesInto(destination, source *OrderedMap, composition string) {
	if source == nil {
		return
	}
	source.Range(func(name string, definition interface{}) bool {
		existing, ok := destination.Get(name)
		if !ok {
			destination.Set(name, definition)
			return true
		}
		if reflect.DeepEqual(existing, definition) {
			return true
		}
		members := compositionPropertyMembers(existing, composition)
		for _, member := range members {
			if reflect.DeepEqual(member, definition) {
				return true
			}
		}
		members = append(members, definition)
		destination.Set(name, NewOrderedMapFromPairs(
			KV(composition, members),
		))
		return true
	})
}

// compositionPropertyMembers unwraps an existing property-level
// composition so repeated definitions remain flat and de-duplicated.
func compositionPropertyMembers(value interface{}, composition string) []interface{} {
	object := compositionOrderedMap(value)
	if object == nil || object.Len() != 1 {
		return []interface{}{value}
	}
	members, ok := object.Get(composition)
	if !ok {
		return []interface{}{value}
	}
	if values, ok := members.([]interface{}); ok {
		return append([]interface{}(nil), values...)
	}
	return []interface{}{value}
}

// intersectCompositionBranchRequired returns fields required by every expanded
// alternative, preserving the order of the first branch.
func intersectCompositionBranchRequired(branches []compositionBranch) []string {
	if len(branches) == 0 {
		return nil
	}
	common := append([]string(nil), branches[0].required...)
	for _, branch := range branches[1:] {
		present := make(map[string]bool, len(branch.required))
		for _, name := range branch.required {
			present[name] = true
		}
		filtered := common[:0]
		for _, name := range common {
			if present[name] {
				filtered = append(filtered, name)
			}
		}
		common = filtered
	}
	return common
}

// allCompositionBranchesDisallowAdditionalProperties reports whether every
// expanded alternative explicitly closes its object shape.
func allCompositionBranchesDisallowAdditionalProperties(branches []compositionBranch) bool {
	if len(branches) == 0 {
		return false
	}
	for _, branch := range branches {
		if !branch.additionalPropertiesFalse {
			return false
		}
	}
	return true
}

// hasFalseAdditionalProperties recognizes the typed representation of
// additionalProperties:false.
func hasFalseAdditionalProperties(value *AdditionalPropertiesStruct) bool {
	return value != nil && value.AdditionalPropertiesBool != nil && !*value.AdditionalPropertiesBool
}

// compositionOrderedMap converts schema object representations into OrderedMap.
func compositionOrderedMap(value interface{}) *OrderedMap {
	switch typed := value.(type) {
	case *OrderedMap:
		return typed
	case OrderedMap:
		return &typed
	case map[string]interface{}:
		return OrderedMapFromMap(typed)
	default:
		return nil
	}
}

// compositionOrderedMapSlice converts a decoded JSON Schema array into objects.
func compositionOrderedMapSlice(value interface{}) []*OrderedMap {
	var values []interface{}
	switch typed := value.(type) {
	case []interface{}:
		values = typed
	case []OrderedMap:
		values = make([]interface{}, len(typed))
		for i := range typed {
			values[i] = &typed[i]
		}
	case []*OrderedMap:
		return typed
	default:
		return nil
	}
	objects := make([]*OrderedMap, 0, len(values))
	for _, item := range values {
		// A boolean schema: true accepts anything (an empty object), false accepts nothing.
		if accepts, ok := item.(bool); ok {
			if accepts {
				objects = append(objects, NewOrderedMap())
			} else {
				objects = append(objects, NewOrderedMapFromPairs(KV("not", NewOrderedMap())))
			}
			continue
		}
		if object := compositionOrderedMap(item); object != nil {
			objects = append(objects, object)
		}
	}
	return objects
}

// neverMatches reports a schema no value satisfies ({"not": {}}, the object form of false), which
// an alternative can drop without changing the union.
func neverMatches(schema *OrderedMap) bool {
	if schema == nil || schema.Len() != 1 {
		return false
	}
	value, ok := schema.Get("not")
	if !ok {
		return false
	}
	object := compositionOrderedMap(value)
	return object != nil && object.Len() == 0
}

// refPointerToken decodes one JSON Pointer token, the definition name in an already percent-decoded
// $ref: ~1 is "/" and ~0 is "~". A name holding a "/" points below a definition, which is not resolved.
func refPointerToken(name string) (string, bool) {
	if strings.Contains(name, "/") {
		return "", false
	}
	return strings.ReplaceAll(strings.ReplaceAll(name, "~1", "/"), "~0", "~"), true
}

// refAnnotations are keywords beside a $ref that describe it without constraining it.
var refAnnotations = map[string]bool{"$ref": true, "description": true, "title": true, "$comment": true, "examples": true, "default": true, "deprecated": true}

// refSiblings returns the constraining keywords beside a schema's $ref, or nil when it has no
// $ref or only annotations beside it.
func refSiblings(schema *OrderedMap) *OrderedMap {
	if _, ok := schema.Get("$ref"); !ok {
		return nil
	}
	var siblings *OrderedMap
	schema.Range(func(key string, value interface{}) bool {
		if !refAnnotations[key] {
			if siblings == nil {
				siblings = NewOrderedMap()
			}
			siblings.Set(key, value)
		}
		return true
	})
	return siblings
}

// compositionStringSlice converts decoded JSON arrays and typed string slices.
func compositionStringSlice(value interface{}) []string {
	switch typed := value.(type) {
	case []string:
		return append([]string(nil), typed...)
	case []interface{}:
		strings := make([]string, 0, len(typed))
		for _, item := range typed {
			if value, ok := item.(string); ok {
				strings = append(strings, value)
			}
		}
		return strings
	default:
		return nil
	}
}

// unionCompositionStrings returns a stable, de-duplicated union.
func unionCompositionStrings(left, right []string) []string {
	union := append([]string(nil), left...)
	seen := make(map[string]bool, len(left)+len(right))
	for _, value := range left {
		seen[value] = true
	}
	for _, value := range right {
		if !seen[value] {
			union = append(union, value)
			seen[value] = true
		}
	}
	return union
}
