package governance

import (
	"encoding/json"
	"testing"
	"time"
)

func TestCustomerResponsesIncludeAssignedVirtualKeys(t *testing.T) {
	t.Parallel()
	testData := NewGlobalTestData()
	defer testData.Cleanup(t)

	customerName := "test-customer-vk-response-" + generateRandomID()
	createCustomerResp := MakeRequest(t, APIRequest{
		Method: "POST",
		Path:   "/api/governance/customers",
		Body: CreateCustomerRequest{
			Name: customerName,
		},
	})

	if createCustomerResp.StatusCode != 200 {
		t.Fatalf("Failed to create customer: status %d", createCustomerResp.StatusCode)
	}

	customerID := ExtractIDFromResponse(t, createCustomerResp)
	testData.AddCustomer(customerID)

	customerData := createCustomerResp.Body["customer"].(map[string]interface{})
	assertJSONArrayField(t, customerData, "teams", 0)
	assertJSONArrayField(t, customerData, "virtual_keys", 0)

	vkName := "test-vk-customer-response-" + generateRandomID()
	createVKResp := MakeRequest(t, APIRequest{
		Method: "POST",
		Path:   "/api/governance/virtual-keys",
		Body: CreateVirtualKeyRequest{
			Name:       vkName,
			CustomerID: &customerID,
		},
	})

	if createVKResp.StatusCode != 200 {
		t.Fatalf("Failed to create VK: status %d", createVKResp.StatusCode)
	}

	vkID := ExtractIDFromResponse(t, createVKResp)
	testData.AddVirtualKey(vkID)

	customerDetailReq := APIRequest{
		Method: "GET",
		Path:   "/api/governance/customers/" + customerID,
	}
	customerListReq := APIRequest{
		Method: "GET",
		Path:   "/api/governance/customers",
	}
	customerMemoryDetailReq := APIRequest{
		Method: "GET",
		Path:   "/api/governance/customers/" + customerID + "?from_memory=true",
	}
	customerMemoryListReq := APIRequest{
		Method: "GET",
		Path:   "/api/governance/customers?from_memory=true",
	}

	if _, ok := WaitForAPICondition(t, customerDetailReq, func(resp *APIResponse) bool {
		return customerHasVirtualKey(resp, customerID, vkID)
	}, 5*time.Second, "db customer detail shows assigned virtual key"); !ok {
		t.Fatalf("Customer detail never showed assigned virtual key")
	}

	if _, ok := WaitForAPICondition(t, customerListReq, func(resp *APIResponse) bool {
		return customerHasVirtualKey(resp, customerID, vkID)
	}, 5*time.Second, "db customer list shows assigned virtual key"); !ok {
		t.Fatalf("Customer list never showed assigned virtual key")
	}

	if _, ok := WaitForAPICondition(t, customerMemoryDetailReq, func(resp *APIResponse) bool {
		return customerHasVirtualKey(resp, customerID, vkID)
	}, 5*time.Second, "in-memory customer detail shows assigned virtual key"); !ok {
		t.Fatalf("In-memory customer detail never showed assigned virtual key")
	}

	if _, ok := WaitForAPICondition(t, customerMemoryListReq, func(resp *APIResponse) bool {
		return customerHasVirtualKey(resp, customerID, vkID)
	}, 5*time.Second, "in-memory customer list shows assigned virtual key"); !ok {
		t.Fatalf("In-memory customer list never showed assigned virtual key")
	}
}

