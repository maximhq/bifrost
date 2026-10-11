package anthropic

import (
	"reflect"
	"strings"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
)

const (
	// Keep compatibility normalization bounded. A compact schema can otherwise
	// produce exponentially many intermediate branches through nested unions.
	maxAnthropicSchemaExpansionDepth = 32
	maxAnthropicSchemaBranches       = 64
)

// anthropicSchemaBranch is one object-shaped alternative extracted from a
// top-level JSON Schema composition.
type anthropicSchemaBranch struct {
	properties                *schemas.OrderedMap
	required                  []string
	additionalPropertiesFalse bool
}

// normalizeAnthropicToolInputSchema converts root-level JSON Schema
// compositions into an object schema accepted by Anthropic. Anthropic accepts
// composition inside properties, but rejects oneOf, anyOf, and allOf directly
// on a custom tool's input_schema. Schemas without a root composition take a
// zero-allocation fast path. Rewrites use a new root and property map while
// sharing immutable nested definitions, so the caller-owned request is not
// mutated and large $defs trees are not copied twice before normalization.
func normalizeAnthropicToolInputSchema(input *schemas.ToolFunctionParameters) (*schemas.ToolFunctionParameters, error) {
	if input == nil {
		return nil, nil
	}
	if len(input.OneOf) == 0 && len(input.AnyOf) == 0 && len(input.AllOf) == 0 {
		return input, nil
	}

	out := new(*input)

	base := anthropicSchemaBranch{
		properties:                out.Properties,
		required:                  append([]string(nil), out.Required...),
		additionalPropertiesFalse: hasFalseAdditionalProperties(out.AdditionalProperties),
	}
	branches := []anthropicSchemaBranch{base}

	// Multiple composition keywords at the root are cumulative constraints.
	// Expanding each group in turn preserves their object fields before the
	// alternatives are broadened into the Anthropic-compatible root object.
	for _, alternatives := range [][]schemas.OrderedMap{out.OneOf, out.AnyOf} {
		if len(alternatives) == 0 {
			continue
		}
		if len(alternatives) > maxAnthropicSchemaBranches {
			return nil, anthropicSchemaComplexityError("expanded branch count exceeds %d", maxAnthropicSchemaBranches)
		}
		expanded := make([]anthropicSchemaBranch, 0, len(alternatives))
		for i := range alternatives {
			alternativeBranches, err := expandAnthropicSchemaBranch(&alternatives[i], out.Defs, out.Definitions, nil, 1)
			if err != nil {
				return nil, err
			}
			expanded, err = appendAnthropicSchemaBranches(expanded, alternativeBranches)
			if err != nil {
				return nil, err
			}
		}
		var err error
		branches, err = combineAnthropicSchemaBranches(branches, expanded)
		if err != nil {
			return nil, err
		}
	}

	// Every allOf member applies, so combine each member with the accumulated
	// alternatives rather than treating the members as alternatives themselves.
	for i := range out.AllOf {
		expanded, err := expandAnthropicSchemaBranch(&out.AllOf[i], out.Defs, out.Definitions, nil, 1)
		if err != nil {
			return nil, err
		}
		branches, err = combineAnthropicSchemaBranches(branches, expanded)
		if err != nil {
			return nil, err
		}
	}

	out.Type = "object"
	out.Properties = mergeAnthropicBranchProperties(branches)
	out.Required = intersectAnthropicBranchRequired(branches)
	if allAnthropicBranchesDisallowAdditionalProperties(branches) {
		allow := false
		out.AdditionalProperties = &schemas.AdditionalPropertiesStruct{AdditionalPropertiesBool: &allow}
	}
	out.OneOf = nil
	out.AnyOf = nil
	out.AllOf = nil
	return out, nil
}

