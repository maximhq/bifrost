package datasheet

import (
	"strconv"

	"github.com/maximhq/bifrost/core/schemas"
)

// VideoPricingDimensions is every factor that can move the price of one video job.
//
// It exists because a video is billed long after it is asked for. The request says
// what was ordered, the terminal response says what was actually produced, and
// neither alone is enough: most providers' retrieve response reports little beyond
// "done", so a dimension not captured at submission is usually unrecoverable by the
// time settlement runs.
//
// Adding a dimension is deliberately cheap. The whole struct is persisted as JSON
// in a single TEXT column, so a new field needs no migration and no capture-side
// change beyond setting it — which is the point: the provider rate cards are not
// fully mapped yet, and a factor worth capturing should not have to wait for one.
// Fields that no rate reads yet are still worth recording.
type VideoPricingDimensions struct {
	// Model is the resolved model, which matters because several providers never
	// echo it back on retrieve.
	Model string `json:"model,omitempty"`
	// RequestType distinguishes generation from remix and edit. They can price on
	// different bases even for the same model.
	RequestType schemas.RequestType `json:"request_type,omitempty"`

	// Seconds and Size are the two dimensions every published video rate card uses
	// today. Size is "WxH"; the rate is banded on its short edge.
	Seconds *int   `json:"seconds,omitempty"`
	Size    string `json:"size,omitempty"`

	// Type is the operation selector ("upscale", "3d"). These do not bill per
	// second of output at all, so a rate keyed only on duration is wrong for them.
	Type *string `json:"type,omitempty"`
	// UpscaleFactor and TargetMegapixels are the upscale operation's own basis;
	// they are mutually exclusive.
	UpscaleFactor    *int `json:"upscale_factor,omitempty"`
	TargetMegapixels *int `json:"target_megapixels,omitempty"`

	// Audio: Veo publishes distinct with- and without-audio rates.
	Audio *bool `json:"audio,omitempty"`

	// OutputCount is how many clips the job actually returned. It is 0 until the
	// job produces them, and a job that has not finished still owes one clip's
	// worth rather than nothing — see computeVideoCost.
	OutputCount int `json:"output_count,omitempty"`

	// ProviderCost is the provider's own figure for the job, when it reports one
	// (Runware does). It wins over every rate below.
	ProviderCost *float64 `json:"provider_cost,omitempty"`

	// Extra carries request knobs that are not modeled above yet. It is the
	// research hatch: capture a provider-specific parameter here as soon as it
	// looks price-relevant, and promote it to a typed field once its rate is
	// actually published. Nothing reads it during pricing today.
	Extra map[string]any `json:"extra,omitempty"`
}

// MergedWith layers observed dimensions over captured ones: whatever the argument
// actually knows wins, and the receiver fills every remaining gap.
//
// The direction matters at settlement. The request is a statement of intent — a
// provider may clamp a duration, downscale a resolution, or return fewer clips than
// asked for — so the terminal response is authoritative wherever it says anything
// at all, and the submitted request is the fallback for everything it stays silent
// about (which, for most providers, is nearly everything).
func (d VideoPricingDimensions) MergedWith(observed VideoPricingDimensions) VideoPricingDimensions {
	out := d
	if observed.Model != "" {
		out.Model = observed.Model
	}
	if observed.RequestType != "" {
		out.RequestType = observed.RequestType
	}
	if observed.Seconds != nil {
		out.Seconds = observed.Seconds
	}
	if observed.Size != "" {
		out.Size = observed.Size
	}
	if observed.Type != nil {
		out.Type = observed.Type
	}
	if observed.UpscaleFactor != nil {
		out.UpscaleFactor = observed.UpscaleFactor
	}
	if observed.TargetMegapixels != nil {
		out.TargetMegapixels = observed.TargetMegapixels
	}
	if observed.Audio != nil {
		out.Audio = observed.Audio
	}
	// A finished job reporting zero clips is a job that produced nothing, not a job
	// we lack a count for — but only the response can say that, so it only ever
	// overwrites upward from the request, which never carries a count.
	if observed.OutputCount > 0 {
		out.OutputCount = observed.OutputCount
	}
	if observed.ProviderCost != nil {
		out.ProviderCost = observed.ProviderCost
	}
	for key, value := range observed.Extra {
		if out.Extra == nil {
			out.Extra = map[string]any{}
		}
		out.Extra[key] = value
	}
	return out
}

// VideoCostDetails is the outcome of pricing one video job. Priced separates "this
// costs nothing" from "no rate could be found", which the settlement engine treats
// as entirely different states.
type VideoCostDetails struct {
	Cost             float64
	Priced           bool
	ProviderCostUsed bool
	Breakdown        *schemas.BifrostCost
}

// CalculateVideoCostDetails prices a video job from its merged dimensions.
//
// Resolution order is provider-reported cost, then the catalog rate, then nothing.
// A provider that hands back an exact figure is always right about its own bill, so
// no estimate is allowed to override it.
func (s *Store) CalculateVideoCostDetails(dims VideoPricingDimensions, provider schemas.ModelProvider, scopes *LookupScopes) VideoCostDetails {
	if dims.ProviderCost != nil && *dims.ProviderCost > 0 {
		return VideoCostDetails{
			Cost:             *dims.ProviderCost,
			Priced:           true,
			ProviderCostUsed: true,
			Breakdown:        &schemas.BifrostCost{TotalCost: *dims.ProviderCost},
		}
	}

	requestType := dims.RequestType
	if requestType == "" {
		requestType = schemas.VideoGenerationRequest
	}

	var lookupScopes LookupScopes
	if scopes != nil {
		lookupScopes = *scopes
	}
	pricing := s.resolvePricing(schemas.RoutingInfo{Provider: provider, Model: dims.Model}, requestType, lookupScopes)
	if pricing == nil {
		return VideoCostDetails{}
	}

	// Duration is the only basis wired today. Operation-selector jobs (upscale, 3d)
	// bill on factor or megapixels instead, and no rate for those is published to
	// the catalog yet — so they resolve to unpriced rather than being silently
	// billed as if they were a per-second generation of the same length.
	breakdown := computeVideoCost(pricing, nil, dims.Seconds, dims.Size, dims.OutputCount, serviceTier{})
	if breakdown == nil || breakdown.TotalCost <= 0 {
		return VideoCostDetails{}
	}
	return VideoCostDetails{Cost: breakdown.TotalCost, Priced: true, Breakdown: breakdown}
}

// VideoDimensionsFromResponse reads back everything a terminal video response
// actually reports. What that amounts to varies enormously by provider — OpenAI
// echoes model, duration and size; several others report only a status and a URL —
// which is precisely why the submitted request is kept as the fallback.
func VideoDimensionsFromResponse(resp *schemas.BifrostVideoGenerationResponse) VideoPricingDimensions {
	if resp == nil {
		return VideoPricingDimensions{}
	}
	dims := VideoPricingDimensions{
		Model:       resp.Model,
		Size:        resp.Size,
		OutputCount: len(resp.Videos),
	}
	if resp.Seconds != nil {
		if seconds, err := strconv.Atoi(*resp.Seconds); err == nil {
			dims.Seconds = &seconds
		}
	}
	if resp.Usage != nil && resp.Usage.Cost != nil && resp.Usage.Cost.TotalCost > 0 {
		total := resp.Usage.Cost.TotalCost
		dims.ProviderCost = &total
	}
	return dims
}
