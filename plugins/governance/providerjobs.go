package governance

import (
	"fmt"

	bifrost "github.com/maximhq/bifrost/core"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
)

// Provider objects (batches, videos, files, containers, cached contents) are created through the
// operator's provider key, which every virtual key allowed on the provider shares, and are then
// addressed by the provider-side id alone. This file makes them belong to the virtual key that
// created them: the create response records the key, the read, download, delete, remix and
// container-file paths honour it, and list pages are narrowed to what the key may see.
//
// Who created an object is read from the ownership row written here, and, for batches and
// videos, from the accounting job row the logging plugin writes, which predates ownership rows.
// A batch row also names the batch's input, output and error files, so those files are bound to
// the batch's key even when they were never recorded on their own.
//
// The rule is deliberately narrow. A request that presented no virtual key is unrestricted, as it
// is everywhere else in governance. An object with no row (created outside the gateway, or before
// its kind was recorded) cannot be bound to anyone and stays reachable. Only a row that names a
// different virtual key is refused, and it is refused as not found, so the other tenant's object
// is not confirmed to exist.

// providerObjectRef names the provider object a request addresses, or the kind a list request
// pages through.
type providerObjectRef struct {
	kind      string
	provider  string
	requestID string   // the id as the caller wrote it, for messages
	ids       []string // the forms the id may be recorded under
	isList    bool
}

// providerObjectLabel is the noun a kind goes by in client-facing messages.
func providerObjectLabel(kind string) string {
	switch kind {
	case configstoreTables.ProviderObjectKindBatch:
		return "batch"
	case configstoreTables.ProviderObjectKindVideo:
		return "video"
	case configstoreTables.ProviderObjectKindFile:
		return "file"
	case configstoreTables.ProviderObjectKindContainer:
		return "container"
	case configstoreTables.ProviderObjectKindCachedContent:
		return "cached content"
	}
	return "object"
}

// videoIDForms returns the forms a video id may be recorded under: as written, bare, and scoped
// with its ":<provider>" suffix the way providers hand it back.
func videoIDForms(provider schemas.ModelProvider, id string) []string {
	if id == "" {
		return nil
	}
	bare := providerUtils.StripVideoIDProviderSuffix(id, provider)
	forms := []string{id}
	for _, form := range []string{bare, providerUtils.AddVideoIDProviderSuffix(bare, provider)} {
		if form != "" && form != id && (len(forms) < 2 || form != forms[1]) {
			forms = append(forms, form)
		}
	}
	return forms
}

func objectRef(kind string, provider schemas.ModelProvider, id string) (providerObjectRef, bool) {
	if id == "" {
		return providerObjectRef{}, false
	}
	return providerObjectRef{kind: kind, provider: string(provider), requestID: id, ids: []string{id}}, true
}

func listRef(kind string, provider schemas.ModelProvider) (providerObjectRef, bool) {
	return providerObjectRef{kind: kind, provider: string(provider), isList: true}, true
}

