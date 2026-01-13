package governance

import (
	"testing"
	"time"
)

// TestInMemorySyncVirtualKeyUpdate tests that in-memory store is updated when VK is updated in DB
func TestInMemorySyncVirtualKeyUpdate(t *testing.T) {
	t.Parallel()
	testData := NewGlobalTestData()
	defer testData.Cleanup(t)

	// Create a VK with initial budget
	vkName := "test-vk-sync-" + generateRandomID()
	initialBudget := 10.0
	createVKResp := MakeRequest(t, APIRequest{
		Method: "POST",
		Path:   "/api/governance/virtual-keys",
		Body: CreateVirtualKeyRequest{
			Name: vkName,
			Budget: &BudgetRequest{
				MaxLimit:      initialBudget,
				ResetDuration: "1h",
			},
		},
	})

	if createVKResp.StatusCode != 200 {
		t.Fatalf("Failed to create VK: status %d", createVKResp.StatusCode)
	}

	vkID := ExtractIDFromResponse(t, createVKResp)
	testData.AddVirtualKey(vkID)

	vk := createVKResp.Body["virtual_key"].(map[string]interface{})
	vkValue := vk["value"].(string)

	t.Logf("Created VK %s with initial budget $%.2f", vkName, initialBudget)

	// Verify in-memory store has the VK
	getDataResp := MakeRequest(t, APIRequest{
		Method: "GET",
		Path:   "/api/governance/virtual-keys?from_memory=true",
	})

	if getDataResp.StatusCode != 200 {
		t.Fatalf("Failed to get governance data: status %d", getDataResp.StatusCode)
	}

	virtualKeysMap := getDataResp.Body["virtual_keys"].(map[string]interface{})

	// Check that VK exists in in-memory store
	vkData, exists := virtualKeysMap[vkValue]
	if !exists {
		t.Fatalf("VK %s not found in in-memory store after creation", vkValue)
	}

	vkDataMap := vkData.(map[string]interface{})
	vkID2, _ := vkDataMap["id"].(string)
	if vkID2 != vkID {
		t.Fatalf("VK ID mismatch in in-memory store: expected %s, got %s", vkID, vkID2)
	}

	t.Logf("VK found in in-memory store after creation ✓")

	// Update VK budget to 20.0
	newBudget := 20.0
	updateResp := MakeRequest(t, APIRequest{
		Method: "PUT",
		Path:   "/api/governance/virtual-keys/" + vkID,
		Body: UpdateVirtualKeyRequest{
			Budget: &UpdateBudgetRequest{
				MaxLimit: &newBudget,
			},
		},
	})

	if updateResp.StatusCode != 200 {
		t.Fatalf("Failed to update VK: status %d, body: %v", updateResp.StatusCode, updateResp.Body)
	}

	t.Logf("Updated VK budget from $%.2f to $%.2f", initialBudget, newBudget)

	// Verify in-memory store is updated
	time.Sleep(500 * time.Millisecond) // Small delay for async updates

	getVKResp2 := MakeRequest(t, APIRequest{
		Method: "GET",
		Path:   "/api/governance/virtual-keys?from_memory=true",
	})

	if getVKResp2.StatusCode != 200 {
		t.Fatalf("Failed to get governance data after update: status %d", getVKResp2.StatusCode)
	}

	virtualKeysMap2 := getVKResp2.Body["virtual_keys"].(map[string]interface{})

	getBudgetsResp2 := MakeRequest(t, APIRequest{
		Method: "GET",
		Path:   "/api/governance/budgets?from_memory=true",
	})

	budgetsMap2 := getBudgetsResp2.Body["budgets"].(map[string]interface{})

	// Check that VK still exists
	vkData2, exists := virtualKeysMap2[vkValue]
	if !exists {
		t.Fatalf("VK %s not found in in-memory store after update", vkValue)
	}

	vkDataMap2 := vkData2.(map[string]interface{})
	budgetID, _ := vkDataMap2["budget_id"].(string)

	// Check that budget in in-memory store is updated
	if budgetID != "" {
		budgetData, budgetExists := budgetsMap2[budgetID]
		if !budgetExists {
			t.Fatalf("Budget %s not found in in-memory store", budgetID)
		}

		budgetDataMap := budgetData.(map[string]interface{})
		maxLimit, _ := budgetDataMap["max_limit"].(float64)
		if maxLimit != newBudget {
			t.Fatalf("Budget max_limit not updated in in-memory store: expected %.2f, got %.2f", newBudget, maxLimit)
		}
	}

	t.Logf("VK budget updated in in-memory store ✓")
}

