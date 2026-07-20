package gigachat

import (
	"fmt"
	"strings"

	schemas "github.com/maximhq/bifrost/core/schemas"
)

func sanitizeGigaChatFunctionSchema(schema interface{}) (*schemas.ToolFunctionParameters, error) {
	if schema == nil {
		return nil, fmt.Errorf("function parameters JSON schema is required")
	}

	raw, err := schemas.MarshalSorted(schema)
	if err != nil {
		return nil, fmt.Errorf("function parameters JSON schema is invalid: %w", err)
	}

	var root schemas.OrderedMap
	if err := schemas.Unmarshal(raw, &root); err != nil {
		return nil, fmt.Errorf("function parameters JSON schema is invalid: %w", err)
	}

	sanitized, nullOnly, err := sanitizeGigaChatSchemaMap(&root, &root, make(map[string]bool), "$")
	if err != nil {
		return nil, err
	}
	if nullOnly {
		return nil, fmt.Errorf("function parameters JSON schema cannot be null-only")
	}

	sanitizedRaw, err := schemas.MarshalSorted(sanitized)
	if err != nil {
		return nil, fmt.Errorf("function parameters JSON schema is invalid: %w", err)
	}

	var parameters schemas.ToolFunctionParameters
	if err := schemas.Unmarshal(sanitizedRaw, &parameters); err != nil {
		return nil, fmt.Errorf("function parameters JSON schema is invalid after GigaChat sanitization: %w", err)
	}
	return &parameters, nil
}

func sanitizeGigaChatSchemaMap(schema *schemas.OrderedMap, root *schemas.OrderedMap, resolving map[string]bool, path string) (*schemas.OrderedMap, bool, error) {
	if schema == nil {
		return nil, false, fmt.Errorf("%s: JSON schema object is required", path)
	}

	if refValue, ok := schema.Get("$ref"); ok {
		ref, ok := refValue.(string)
		if !ok || strings.TrimSpace(ref) == "" {
			return nil, false, fmt.Errorf("%s: $ref must be a non-empty string", path)
		}
		ref = strings.TrimSpace(ref)
		if resolving[ref] {
			return nil, false, fmt.Errorf("%s: circular local $ref %q is not supported", path, ref)
		}
		resolved, ok := lookupGigaChatLocalSchemaRef(root, ref)
		if !ok {
			return nil, false, fmt.Errorf("%s: unsupported or unresolved local $ref %q", path, ref)
		}
		resolving[ref] = true
		merged, err := cloneGigaChatSchemaMap(resolved)
		if err != nil {
			delete(resolving, ref)
			return nil, false, fmt.Errorf("%s: clone $ref %q: %w", path, ref, err)
		}
		var copyErr error
		schema.Range(func(key string, value interface{}) bool {
			if key == "$ref" || key == "$defs" || key == "definitions" {
				return true
			}
			copied, err := cloneGigaChatSchemaValue(value)
			if err != nil {
				copyErr = err
				return false
			}
			merged.Set(key, copied)
			return true
		})
		if copyErr != nil {
			delete(resolving, ref)
			return nil, false, fmt.Errorf("%s: merge $ref siblings: %w", path, copyErr)
		}
		out, nullOnly, err := sanitizeGigaChatSchemaMap(merged, root, resolving, path)
		delete(resolving, ref)
		return out, nullOnly, err
	}

	for _, keyword := range []string{"anyOf", "oneOf"} {
		if _, ok := schema.Get(keyword); ok {
			return sanitizeGigaChatSchemaComposition(schema, root, resolving, path, keyword)
		}
	}

	if nullOnly, err := sanitizeGigaChatSchemaType(schema, path); err != nil || nullOnly {
		return nil, nullOnly, err
	}

	schema.Delete("nullable")

	if err := sanitizeGigaChatSchemaProperties(schema, root, resolving, path); err != nil {
		return nil, false, err
	}
	if err := sanitizeGigaChatSchemaItems(schema, root, resolving, path); err != nil {
		return nil, false, err
	}
	if err := sanitizeGigaChatSchemaAdditionalProperties(schema, root, resolving, path); err != nil {
		return nil, false, err
	}
	if nullOnly, err := sanitizeGigaChatSchemaAllOf(schema, root, resolving, path); err != nil || nullOnly {
		return nil, nullOnly, err
	}

	schema.Delete("$defs")
	schema.Delete("definitions")
	if typeValue, ok := schema.Get("type"); ok && typeValue == "object" {
		if _, hasProperties := schema.Get("properties"); !hasProperties {
			schema.Set("properties", schemas.NewOrderedMap())
		}
	}

	return schema, false, nil
}