// expandAnthropicSchemaBranch resolves local root references and recursively
// expands root-level compositions into object-shaped alternatives.
func expandAnthropicSchemaBranch(schema *schemas.OrderedMap, defs, definitions *schemas.OrderedMap, seen map[string]bool, depth int) ([]anthropicSchemaBranch, error) {
	if depth > maxAnthropicSchemaExpansionDepth {
		return nil, anthropicSchemaComplexityError("composition depth exceeds %d", maxAnthropicSchemaExpansionDepth)
	}
	if schema == nil {
		return []anthropicSchemaBranch{{properties: schemas.NewOrderedMap()}}, nil
	}
	resolved, err := resolveAnthropicSchemaRootRef(schema, defs, definitions, seen, depth)
	if err != nil {
		return nil, err
	}
	base := anthropicSchemaBranch{properties: schemas.NewOrderedMap()}
	if properties, ok := resolved.Get("properties"); ok {
		if object := anthropicOrderedMap(properties); object != nil {
			base.properties = object
		}
	}
	if required, ok := resolved.Get("required"); ok {
		base.required = anthropicStringSlice(required)
	}
	if additional, ok := resolved.Get("additionalProperties"); ok {
		if value, ok := additional.(bool); ok && !value {
			base.additionalPropertiesFalse = true
		}
	}

	branches := []anthropicSchemaBranch{base}
	for _, keyword := range []string{"oneOf", "anyOf"} {
		value, ok := resolved.Get(keyword)
		if !ok {
			continue
		}
		alternatives := anthropicOrderedMapSlice(value)
		if len(alternatives) > maxAnthropicSchemaBranches {
			return nil, anthropicSchemaComplexityError("expanded branch count exceeds %d", maxAnthropicSchemaBranches)
		}
		expanded := make([]anthropicSchemaBranch, 0, len(alternatives))
		for _, alternative := range alternatives {
			alternativeBranches, err := expandAnthropicSchemaBranch(alternative, defs, definitions, cloneAnthropicSeenRefs(seen), depth+1)
			if err != nil {
				return nil, err
			}
			expanded, err = appendAnthropicSchemaBranches(expanded, alternativeBranches)
			if err != nil {
				return nil, err
			}
		}
		branches, err = combineAnthropicSchemaBranches(branches, expanded)
		if err != nil {
			return nil, err
		}
	}
	if value, ok := resolved.Get("allOf"); ok {
		for _, member := range anthropicOrderedMapSlice(value) {
			memberBranches, err := expandAnthropicSchemaBranch(member, defs, definitions, cloneAnthropicSeenRefs(seen), depth+1)
			if err != nil {
				return nil, err
			}
			branches, err = combineAnthropicSchemaBranches(branches, memberBranches)
			if err != nil {
				return nil, err
			}
		}
	}
	return branches, nil
}

func appendAnthropicSchemaBranches(destination, source []anthropicSchemaBranch) ([]anthropicSchemaBranch, error) {
	if len(source) > maxAnthropicSchemaBranches-len(destination) {
		return nil, anthropicSchemaComplexityError("expanded branch count exceeds %d", maxAnthropicSchemaBranches)
	}
	return append(destination, source...), nil
}

func anthropicSchemaComplexityError(format string, args ...any) error {
	return providerUtils.InvalidRequestErrorf("anthropic tool input schema is too complex: "+format, args...)
}

// cloneAnthropicSeenRefs isolates reference-cycle tracking between sibling
// alternatives while retaining the reference chain that led to their parent.
func cloneAnthropicSeenRefs(seen map[string]bool) map[string]bool {
	if seen == nil {
		return nil
	}
	cloned := make(map[string]bool, len(seen))
	for ref, visited := range seen {
		cloned[ref] = visited
	}
	return cloned
}