// providerObjectReference returns the provider object a request addresses, or false when the
// request addresses none.
func providerObjectReference(req *schemas.BifrostRequest) (providerObjectRef, bool) {
	if req == nil {
		return providerObjectRef{}, false
	}
	batch := configstoreTables.ProviderObjectKindBatch
	video := configstoreTables.ProviderObjectKindVideo
	file := configstoreTables.ProviderObjectKindFile
	container := configstoreTables.ProviderObjectKindContainer
	cached := configstoreTables.ProviderObjectKindCachedContent
	videoRef := func(provider schemas.ModelProvider, id string) (providerObjectRef, bool) {
		ref, ok := objectRef(video, provider, id)
		if ok {
			ref.ids = videoIDForms(provider, id)
		}
		return ref, ok
	}
	switch {
	case req.BatchRetrieveRequest != nil:
		return objectRef(batch, req.BatchRetrieveRequest.Provider, req.BatchRetrieveRequest.BatchID)
	case req.BatchCancelRequest != nil:
		return objectRef(batch, req.BatchCancelRequest.Provider, req.BatchCancelRequest.BatchID)
	case req.BatchResultsRequest != nil:
		return objectRef(batch, req.BatchResultsRequest.Provider, req.BatchResultsRequest.BatchID)
	case req.BatchDeleteRequest != nil:
		return objectRef(batch, req.BatchDeleteRequest.Provider, req.BatchDeleteRequest.BatchID)
	case req.BatchListRequest != nil:
		return listRef(batch, req.BatchListRequest.Provider)
	case req.BatchCreateRequest != nil:
		// A batch runs on its input file; another key's file is as closed to it as the file itself.
		return objectRef(file, req.BatchCreateRequest.Provider, req.BatchCreateRequest.InputFileID)
	case req.FileRetrieveRequest != nil:
		return objectRef(file, req.FileRetrieveRequest.Provider, req.FileRetrieveRequest.FileID)
	case req.FileContentRequest != nil:
		return objectRef(file, req.FileContentRequest.Provider, req.FileContentRequest.FileID)
	case req.FileDeleteRequest != nil:
		return objectRef(file, req.FileDeleteRequest.Provider, req.FileDeleteRequest.FileID)
	case req.FileListRequest != nil:
		return listRef(file, req.FileListRequest.Provider)
	case req.VideoRetrieveRequest != nil:
		return videoRef(req.VideoRetrieveRequest.Provider, req.VideoRetrieveRequest.ID)
	case req.VideoDownloadRequest != nil:
		return videoRef(req.VideoDownloadRequest.Provider, req.VideoDownloadRequest.ID)
	case req.VideoDeleteRequest != nil:
		return videoRef(req.VideoDeleteRequest.Provider, req.VideoDeleteRequest.ID)
	case req.VideoRemixRequest != nil:
		return videoRef(req.VideoRemixRequest.Provider, req.VideoRemixRequest.ID)
	case req.VideoListRequest != nil:
		return listRef(video, req.VideoListRequest.Provider)
	case req.ContainerRetrieveRequest != nil:
		return objectRef(container, req.ContainerRetrieveRequest.Provider, req.ContainerRetrieveRequest.ContainerID)
	case req.ContainerDeleteRequest != nil:
		return objectRef(container, req.ContainerDeleteRequest.Provider, req.ContainerDeleteRequest.ContainerID)
	case req.ContainerFileCreateRequest != nil:
		return objectRef(container, req.ContainerFileCreateRequest.Provider, req.ContainerFileCreateRequest.ContainerID)
	case req.ContainerFileListRequest != nil:
		return objectRef(container, req.ContainerFileListRequest.Provider, req.ContainerFileListRequest.ContainerID)
	case req.ContainerFileRetrieveRequest != nil:
		return objectRef(container, req.ContainerFileRetrieveRequest.Provider, req.ContainerFileRetrieveRequest.ContainerID)
	case req.ContainerFileContentRequest != nil:
		return objectRef(container, req.ContainerFileContentRequest.Provider, req.ContainerFileContentRequest.ContainerID)
	case req.ContainerFileDeleteRequest != nil:
		return objectRef(container, req.ContainerFileDeleteRequest.Provider, req.ContainerFileDeleteRequest.ContainerID)
	case req.ContainerListRequest != nil:
		return listRef(container, req.ContainerListRequest.Provider)
	case req.CachedContentRetrieveRequest != nil:
		return objectRef(cached, req.CachedContentRetrieveRequest.Provider, req.CachedContentRetrieveRequest.Name)
	case req.CachedContentUpdateRequest != nil:
		return objectRef(cached, req.CachedContentUpdateRequest.Provider, req.CachedContentUpdateRequest.Name)
	case req.CachedContentDeleteRequest != nil:
		return objectRef(cached, req.CachedContentDeleteRequest.Provider, req.CachedContentDeleteRequest.Name)
	case req.CachedContentListRequest != nil:
		return listRef(cached, req.CachedContentListRequest.Provider)
	}
	return providerObjectRef{}, false
}

// providerObjectOwners returns, per provider-side id, the virtual key recorded as its creator.
// The ownership row wins; for batches and videos the accounting job row is read next, and for
// files the batch rows naming the file as input, output or error. Ids nobody recorded are absent.
func (p *GovernancePlugin) providerObjectOwners(ctx *schemas.BifrostContext, kind, provider string, ids []string) (map[string]string, error) {
	owners := make(map[string]string, len(ids))
	if len(ids) == 0 {
		return owners, nil
	}
	objectIDs := make([]string, 0, len(ids))
	for _, id := range ids {
		objectIDs = append(objectIDs, configstoreTables.ProviderObjectID(kind, provider, id))
	}
	objects, err := p.configStore.GetProviderObjectsByIDs(ctx, objectIDs)
	if err != nil {
		return nil, err
	}
	for _, object := range objects {
		if object != nil && object.VirtualKeyID != "" {
			owners[object.ObjectID] = object.VirtualKeyID
		}
	}
	claim := func(id, owner string) {
		if id == "" || owner == "" {
			return
		}
		if _, seen := owners[id]; !seen {
			owners[id] = owner
		}
	}
	switch kind {
	case configstoreTables.ProviderObjectKindBatch, configstoreTables.ProviderObjectKindVideo:
		jobIDs := make([]string, 0, len(ids))
		for _, id := range ids {
			jobIDs = append(jobIDs, configstoreTables.ProviderJobID(kind, provider, id))
		}
		jobs, err := p.configStore.GetProviderJobsByIDs(ctx, jobIDs)
		if err != nil {
			return nil, err
		}
		for _, job := range jobs {
			if job != nil && job.VirtualKeyID != nil {
				claim(job.JobID, *job.VirtualKeyID)
			}
		}
	case configstoreTables.ProviderObjectKindFile:
		jobs, err := p.configStore.GetProviderJobsByFileIDs(ctx, provider, ids)
		if err != nil {
			return nil, err
		}
		wanted := make(map[string]struct{}, len(ids))
		for _, id := range ids {
			wanted[id] = struct{}{}
		}
		for _, job := range jobs {
			if job == nil || job.VirtualKeyID == nil {
				continue
			}
			for _, fileID := range []string{job.InputFileID, derefString(job.OutputFileID), derefString(job.ErrorFileID)} {
				if _, ok := wanted[fileID]; ok {
					claim(fileID, *job.VirtualKeyID)
				}
			}
		}
	}
	return owners, nil
}

