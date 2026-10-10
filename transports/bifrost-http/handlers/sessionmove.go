// This file holds the admin action that moves a session from one virtual key to another.
package handlers

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/framework/configstore"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/valyala/fasthttp"
)

// MoveSessionVirtualKeyRequest is the body of POST /api/governance/sessions/{session_id}/move-virtual-key.
type MoveSessionVirtualKeyRequest struct {
	SourceVirtualKeyID string `json:"source_virtual_key_id"`
	TargetVirtualKeyID string `json:"target_virtual_key_id"`
	// DryRun reports the plan without changing anything.
	DryRun bool `json:"dry_run,omitempty"`
	// Force moves the session even when the target key would exceed a budget.
	Force bool `json:"force,omitempty"`
}

// SessionMoveBudgetChange is one budget's usage before and after a move.
type SessionMoveBudgetChange struct {
	ID          string  `json:"id"`
	Delta       float64 `json:"delta"`
	UsageBefore float64 `json:"usage_before"`
	UsageAfter  float64 `json:"usage_after"`
	MaxLimit    float64 `json:"max_limit"`
	// ExceedsLimit is true when the move leaves the budget over its limit.
	ExceedsLimit bool `json:"exceeds_limit"`
}

// SessionMoveRateLimitChange is one rate limit's counters before and after a move.
type SessionMoveRateLimitChange struct {
	ID             string `json:"id"`
	TokensDelta    int64  `json:"tokens_delta"`
	RequestsDelta  int64  `json:"requests_delta"`
	TokensBefore   int64  `json:"tokens_before"`
	TokensAfter    int64  `json:"tokens_after"`
	RequestsBefore int64  `json:"requests_before"`
	RequestsAfter  int64  `json:"requests_after"`
}

// MoveSessionVirtualKeyResponse describes what a move did, or with dry_run what it would do.
type MoveSessionVirtualKeyResponse struct {
	SessionID          string                       `json:"session_id"`
	SourceVirtualKeyID string                       `json:"source_virtual_key_id"`
	TargetVirtualKeyID string                       `json:"target_virtual_key_id"`
	DryRun             bool                         `json:"dry_run"`
	MovedLogs          int64                        `json:"moved_logs"`
	TotalCost          float64                      `json:"total_cost"`
	TotalTokens        int64                        `json:"total_tokens"`
	Budgets            []SessionMoveBudgetChange    `json:"budgets"`
	RateLimits         []SessionMoveRateLimitChange `json:"rate_limits"`
}

// sessionMoveHolders is the set of budgets and rate limits a key answers to through
// itself, its team and its customer.
type sessionMoveHolders struct {
	budgets    map[string]struct{}
	rateLimits map[string]struct{}
}

// holdersOfVirtualKey collects the budgets and rate limits a request on vk is charged
// to: the key's own, then its team's, then its customer's (the key's own customer, or
// the team's when the key names none). Team and customer limits come from the live
// governance data so they match what enforcement reads.
func holdersOfVirtualKey(vk *configstoreTables.TableVirtualKey, teams map[string]*configstoreTables.TableTeam, customers map[string]*configstoreTables.TableCustomer) sessionMoveHolders {
	h := sessionMoveHolders{budgets: map[string]struct{}{}, rateLimits: map[string]struct{}{}}
	addBudgets := func(budgets []configstoreTables.TableBudget) {
		for _, b := range budgets {
			h.budgets[b.ID] = struct{}{}
		}
	}
	addRateLimit := func(id *string) {
		if id != nil && *id != "" {
			h.rateLimits[*id] = struct{}{}
		}
	}
	addBudgets(vk.Budgets)
	addRateLimit(vk.RateLimitID)

	customerID := ""
	if vk.CustomerID != nil {
		customerID = *vk.CustomerID
	}
	if vk.TeamID != nil && *vk.TeamID != "" {
		if team := teams[*vk.TeamID]; team != nil {
			addBudgets(team.Budgets)
			addRateLimit(team.RateLimitID)
			if customerID == "" && team.CustomerID != nil {
				customerID = *team.CustomerID
			}
		}
	}
	if customerID != "" {
		if customer := customers[customerID]; customer != nil {
			addBudgets(customer.Budgets)
			addRateLimit(customer.RateLimitID)
		}
	}
	return h
}

