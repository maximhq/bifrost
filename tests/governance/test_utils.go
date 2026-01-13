package governance

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"strings"
	"testing"
	"time"
)

// ModelCost defines the cost structure for a model
type ModelCost struct {
	Provider           string
	InputCostPerToken  float64
	OutputCostPerToken float64
	MaxInputTokens     int
	MaxOutputTokens    int
}

// TestModels defines all models used for testing
var TestModels = map[string]ModelCost{
	"openai/gpt-4o": {
		Provider:           "openai",
		InputCostPerToken:  0.0000025,
		OutputCostPerToken: 0.00001,
		MaxInputTokens:     128000,
		MaxOutputTokens:    16384,
	},
	"anthropic/claude-3-7-sonnet-20250219": {
		Provider:           "anthropic",
		InputCostPerToken:  0.000003,
		OutputCostPerToken: 0.000015,
		MaxInputTokens:     200000,
		MaxOutputTokens:    128000,
	},
	"anthropic/claude-4-opus-20250514": {
		Provider:           "anthropic",
		InputCostPerToken:  0.000015,
		OutputCostPerToken: 0.000075,
		MaxInputTokens:     200000,
		MaxOutputTokens:    32000,
	},
	"openrouter/anthropic/claude-3.7-sonnet": {
		Provider:           "openrouter",
		InputCostPerToken:  0.000003,
		OutputCostPerToken: 0.000015,
		MaxInputTokens:     200000,
		MaxOutputTokens:    128000,
	},
	"openrouter/openai/gpt-4o": {
		Provider:           "openrouter",
		InputCostPerToken:  0.0000025,
		OutputCostPerToken: 0.00001,
		MaxInputTokens:     128000,
		MaxOutputTokens:    4096,
	},
}

// CalculateCost calculates the cost based on input and output tokens
func CalculateCost(model string, inputTokens, outputTokens int) (float64, error) {
	modelInfo, ok := TestModels[model]
	if !ok {
		return 0, fmt.Errorf("unknown model: %s", model)
	}

	inputCost := float64(inputTokens) * modelInfo.InputCostPerToken
	outputCost := float64(outputTokens) * modelInfo.OutputCostPerToken
	return inputCost + outputCost, nil
}

// APIRequest represents a request to the Bifrost API
type APIRequest struct {
	Method   string
	Path     string
	Body     interface{}
	VKHeader *string
}

// APIResponse represents a response from the Bifrost API
type APIResponse struct {
	StatusCode int
	Body       map[string]interface{}
	RawBody    []byte
}

