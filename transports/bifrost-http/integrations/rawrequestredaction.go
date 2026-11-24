package integrations

import (
	"fmt"
	"maps"
	"strconv"
	"strings"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/tidwall/gjson"
)

// rawRequestTextPathCollector returns the integration-owned JSON string paths that correspond to mutable normalized text.
type rawRequestTextPathCollector func(root gjson.Result, replacements map[string]string) ([]string, error)

// rewriteRawRequestTextFields applies literal replacements only to paths selected by the native integration.
// It consumes caller-owned rawBody bytes so repeated field updates can reuse the same backing array, then
// verifies every requested literal was found and every patched value persisted before returning the body.
// A literal holding JSON escapes also matches its decoded form, which is how a field that carries a JSON
// document as decoded values (tool-call arguments as an object) holds it. Literals found nowhere are
// reported as a *schemas.RawRequestLiteralsNotFoundError.
func rewriteRawRequestTextFields(rawBody []byte, replacements map[string]string, collect rawRequestTextPathCollector) ([]byte, error) {
	if len(replacements) == 0 {
		return rawBody, nil
	}
	if len(rawBody) == 0 {
		return nil, fmt.Errorf("raw request body is empty")
	}
	if !gjson.ValidBytes(rawBody) {
		return nil, fmt.Errorf("raw request body is not valid JSON")
	}
	root := gjson.ParseBytes(rawBody)
	if err := validateRawRequestUniqueObjectKeys(root); err != nil {
		return nil, err
	}
	if collect == nil {
		return nil, fmt.Errorf("raw request body text path collector is unavailable")
	}

	paths, err := collect(root, replacements)
	if err != nil {
		return nil, err
	}

	// Each literal's decoded form is computed once here, not once per field.
	effective, decoded := replacements, map[string]string(nil)
	for literal, replacement := range replacements {
		form, ok := schemas.DecodedJSONLiteral(literal)
		if !ok {
			continue
		}
		if decoded == nil {
			effective, decoded = maps.Clone(replacements), make(map[string]string)
		}
		decoded[literal] = form
		if _, taken := replacements[form]; !taken {
			effective[form] = replacement
		}
	}
	found := make(map[string]bool, len(replacements))
	expected := make(map[string]string)
	seenPaths := make(map[string]struct{}, len(paths))
	patched := rawBody
	for _, path := range paths {
		if _, seen := seenPaths[path]; seen {
			continue
		}
		seenPaths[path] = struct{}{}

		field := gjson.GetBytes(patched, path)
		if !field.Exists() || field.Type != gjson.String {
			return nil, fmt.Errorf("raw request content path %q is no longer a string", path)
		}
		original := field.String()
		for literal := range replacements {
			if literal == "" || found[literal] {
				continue
			}
			if form, ok := decoded[literal]; strings.Contains(original, literal) || ok && strings.Contains(original, form) {
				found[literal] = true
			}
		}

		// Most owned fields hold no literal; skipping them avoids ordering the keys once per field.
		if !containsRuntimeLiteral(original, effective) {
			continue
		}
		redacted := schemas.ApplyLiteralReplacements(original, effective)
		if redacted == original {
			continue
		}
		// In-place SJSON updates avoid allocating one full raw body per changed field.
		// They still perform one path search and may shift JSON bytes for every field;
		// this is deliberately an allocation optimization rather than a bulk patcher.
		patched, err = providerUtils.SetJSONStringFieldInPlace(patched, path, redacted)
		if err != nil {
			return nil, fmt.Errorf("redact raw request content path %q: %w", path, err)
		}
		expected[path] = redacted
	}

	var missing []string
	for literal := range replacements {
		if literal != "" && !found[literal] {
			missing = append(missing, literal)
		}
	}
	if len(missing) > 0 {
		return nil, &schemas.RawRequestLiteralsNotFoundError{Literals: missing}
	}
	if !gjson.ValidBytes(patched) {
		return nil, fmt.Errorf("redacted raw request body is not valid JSON")
	}
	for path, want := range expected {
		field := gjson.GetBytes(patched, path)
		if !field.Exists() || field.Type != gjson.String || field.String() != want {
			return nil, fmt.Errorf("redacted raw request content path %q failed verification", path)
		}
	}
	return patched, nil
}