func TestVirtualKeyResponsesEmbedConsistentCustomerRelations(t *testing.T) {
	t.Parallel()
	testData := NewGlobalTestData()
	defer testData.Cleanup(t)

	customerName := "test-vk-embedded-customer-" + generateRandomID()
	createCustomerResp := MakeRequest(t, APIRequest{
		Method: "POST",
		Path:   "/api/governance/customers",
		Body: CreateCustomerRequest{
			Name: customerName,
		},
	})

	if createCustomerResp.StatusCode != 200 {
		t.Fatalf("Failed to create customer: status %d", createCustomerResp.StatusCode)
	}

	customerID := ExtractIDFromResponse(t, createCustomerResp)
	testData.AddCustomer(customerID)

	vkName := "test-vk-embedded-customer-" + generateRandomID()
	createVKResp := MakeRequest(t, APIRequest{
		Method: "POST",
		Path:   "/api/governance/virtual-keys",
		Body: CreateVirtualKeyRequest{
			Name:       vkName,
			CustomerID: &customerID,
		},
	})

	if createVKResp.StatusCode != 200 {
		t.Fatalf("Failed to create VK: status %d", createVKResp.StatusCode)
	}

	vkID := ExtractIDFromResponse(t, createVKResp)
	testData.AddVirtualKey(vkID)

	getVKReq := APIRequest{
		Method: "GET",
		Path:   "/api/governance/virtual-keys/" + vkID,
	}
	getVKMemoryReq := APIRequest{
		Method: "GET",
		Path:   "/api/governance/virtual-keys/" + vkID + "?from_memory=true",
	}

	if _, ok := WaitForAPICondition(t, getVKReq, func(resp *APIResponse) bool {
		return embeddedCustomerHasExpectedRelations(resp, customerID, vkID) && responseJSONIsMarshalable(resp)
	}, 5*time.Second, "db virtual key embeds normalized customer"); !ok {
		t.Fatalf("Virtual key detail never returned embedded customer with normalized relations")
	}

	if _, ok := WaitForAPICondition(t, getVKMemoryReq, func(resp *APIResponse) bool {
		return embeddedCustomerHasExpectedRelations(resp, customerID, vkID) && responseJSONIsMarshalable(resp)
	}, 5*time.Second, "in-memory virtual key embeds normalized customer"); !ok {
		t.Fatalf("In-memory virtual key detail never returned embedded customer with normalized relations")
	}
}

func TestCustomerResponsesIncludeMultipleAssignedVirtualKeys(t *testing.T) {
	t.Parallel()
	testData := NewGlobalTestData()
	defer testData.Cleanup(t)

	customerName := "test-customer-multi-vk-" + generateRandomID()
	createCustomerResp := MakeRequest(t, APIRequest{
		Method: "POST",
		Path:   "/api/governance/customers",
		Body: CreateCustomerRequest{
			Name: customerName,
		},
	})

	if createCustomerResp.StatusCode != 200 {
		t.Fatalf("Failed to create customer: status %d", createCustomerResp.StatusCode)
	}

	customerID := ExtractIDFromResponse(t, createCustomerResp)
	testData.AddCustomer(customerID)

	vk1ID := createCustomerVirtualKeyForTest(t, testData, customerID, "test-customer-multi-vk-1-")
	vk2ID := createCustomerVirtualKeyForTest(t, testData, customerID, "test-customer-multi-vk-2-")

	customerDetailReq := APIRequest{
		Method: "GET",
		Path:   "/api/governance/customers/" + customerID,
	}
	customerListReq := APIRequest{
		Method: "GET",
		Path:   "/api/governance/customers",
	}
	customerMemoryDetailReq := APIRequest{
		Method: "GET",
		Path:   "/api/governance/customers/" + customerID + "?from_memory=true",
	}
	customerMemoryListReq := APIRequest{
		Method: "GET",
		Path:   "/api/governance/customers?from_memory=true",
	}

	expectedVKs := []string{vk1ID, vk2ID}

	if _, ok := WaitForAPICondition(t, customerDetailReq, func(resp *APIResponse) bool {
		return customerHasExactVirtualKeys(resp, customerID, expectedVKs)
	}, 5*time.Second, "db customer detail shows both assigned virtual keys"); !ok {
		t.Fatalf("Customer detail never showed both assigned virtual keys")
	}

	if _, ok := WaitForAPICondition(t, customerListReq, func(resp *APIResponse) bool {
		return customerHasExactVirtualKeys(resp, customerID, expectedVKs)
	}, 5*time.Second, "db customer list shows both assigned virtual keys"); !ok {
		t.Fatalf("Customer list never showed both assigned virtual keys")
	}

	if _, ok := WaitForAPICondition(t, customerMemoryDetailReq, func(resp *APIResponse) bool {
		return customerHasExactVirtualKeys(resp, customerID, expectedVKs)
	}, 5*time.Second, "in-memory customer detail shows both assigned virtual keys"); !ok {
		t.Fatalf("In-memory customer detail never showed both assigned virtual keys")
	}

	if _, ok := WaitForAPICondition(t, customerMemoryListReq, func(resp *APIResponse) bool {
		return customerHasExactVirtualKeys(resp, customerID, expectedVKs)
	}, 5*time.Second, "in-memory customer list shows both assigned virtual keys"); !ok {
		t.Fatalf("In-memory customer list never showed both assigned virtual keys")
	}
}