// MakeRequest makes an HTTP request to the Bifrost API
func MakeRequest(t *testing.T, req APIRequest) *APIResponse {
	client := &http.Client{}
	url := fmt.Sprintf("http://localhost:8080%s", req.Path)

	var body io.Reader
	if req.Body != nil {
		bodyBytes, err := json.Marshal(req.Body)
		if err != nil {
			t.Fatalf("Failed to marshal request body: %v", err)
		}
		body = bytes.NewReader(bodyBytes)
	}

	httpReq, err := http.NewRequest(req.Method, url, body)
	if err != nil {
		t.Fatalf("Failed to create HTTP request: %v", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")

	// Add virtual key header if provided
	if req.VKHeader != nil {
		httpReq.Header.Set("x-bf-vk", *req.VKHeader)
	}

	resp, err := client.Do(httpReq)
	if err != nil {
		t.Fatalf("Failed to execute HTTP request: %v", err)
	}
	defer resp.Body.Close()

	rawBody, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("Failed to read response body: %v", err)
	}

	var responseBody map[string]interface{}
	if len(rawBody) > 0 {
		err = json.Unmarshal(rawBody, &responseBody)
		if err != nil {
			// If unmarshaling fails, store the raw response
			responseBody = map[string]interface{}{"raw": string(rawBody)}
		}
	}

	return &APIResponse{
		StatusCode: resp.StatusCode,
		Body:       responseBody,
		RawBody:    rawBody,
	}
}

// MakeRequestWithCustomHeaders makes an HTTP request with custom headers
// Use this when you need to test specific header formats (e.g., Authorization, x-api-key)
func MakeRequestWithCustomHeaders(t *testing.T, req APIRequest, customHeaders map[string]string) *APIResponse {
	client := &http.Client{}
	url := fmt.Sprintf("http://localhost:8080%s", req.Path)

	var body io.Reader
	if req.Body != nil {
		bodyBytes, err := json.Marshal(req.Body)
		if err != nil {
			t.Fatalf("Failed to marshal request body: %v", err)
		}
		body = bytes.NewReader(bodyBytes)
	}

	httpReq, err := http.NewRequest(req.Method, url, body)
	if err != nil {
		t.Fatalf("Failed to create HTTP request: %v", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")

	// Add custom headers
	for key, value := range customHeaders {
		httpReq.Header.Set(key, value)
	}

	resp, err := client.Do(httpReq)
	if err != nil {
		t.Fatalf("Failed to execute HTTP request: %v", err)
	}
	defer resp.Body.Close()

	rawBody, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("Failed to read response body: %v", err)
	}

	var responseBody map[string]interface{}
	if len(rawBody) > 0 {
		err = json.Unmarshal(rawBody, &responseBody)
		if err != nil {
			// If unmarshaling fails, store the raw response
			responseBody = map[string]interface{}{"raw": string(rawBody)}
		}
	}

	return &APIResponse{
		StatusCode: resp.StatusCode,
		Body:       responseBody,
		RawBody:    rawBody,
	}
}

// generateRandomID generates a random ID for test resources
func generateRandomID() string {
	rand.Seed(time.Now().UnixNano())
	const letters = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, 8)
	for i := range b {
		b[i] = letters[rand.Intn(len(letters))]
	}
	return string(b)
}

// CreateVirtualKeyRequest represents a request to create a virtual key
type CreateVirtualKeyRequest struct {
	Name            string                  `json:"name"`
	Description     string                  `json:"description,omitempty"`
	IsActive        *bool                   `json:"is_active,omitempty"`
	TeamID          *string                 `json:"team_id,omitempty"`
	CustomerID      *string                 `json:"customer_id,omitempty"`
	Budget          *BudgetRequest          `json:"budget,omitempty"`
	RateLimit       *CreateRateLimitRequest `json:"rate_limit,omitempty"`
	ProviderConfigs []ProviderConfigRequest `json:"provider_configs,omitempty"`
}

// ProviderConfigRequest represents a provider configuration for a virtual key
type ProviderConfigRequest struct {
	ID            *uint                   `json:"id,omitempty"`
	Provider      string                  `json:"provider"`
	Weight        float64                 `json:"weight,omitempty"`
	AllowedModels []string                `json:"allowed_models,omitempty"`
	Budget        *BudgetRequest          `json:"budget,omitempty"`
	RateLimit     *CreateRateLimitRequest `json:"rate_limit,omitempty"`
}

// BudgetRequest represents a budget request
type BudgetRequest struct {
	MaxLimit      float64 `json:"max_limit"`
	ResetDuration string  `json:"reset_duration"`
}

// CreateTeamRequest represents a request to create a team
type CreateTeamRequest struct {
	Name       string         `json:"name"`
	CustomerID *string        `json:"customer_id,omitempty"`
	Budget     *BudgetRequest `json:"budget,omitempty"`
}

// CreateCustomerRequest represents a request to create a customer
type CreateCustomerRequest struct {
	Name   string         `json:"name"`
	Budget *BudgetRequest `json:"budget,omitempty"`
}

// UpdateBudgetRequest represents a request to update a budget
type UpdateBudgetRequest struct {
	MaxLimit      *float64 `json:"max_limit,omitempty"`
	ResetDuration *string  `json:"reset_duration,omitempty"`
}

// CreateRateLimitRequest represents a request to create a rate limit
type CreateRateLimitRequest struct {
	TokenMaxLimit        *int64  `json:"token_max_limit,omitempty"`
	TokenResetDuration   *string `json:"token_reset_duration,omitempty"`
	RequestMaxLimit      *int64  `json:"request_max_limit,omitempty"`
	RequestResetDuration *string `json:"request_reset_duration,omitempty"`
}

// UpdateVirtualKeyRequest represents a request to update a virtual key
type UpdateVirtualKeyRequest struct {
	Name            *string                 `json:"name,omitempty"`
	TeamID          *string                 `json:"team_id,omitempty"`
	CustomerID      *string                 `json:"customer_id,omitempty"`
	Budget          *UpdateBudgetRequest    `json:"budget,omitempty"`
	RateLimit       *CreateRateLimitRequest `json:"rate_limit,omitempty"`
	IsActive        *bool                   `json:"is_active,omitempty"`
	ProviderConfigs []ProviderConfigRequest `json:"provider_configs,omitempty"`
}

// UpdateTeamRequest represents a request to update a team
type UpdateTeamRequest struct {
	Name   *string              `json:"name,omitempty"`
	Budget *UpdateBudgetRequest `json:"budget,omitempty"`
}

// UpdateCustomerRequest represents a request to update a customer
type UpdateCustomerRequest struct {
	Name   *string              `json:"name,omitempty"`
	Budget *UpdateBudgetRequest `json:"budget,omitempty"`
}

// ChatCompletionRequest represents an OpenAI-compatible chat completion request
type ChatCompletionRequest struct {
	Model       string        `json:"model"`
	Messages    []ChatMessage `json:"messages"`
	Temperature *float64      `json:"temperature,omitempty"`
	MaxTokens   *int          `json:"max_tokens,omitempty"`
	TopP        *float64      `json:"top_p,omitempty"`
}

// ChatMessage represents a chat message in OpenAI format
type ChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// ExtractIDFromResponse extracts the ID from a creation response
func ExtractIDFromResponse(t *testing.T, resp *APIResponse) string {
	if resp.StatusCode >= 400 {
		t.Fatalf("Request failed with status %d: %v", resp.StatusCode, resp.Body)
	}

	// Navigate through the response to find the ID
	data := resp.Body
	parts := []string{"virtual_key", "team", "customer"}
	for _, part := range parts {
		if val, ok := data[part]; ok {
			if nested, ok := val.(map[string]interface{}); ok {
				if id, ok := nested["id"].(string); ok {
					return id
				}
			}
		}
	}

	t.Fatalf("Could not extract ID from response: %v", resp.Body)
	return ""
}

// CheckErrorMessage checks if the response error contains expected text
// Returns true if error found, false otherwise. Asserts fail if status is not >= 400.
func CheckErrorMessage(t *testing.T, resp *APIResponse, expectedText string) bool {
	if resp.StatusCode < 400 {
		t.Fatalf("Expected error response but got status %d. Response: %v", resp.StatusCode, resp.Body)
	}

	// Check in various fields where errors might appear
	if msg, ok := resp.Body["message"].(string); ok && contains(msg, expectedText) {
		return true
	}

	if err, ok := resp.Body["error"].(string); ok && contains(err, expectedText) {
		return true
	}

	// Check raw body as fallback
	if contains(string(resp.RawBody), expectedText) {
		return true
	}

	return false
}

// contains checks if a string contains a substring (case-insensitive)
func contains(haystack, needle string) bool {
	return strings.Contains(strings.ToLower(haystack), strings.ToLower(needle))
}

// GlobalTestData stores IDs of created resources for cleanup
type GlobalTestData struct {
	VirtualKeys []string
	Teams       []string
	Customers   []string
}

// NewGlobalTestData creates a new test data holder
func NewGlobalTestData() *GlobalTestData {
	return &GlobalTestData{
		VirtualKeys: make([]string, 0),
		Teams:       make([]string, 0),
		Customers:   make([]string, 0),
	}
}

// AddVirtualKey adds a virtual key ID to the test data
func (g *GlobalTestData) AddVirtualKey(id string) {
	g.VirtualKeys = append(g.VirtualKeys, id)
}

// AddTeam adds a team ID to the test data
func (g *GlobalTestData) AddTeam(id string) {
	g.Teams = append(g.Teams, id)
}

// AddCustomer adds a customer ID to the test data
func (g *GlobalTestData) AddCustomer(id string) {
	g.Customers = append(g.Customers, id)
}

// deleteWithRetry performs a DELETE request with retry logic
// Retries up to 5 times if the response status is not 200 or 204
// Delete requests don't require VK headers
func deleteWithRetry(t *testing.T, path string, resourceType string, resourceID string) bool {
	maxRetries := 5
	for attempt := 1; attempt <= maxRetries; attempt++ {
		resp := MakeRequest(t, APIRequest{
			Method: "DELETE",
			Path:   path,
			// Note: VKHeader is intentionally not set for DELETE requests
		})

		// Success: 200 or 204 means the resource was deleted successfully
		if resp.StatusCode == 200 || resp.StatusCode == 204 {
			if attempt > 1 {
				t.Logf("Successfully deleted %s %s after %d attempts", resourceType, resourceID, attempt)
			}
			return true
		}

		// 404 means resource doesn't exist, which is fine for cleanup
		if resp.StatusCode == 404 {
			t.Logf("%s %s not found (already deleted or never existed)", resourceType, resourceID)
			return true
		}

		// If this is not the last attempt, log and retry
		if attempt < maxRetries {
			t.Logf("Attempt %d/%d: Failed to delete %s %s: status %d, retrying...", attempt, maxRetries, resourceType, resourceID, resp.StatusCode)
			// Progressive backoff: 100ms, 200ms, 300ms, 400ms
			time.Sleep(time.Duration(100*attempt) * time.Millisecond)
		} else {
			// Last attempt failed
			t.Logf("Warning: Failed to delete %s %s after %d attempts: status %d", resourceType, resourceID, maxRetries, resp.StatusCode)
			return false
		}
	}

	return false
}

// Cleanup deletes all created resources
// Retries up to 5 times for each delete operation if status is not 200 or 204
// Delete requests don't require VK headers
func (g *GlobalTestData) Cleanup(t *testing.T) {
	// Delete virtual keys
	for _, vkID := range g.VirtualKeys {
		deleteWithRetry(t, fmt.Sprintf("/api/governance/virtual-keys/%s", vkID), "virtual key", vkID)
	}

	// Delete teams
	for _, teamID := range g.Teams {
		deleteWithRetry(t, fmt.Sprintf("/api/governance/teams/%s", teamID), "team", teamID)
	}

	// Delete customers
	for _, customerID := range g.Customers {
		deleteWithRetry(t, fmt.Sprintf("/api/governance/customers/%s", customerID), "customer", customerID)
	}

	t.Logf("Cleanup completed: deleted %d VKs, %d teams, %d customers",
		len(g.VirtualKeys), len(g.Teams), len(g.Customers))
}

// WaitForCondition polls a condition function until it returns true or times out
// Useful for waiting for async updates to propagate to in-memory store
func WaitForCondition(t *testing.T, checkFunc func() bool, timeout time.Duration, description string) bool {
	deadline := time.Now().Add(timeout)
	attempt := 0

	for time.Now().Before(deadline) {
		attempt++
		if checkFunc() {
			if attempt > 1 {
				t.Logf("Condition '%s' met after %d attempts", description, attempt)
			}
			return true
		}

		// Progressive backoff: start with 50ms, max 500ms
		sleepDuration := time.Duration(50*attempt) * time.Millisecond
		if sleepDuration > 500*time.Millisecond {
			sleepDuration = 500 * time.Millisecond
		}
		time.Sleep(sleepDuration)
	}

	t.Logf("Timeout waiting for condition '%s' after %d attempts (%.1fs)", description, attempt, timeout.Seconds())
	return false
}

// WaitForAPICondition makes repeated API requests until a condition is satisfied or times out
// Useful for verifying async updates in API responses
func WaitForAPICondition(t *testing.T, req APIRequest, condition func(*APIResponse) bool, timeout time.Duration, description string) (*APIResponse, bool) {
	deadline := time.Now().Add(timeout)
	attempt := 0
	var lastResp *APIResponse

	for time.Now().Before(deadline) {
		attempt++
		lastResp = MakeRequest(t, req)

		if condition(lastResp) {
			if attempt > 1 {
				t.Logf("API condition '%s' met after %d attempts", description, attempt)
			}
			return lastResp, true
		}

		// Progressive backoff: start with 100ms, max 500ms
		sleepDuration := time.Duration(100*attempt) * time.Millisecond
		if sleepDuration > 500*time.Millisecond {
			sleepDuration = 500 * time.Millisecond
		}
		time.Sleep(sleepDuration)
	}

	t.Logf("Timeout waiting for API condition '%s' after %d attempts (%.1fs)", description, attempt, timeout.Seconds())
	return lastResp, false
}

// ParseDuration function to parse duration strings
// Copied from framework/configstore/tables/utils.go
func ParseDuration(duration string) (time.Duration, error) {
	if duration == "" {
		return 0, fmt.Errorf("duration is empty")
	}

	// Handle special cases for days, weeks, months, years
	switch {
	case duration[len(duration)-1:] == "d":
		days := duration[:len(duration)-1]
		if d, err := time.ParseDuration(days + "h"); err == nil {
			return d * 24, nil
		}
		return 0, fmt.Errorf("invalid day duration: %s", duration)
	case duration[len(duration)-1:] == "w":
		weeks := duration[:len(duration)-1]
		if w, err := time.ParseDuration(weeks + "h"); err == nil {
			return w * 24 * 7, nil
		}
		return 0, fmt.Errorf("invalid week duration: %s", duration)
	case duration[len(duration)-1:] == "M":
		months := duration[:len(duration)-1]
		if m, err := time.ParseDuration(months + "h"); err == nil {
			return m * 24 * 30, nil // Approximate month as 30 days
		}
		return 0, fmt.Errorf("invalid month duration: %s", duration)
	case duration[len(duration)-1:] == "Y":
		years := duration[:len(duration)-1]
		if y, err := time.ParseDuration(years + "h"); err == nil {
			return y * 24 * 365, nil // Approximate year as 365 days
		}
		return 0, fmt.Errorf("invalid year duration: %s", duration)
	default:
		return time.ParseDuration(duration)
	}
}
