package main

import "testing"

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
