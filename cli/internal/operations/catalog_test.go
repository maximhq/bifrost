package operations

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// TestCatalogMatchesOpenAPI verifies every exposed operation still exists in the source document.
func TestCatalogMatchesOpenAPI(t *testing.T) {
	body, err := os.ReadFile("../../../docs/openapi/openapi.json")
	if err != nil {
		t.Fatal(err)
	}
	var document struct {
		Paths map[string]map[string]json.RawMessage `json:"paths"`
	}
	if err := json.Unmarshal(body, &document); err != nil {
		t.Fatal(err)
	}
	methods := map[string]bool{"get": true, "post": true, "put": true, "patch": true, "delete": true, "head": true, "options": true}
	ids := map[string]struct{}{}
	for _, item := range document.Paths {
		for method, raw := range item {
			if !methods[strings.ToLower(method)] {
				continue
			}
			var operation struct {
				ID string `json:"operationId"`
			}
			if err := json.Unmarshal(raw, &operation); err != nil {
				t.Fatal(err)
			}
			ids[operation.ID] = struct{}{}
		}
	}
	if len(Catalog) == 0 {
		t.Fatal("generated catalog is empty; run go generate ./internal/operations")
	}
	seen := make(map[string]struct{}, len(Catalog))
	for _, operation := range Catalog {
		if _, ok := ids[operation.ID]; !ok {
			t.Fatalf("generated operation %q is absent from OpenAPI", operation.ID)
		}
		if _, duplicate := seen[operation.ID]; duplicate {
			t.Fatalf("generated operation %q is duplicated", operation.ID)
		}
		seen[operation.ID] = struct{}{}
	}
}

// TestSearch filters operations by text and exact tag.
func TestSearch(t *testing.T) {
	matches := Search("virtual", "Virtual Keys")
	if len(matches) == 0 {
		t.Fatal("expected at least one virtual-key operation")
	}
	for _, operation := range matches {
		if !strings.Contains(strings.ToLower(operation.ID+operation.Path+operation.Summary), "virtual") {
			t.Fatalf("operation %q did not match the search term", operation.ID)
		}
		if !containsTag(operation.Tags, "virtual keys") {
			t.Fatalf("operation %q did not match the tag", operation.ID)
		}
	}
}
