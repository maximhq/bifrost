// Package operations contains the generated public OpenAPI operation catalog.
package operations

//go:generate go run ./gen -spec ../../../docs/openapi/openapi.json -out catalog.gen.go

import "strings"

// Operation identifies one public operation from the bundled Bifrost OpenAPI document.
type Operation struct {
	ID         string   `json:"id"`
	Method     string   `json:"method"`
	Path       string   `json:"path"`
	Summary    string   `json:"summary,omitempty"`
	Tags       []string `json:"tags,omitempty"`
	Deprecated bool     `json:"deprecated,omitempty"`
}

// Find returns an operation by its stable operation ID.
func Find(id string) (Operation, bool) {
	needle := strings.TrimSpace(id)
	for _, operation := range Catalog {
		if operation.ID == needle {
			return operation, true
		}
	}
	return Operation{}, false
}

// Search returns operations matching an optional term and tag.
func Search(term, tag string) []Operation {
	term = strings.ToLower(strings.TrimSpace(term))
	tag = strings.ToLower(strings.TrimSpace(tag))
	result := make([]Operation, 0, len(Catalog))
	for _, operation := range Catalog {
		haystack := strings.ToLower(operation.ID + " " + operation.Method + " " + operation.Path + " " + operation.Summary)
		if term != "" && !strings.Contains(haystack, term) {
			continue
		}
		if tag != "" && !containsTag(operation.Tags, tag) {
			continue
		}
		result = append(result, operation)
	}
	return result
}

// containsTag reports whether a tag list contains the requested lower-case tag.
func containsTag(tags []string, target string) bool {
	for _, tag := range tags {
		if strings.ToLower(tag) == target {
			return true
		}
	}
	return false
}
