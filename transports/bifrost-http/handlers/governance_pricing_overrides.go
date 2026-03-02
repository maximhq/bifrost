package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/valyala/fasthttp"
)

type CreatePricingOverrideRequest struct {
	Name          string                           `json:"name"`
	ScopeKind     schemas.PricingOverrideScopeKind `json:"scope_kind"`
	VirtualKeyID  *string                          `json:"virtual_key_id,omitempty"`
	ProviderID    *string                          `json:"provider_id,omitempty"`
	ProviderKeyID *string                          `json:"provider_key_id,omitempty"`
	MatchType     schemas.PricingOverrideMatchType `json:"match_type"`
	Pattern       string                           `json:"pattern"`
	RequestTypes  []schemas.RequestType            `json:"request_types,omitempty"`
	Patch         schemas.PricingOverridePatch     `json:"patch,omitempty"`
}

type PatchPricingOverrideRequest struct {
	Name          *string                           `json:"name,omitempty"`
	ScopeKind     *schemas.PricingOverrideScopeKind `json:"scope_kind,omitempty"`
	VirtualKeyID  *string                           `json:"virtual_key_id,omitempty"`
	ProviderID    *string                           `json:"provider_id,omitempty"`
	ProviderKeyID *string                           `json:"provider_key_id,omitempty"`
	MatchType     *schemas.PricingOverrideMatchType `json:"match_type,omitempty"`
	Pattern       *string                           `json:"pattern,omitempty"`
	RequestTypes  *[]schemas.RequestType            `json:"request_types,omitempty"`
	Patch         *schemas.PricingOverridePatch     `json:"patch,omitempty"`
}

func (h *GovernanceHandler) getPricingOverrides(ctx *fasthttp.RequestCtx) {
	overrides, err := h.configStore.GetPricingOverrides(ctx, pricingOverrideFilterFromQuery(ctx))
	if err != nil {
		logger.Error("failed to retrieve pricing overrides: %v", err)
		SendError(ctx, fasthttp.StatusInternalServerError, "Failed to retrieve pricing overrides")
		return
	}

	SendJSON(ctx, map[string]interface{}{
		"pricing_overrides": overrides,
		"count":             len(overrides),
	})
}

func (h *GovernanceHandler) createPricingOverride(ctx *fasthttp.RequestCtx) {
	var req CreatePricingOverrideRequest
	if !decodePricingOverrideJSON(ctx, &req) {
		return
	}

	name, err := normalizeAndValidatePricingOverrideName(req.Name)
	if err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, err.Error())
		return
	}

	if err := validatePricingOverrideRequest(req.ScopeKind, req.VirtualKeyID, req.ProviderID, req.ProviderKeyID, req.MatchType, req.Pattern, req.RequestTypes, req.Patch); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, err.Error())
		return
	}

	now := time.Now()
	override := configstoreTables.TablePricingOverride{
		ID:            uuid.NewString(),
		Name:          name,
		ScopeKind:     req.ScopeKind,
		VirtualKeyID:  normalizeOptionalString(req.VirtualKeyID),
		ProviderID:    normalizeOptionalString(req.ProviderID),
		ProviderKeyID: normalizeOptionalString(req.ProviderKeyID),
		MatchType:     req.MatchType,
		Pattern:       strings.TrimSpace(req.Pattern),
		RequestTypes:  req.RequestTypes,
		Patch:         req.Patch,
		CreatedAt:     now,
		UpdatedAt:     now,
	}

	if err := h.configStore.CreatePricingOverride(ctx, &override); err != nil {
		logger.Error("failed to create pricing override: %v", err)
		SendError(ctx, fasthttp.StatusInternalServerError, "Failed to create pricing override")
		return
	}

	h.refreshPricingOverrides(ctx)
	SendJSONWithStatus(ctx, map[string]interface{}{
		"message":          "Pricing override created successfully",
		"pricing_override": override,
	}, fasthttp.StatusCreated)
}

func (h *GovernanceHandler) patchPricingOverride(ctx *fasthttp.RequestCtx) {
	id := ctx.UserValue("id").(string)

	var req PatchPricingOverrideRequest
	if !decodePricingOverrideJSON(ctx, &req) {
		return
	}

	override, ok := h.getPricingOverrideOrSendError(ctx, id)
	if !ok {
		return
	}

	if req.ScopeKind != nil {
		override.ScopeKind = *req.ScopeKind
	}
	if req.Name != nil {
		name, err := normalizeAndValidatePricingOverrideName(*req.Name)
		if err != nil {
			SendError(ctx, fasthttp.StatusBadRequest, err.Error())
			return
		}
		override.Name = name
	}
	if req.VirtualKeyID != nil {
		override.VirtualKeyID = normalizeOptionalString(req.VirtualKeyID)
	}
	if req.ProviderID != nil {
		override.ProviderID = normalizeOptionalString(req.ProviderID)
	}
	if req.ProviderKeyID != nil {
		override.ProviderKeyID = normalizeOptionalString(req.ProviderKeyID)
	}
	if req.MatchType != nil {
		override.MatchType = *req.MatchType
	}
	if req.Pattern != nil {
		override.Pattern = strings.TrimSpace(*req.Pattern)
	}
	if req.RequestTypes != nil {
		override.RequestTypes = *req.RequestTypes
	}
	if req.Patch != nil {
		override.Patch = *req.Patch
	}
	override.UpdatedAt = time.Now()

	if err := validatePricingOverrideRequest(override.ScopeKind, override.VirtualKeyID, override.ProviderID, override.ProviderKeyID, override.MatchType, override.Pattern, override.RequestTypes, override.Patch); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, err.Error())
		return
	}

	if err := h.configStore.UpdatePricingOverride(ctx, override); err != nil {
		logger.Error("failed to update pricing override: %v", err)
		SendError(ctx, fasthttp.StatusInternalServerError, "Failed to update pricing override")
		return
	}

	h.refreshPricingOverrides(ctx)
	SendJSON(ctx, map[string]interface{}{
		"message":          "Pricing override updated successfully",
		"pricing_override": override,
	})
}