// validateRawRequestUniqueObjectKeys rejects ambiguous objects whose first and last values can differ across JSON parsers.
func validateRawRequestUniqueObjectKeys(value gjson.Result) error {
	switch {
	case value.IsObject():
		seen := make(map[string]struct{})
		var validationErr error
		value.ForEach(func(key, child gjson.Result) bool {
			keyText := key.String()
			if _, exists := seen[keyText]; exists {
				validationErr = fmt.Errorf("raw request body contains duplicate JSON object keys")
				return false
			}
			seen[keyText] = struct{}{}
			validationErr = validateRawRequestUniqueObjectKeys(child)
			return validationErr == nil
		})
		return validationErr
	case value.IsArray():
		var validationErr error
		value.ForEach(func(_, child gjson.Result) bool {
			validationErr = validateRawRequestUniqueObjectKeys(child)
			return validationErr == nil
		})
		return validationErr
	default:
		return nil
	}
}

// appendRawRequestStringPath adds an optional JSON string field to the integration-owned path set.
func appendRawRequestStringPath(paths *[]string, field gjson.Result, path string) error {
	if !field.Exists() || field.Type == gjson.Null {
		return nil
	}
	if field.Type != gjson.String {
		return fmt.Errorf("raw request content path %q must be a string", path)
	}
	*paths = append(*paths, path)
	return nil
}

// appendRawRequestJSONLeafPaths adds string values below an integration-owned JSON content subtree.
func appendRawRequestJSONLeafPaths(paths *[]string, value gjson.Result, path string, replacements map[string]string) error {
	switch {
	case !value.Exists() || value.Type == gjson.Null:
		return nil
	case value.Type == gjson.String:
		*paths = append(*paths, path)
		return nil
	case value.IsArray():
		var collectErr error
		value.ForEach(func(index, child gjson.Result) bool {
			collectErr = appendRawRequestJSONLeafPaths(paths, child, rawRequestArrayPath(path, int(index.Int())), replacements)
			return collectErr == nil
		})
		return collectErr
	case value.IsObject():
		var collectErr error
		value.ForEach(func(key, child gjson.Result) bool {
			if containsRuntimeLiteral(key.String(), replacements) {
				collectErr = fmt.Errorf("a runtime redaction literal appears in an unsupported JSON object key at %q", path)
				return false
			}
			collectErr = appendRawRequestJSONLeafPaths(paths, child, rawRequestObjectPath(path, key.String()), replacements)
			return collectErr == nil
		})
		return collectErr
	default:
		if containsRuntimeLiteral(value.Raw, replacements) {
			return fmt.Errorf("a runtime redaction literal appears in an unsupported non-string JSON value at %q", path)
		}
		return nil
	}
}

// collectRawRequestArgumentStringPaths selects the string values of tool-call arguments carried as a JSON value,
// never object keys or other value types, so tool metadata and argument structure stay unchanged.
func collectRawRequestArgumentStringPaths(paths *[]string, value gjson.Result, path string) error {
	if value.Type == gjson.String {
		return appendRawRequestStringPath(paths, value, path)
	}
	var err error
	if value.IsObject() || value.IsArray() {
		value.ForEach(func(key, child gjson.Result) bool {
			childPath := rawRequestObjectPath(path, key.String())
			if value.IsArray() {
				childPath = rawRequestArrayPath(path, int(key.Int()))
			}
			err = collectRawRequestArgumentStringPaths(paths, child, childPath)
			return err == nil
		})
	}
	return err
}

// containsRuntimeLiteral reports whether text contains any non-empty replacement key.
func containsRuntimeLiteral(text string, replacements map[string]string) bool {
	for literal := range replacements {
		if literal != "" && strings.Contains(text, literal) {
			return true
		}
	}
	return false
}

// rawRequestObjectPath appends one escaped object key to a GJSON/SJSON path.
func rawRequestObjectPath(path, key string) string {
	escaped := strings.ReplaceAll(gjson.Escape(key), ":", `\:`)
	if path == "" {
		return escaped
	}
	return path + "." + escaped
}

// rawRequestArrayPath appends one array index to a GJSON/SJSON path.
func rawRequestArrayPath(path string, index int) string {
	indexText := strconv.Itoa(index)
	if path == "" {
		return indexText
	}
	return path + "." + indexText
}