// resolveAnthropicSchemaRootRef follows a local $defs or definitions reference
// used as a composition branch. Cycles stop at the last resolvable schema.
func resolveAnthropicSchemaRootRef(schema *schemas.OrderedMap, defs, definitions *schemas.OrderedMap, seen map[string]bool, depth int) (*schemas.OrderedMap, error) {
	if depth > maxAnthropicSchemaExpansionDepth {
		return nil, anthropicSchemaComplexityError("reference depth exceeds %d", maxAnthropicSchemaExpansionDepth)
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

	var collection *schemas.OrderedMap
	var name string
	switch {
	case strings.HasPrefix(ref, "#/$defs/"):
		collection = defs
		name = strings.TrimPrefix(ref, "#/$defs/")
	case strings.HasPrefix(ref, "#/definitions/"):
		collection = definitions
		name = strings.TrimPrefix(ref, "#/definitions/")
	default:
		return schema, nil
	}
	value, ok := collection.Get(name)
	if !ok {
		return schema, nil
	}
	resolved := anthropicOrderedMap(value)
	if resolved == nil {
		return schema, nil
	}
	return resolveAnthropicSchemaRootRef(resolved, defs, definitions, seen, depth+1)
}

// combineAnthropicSchemaBranches computes the combinations imposed by two
// schema groups while retaining every property seen in either side.
func combineAnthropicSchemaBranches(left, right []anthropicSchemaBranch) ([]anthropicSchemaBranch, error) {
	if len(right) == 0 {
		return left, nil
	}
	if len(left) > maxAnthropicSchemaBranches/len(right) {
		return nil, anthropicSchemaComplexityError("expanded branch count exceeds %d", maxAnthropicSchemaBranches)
	}
	combined := make([]anthropicSchemaBranch, 0, len(left)*len(right))
	for _, lhs := range left {
		for _, rhs := range right {
			properties := schemas.NewOrderedMapWithCapacity(lhs.properties.Len() + rhs.properties.Len())
			mergeAnthropicPropertiesInto(properties, lhs.properties, "allOf")
			mergeAnthropicPropertiesInto(properties, rhs.properties, "allOf")
			combined = append(combined, anthropicSchemaBranch{
				properties:                properties,
				required:                  unionAnthropicStrings(lhs.required, rhs.required),
				additionalPropertiesFalse: lhs.additionalPropertiesFalse || rhs.additionalPropertiesFalse,
			})
		}
	}
	return combined, nil
}

// mergeAnthropicBranchProperties creates a deterministic union of all branch
// properties. Conflicting definitions become a property-level anyOf, which is
// accepted by Anthropic even though the same keyword is rejected at the root.
func mergeAnthropicBranchProperties(branches []anthropicSchemaBranch) *schemas.OrderedMap {
	merged := schemas.NewOrderedMap()
	for _, branch := range branches {
		mergeAnthropicPropertiesInto(merged, branch.properties, "anyOf")
	}
	return merged
}

// mergeAnthropicPropertiesInto appends source properties to destination and
// combines conflicting definitions under the requested composition keyword.
func mergeAnthropicPropertiesInto(destination, source *schemas.OrderedMap, composition string) {
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
		members := anthropicPropertyCompositionMembers(existing, composition)
		for _, member := range members {
			if reflect.DeepEqual(member, definition) {
				return true
			}
		}
		members = append(members, definition)
		destination.Set(name, schemas.NewOrderedMapFromPairs(
			schemas.KV(composition, members),
		))
		return true
	})
}

// anthropicPropertyCompositionMembers unwraps an existing property-level
// composition so repeated definitions remain flat and de-duplicated.
func anthropicPropertyCompositionMembers(value interface{}, composition string) []interface{} {
	object := anthropicOrderedMap(value)
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

// intersectAnthropicBranchRequired returns fields required by every expanded
// alternative, preserving the order of the first branch.
func intersectAnthropicBranchRequired(branches []anthropicSchemaBranch) []string {
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

// allAnthropicBranchesDisallowAdditionalProperties reports whether every
// expanded alternative explicitly closes its object shape.
func allAnthropicBranchesDisallowAdditionalProperties(branches []anthropicSchemaBranch) bool {
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
func hasFalseAdditionalProperties(value *schemas.AdditionalPropertiesStruct) bool {
	return value != nil && value.AdditionalPropertiesBool != nil && !*value.AdditionalPropertiesBool
}

// anthropicOrderedMap converts schema object representations into OrderedMap.
func anthropicOrderedMap(value interface{}) *schemas.OrderedMap {
	switch typed := value.(type) {
	case *schemas.OrderedMap:
		return typed
	case schemas.OrderedMap:
		return &typed
	case map[string]interface{}:
		return schemas.OrderedMapFromMap(typed)
	default:
		return nil
	}
}

// anthropicOrderedMapSlice converts a decoded JSON Schema array into objects.
func anthropicOrderedMapSlice(value interface{}) []*schemas.OrderedMap {
	var values []interface{}
	switch typed := value.(type) {
	case []interface{}:
		values = typed
	case []schemas.OrderedMap:
		values = make([]interface{}, len(typed))
		for i := range typed {
			values[i] = &typed[i]
		}
	case []*schemas.OrderedMap:
		return typed
	default:
		return nil
	}
	objects := make([]*schemas.OrderedMap, 0, len(values))
	for _, item := range values {
		if object := anthropicOrderedMap(item); object != nil {
			objects = append(objects, object)
		}
	}
	return objects
}

// anthropicStringSlice converts decoded JSON arrays and typed string slices.
func anthropicStringSlice(value interface{}) []string {
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

// unionAnthropicStrings returns a stable, de-duplicated union.
func unionAnthropicStrings(left, right []string) []string {
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