func TestCustomerResponsesExcludeTeamScopedVirtualKeys(t *testing.T) {
	t.Parallel()
	testData := NewGlobalTestData()
	defer testData.Cleanup(t)

	customerName := "test-customer-team-vk-" + generateRandomID()
	createCustomerResp := MakeRequest(t, APIRequest{
		Method: "POST",
		Path:   "/api/governance/customers",
		Body: CreateCustomerRequest{
			Name: customerName,
		},
	})

	if createCustomerResp.StatusCode != 200 {
		t.Fatalf("Failed to create customer: status %d", createCustomerResp.StatusCode)
	}

	customerID := ExtractIDFromResponse(t, createCustomerResp)
	testData.AddCustomer(customerID)

	teamName := "test-team-team-vk-" + generateRandomID()
	createTeamResp := MakeRequest(t, APIRequest{
		Method: "POST",
		Path:   "/api/governance/teams",
		Body: CreateTeamRequest{
			Name:       teamName,
			CustomerID: &customerID,
		},
	})

	if createTeamResp.StatusCode != 200 {
		t.Fatalf("Failed to create team: status %d", createTeamResp.StatusCode)
	}

	teamID := ExtractIDFromResponse(t, createTeamResp)
	testData.AddTeam(teamID)

	vkName := "test-team-scoped-vk-" + generateRandomID()
	createVKResp := MakeRequest(t, APIRequest{
		Method: "POST",
		Path:   "/api/governance/virtual-keys",
		Body: CreateVirtualKeyRequest{
			Name:   vkName,
			TeamID: &teamID,
		},
	})

	if createVKResp.StatusCode != 200 {
		t.Fatalf("Failed to create team-scoped VK: status %d", createVKResp.StatusCode)
	}

	vkID := ExtractIDFromResponse(t, createVKResp)
	testData.AddVirtualKey(vkID)

	customerDetailReq := APIRequest{
		Method: "GET",
		Path:   "/api/governance/customers/" + customerID,
	}
	customerListReq := APIRequest{
		Method: "GET",
		Path:   "/api/governance/customers",
	}
	customerMemoryDetailReq := APIRequest{
		Method: "GET",
		Path:   "/api/governance/customers/" + customerID + "?from_memory=true",
	}
	customerMemoryListReq := APIRequest{
		Method: "GET",
		Path:   "/api/governance/customers?from_memory=true",
	}
	getVKReq := APIRequest{
		Method: "GET",
		Path:   "/api/governance/virtual-keys/" + vkID,
	}
	getVKMemoryReq := APIRequest{
		Method: "GET",
		Path:   "/api/governance/virtual-keys/" + vkID + "?from_memory=true",
	}

	if _, ok := WaitForAPICondition(t, customerDetailReq, func(resp *APIResponse) bool {
		return customerExcludesVirtualKey(resp, customerID, vkID, 0)
	}, 5*time.Second, "db customer detail excludes team-scoped virtual key"); !ok {
		t.Fatalf("Customer detail incorrectly included team-scoped virtual key")
	}

	if _, ok := WaitForAPICondition(t, customerListReq, func(resp *APIResponse) bool {
		return customerExcludesVirtualKey(resp, customerID, vkID, 0)
	}, 5*time.Second, "db customer list excludes team-scoped virtual key"); !ok {
		t.Fatalf("Customer list incorrectly included team-scoped virtual key")
	}

	if _, ok := WaitForAPICondition(t, customerMemoryDetailReq, func(resp *APIResponse) bool {
		return customerExcludesVirtualKey(resp, customerID, vkID, 0)
	}, 5*time.Second, "in-memory customer detail excludes team-scoped virtual key"); !ok {
		t.Fatalf("In-memory customer detail incorrectly included team-scoped virtual key")
	}

	if _, ok := WaitForAPICondition(t, customerMemoryListReq, func(resp *APIResponse) bool {
		return customerExcludesVirtualKey(resp, customerID, vkID, 0)
	}, 5*time.Second, "in-memory customer list excludes team-scoped virtual key"); !ok {
		t.Fatalf("In-memory customer list incorrectly included team-scoped virtual key")
	}

	if _, ok := WaitForAPICondition(t, getVKReq, func(resp *APIResponse) bool {
		return virtualKeyHasExpectedTeam(resp, vkID, teamID) && responseJSONIsMarshalable(resp)
	}, 5*time.Second, "db virtual key retains team relationship"); !ok {
		t.Fatalf("DB virtual key detail never returned expected team relationship")
	}

	if _, ok := WaitForAPICondition(t, getVKMemoryReq, func(resp *APIResponse) bool {
		return virtualKeyHasExpectedTeam(resp, vkID, teamID) && responseJSONIsMarshalable(resp)
	}, 5*time.Second, "in-memory virtual key retains team relationship"); !ok {
		t.Fatalf("In-memory virtual key detail never returned expected team relationship")
	}
}