func (h *GovernanceHandler) deletePricingOverride(ctx *fasthttp.RequestCtx) {
	id := ctx.UserValue("id").(string)
	if err := h.configStore.DeletePricingOverride(ctx, id); err != nil {
		if errors.Is(err, configstore.ErrNotFound) {
			SendError(ctx, fasthttp.StatusNotFound, "Pricing override not found")
			return
		}
		logger.Error("failed to delete pricing override: %v", err)
		SendError(ctx, fasthttp.StatusInternalServerError, "Failed to delete pricing override")
		return
	}

	h.refreshPricingOverrides(ctx)
	SendJSON(ctx, map[string]interface{}{
		"message": "Pricing override deleted successfully",
	})
}

func pricingOverrideFilterFromQuery(ctx *fasthttp.RequestCtx) configstore.PricingOverrideFilter {
	var filter configstore.PricingOverrideFilter
	if scopeKindRaw := strings.TrimSpace(string(ctx.QueryArgs().Peek("scope_kind"))); scopeKindRaw != "" {
		scopeKind := schemas.PricingOverrideScopeKind(scopeKindRaw)
		filter.ScopeKind = &scopeKind
	}
	if virtualKeyID := strings.TrimSpace(string(ctx.QueryArgs().Peek("virtual_key_id"))); virtualKeyID != "" {
		filter.VirtualKeyID = &virtualKeyID
	}
	if providerID := strings.TrimSpace(string(ctx.QueryArgs().Peek("provider_id"))); providerID != "" {
		filter.ProviderID = &providerID
	}
	if providerKeyID := strings.TrimSpace(string(ctx.QueryArgs().Peek("provider_key_id"))); providerKeyID != "" {
		filter.ProviderKeyID = &providerKeyID
	}
	return filter
}

func decodePricingOverrideJSON(ctx *fasthttp.RequestCtx, dst any) bool {
	if err := json.Unmarshal(ctx.PostBody(), dst); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, "Invalid JSON")
		return false
	}
	return true
}

func (h *GovernanceHandler) getPricingOverrideOrSendError(ctx *fasthttp.RequestCtx, id string) (*configstoreTables.TablePricingOverride, bool) {
	override, err := h.configStore.GetPricingOverrideByID(ctx, id)
	if err == nil {
		return override, true
	}
	if errors.Is(err, configstore.ErrNotFound) {
		SendError(ctx, fasthttp.StatusNotFound, "Pricing override not found")
		return nil, false
	}
	logger.Error("failed to retrieve pricing override by id %s: %v", id, err)
	SendError(ctx, fasthttp.StatusInternalServerError, "Failed to retrieve pricing override")
	return nil, false
}

func (h *GovernanceHandler) refreshPricingOverrides(ctx context.Context) {
	if h.modelCatalog == nil {
		return
	}
	rows, err := h.configStore.GetPricingOverrides(ctx, configstore.PricingOverrideFilter{})
	if err != nil {
		logger.Warn("failed to load pricing overrides for model catalog refresh: %v", err)
		return
	}
	if err := h.modelCatalog.SetPricingOverrides(toSchemaPricingOverrides(rows)); err != nil {
		logger.Warn("failed to apply pricing override refresh: %v", err)
	}
}

func toSchemaPricingOverrides(rows []configstoreTables.TablePricingOverride) []schemas.PricingOverride {
	overrides := make([]schemas.PricingOverride, 0, len(rows))
	for i := range rows {
		overrides = append(overrides, schemas.PricingOverride{
			ID:            rows[i].ID,
			Name:          rows[i].Name,
			ScopeKind:     rows[i].ScopeKind,
			VirtualKeyID:  rows[i].VirtualKeyID,
			ProviderID:    rows[i].ProviderID,
			ProviderKeyID: rows[i].ProviderKeyID,
			MatchType:     rows[i].MatchType,
			Pattern:       rows[i].Pattern,
			RequestTypes:  rows[i].RequestTypes,
			Patch:         rows[i].Patch,
			ConfigHash:    rows[i].ConfigHash,
			CreatedAt:     rows[i].CreatedAt,
			UpdatedAt:     rows[i].UpdatedAt,
		})
	}
	return overrides
}

func validatePricingOverrideRequest(
	scopeKind schemas.PricingOverrideScopeKind,
	virtualKeyID, providerID, providerKeyID *string,
	matchType schemas.PricingOverrideMatchType,
	pattern string,
	requestTypes []schemas.RequestType,
	patch schemas.PricingOverridePatch,
) error {
	if err := schemas.ValidatePricingOverrideScopeKind(scopeKind, virtualKeyID, providerID, providerKeyID); err != nil {
		return err
	}
	if _, err := schemas.ValidatePricingOverridePattern(matchType, pattern); err != nil {
		return err
	}
	if err := schemas.ValidatePricingOverrideRequestTypes(requestTypes); err != nil {
		return err
	}
	return schemas.ValidatePricingOverridePatchNonNegative(patch)
}

func normalizeAndValidatePricingOverrideName(name string) (string, error) {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return "", errors.New("name is required")
	}
	return trimmed, nil
}

func normalizeOptionalString(value *string) *string {
	if value == nil {
		return nil
	}
	trimmed := strings.TrimSpace(*value)
	if trimmed == "" {
		return nil
	}
	return &trimmed
}