func sanitizeGigaChatSchemaComposition(schema *schemas.OrderedMap, root *schemas.OrderedMap, resolving map[string]bool, path string, keyword string) (*schemas.OrderedMap, bool, error) {
	value, _ := schema.Get(keyword)
	branches, ok := value.([]interface{})
	if !ok {
		return nil, false, fmt.Errorf("%s.%s: expected an array", path, keyword)
	}

	var selected *schemas.OrderedMap
	for index, branch := range branches {
		branchMap, ok := asGigaChatSchemaMap(branch)
		if !ok {
			return nil, false, fmt.Errorf("%s.%s[%d]: expected a JSON schema object", path, keyword, index)
		}
		branchCopy, err := cloneGigaChatSchemaMap(branchMap)
		if err != nil {
			return nil, false, fmt.Errorf("%s.%s[%d]: clone branch: %w", path, keyword, index, err)
		}
		sanitizedBranch, nullOnly, err := sanitizeGigaChatSchemaMap(branchCopy, root, resolving, fmt.Sprintf("%s.%s[%d]", path, keyword, index))
		if err != nil {
			return nil, false, err
		}
		if nullOnly {
			continue
		}
		if selected != nil {
			return nil, false, fmt.Errorf("%s.%s: multiple non-null branches are not supported by GigaChat function schemas", path, keyword)
		}
		selected = sanitizedBranch
	}
	if selected == nil {
		return nil, true, nil
	}

	merged, err := cloneGigaChatSchemaMap(selected)
	if err != nil {
		return nil, false, fmt.Errorf("%s.%s: clone selected branch: %w", path, keyword, err)
	}
	var copyErr error
	schema.Range(func(key string, value interface{}) bool {
		if key == keyword || key == "$defs" || key == "definitions" {
			return true
		}
		copied, err := cloneGigaChatSchemaValue(value)
		if err != nil {
			copyErr = err
			return false
		}
		merged.Set(key, copied)
		return true
	})
	if copyErr != nil {
		return nil, false, fmt.Errorf("%s.%s: merge branch siblings: %w", path, keyword, copyErr)
	}
	return sanitizeGigaChatSchemaMap(merged, root, resolving, path)
}

func sanitizeGigaChatSchemaType(schema *schemas.OrderedMap, path string) (bool, error) {
	value, ok := schema.Get("type")
	if !ok {
		return false, nil
	}

	switch typed := value.(type) {
	case string:
		if typed == "null" {
			return true, nil
		}
		return false, nil
	case []interface{}:
		nonNullTypes := make([]string, 0, len(typed))
		for _, item := range typed {
			typeName, ok := item.(string)
			if !ok {
				return false, fmt.Errorf("%s.type: type arrays must contain strings", path)
			}
			if typeName != "null" {
				nonNullTypes = append(nonNullTypes, typeName)
			}
		}
		switch len(nonNullTypes) {
		case 0:
			return true, nil
		case 1:
			schema.Set("type", nonNullTypes[0])
			return false, nil
		default:
			return false, fmt.Errorf("%s.type: multiple non-null types are not supported by GigaChat function schemas", path)
		}
	default:
		return false, fmt.Errorf("%s.type: expected a string or string array", path)
	}
}