func createCustomerVirtualKeyForTest(t *testing.T, testData *GlobalTestData, customerID, namePrefix string) string {
	t.Helper()

	vkName := namePrefix + generateRandomID()
	createVKResp := MakeRequest(t, APIRequest{
		Method: "POST",
		Path:   "/api/governance/virtual-keys",
		Body: CreateVirtualKeyRequest{
			Name:       vkName,
			CustomerID: &customerID,
		},
	})

	if createVKResp.StatusCode != 200 {
		t.Fatalf("Failed to create VK: status %d", createVKResp.StatusCode)
	}

	vkID := ExtractIDFromResponse(t, createVKResp)
	testData.AddVirtualKey(vkID)
	return vkID
}

func extractCustomerFromResponse(body map[string]interface{}, customerID string) map[string]interface{} {
	if customer, ok := body["customer"].(map[string]interface{}); ok {
		id, _ := customer["id"].(string)
		if id == customerID {
			return customer
		}
	}

	if customers, ok := body["customers"].([]interface{}); ok {
		for _, item := range customers {
			customer, ok := item.(map[string]interface{})
			if !ok {
				continue
			}
			id, _ := customer["id"].(string)
			if id == customerID {
				return customer
			}
		}
	}

	if customers, ok := body["customers"].(map[string]interface{}); ok {
		if customer, ok := customers[customerID].(map[string]interface{}); ok {
			return customer
		}
	}

	return nil
}

func customerHasVirtualKey(resp *APIResponse, customerID, vkID string) bool {
	if resp.StatusCode != 200 {
		return false
	}

	customer := extractCustomerFromResponse(resp.Body, customerID)
	if customer == nil {
		return false
	}

	if !arrayFieldContainsID(customer, "virtual_keys", vkID) {
		return false
	}

	return fieldIsJSONArray(customer, "teams")
}