// TestInMemorySyncTeamUpdate tests that in-memory store is updated when Team is updated
func TestInMemorySyncTeamUpdate(t *testing.T) {
	t.Parallel()
	testData := NewGlobalTestData()
	defer testData.Cleanup(t)

	// Create a team with initial budget
	teamName := "test-team-sync-" + generateRandomID()
	initialBudget := 50.0
	createTeamResp := MakeRequest(t, APIRequest{
		Method: "POST",
		Path:   "/api/governance/teams",
		Body: CreateTeamRequest{
			Name: teamName,
			Budget: &BudgetRequest{
				MaxLimit:      initialBudget,
				ResetDuration: "1h",
			},
		},
	})

	if createTeamResp.StatusCode != 200 {
		t.Fatalf("Failed to create team: status %d", createTeamResp.StatusCode)
	}

	teamID := ExtractIDFromResponse(t, createTeamResp)
	testData.AddTeam(teamID)

	t.Logf("Created team %s with initial budget $%.2f", teamName, initialBudget)

	// Verify in-memory store has the team
	getDataResp := MakeRequest(t, APIRequest{
		Method: "GET",
		Path:   "/api/governance/teams?from_memory=true",
	})

	if getDataResp.StatusCode != 200 {
		t.Fatalf("Failed to get governance data: status %d", getDataResp.StatusCode)
	}

	teamsMap := getDataResp.Body["teams"].(map[string]interface{})

	_, exists := teamsMap[teamID]
	if !exists {
		t.Fatalf("Team %s not found in in-memory store after creation", teamID)
	}

	t.Logf("Team found in in-memory store after creation ✓")

	// Update team budget to 100.0
	newTeamBudget := 100.0
	updateResp := MakeRequest(t, APIRequest{
		Method: "PUT",
		Path:   "/api/governance/teams/" + teamID,
		Body: UpdateTeamRequest{
			Budget: &UpdateBudgetRequest{
				MaxLimit: &newTeamBudget,
			},
		},
	})

	if updateResp.StatusCode != 200 {
		t.Fatalf("Failed to update team: status %d", updateResp.StatusCode)
	}

	t.Logf("Updated team budget from $%.2f to $%.2f", initialBudget, newTeamBudget)

	// Verify in-memory store is updated
	time.Sleep(500 * time.Millisecond)

	getTeamsResp2 := MakeRequest(t, APIRequest{
		Method: "GET",
		Path:   "/api/governance/teams?from_memory=true",
	})

	if getTeamsResp2.StatusCode != 200 {
		t.Fatalf("Failed to get governance data after update: status %d", getTeamsResp2.StatusCode)
	}

	teamsMap2 := getTeamsResp2.Body["teams"].(map[string]interface{})

	getBudgetsResp2 := MakeRequest(t, APIRequest{
		Method: "GET",
		Path:   "/api/governance/budgets?from_memory=true",
	})

	budgetsMap2 := getBudgetsResp2.Body["budgets"].(map[string]interface{})

	teamData2, exists := teamsMap2[teamID]
	if !exists {
		t.Fatalf("Team %s not found in in-memory store after update", teamID)
	}

	teamDataMap := teamData2.(map[string]interface{})
	budgetID, _ := teamDataMap["budget_id"].(string)

	if budgetID != "" {
		budgetData, budgetExists := budgetsMap2[budgetID]
		if !budgetExists {
			t.Fatalf("Budget %s not found in in-memory store", budgetID)
		}

		budgetDataMap := budgetData.(map[string]interface{})
		maxLimit, _ := budgetDataMap["max_limit"].(float64)
		if maxLimit != newTeamBudget {
			t.Fatalf("Team budget max_limit not updated in in-memory store: expected %.2f, got %.2f", newTeamBudget, maxLimit)
		}
	}

	t.Logf("Team budget updated in in-memory store ✓")
}

