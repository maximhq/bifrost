package governance

import (
	"errors"
	"fmt"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
)

// Provider objects (batches today) are created through the operator's provider key, which every
// virtual key allowed on the provider shares, and are then addressed by the provider-side id alone.
// The creating virtual key is recorded on the job row for accounting; this file makes the read,
// cancel and results paths honour it too, so one key cannot reach what another key created.
//
// The rule is deliberately narrow. A request that presented no virtual key is unrestricted, as it
// is everywhere else in governance. A job with no row (created outside the gateway, or before the
// row carried an owner) or a row with no recorded key cannot be bound to anyone and stays reachable.
// Only a row that names a different virtual key is refused, and it is refused as not found, so the
// other tenant's object is not confirmed to exist.

// providerJobReference returns the provider and provider-side id of the batch a request addresses,
// or an empty id when the request addresses none.
func providerJobReference(req *schemas.BifrostRequest) (provider string, jobID string) {
	if req == nil {
		return "", ""
	}
	switch {
	case req.BatchRetrieveRequest != nil:
		return string(req.BatchRetrieveRequest.Provider), req.BatchRetrieveRequest.BatchID
	case req.BatchCancelRequest != nil:
		return string(req.BatchCancelRequest.Provider), req.BatchCancelRequest.BatchID
	case req.BatchResultsRequest != nil:
		return string(req.BatchResultsRequest.Provider), req.BatchResultsRequest.BatchID
	case req.BatchDeleteRequest != nil:
		return string(req.BatchDeleteRequest.Provider), req.BatchDeleteRequest.BatchID
	}
	return "", ""
}

// ownedByAnotherKey reports whether job records a creating virtual key other than vkID.
func ownedByAnotherKey(job *configstoreTables.TableProviderJob, vkID string) bool {
	return job != nil && job.VirtualKeyID != nil && *job.VirtualKeyID != "" && *job.VirtualKeyID != vkID
}

// enforceProviderJobOwnership refuses a batch retrieve, cancel, results or delete request whose job row
// records a creating virtual key other than the request's own. It runs after evaluation, which is
// what stamps the request's virtual key id. A store failure refuses the request rather than letting
// an unverified read through, and so does running without a config store: no owner is recorded
// anywhere then, so a virtual-key batch request, a list included, cannot be checked and is refused.
func (p *GovernancePlugin) enforceProviderJobOwnership(ctx *schemas.BifrostContext, req *schemas.BifrostRequest) *schemas.LLMPluginShortCircuit {
	provider, jobID := providerJobReference(req)
	isList := req != nil && req.BatchListRequest != nil
	if jobID == "" && !isList {
		return nil
	}
	vkID := bifrost.GetStringFromContext(ctx, schemas.BifrostContextKeyGovernanceVirtualKeyID)
	if vkID == "" {
		return nil
	}
	if p.configStore == nil {
		ctx.SetValue(governanceRejectedContextKey, true)
		return &schemas.LLMPluginShortCircuit{Error: &schemas.BifrostError{
			Type:       bifrost.Ptr(string(DecisionAccessBlocked)),
			StatusCode: bifrost.Ptr(403),
			Error:      &schemas.ErrorField{Message: "batch access cannot be verified for virtual keys without a config store"},
		}}
	}
	if isList {
		return nil
	}
	job, err := p.configStore.GetProviderJob(ctx, configstoreTables.ProviderJobID(configstoreTables.ProviderJobKindBatch, provider, jobID))
	if err != nil {
		if errors.Is(err, configstore.ErrNotFound) {
			return nil
		}
		p.logger.Error("failed to load provider job %s for ownership check: %v", jobID, err)
		ctx.SetValue(governanceRejectedContextKey, true)
		return &schemas.LLMPluginShortCircuit{Error: &schemas.BifrostError{
			StatusCode: bifrost.Ptr(500),
			Error:      &schemas.ErrorField{Message: fmt.Sprintf("failed to verify access to batch '%s'", jobID)},
		}}
	}
	if !ownedByAnotherKey(job, vkID) {
		return nil
	}
	ctx.SetValue(governanceRejectedContextKey, true)
	return &schemas.LLMPluginShortCircuit{Error: &schemas.BifrostError{
		Type:       bifrost.Ptr(string(DecisionAccessBlocked)),
		StatusCode: bifrost.Ptr(404),
		Error:      &schemas.ErrorField{Message: fmt.Sprintf("batch '%s' not found", jobID)},
	}}
}

// filterProviderJobList drops from a batch list the batches whose job row records a creating
// virtual key other than the request's own. Pagination cursors are the provider's and are left
// alone; only the page's items are narrowed. If the ownership lookup fails it returns an error and
// leaves the list untouched; the caller fails the request rather than send a page it could not check.
func (p *GovernancePlugin) filterProviderJobList(ctx *schemas.BifrostContext, provider string, list *schemas.BifrostBatchListResponse) error {
	if p.configStore == nil || list == nil || len(list.Data) == 0 {
		return nil
	}
	vkID := bifrost.GetStringFromContext(ctx, schemas.BifrostContextKeyGovernanceVirtualKeyID)
	if vkID == "" {
		return nil
	}
	ids := make([]string, 0, len(list.Data))
	for _, item := range list.Data {
		ids = append(ids, configstoreTables.ProviderJobID(configstoreTables.ProviderJobKindBatch, provider, item.ID))
	}
	jobs, err := p.configStore.GetProviderJobsByIDs(ctx, ids)
	if err != nil {
		p.logger.Error("failed to load provider jobs for list ownership filter: %v", err)
		return fmt.Errorf("failed to verify access to the batch list: %w", err)
	}
	foreign := make(map[string]struct{}, len(jobs))
	for _, job := range jobs {
		if ownedByAnotherKey(job, vkID) {
			foreign[job.JobID] = struct{}{}
		}
	}
	if len(foreign) == 0 {
		return nil
	}
	kept := make([]schemas.BifrostBatchRetrieveResponse, 0, len(list.Data))
	for _, item := range list.Data {
		if _, hidden := foreign[item.ID]; !hidden {
			kept = append(kept, item)
		}
	}
	list.Data = kept
	return nil
}