func customerHasExactVirtualKeys(resp *APIResponse, customerID string, expectedVKIDs []string) bool {
	if resp.StatusCode != 200 {
		return false
	}

	customer := extractCustomerFromResponse(resp.Body, customerID)
	if customer == nil || !fieldIsJSONArray(customer, "teams") {
		return false
	}

	values, ok := customer["virtual_keys"].([]interface{})
	if !ok || len(values) != len(expectedVKIDs) {
		return false
	}

	for _, expectedID := range expectedVKIDs {
		if !arrayFieldContainsID(customer, "virtual_keys", expectedID) {
			return false
		}
	}

	return true
}

func customerExcludesVirtualKey(resp *APIResponse, customerID, vkID string, expectedLen int) bool {
	if resp.StatusCode != 200 {
		return false
	}

	customer := extractCustomerFromResponse(resp.Body, customerID)
	if customer == nil || !fieldIsJSONArray(customer, "teams") {
		return false
	}

	values, ok := customer["virtual_keys"].([]interface{})
	if !ok || len(values) != expectedLen {
		return false
	}

	return !arrayFieldContainsID(customer, "virtual_keys", vkID)
}

func embeddedCustomerHasExpectedRelations(resp *APIResponse, customerID, vkID string) bool {
	if resp.StatusCode != 200 {
		return false
	}

	virtualKey, ok := resp.Body["virtual_key"].(map[string]interface{})
	if !ok {
		return false
	}

	customer, ok := virtualKey["customer"].(map[string]interface{})
	if !ok {
		return false
	}

	id, _ := customer["id"].(string)
	if id != customerID {
		return false
	}

	if !fieldIsJSONArray(customer, "teams") {
		return false
	}

	if !arrayFieldContainsID(customer, "virtual_keys", vkID) {
		return false
	}

	values, ok := customer["virtual_keys"].([]interface{})
	if !ok {
		return false
	}
	for _, item := range values {
		entry, ok := item.(map[string]interface{})
		if !ok {
			return false
		}
		if nestedCustomer, exists := entry["customer"]; exists && nestedCustomer != nil {
			return false
		}
	}

	return true
}

func virtualKeyHasExpectedTeam(resp *APIResponse, vkID, teamID string) bool {
	if resp.StatusCode != 200 {
		return false
	}

	virtualKey, ok := resp.Body["virtual_key"].(map[string]interface{})
	if !ok {
		return false
	}

	id, _ := virtualKey["id"].(string)
	if id != vkID {
		return false
	}

	team, ok := virtualKey["team"].(map[string]interface{})
	if !ok {
		return false
	}

	actualTeamID, _ := team["id"].(string)
	if actualTeamID != teamID {
		return false
	}

	customer, exists := virtualKey["customer"]
	return !exists || customer == nil
}

func arrayFieldContainsID(parent map[string]interface{}, field, id string) bool {
	values, ok := parent[field].([]interface{})
	if !ok {
		return false
	}
	for _, item := range values {
		entry, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		entryID, _ := entry["id"].(string)
		if entryID == id {
			return true
		}
	}
	return false
}

func fieldIsJSONArray(parent map[string]interface{}, field string) bool {
	_, ok := parent[field].([]interface{})
	return ok
}

func assertJSONArrayField(t *testing.T, parent map[string]interface{}, field string, expectedLen int) {
	t.Helper()

	values, ok := parent[field].([]interface{})
	if !ok {
		t.Fatalf("Expected %q to be an array, got %T", field, parent[field])
	}
	if len(values) != expectedLen {
		t.Fatalf("Expected %q length %d, got %d", field, expectedLen, len(values))
	}
}

func responseJSONIsMarshalable(resp *APIResponse) bool {
	if !json.Valid(resp.RawBody) {
		return false
	}
	_, err := json.Marshal(resp.Body)
	return err == nil
}