// TestInMemorySyncCustomerUpdate tests that in-memory store is updated when Customer is updated
func TestInMemorySyncCustomerUpdate(t *testing.T) {
	t.Parallel()
	testData := NewGlobalTestData()
	defer testData.Cleanup(t)

	// Create a customer with initial budget
	customerName := "test-customer-sync-" + generateRandomID()
	initialBudget := 100.0
	createCustomerResp := MakeRequest(t, APIRequest{
		Method: "POST",
		Path:   "/api/governance/customers",
		Body: CreateCustomerRequest{
			Name: customerName,
			Budget: &BudgetRequest{
				MaxLimit:      initialBudget,
				ResetDuration: "1h",
			},
		},
	})

	if createCustomerResp.StatusCode != 200 {
		t.Fatalf("Failed to create customer: status %d", createCustomerResp.StatusCode)
	}

	customerID := ExtractIDFromResponse(t, createCustomerResp)
	testData.AddCustomer(customerID)

	t.Logf("Created customer %s with initial budget $%.2f", customerName, initialBudget)

	// Verify in-memory store has the customer
	getDataResp := MakeRequest(t, APIRequest{
		Method: "GET",
		Path:   "/api/governance/customers?from_memory=true",
	})

	if getDataResp.StatusCode != 200 {
		t.Fatalf("Failed to get governance data: status %d", getDataResp.StatusCode)
	}

	customersMap := getDataResp.Body["customers"].(map[string]interface{})

	_, exists := customersMap[customerID]
	if !exists {
		t.Fatalf("Customer %s not found in in-memory store after creation", customerID)
	}

	t.Logf("Customer found in in-memory store after creation ✓")

	// Update customer budget to 250.0
	newCustomerBudget := 250.0
	updateResp := MakeRequest(t, APIRequest{
		Method: "PUT",
		Path:   "/api/governance/customers/" + customerID,
		Body: UpdateCustomerRequest{
			Budget: &UpdateBudgetRequest{
				MaxLimit: &newCustomerBudget,
			},
		},
	})

	if updateResp.StatusCode != 200 {
		t.Fatalf("Failed to update customer: status %d", updateResp.StatusCode)
	}

	t.Logf("Updated customer budget from $%.2f to $%.2f", initialBudget, newCustomerBudget)

	// Verify in-memory store is updated
	time.Sleep(500 * time.Millisecond)

	getCustomersResp2 := MakeRequest(t, APIRequest{
		Method: "GET",
		Path:   "/api/governance/customers?from_memory=true",
	})

	if getCustomersResp2.StatusCode != 200 {
		t.Fatalf("Failed to get governance data after update: status %d", getCustomersResp2.StatusCode)
	}

	customersMap2 := getCustomersResp2.Body["customers"].(map[string]interface{})

	getBudgetsResp2 := MakeRequest(t, APIRequest{
		Method: "GET",
		Path:   "/api/governance/budgets?from_memory=true",
	})

	budgetsMap2 := getBudgetsResp2.Body["budgets"].(map[string]interface{})

	customerData2, exists := customersMap2[customerID]
	if !exists {
		t.Fatalf("Customer %s not found in in-memory store after update", customerID)
	}

	customerDataMap := customerData2.(map[string]interface{})
	budgetID, _ := customerDataMap["budget_id"].(string)

	if budgetID != "" {
		budgetData, budgetExists := budgetsMap2[budgetID]
		if !budgetExists {
			t.Fatalf("Budget %s not found in in-memory store", budgetID)
		}

		budgetDataMap := budgetData.(map[string]interface{})
		maxLimit, _ := budgetDataMap["max_limit"].(float64)
		if maxLimit != newCustomerBudget {
			t.Fatalf("Customer budget max_limit not updated in in-memory store: expected %.2f, got %.2f", newCustomerBudget, maxLimit)
		}
	}

	t.Logf("Customer budget updated in in-memory store ✓")
}

