package requestguard

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"hash"
	"strings"

	"github.com/maximhq/bifrost/core/schemas"
)

type requestGuardBypassHeaderContextKey struct{}

var bypassHeaderContextKey requestGuardBypassHeaderContextKey

type fingerprintTenantIdentity struct {
	key          []byte
	virtualKeyID string
	customerIDs  []string
}

// ComputeFingerprint returns a stable SHA-256 request key for eligible unary
// or streaming chat/Responses requests. It only reads the pooled request during
// this call and returns scalar output.
func ComputeFingerprint(ctx *schemas.BifrostContext, req *schemas.BifrostRequest, features RequestFeatures, cfg CoalescerConfig) (hashValue string, eligible bool, bypassReason string) {
	if !cfg.Enabled {
		return "", false, "coalescer is disabled"
	}
	if req == nil {
		return "", false, "request is nil"
	}
	if ctx != nil {
		if largePayload, _ := ctx.Value(schemas.BifrostContextKeyLargePayloadMode).(bool); largePayload {
			return "", false, "large payload passthrough is not coalesced"
		}
	}
	if !features.Supported {
		return "", false, "request type is unsupported"
	}
	requestType := features.RequestType
	if requestType == "" {
		requestType = req.RequestType
	}
	if requestType == "" {
		switch {
		case req.ChatRequest != nil:
			requestType = schemas.ChatCompletionRequest
		case req.ResponsesRequest != nil:
			requestType = schemas.ResponsesRequest
		}
	}
	streaming := features.IsStreaming || requestType == schemas.ChatCompletionStreamRequest || requestType == schemas.ResponsesStreamRequest
	if streaming && !cfg.EnableStreaming {
		return "", false, "streaming coalescing is disabled"
	}
	if cfg.BypassHeader != "" && hasHeaderKey(ctx, cfg.BypassHeader) {
		return "", false, "bypass header is present"
	}
	if !finite(cfg.MaxTemperature) {
		return "", false, "maximum temperature is invalid"
	}

	provider, model := features.Provider, features.Model
	if req.ChatRequest != nil && (provider == "" || model == "") {
		if provider == "" {
			provider = req.ChatRequest.Provider
		}
		if model == "" {
			model = req.ChatRequest.Model
		}
	}
	if req.ResponsesRequest != nil && (provider == "" || model == "") {
		if provider == "" {
			provider = req.ResponsesRequest.Provider
		}
		if model == "" {
			model = req.ResponsesRequest.Model
		}
	}

	h := sha256.New()
	writeHashPart(h, "provider", []byte(provider))
	writeHashPart(h, "model", []byte(model))
	writeHashPart(h, "request_type", []byte(requestType))
	identity, hasTenant, err := tenantIdentityForFingerprint(ctx)
	if err != nil {
		return "", false, "tenant identity cannot be encoded: " + err.Error()
	}
	if !hasTenant {
		return "", false, "tenant identity is unavailable"
	}
	writeHashPart(h, "tenant_scope", identity.key)
	if cfg.IncludeVirtualKeyScope && identity.virtualKeyID != "" {
		writeHashPart(h, "virtual_key_scope", []byte(identity.virtualKeyID))
	}
	if cfg.IncludeCustomerScope {
		for _, customerID := range identity.customerIDs {
			writeHashPart(h, "customer_scope", []byte(customerID))
		}
	}

	var temperature *float64
	if req.ChatRequest != nil {
		params := req.ChatRequest.Params
		if params != nil {
			temperature = params.Temperature
		}
	} else if req.ResponsesRequest != nil {
		params := req.ResponsesRequest.Params
		if params != nil {
			temperature = params.Temperature
		}
	}
	if temperature != nil && *temperature > cfg.MaxTemperature {
		return "", false, "explicit temperature exceeds the coalescer maximum"
	}

	addJSON := func(label string, value any) bool {
		encoded, err := schemas.MarshalSorted(value)
		if err != nil {
			bypassReason = "cannot canonicalize " + label + ": " + err.Error()
			return false
		}
		writeHashPart(h, label, encoded)
		return true
	}
	if req.ChatRequest != nil && (requestType == schemas.ChatCompletionRequest || requestType == schemas.ChatCompletionStreamRequest) {
		chat := req.ChatRequest
		if !addJSON("messages", chat.Input) {
			return "", false, bypassReason
		}
		var tools any
		var toolChoice any
		var responseFormat any
		var temp, topP, seed, maxTokens any
		var stop any
		if chat.Params != nil {
			tools = chat.Params.Tools
			toolChoice = chat.Params.ToolChoice
			if chat.Params.ResponseFormat != nil {
				responseFormat = *chat.Params.ResponseFormat
			}
			temp = chat.Params.Temperature
			topP = chat.Params.TopP
			seed = chat.Params.Seed
			maxTokens = chat.Params.MaxCompletionTokens
			stop = chat.Params.Stop
		}
		for _, part := range []struct {
			label string
			value any
		}{
			{"tools", tools}, {"tool_choice", toolChoice}, {"response_format", responseFormat},
			{"temperature", temp}, {"top_p", topP}, {"seed", seed}, {"max_tokens", maxTokens}, {"stop", stop},
		} {
			if !addJSON(part.label, part.value) {
				return "", false, bypassReason
			}
		}
	} else if req.ResponsesRequest != nil && (requestType == schemas.ResponsesRequest || requestType == schemas.ResponsesStreamRequest) {
		responses := req.ResponsesRequest
		if !addJSON("input", responses.Input) {
			return "", false, bypassReason
		}
		var tools any
		var toolChoice any
		var responseFormat any
		var instructions any
		var temp, topP, maxTokens any
		if responses.Params != nil {
			tools = responses.Params.Tools
			toolChoice = responses.Params.ToolChoice
			instructions = responses.Params.Instructions
			if responses.Params.Text != nil {
				responseFormat = responses.Params.Text.Format
			}
			temp = responses.Params.Temperature
			topP = responses.Params.TopP
			maxTokens = responses.Params.MaxOutputTokens
		}
		for _, part := range []struct {
			label string
			value any
		}{
			{"instructions", instructions}, {"tools", tools}, {"tool_choice", toolChoice}, {"response_format", responseFormat},
			{"temperature", temp}, {"top_p", topP}, {"seed", nil}, {"max_tokens", maxTokens}, {"stop", nil},
		} {
			if !addJSON(part.label, part.value) {
				return "", false, bypassReason
			}
		}
	} else {
		return "", false, "request type does not match a supported chat or Responses payload"
	}

	return hex.EncodeToString(h.Sum(nil)), true, ""
}

