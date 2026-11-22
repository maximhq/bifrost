package main

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"
)

func TestIncludeInCatalogAllowsOnlyDeclaredSDKBridgeMounts(t *testing.T) {
	canonical := openAPIOperation{Summary: "Create message (Anthropic format)", Tags: []string{"Anthropic Integration"}}
	if !includeInCatalog("/anthropic/v1/messages", canonical) {
		t.Fatal("canonical provider operation was excluded")
	}
	exposed := openAPIOperation{Summary: "Create message (LangChain - Anthropic format)", Tags: []string{"LangChain Integration"}}
	if !includeInCatalog("/langchain/anthropic/v1/messages", exposed) {
		t.Fatal("declared SDK bridge was excluded")
	}
	hidden := openAPIOperation{Summary: "Create message (LegacyBridge - Anthropic format)", Tags: []string{"LegacyBridge Integration"}}
	if includeInCatalog("/legacybridge/anthropic/v1/messages", hidden) {
		t.Fatal("undeclared SDK bridge was exposed")
	}
}

func TestCollectOperationsClassifiesDestructiveOperations(t *testing.T) {
	document := openAPIDocument{Paths: map[string]map[string]openAPIOperation{
		"/items/{id}":          {"delete": {OperationID: "deleteItem"}},
		"/batches/{id}/cancel": {"post": {OperationID: "cancelBatch"}},
		"/items":               {"post": {OperationID: "createItem"}},
	}}
	operations, err := collectOperations(document)
	if err != nil {
		t.Fatal(err)
	}
	destructive := map[string]bool{}
	for _, operation := range operations {
		destructive[operation.ID] = operation.Destructive
	}
	if !destructive["deleteItem"] || !destructive["cancelBatch"] {
		t.Fatalf("destructive classifications = %#v", destructive)
	}
	if destructive["createItem"] {
		t.Fatalf("createItem was classified as destructive: %#v", destructive)
	}
}

func TestGeneratedCatalogIsCurrent(t *testing.T) {
	var document openAPIDocument
	for _, path := range []string{"../../../../docs/openapi/openapi.json", "../management.json"} {
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var source openAPIDocument
		if err := json.Unmarshal(body, &source); err != nil {
			t.Fatal(err)
		}
		if err := mergeDocuments(&document, source); err != nil {
			t.Fatal(err)
		}
	}
	operations, err := collectOperations(document)
	if err != nil {
		t.Fatal(err)
	}
	expected, err := render(operations)
	if err != nil {
		t.Fatal(err)
	}
	actual, err := os.ReadFile("../catalog.gen.go")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(actual, expected) {
		t.Fatal("catalog is stale; run go generate ./internal/operations")
	}
}

func TestManagementSupplementCannotOverridePublicRoute(t *testing.T) {
	document := openAPIDocument{Paths: map[string]map[string]openAPIOperation{"/api/items": {"get": {OperationID: "items"}}}}
	if err := mergeDocuments(&document, document); err == nil {
		t.Fatal("duplicate route accepted")
	}
}

func TestCollectOperationsRejectsRouteAndAuthDrift(t *testing.T) {
	duplicate := openAPIDocument{Paths: map[string]map[string]openAPIOperation{
		"/api/items/{id}":      {"get": {OperationID: "getItem"}},
		"/api/items/{item_id}": {"get": {OperationID: "getRenamedItem"}},
	}}
	if _, err := collectOperations(duplicate); err == nil {
		t.Fatal("duplicate normalized route accepted")
	}
	invalid := openAPIDocument{Paths: map[string]map[string]openAPIOperation{
		"/api/items": {"get": {OperationID: "listItems", Auth: "typo"}},
	}}
	if _, err := collectOperations(invalid); err == nil {
		t.Fatal("invalid credential mode accepted")
	}
}
