package nace

import (
	"strings"
	"time"

	"github.com/bytedance/sonic"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

// ListModels fetches the caller's live catalog and applies normal key filtering.
func (provider *NaceProvider) ListModels(ctx *schemas.BifrostContext, keys []schemas.Key, request *schemas.BifrostListModelsRequest) (*schemas.BifrostListModelsResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.Nace, provider.customProviderConfig, schemas.ListModelsRequest); err != nil {
		return nil, err
	}
	return providerUtils.HandleMultipleListModelsRequests(ctx, keys, request, provider.listModelsByKey)
}

func (provider *NaceProvider) listModelsByKey(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostListModelsRequest) (*schemas.BifrostListModelsResponse, *schemas.BifrostError) {
	pipeline := &providerUtils.ListModelsPipeline{AllowedModels: key.Models, BlacklistedModels: key.BlacklistedModels, Aliases: key.Aliases, Unfiltered: request.Unfiltered, ProviderKey: provider.GetProviderKey(), MatchFns: providerUtils.DefaultMatchFns()}
	response := &schemas.BifrostListModelsResponse{Data: []schemas.Model{}}
	if pipeline.ShouldEarlyExit() {
		return response, nil
	}
	req, resp := fasthttp.AcquireRequest(), fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)
	provider.setHeaders(ctx, req, key)
	req.SetRequestURI(provider.buildRequestURL(ctx, "/v1/models", schemas.ListModelsRequest))
	req.Header.SetMethod("GET")
	latency, err, wait := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
	defer wait()
	if err != nil {
		return nil, err
	}
	headers := providerUtils.ExtractProviderResponseHeaders(resp)
	ctx.SetValue(schemas.BifrostContextKeyProviderResponseHeaders, headers)
	if resp.StatusCode() != fasthttp.StatusOK {
		return nil, providerUtils.SetErrorLatency(parseNaceError(resp), latency)
	}
	body, decodeErr := providerUtils.CheckAndDecodeBody(resp)
	if decodeErr != nil {
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, decodeErr)
	}
	var native NaceListModelsResponse
	if decodeErr := sonic.Unmarshal(body, &native); decodeErr != nil {
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseUnmarshal, decodeErr)
	}
	included := make(map[string]bool, len(native.Models))
	for _, model := range native.Models {
		for _, result := range pipeline.FilterModel(model.Name) {
			entry := ToBifrostModel(model, provider.GetProviderKey())
			entry.ID = string(provider.GetProviderKey()) + "/" + result.ResolvedID
			if result.AliasValue != "" {
				entry.Alias = schemas.Ptr(result.AliasValue)
			}
			response.Data = append(response.Data, entry)
			included[strings.ToLower(result.ResolvedID)] = true
		}
	}
	response.Data = append(response.Data, pipeline.BackfillModels(included)...)
	response.ExtraFields.Latency = latency.Milliseconds()
	response.ExtraFields.ProviderResponseHeaders = headers
	return response, nil
}

// ToBifrostModel attaches native catalog metadata, decision capability and known pricing.
func ToBifrostModel(native NaceModel, provider schemas.ModelProvider) schemas.Model {
	model := native.Name
	if native.AliasFor != nil {
		model = *native.AliasFor
	}
	model = resolveModelAlias(model)
	entry := schemas.Model{ID: string(provider) + "/" + native.Name, Name: schemas.Ptr(native.Name), Description: schemas.Ptr(native.Description), OwnedBy: schemas.Ptr("nace"), SupportedMethods: []string{"decisions"}}
	if date, err := time.Parse("2006-01-02", native.ReleaseDate); err == nil {
		entry.Created = schemas.Ptr(date.Unix())
	}
	entry.IsDeprecated = strings.HasPrefix(native.Description, "Retired on ")
	if extra, err := providerUtils.MarshalSorted(native); err == nil {
		entry.ProviderExtra = extra
	}
	switch model {
	case "drex-v2.0":
		entry.MaxInputTokens = schemas.Ptr(131072)
		entry.Pricing = &schemas.Pricing{Prompt: schemas.Ptr("0.00000006"), Completion: schemas.Ptr("0")}
	case "drex-v1.5":
		entry.MaxInputTokens = schemas.Ptr(131072)
		entry.Pricing = &schemas.Pricing{Prompt: schemas.Ptr("0.00000005"), Completion: schemas.Ptr("0")}
	case "drex-max", "drex-max-v1.1", "drex-max-finance":
		entry.MaxInputTokens = schemas.Ptr(262144)
		entry.Pricing = &schemas.Pricing{Prompt: schemas.Ptr("0.00000003"), Completion: schemas.Ptr("0")}
	}
	if entry.MaxInputTokens != nil {
		entry.ContextLength = schemas.Ptr(*entry.MaxInputTokens)
	}
	modalities := []string{"text"}
	if readsImages(model) {
		modalities = append(modalities, "image")
	}
	entry.Architecture = &schemas.Architecture{InputModalities: modalities, OutputModalities: []string{"text"}}
	return entry
}