func hasHeaderKey(ctx *schemas.BifrostContext, header string) bool {
	if ctx == nil {
		return false
	}
	if present, _ := ctx.Value(bypassHeaderContextKey).(bool); present {
		return true
	}
	headers, _ := ctx.Value(schemas.BifrostContextKeyExtraHeaders).(map[string][]string)
	for key := range headers {
		if strings.EqualFold(key, header) {
			return true
		}
	}
	return false
}

func tenantIdentityForFingerprint(ctx *schemas.BifrostContext) (fingerprintTenantIdentity, bool, error) {
	if ctx == nil {
		return fingerprintTenantIdentity{}, false, nil
	}
	grant := ctx.Grant()
	if grant == nil {
		return fingerprintTenantIdentity{}, false, nil
	}
	identity := grant.Identity()
	if identity == nil {
		return fingerprintTenantIdentity{}, false, nil
	}

	h := sha256.New()
	found := false
	add := func(label, value string) {
		value = strings.TrimSpace(value)
		if value == "" {
			return
		}
		writeHashPart(h, label, []byte(value))
		found = true
	}

	tenant := fingerprintTenantIdentity{}
	if user := identity.User(); user != nil {
		add("user", user.ID)
	}
	if virtualKey := identity.VirtualKey(); virtualKey != nil {
		tenant.virtualKeyID = strings.TrimSpace(virtualKey.ID)
		add("virtual_key", tenant.virtualKeyID)
	}
	for _, customer := range identity.Customers() {
		customerID := strings.TrimSpace(customer.ID)
		if customerID == "" {
			continue
		}
		tenant.customerIDs = append(tenant.customerIDs, customerID)
		add("customer", customerID)
	}
	for _, team := range identity.Teams() {
		add("team", team.ID)
	}
	for _, unit := range identity.BusinessUnits() {
		add("business_unit", unit.ID)
	}
	if project := identity.Project(); project != nil {
		add("project", project.ID)
	}
	if !found {
		return fingerprintTenantIdentity{}, false, nil
	}
	tenant.key = h.Sum(nil)
	return tenant, true, nil
}

func writeHashPart(h hash.Hash, label string, value []byte) {
	writeHashBytes(h, []byte(label))
	writeHashBytes(h, value)
}

func writeHashBytes(h hash.Hash, value []byte) {
	var length [8]byte
	binary.BigEndian.PutUint64(length[:], uint64(len(value)))
	_, _ = h.Write(length[:])
	_, _ = h.Write(value)
}