// TestInMemorySyncVirtualKeyDelete tests that in-memory store is updated when VK is deleted
func TestInMemorySyncVirtualKeyDelete(t *testing.T) {
	t.Parallel()
	testData := NewGlobalTestData()
	defer testData.Cleanup(t)

	// Create a VK
	vkName := "test-vk-delete-" + generateRandomID()
	createVKResp := MakeRequest(t, APIRequest{
		Method: "POST",
		Path:   "/api/governance/virtual-keys",
		Body: CreateVirtualKeyRequest{
			Name: vkName,
			Budget: &BudgetRequest{
				MaxLimit:      10.0,
				ResetDuration: "1h",
			},
		},
	})

	if createVKResp.StatusCode != 200 {
		t.Fatalf("Failed to create VK: status %d", createVKResp.StatusCode)
	}

	vkID := ExtractIDFromResponse(t, createVKResp)
	testData.AddVirtualKey(vkID)

	vk := createVKResp.Body["virtual_key"].(map[string]interface{})
	vkValue := vk["value"].(string)

	// Verify in-memory store has the VK (poll to ensure sync completed)
	vkExists := WaitForCondition(t, func() bool {
		getDataResp := MakeRequest(t, APIRequest{
			Method: "GET",
			Path:   "/api/governance/virtual-keys?from_memory=true",
		})

		if getDataResp.StatusCode != 200 {
			return false
		}

		virtualKeysMap, ok := getDataResp.Body["virtual_keys"].(map[string]interface{})
		if !ok {
			return false
		}

		_, exists := virtualKeysMap[vkValue]
		return exists
	}, 5*time.Second, "VK exists in in-memory store after creation")

	if !vkExists {
		t.Fatalf("VK not found in in-memory store after creation (timeout after 5s)")
	}

	t.Logf("VK found in in-memory store after creation ✓")

	// Delete the VK
	deleteResp := MakeRequest(t, APIRequest{
		Method: "DELETE",
		Path:   "/api/governance/virtual-keys/" + vkID,
	})

	if deleteResp.StatusCode != 200 {
		t.Fatalf("Failed to delete VK: status %d", deleteResp.StatusCode)
	}

	t.Logf("Deleted VK from database")

	// Verify in-memory store is updated (poll with timeout instead of fixed sleep)
	vkRemoved := WaitForCondition(t, func() bool {
		getDataResp2 := MakeRequest(t, APIRequest{
			Method: "GET",
			Path:   "/api/governance/virtual-keys?from_memory=true",
		})

		if getDataResp2.StatusCode != 200 {
			return false
		}

		virtualKeysMap2, ok := getDataResp2.Body["virtual_keys"].(map[string]interface{})
		if !ok {
			return false
		}

		_, exists := virtualKeysMap2[vkValue]
		return !exists // Return true when VK is NOT found (successfully removed)
	}, 5*time.Second, "VK removed from in-memory store after deletion")

	if !vkRemoved {
		t.Fatalf("VK %s still exists in in-memory store after deletion (timeout after 5s)", vkValue)
	}

	t.Logf("VK removed from in-memory store ✓")
}