// difference returns the ids in a that are not in b, sorted.
func difference(a, b map[string]struct{}) []string {
	out := make([]string, 0, len(a))
	for id := range a {
		if _, shared := b[id]; !shared {
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out
}

// sessionMoveTargetFor builds the attribution moved rows take on from the target key.
func (h *GovernanceHandler) sessionMoveTargetFor(ctx context.Context, vk *configstoreTables.TableVirtualKey) logstore.SessionMoveTarget {
	target := logstore.SessionMoveTarget{VirtualKeyID: vk.ID, VirtualKeyName: vk.Name}
	if vk.Team != nil {
		id, name := vk.Team.ID, vk.Team.Name
		target.TeamID, target.TeamName = &id, &name
	}
	customer := vk.Customer
	if customer == nil && vk.Team != nil {
		customer = vk.Team.Customer
	}
	if customer != nil {
		id, name := customer.ID, customer.Name
		target.CustomerID, target.CustomerName = &id, &name
	}
	if vk.BusinessUnitID != nil && *vk.BusinessUnitID != "" {
		id := *vk.BusinessUnitID
		target.BusinessUnitID = &id
		h.applyBusinessUnits(ctx, []*configstoreTables.TableVirtualKey{vk})
		if vk.BusinessUnit != nil {
			name := vk.BusinessUnit.Name
			target.BusinessUnitName = &name
		}
	}
	return target
}

// planSessionMove turns the rows a move touched into usage deltas.
//
// Only spend inside a budget's current window is moved: a row older than the window's
// last reset was already wiped from the counter, so subtracting it would under-count
// spend that is still live. The same holds per counter for rate limits.
func planSessionMove(
	res *logstore.SessionMoveResult,
	remove, add holdersDiff,
	budgets map[string]*configstoreTables.TableBudget,
	rateLimits map[string]*configstoreTables.TableRateLimit,
) (map[string]float64, map[string]RateLimitUsageDelta) {
	budgetDeltas := map[string]float64{}
	rateLimitDeltas := map[string]RateLimitUsageDelta{}
	removeBudgets := toSet(remove.budgets)
	removeRateLimits := toSet(remove.rateLimits)

	for _, row := range res.Rows {
		inWindow := func(id string) bool {
			b := budgets[id]
			return b != nil && !row.Timestamp.Before(b.LastReset)
		}
		for _, id := range row.BudgetIDs {
			if _, drop := removeBudgets[id]; drop && inWindow(id) {
				budgetDeltas[id] -= row.Cost
			}
		}
		for _, id := range add.budgets {
			if inWindow(id) {
				budgetDeltas[id] += row.Cost
			}
		}
		for _, id := range row.RateLimitIDs {
			if _, drop := removeRateLimits[id]; drop {
				d := rateLimitDeltas[id]
				if rl := rateLimits[id]; rl != nil {
					if !row.Timestamp.Before(rl.TokenLastReset) {
						d.Tokens -= row.TotalTokens
					}
					if !row.Timestamp.Before(rl.RequestLastReset) {
						d.Requests--
					}
				}
				rateLimitDeltas[id] = d
			}
		}
		for _, id := range add.rateLimits {
			d := rateLimitDeltas[id]
			if rl := rateLimits[id]; rl != nil {
				if !row.Timestamp.Before(rl.TokenLastReset) {
					d.Tokens += row.TotalTokens
				}
				if !row.Timestamp.Before(rl.RequestLastReset) {
					d.Requests++
				}
			}
			rateLimitDeltas[id] = d
		}
	}
	return budgetDeltas, rateLimitDeltas
}

// holdersDiff is the budgets and rate limits exclusive to one side of a move.
type holdersDiff struct {
	budgets    []string
	rateLimits []string
}

func toSet(ids []string) map[string]struct{} {
	set := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		set[id] = struct{}{}
	}
	return set
}

// moveSessionVirtualKey handles POST /api/governance/sessions/{session_id}/move-virtual-key.
//
// It re-attributes a session's logs from a source virtual key to a target one and moves
// the spend with them: usage leaves the budgets and rate limits only the source answers
// to and lands on those only the target answers to. Budgets the two keys share are
// untouched, since the same budget paid either way.
func (h *GovernanceHandler) moveSessionVirtualKey(ctx *fasthttp.RequestCtx) {
	if h.logManager == nil {
		SendError(ctx, fasthttp.StatusServiceUnavailable, "Logging is not enabled")
		return
	}
	sessionID, _ := ctx.UserValue("session_id").(string)
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		SendError(ctx, fasthttp.StatusBadRequest, "session_id is required")
		return
	}
	var req MoveSessionVirtualKeyRequest
	if err := sonic.Unmarshal(ctx.PostBody(), &req); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, "Invalid JSON")
		return
	}
	if req.SourceVirtualKeyID == "" || req.TargetVirtualKeyID == "" {
		SendError(ctx, fasthttp.StatusBadRequest, "source_virtual_key_id and target_virtual_key_id are required")
		return
	}
	if req.SourceVirtualKeyID == req.TargetVirtualKeyID {
		SendError(ctx, fasthttp.StatusBadRequest, "source and target virtual keys must differ")
		return
	}

	loadVK := func(id, role string) (*configstoreTables.TableVirtualKey, bool) {
		vk, err := h.configStore.GetVirtualKey(ctx, id)
		if err != nil {
			if errors.Is(err, configstore.ErrNotFound) {
				SendError(ctx, fasthttp.StatusNotFound, fmt.Sprintf("%s virtual key not found", role))
			} else {
				logger.Error("failed to load %s virtual key %s for session move: %v", role, id, err)
				SendError(ctx, fasthttp.StatusInternalServerError, "Failed to retrieve virtual key")
			}
			return nil, false
		}
		return vk, true
	}
	source, ok := loadVK(req.SourceVirtualKeyID, "source")
	if !ok {
		return
	}
	target, ok := loadVK(req.TargetVirtualKeyID, "target")
	if !ok {
		return
	}
	data := h.governanceManager.GetGovernanceData(ctx)
	if data == nil {
		SendError(ctx, fasthttp.StatusInternalServerError, "Governance data is not available")
		return
	}

	sourceHolders := holdersOfVirtualKey(source, data.Teams, data.Customers)
	targetHolders := holdersOfVirtualKey(target, data.Teams, data.Customers)
	remove := holdersDiff{
		budgets:    difference(sourceHolders.budgets, targetHolders.budgets),
		rateLimits: difference(sourceHolders.rateLimits, targetHolders.rateLimits),
	}
	add := holdersDiff{
		budgets:    difference(targetHolders.budgets, sourceHolders.budgets),
		rateLimits: difference(targetHolders.rateLimits, sourceHolders.rateLimits),
	}
	moveReq := logstore.SessionMoveRequest{
		SessionID:          sessionID,
		SourceVirtualKeyID: source.ID,
		Target:             h.sessionMoveTargetFor(ctx, target),
		RemoveBudgetIDs:    remove.budgets,
		AddBudgetIDs:       add.budgets,
		RemoveRateLimitIDs: remove.rateLimits,
		AddRateLimitIDs:    add.rateLimits,
		DryRun:             true,
	}

	// Plan first, against the rows as they are now, so the target's limits can be checked
	// before anything is written.
	preview, err := h.logManager.MoveSessionVirtualKey(ctx, moveReq)
	if err != nil {
		h.sendSessionMoveError(ctx, err)
		return
	}
	if preview.MovedCount == 0 {
		SendError(ctx, fasthttp.StatusNotFound, "No logs for this session were recorded under the source virtual key")
		return
	}
	budgetDeltas, rateLimitDeltas := planSessionMove(preview, remove, add, data.Budgets, data.RateLimits)
	resp := buildSessionMoveResponse(sessionID, req, preview, budgetDeltas, rateLimitDeltas, data.Budgets, data.RateLimits)

	if req.DryRun {
		SendJSON(ctx, resp)
		return
	}
	if !req.Force {
		for _, change := range resp.Budgets {
			if change.ExceedsLimit && change.Delta > 0 {
				SendJSONWithStatus(ctx, resp, fasthttp.StatusConflict)
				return
			}
		}
	}

	// The real move reports the rows it actually changed, which can differ from the preview
	// if the session received requests in between; the deltas follow those rows.
	moveReq.DryRun = false
	moved, err := h.logManager.MoveSessionVirtualKey(ctx, moveReq)
	if err != nil {
		h.sendSessionMoveError(ctx, err)
		return
	}
	budgetDeltas, rateLimitDeltas = planSessionMove(moved, remove, add, data.Budgets, data.RateLimits)
	if err := h.governanceManager.AdjustUsageInMemory(ctx, budgetDeltas, rateLimitDeltas); err != nil {
		logger.Error("session %s moved from %s to %s but adjusting usage failed: %v", sessionID, source.ID, target.ID, err)
		SendError(ctx, fasthttp.StatusInternalServerError, "Session logs were moved but budget usage could not be adjusted")
		return
	}
	logger.Info("moved session %s (%d logs, $%.6f) from virtual key %s to %s", sessionID, moved.MovedCount, moved.TotalCost, source.ID, target.ID)
	resp = buildSessionMoveResponse(sessionID, req, moved, budgetDeltas, rateLimitDeltas, data.Budgets, data.RateLimits)
	SendJSON(ctx, resp)
}