func sanitizeGigaChatSchemaProperties(schema *schemas.OrderedMap, root *schemas.OrderedMap, resolving map[string]bool, path string) error {
	value, ok := schema.Get("properties")
	if !ok || value == nil {
		return nil
	}

	properties, ok := asGigaChatSchemaMap(value)
	if !ok {
		return fmt.Errorf("%s.properties: expected an object", path)
	}

	sanitizedProperties := schemas.NewOrderedMap()
	var sanitizeErr error
	properties.Range(func(name string, propertyValue interface{}) bool {
		propertySchema, ok := asGigaChatSchemaMap(propertyValue)
		if !ok {
			sanitizeErr = fmt.Errorf("%s.properties.%s: expected a JSON schema object", path, name)
			return false
		}
		propertyCopy, err := cloneGigaChatSchemaMap(propertySchema)
		if err != nil {
			sanitizeErr = fmt.Errorf("%s.properties.%s: clone schema: %w", path, name, err)
			return false
		}
		sanitizedProperty, nullOnly, err := sanitizeGigaChatSchemaMap(propertyCopy, root, resolving, path+".properties."+name)
		if err != nil {
			sanitizeErr = err
			return false
		}
		if nullOnly {
			sanitizeErr = fmt.Errorf("%s.properties.%s: null-only schemas are not supported by GigaChat function schemas", path, name)
			return false
		}
		sanitizedProperties.Set(name, sanitizedProperty)
		return true
	})
	if sanitizeErr != nil {
		return sanitizeErr
	}

	schema.Set("properties", sanitizedProperties)
	return nil
}

func sanitizeGigaChatSchemaItems(schema *schemas.OrderedMap, root *schemas.OrderedMap, resolving map[string]bool, path string) error {
	value, ok := schema.Get("items")
	if !ok || value == nil {
		return nil
	}

	sanitized, nullOnly, err := sanitizeGigaChatNestedSchemaValue(value, root, resolving, path+".items")
	if err != nil {
		return err
	}
	if nullOnly {
		return fmt.Errorf("%s.items: null-only schemas are not supported by GigaChat function schemas", path)
	}
	schema.Set("items", sanitized)
	return nil
}

func sanitizeGigaChatSchemaAdditionalProperties(schema *schemas.OrderedMap, root *schemas.OrderedMap, resolving map[string]bool, path string) error {
	value, ok := schema.Get("additionalProperties")
	if !ok || value == nil {
		return nil
	}
	if _, ok := value.(bool); ok {
		return nil
	}

	sanitized, nullOnly, err := sanitizeGigaChatNestedSchemaValue(value, root, resolving, path+".additionalProperties")
	if err != nil {
		return err
	}
	if nullOnly {
		return fmt.Errorf("%s.additionalProperties: null-only schemas are not supported by GigaChat function schemas", path)
	}
	schema.Set("additionalProperties", sanitized)
	return nil
}

func sanitizeGigaChatSchemaAllOf(schema *schemas.OrderedMap, root *schemas.OrderedMap, resolving map[string]bool, path string) (bool, error) {
	value, ok := schema.Get("allOf")
	if !ok || value == nil {
		return false, nil
	}
	branches, ok := value.([]interface{})
	if !ok {
		return false, fmt.Errorf("%s.allOf: expected an array", path)
	}

	sanitizedBranches := make([]interface{}, 0, len(branches))
	for index, branch := range branches {
		sanitized, nullOnly, err := sanitizeGigaChatNestedSchemaValue(branch, root, resolving, fmt.Sprintf("%s.allOf[%d]", path, index))
		if err != nil {
			return false, err
		}
		if !nullOnly {
			sanitizedBranches = append(sanitizedBranches, sanitized)
		}
	}
	if len(branches) > 0 && len(sanitizedBranches) == 0 {
		return true, nil
	}
	schema.Set("allOf", sanitizedBranches)
	return false, nil
}