// enforceProviderObjectOwnership refuses a request for a provider object whose recorded creator
// is a virtual key other than the request's own. It runs after evaluation, which is what stamps
// the request's virtual key id. A store failure refuses the request rather than letting an
// unverified read through, and so does running without a config store: no creator is recorded
// anywhere then, so a virtual-key request for a bound kind, a list included, cannot be checked
// and is refused.
func (p *GovernancePlugin) enforceProviderObjectOwnership(ctx *schemas.BifrostContext, req *schemas.BifrostRequest) *schemas.LLMPluginShortCircuit {
	ref, ok := providerObjectReference(req)
	if !ok {
		return nil
	}
	vkID := bifrost.GetStringFromContext(ctx, schemas.BifrostContextKeyGovernanceVirtualKeyID)
	if vkID == "" {
		return nil
	}
	label := providerObjectLabel(ref.kind)
	if p.configStore == nil {
		// A batch create only names its input file; with nothing recorded anywhere the file cannot
		// be anyone else's on record, and the batch's own results stay refused below. Creating is
		// left open so a deployment without a config store can still submit batches.
		if req.BatchCreateRequest != nil {
			return nil
		}
		ctx.SetValue(governanceRejectedContextKey, true)
		return &schemas.LLMPluginShortCircuit{Error: &schemas.BifrostError{
			Type:       bifrost.Ptr(string(DecisionAccessBlocked)),
			StatusCode: bifrost.Ptr(403),
			Error:      &schemas.ErrorField{Message: fmt.Sprintf("%s access cannot be verified for virtual keys without a config store", label)},
		}}
	}
	if ref.isList {
		return nil
	}
	owners, err := p.providerObjectOwners(ctx, ref.kind, ref.provider, ref.ids)
	if err != nil {
		p.logger.Error("failed to load %s %s for ownership check: %v", label, ref.requestID, err)
		ctx.SetValue(governanceRejectedContextKey, true)
		return &schemas.LLMPluginShortCircuit{Error: &schemas.BifrostError{
			StatusCode: bifrost.Ptr(500),
			Error:      &schemas.ErrorField{Message: fmt.Sprintf("failed to verify access to %s '%s'", label, ref.requestID)},
		}}
	}
	for _, owner := range owners {
		if owner != vkID {
			ctx.SetValue(governanceRejectedContextKey, true)
			return &schemas.LLMPluginShortCircuit{Error: &schemas.BifrostError{
				Type:       bifrost.Ptr(string(DecisionAccessBlocked)),
				StatusCode: bifrost.Ptr(404),
				Error:      &schemas.ErrorField{Message: fmt.Sprintf("%s '%s' not found", label, ref.requestID)},
			}}
		}
	}
	return nil
}

// hiddenProviderObjects returns, from a page of ids, the ones another virtual key created.
func (p *GovernancePlugin) hiddenProviderObjects(ctx *schemas.BifrostContext, kind, provider, vkID string, ids []string) (map[string]struct{}, error) {
	owners, err := p.providerObjectOwners(ctx, kind, provider, ids)
	if err != nil {
		return nil, err
	}
	hidden := make(map[string]struct{})
	for id, owner := range owners {
		if owner != vkID {
			hidden[id] = struct{}{}
		}
	}
	return hidden, nil
}