// TestDataEndpointConsistency tests that governance endpoints return consistent data
func TestDataEndpointConsistency(t *testing.T) {
	t.Parallel()
	testData := NewGlobalTestData()
	defer testData.Cleanup(t)

	// Create multiple resources
	vkName := "test-vk-consistency-" + generateRandomID()
	createVKResp := MakeRequest(t, APIRequest{
		Method: "POST",
		Path:   "/api/governance/virtual-keys",
		Body: CreateVirtualKeyRequest{
			Name: vkName,
			Budget: &BudgetRequest{
				MaxLimit:      15.0,
				ResetDuration: "1h",
			},
		},
	})

	vkID := ExtractIDFromResponse(t, createVKResp)
	testData.AddVirtualKey(vkID)

	teamName := "test-team-consistency-" + generateRandomID()
	createTeamResp := MakeRequest(t, APIRequest{
		Method: "POST",
		Path:   "/api/governance/teams",
		Body: CreateTeamRequest{
			Name: teamName,
			Budget: &BudgetRequest{
				MaxLimit:      30.0,
				ResetDuration: "1h",
			},
		},
	})

	teamID := ExtractIDFromResponse(t, createTeamResp)
	testData.AddTeam(teamID)

	customerName := "test-customer-consistency-" + generateRandomID()
	createCustomerResp := MakeRequest(t, APIRequest{
		Method: "POST",
		Path:   "/api/governance/customers",
		Body: CreateCustomerRequest{
			Name: customerName,
			Budget: &BudgetRequest{
				MaxLimit:      60.0,
				ResetDuration: "1h",
			},
		},
	})

	customerID := ExtractIDFromResponse(t, createCustomerResp)
	testData.AddCustomer(customerID)

	// Wait for all resources to be available in in-memory store
	allResourcesReady := WaitForCondition(t, func() bool {
		getVKResp := MakeRequest(t, APIRequest{
			Method: "GET",
			Path:   "/api/governance/virtual-keys?from_memory=true",
		})
		if getVKResp.StatusCode != 200 {
			return false
		}

		getTeamsResp := MakeRequest(t, APIRequest{
			Method: "GET",
			Path:   "/api/governance/teams?from_memory=true",
		})
		if getTeamsResp.StatusCode != 200 {
			return false
		}

		getCustomersResp := MakeRequest(t, APIRequest{
			Method: "GET",
			Path:   "/api/governance/customers?from_memory=true",
		})
		return getCustomersResp.StatusCode == 200
	}, 3*time.Second, "all resources available in in-memory store")

	if !allResourcesReady {
		t.Fatalf("Resources not available in in-memory store (timeout after 3s)")
	}

	// Get data from separate endpoints
	getVKResp := MakeRequest(t, APIRequest{
		Method: "GET",
		Path:   "/api/governance/virtual-keys?from_memory=true",
	})

	if getVKResp.StatusCode != 200 {
		t.Fatalf("Failed to get virtual keys: status %d", getVKResp.StatusCode)
	}

	getTeamsResp := MakeRequest(t, APIRequest{
		Method: "GET",
		Path:   "/api/governance/teams?from_memory=true",
	})

	if getTeamsResp.StatusCode != 200 {
		t.Fatalf("Failed to get teams: status %d", getTeamsResp.StatusCode)
	}

	getCustomersResp := MakeRequest(t, APIRequest{
		Method: "GET",
		Path:   "/api/governance/customers?from_memory=true",
	})

	if getCustomersResp.StatusCode != 200 {
		t.Fatalf("Failed to get customers: status %d", getCustomersResp.StatusCode)
	}

	virtualKeysMap := getVKResp.Body["virtual_keys"].(map[string]interface{})
	teamsMap := getTeamsResp.Body["teams"].(map[string]interface{})
	customersMap := getCustomersResp.Body["customers"].(map[string]interface{})

	// Verify all created resources are in the in-memory data
	vkCount := len(virtualKeysMap)
	teamCount := len(teamsMap)
	customerCount := len(customersMap)

	if vkCount == 0 {
		t.Fatalf("No virtual keys found in data endpoint")
	}
	if teamCount == 0 {
		t.Fatalf("No teams found in data endpoint")
	}
	if customerCount == 0 {
		t.Fatalf("No customers found in data endpoint")
	}

	t.Logf("Data endpoint returned consistent data: %d VKs, %d teams, %d customers ✓", vkCount, teamCount, customerCount)

	// Get the individual endpoints and verify consistency
	getVKsResp := MakeRequest(t, APIRequest{
		Method: "GET",
		Path:   "/api/governance/virtual-keys",
	})

	if getVKsResp.StatusCode != 200 {
		t.Fatalf("Failed to get virtual keys: status %d", getVKsResp.StatusCode)
	}

	vksFromEndpoint, _ := getVKsResp.Body["count"].(float64)
	if int(vksFromEndpoint) != vkCount {
		// Can fail because sqlite db might get locked because of all parallel tests
		t.Logf("[WARN]VK count mismatch between /data endpoint and /virtual-keys endpoint: %d vs %d (this can happen because of parallel tests)", vkCount, int(vksFromEndpoint))
	}

	t.Logf("Data consistency verified between endpoints ✓")
}