func sanitizeGigaChatNestedSchemaValue(value interface{}, root *schemas.OrderedMap, resolving map[string]bool, path string) (interface{}, bool, error) {
	if schemaMap, ok := asGigaChatSchemaMap(value); ok {
		schemaCopy, err := cloneGigaChatSchemaMap(schemaMap)
		if err != nil {
			return nil, false, fmt.Errorf("%s: clone schema: %w", path, err)
		}
		return sanitizeGigaChatSchemaMap(schemaCopy, root, resolving, path)
	}

	items, ok := value.([]interface{})
	if !ok {
		return value, false, nil
	}

	sanitizedItems := make([]interface{}, 0, len(items))
	for index, item := range items {
		sanitized, nullOnly, err := sanitizeGigaChatNestedSchemaValue(item, root, resolving, fmt.Sprintf("%s[%d]", path, index))
		if err != nil {
			return nil, false, err
		}
		if !nullOnly {
			sanitizedItems = append(sanitizedItems, sanitized)
		}
	}
	return sanitizedItems, len(sanitizedItems) == 0 && len(items) > 0, nil
}

func lookupGigaChatLocalSchemaRef(root *schemas.OrderedMap, ref string) (*schemas.OrderedMap, bool) {
	if root == nil || !strings.HasPrefix(ref, "#") {
		return nil, false
	}
	if ref == "#" {
		return root, true
	}
	if !strings.HasPrefix(ref, "#/") {
		return nil, false
	}

	var current interface{} = root
	for _, rawToken := range strings.Split(strings.TrimPrefix(ref, "#/"), "/") {
		token := decodeGigaChatJSONPointerToken(rawToken)
		currentMap, ok := asGigaChatSchemaMap(current)
		if !ok {
			return nil, false
		}
		next, ok := currentMap.Get(token)
		if !ok {
			return nil, false
		}
		current = next
	}
	return asGigaChatSchemaMap(current)
}

func decodeGigaChatJSONPointerToken(token string) string {
	token = strings.ReplaceAll(token, "~1", "/")
	return strings.ReplaceAll(token, "~0", "~")
}

func asGigaChatSchemaMap(value interface{}) (*schemas.OrderedMap, bool) {
	switch typed := value.(type) {
	case *schemas.OrderedMap:
		return typed, typed != nil
	case schemas.OrderedMap:
		return &typed, true
	case map[string]interface{}:
		return schemas.OrderedMapFromMap(typed), true
	default:
		return nil, false
	}
}

func cloneGigaChatSchemaMap(value *schemas.OrderedMap) (*schemas.OrderedMap, error) {
	if value == nil {
		return nil, nil
	}
	raw, err := schemas.MarshalSorted(value)
	if err != nil {
		return nil, err
	}
	var cloned schemas.OrderedMap
	if err := schemas.Unmarshal(raw, &cloned); err != nil {
		return nil, err
	}
	return &cloned, nil
}

func cloneGigaChatSchemaValue(value interface{}) (interface{}, error) {
	switch typed := value.(type) {
	case *schemas.OrderedMap:
		return cloneGigaChatSchemaMap(typed)
	case schemas.OrderedMap:
		return cloneGigaChatSchemaMap(&typed)
	case map[string]interface{}:
		copied := make(map[string]interface{}, len(typed))
		for key, item := range typed {
			itemCopy, err := cloneGigaChatSchemaValue(item)
			if err != nil {
				return nil, err
			}
			copied[key] = itemCopy
		}
		return copied, nil
	case []interface{}:
		copied := make([]interface{}, len(typed))
		for index, item := range typed {
			itemCopy, err := cloneGigaChatSchemaValue(item)
			if err != nil {
				return nil, err
			}
			copied[index] = itemCopy
		}
		return copied, nil
	default:
		return typed, nil
	}
}