// filterProviderObjectList narrows a list of provider objects to the ones the request's virtual
// key may see. Pagination cursors are the provider's and are left alone; only the page's items
// are narrowed. A provider can attach the whole unfiltered page as the raw response (OpenAI does
// so on every batch item), so once anything is removed the raw page is dropped from the list and
// from the kept items. If the ownership lookup fails it returns an error and leaves the list
// untouched; the caller fails the request rather than send a page it could not check.
func (p *GovernancePlugin) filterProviderObjectList(ctx *schemas.BifrostContext, provider string, result *schemas.BifrostResponse) error {
	if p.configStore == nil || result == nil {
		return nil
	}
	vkID := bifrost.GetStringFromContext(ctx, schemas.BifrostContextKeyGovernanceVirtualKeyID)
	if vkID == "" {
		return nil
	}
	fail := func(kind string, err error) error {
		label := providerObjectLabel(kind)
		p.logger.Error("failed to load %s ownership for list filter: %v", label, err)
		return fmt.Errorf("failed to verify access to the %s list", label)
	}
	switch {
	case result.BatchListResponse != nil && len(result.BatchListResponse.Data) > 0:
		list := result.BatchListResponse
		ids := make([]string, 0, len(list.Data))
		for _, item := range list.Data {
			ids = append(ids, item.ID)
		}
		hidden, err := p.hiddenProviderObjects(ctx, configstoreTables.ProviderObjectKindBatch, provider, vkID, ids)
		if err != nil {
			return fail(configstoreTables.ProviderObjectKindBatch, err)
		}
		if len(hidden) == 0 {
			return nil
		}
		kept := make([]schemas.BifrostBatchRetrieveResponse, 0, len(list.Data))
		for _, item := range list.Data {
			if _, drop := hidden[item.ID]; !drop {
				item.ExtraFields.RawResponse = nil
				kept = append(kept, item)
			}
		}
		list.Data = kept
		list.ExtraFields.RawResponse = nil
	case result.FileListResponse != nil && len(result.FileListResponse.Data) > 0:
		list := result.FileListResponse
		ids := make([]string, 0, len(list.Data))
		for _, item := range list.Data {
			ids = append(ids, item.ID)
		}
		hidden, err := p.hiddenProviderObjects(ctx, configstoreTables.ProviderObjectKindFile, provider, vkID, ids)
		if err != nil {
			return fail(configstoreTables.ProviderObjectKindFile, err)
		}
		if len(hidden) == 0 {
			return nil
		}
		kept := make([]schemas.FileObject, 0, len(list.Data))
		for _, item := range list.Data {
			if _, drop := hidden[item.ID]; !drop {
				kept = append(kept, item)
			}
		}
		list.Data = kept
		list.ExtraFields.RawResponse = nil
	case result.VideoListResponse != nil && len(result.VideoListResponse.Data) > 0:
		list := result.VideoListResponse
		ids := make([]string, 0, len(list.Data)*2)
		for _, item := range list.Data {
			ids = append(ids, videoIDForms(schemas.ModelProvider(provider), item.ID)...)
		}
		hidden, err := p.hiddenProviderObjects(ctx, configstoreTables.ProviderObjectKindVideo, provider, vkID, ids)
		if err != nil {
			return fail(configstoreTables.ProviderObjectKindVideo, err)
		}
		if len(hidden) == 0 {
			return nil
		}
		kept := make([]schemas.VideoObject, 0, len(list.Data))
		for _, item := range list.Data {
			drop := false
			for _, form := range videoIDForms(schemas.ModelProvider(provider), item.ID) {
				if _, ok := hidden[form]; ok {
					drop = true
					break
				}
			}
			if !drop {
				kept = append(kept, item)
			}
		}
		list.Data = kept
		list.ExtraFields.RawResponse = nil
	case result.ContainerListResponse != nil && len(result.ContainerListResponse.Data) > 0:
		list := result.ContainerListResponse
		ids := make([]string, 0, len(list.Data))
		for _, item := range list.Data {
			ids = append(ids, item.ID)
		}
		hidden, err := p.hiddenProviderObjects(ctx, configstoreTables.ProviderObjectKindContainer, provider, vkID, ids)
		if err != nil {
			return fail(configstoreTables.ProviderObjectKindContainer, err)
		}
		if len(hidden) == 0 {
			return nil
		}
		kept := make([]schemas.ContainerObject, 0, len(list.Data))
		for _, item := range list.Data {
			if _, drop := hidden[item.ID]; !drop {
				kept = append(kept, item)
			}
		}
		list.Data = kept
		list.ExtraFields.RawResponse = nil
	case result.CachedContentListResponse != nil && len(result.CachedContentListResponse.CachedContents) > 0:
		list := result.CachedContentListResponse
		ids := make([]string, 0, len(list.CachedContents))
		for _, item := range list.CachedContents {
			ids = append(ids, item.Name)
		}
		hidden, err := p.hiddenProviderObjects(ctx, configstoreTables.ProviderObjectKindCachedContent, provider, vkID, ids)
		if err != nil {
			return fail(configstoreTables.ProviderObjectKindCachedContent, err)
		}
		if len(hidden) == 0 {
			return nil
		}
		kept := make([]schemas.CachedContentObject, 0, len(list.CachedContents))
		for _, item := range list.CachedContents {
			if _, drop := hidden[item.Name]; !drop {
				kept = append(kept, item)
			}
		}
		list.CachedContents = kept
		list.ExtraFields.RawResponse = nil
	}
	return nil
}