func (h *GovernanceHandler) sendSessionMoveError(ctx *fasthttp.RequestCtx, err error) {
	if errors.Is(err, logstore.ErrSessionMoveUnsupported) {
		SendError(ctx, fasthttp.StatusNotImplemented, err.Error())
		return
	}
	logger.Error("failed to move session between virtual keys: %v", err)
	SendError(ctx, fasthttp.StatusInternalServerError, "Failed to move session")
}

// buildSessionMoveResponse reports usage before and after the deltas, in a stable order.
func buildSessionMoveResponse(
	sessionID string,
	req MoveSessionVirtualKeyRequest,
	res *logstore.SessionMoveResult,
	budgetDeltas map[string]float64,
	rateLimitDeltas map[string]RateLimitUsageDelta,
	budgets map[string]*configstoreTables.TableBudget,
	rateLimits map[string]*configstoreTables.TableRateLimit,
) MoveSessionVirtualKeyResponse {
	resp := MoveSessionVirtualKeyResponse{
		SessionID:          sessionID,
		SourceVirtualKeyID: req.SourceVirtualKeyID,
		TargetVirtualKeyID: req.TargetVirtualKeyID,
		DryRun:             req.DryRun,
		MovedLogs:          res.MovedCount,
		TotalCost:          res.TotalCost,
		Budgets:            []SessionMoveBudgetChange{},
		RateLimits:         []SessionMoveRateLimitChange{},
	}
	for _, row := range res.Rows {
		resp.TotalTokens += row.TotalTokens
	}
	budgetIDs := make([]string, 0, len(budgetDeltas))
	for id := range budgetDeltas {
		budgetIDs = append(budgetIDs, id)
	}
	sort.Strings(budgetIDs)
	for _, id := range budgetIDs {
		change := SessionMoveBudgetChange{ID: id, Delta: budgetDeltas[id]}
		if b := budgets[id]; b != nil {
			change.UsageBefore = b.CurrentUsage
			change.MaxLimit = b.EffectiveMaxLimit()
		}
		change.UsageAfter = max(change.UsageBefore+change.Delta, 0)
		change.ExceedsLimit = change.MaxLimit > 0 && change.UsageAfter > change.MaxLimit
		resp.Budgets = append(resp.Budgets, change)
	}
	rateLimitIDs := make([]string, 0, len(rateLimitDeltas))
	for id := range rateLimitDeltas {
		rateLimitIDs = append(rateLimitIDs, id)
	}
	sort.Strings(rateLimitIDs)
	for _, id := range rateLimitIDs {
		delta := rateLimitDeltas[id]
		change := SessionMoveRateLimitChange{ID: id, TokensDelta: delta.Tokens, RequestsDelta: delta.Requests}
		if rl := rateLimits[id]; rl != nil {
			change.TokensBefore = rl.TokenCurrentUsage
			change.RequestsBefore = rl.RequestCurrentUsage
		}
		change.TokensAfter = max(change.TokensBefore+delta.Tokens, 0)
		change.RequestsAfter = max(change.RequestsBefore+delta.Requests, 0)
		resp.RateLimits = append(resp.RateLimits, change)
	}
	return resp
}