// recordProviderObjectLifecycle remembers the virtual key a create response was produced under,
// and forgets an object a delete response confirms gone. A video create is told apart from a
// retrieve, which answers with the same shape, by the request type. A write that fails is logged:
// the object exists either way, and the response carrying its id is the caller's.
func (p *GovernancePlugin) recordProviderObjectLifecycle(ctx *schemas.BifrostContext, requestType schemas.RequestType, provider string, result *schemas.BifrostResponse) {
	if p.configStore == nil || result == nil || provider == "" {
		return
	}
	vkID := bifrost.GetStringFromContext(ctx, schemas.BifrostContextKeyGovernanceVirtualKeyID)
	if vkID == "" {
		return
	}
	var createdKind, createdID string
	var deletedKind string
	var deletedIDs []string
	switch {
	case result.FileUploadResponse != nil && requestType == schemas.FileUploadRequest:
		createdKind, createdID = configstoreTables.ProviderObjectKindFile, result.FileUploadResponse.ID
	case result.VideoGenerationResponse != nil && (requestType == schemas.VideoGenerationRequest || requestType == schemas.VideoEditRequest || requestType == schemas.VideoRemixRequest):
		createdKind, createdID = configstoreTables.ProviderObjectKindVideo, result.VideoGenerationResponse.ID
	case result.ContainerCreateResponse != nil:
		createdKind, createdID = configstoreTables.ProviderObjectKindContainer, result.ContainerCreateResponse.ID
	case result.CachedContentCreateResponse != nil:
		createdKind, createdID = configstoreTables.ProviderObjectKindCachedContent, result.CachedContentCreateResponse.Name
	case result.BatchCreateResponse != nil:
		createdKind, createdID = configstoreTables.ProviderObjectKindBatch, result.BatchCreateResponse.ID
	case result.FileDeleteResponse != nil && result.FileDeleteResponse.Deleted:
		deletedKind, deletedIDs = configstoreTables.ProviderObjectKindFile, []string{result.FileDeleteResponse.ID}
	case result.VideoDeleteResponse != nil && result.VideoDeleteResponse.Deleted:
		deletedKind, deletedIDs = configstoreTables.ProviderObjectKindVideo, videoIDForms(schemas.ModelProvider(provider), result.VideoDeleteResponse.ID)
	case result.ContainerDeleteResponse != nil && result.ContainerDeleteResponse.Deleted:
		deletedKind, deletedIDs = configstoreTables.ProviderObjectKindContainer, []string{result.ContainerDeleteResponse.ID}
	case result.CachedContentDeleteResponse != nil && result.CachedContentDeleteResponse.Deleted:
		deletedKind, deletedIDs = configstoreTables.ProviderObjectKindCachedContent, []string{result.CachedContentDeleteResponse.Name}
	case result.BatchDeleteResponse != nil:
		deletedKind, deletedIDs = configstoreTables.ProviderObjectKindBatch, []string{result.BatchDeleteResponse.ID}
	default:
		return
	}
	if createdKind != "" && createdID != "" {
		if err := p.configStore.UpsertProviderObject(ctx, &configstoreTables.TableProviderObject{
			ID:           configstoreTables.ProviderObjectID(createdKind, provider, createdID),
			Kind:         createdKind,
			Provider:     provider,
			ObjectID:     createdID,
			VirtualKeyID: vkID,
		}); err != nil {
			p.logger.Warn("failed to record the creating virtual key of %s %s on %s: %v", providerObjectLabel(createdKind), createdID, provider, err)
		}
		return
	}
	for _, id := range deletedIDs {
		if id == "" {
			continue
		}
		if err := p.configStore.DeleteProviderObject(ctx, configstoreTables.ProviderObjectID(deletedKind, provider, id)); err != nil {
			p.logger.Warn("failed to forget deleted %s %s on %s: %v", providerObjectLabel(deletedKind), id, provider, err)
		}
	}
}

func derefString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
