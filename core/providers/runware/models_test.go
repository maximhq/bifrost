package runware

import (
	"testing"

	schemas "github.com/maximhq/bifrost/core/schemas"
)

func catalogModels() []RunwareModel {
	return []RunwareModel{
		{
			AIR:          "xai:grok-imagine@image-2.0",
			Name:         "Grok Imagine Image 2.0",
			Category:     "checkpoint",
			Architecture: "grok_imagine_image_2_0",
			Capabilities: []string{"io:text-to-image", "io:image-to-image", "form:checkpoint"},
			Comment:      "Next-generation Grok image generation and editing",
			Creator:      &RunwareModelCreator{ID: "xai", Name: "xAI"},
		},
		{
			AIR:                "tripo:v3.1@0",
			Name:               "Tripo 3.1",
			Category:           "others",
			Capabilities:       []string{"io:image-to-3d", "io:text-to-3d"},
			AddedUnixTimestamp: 1778112000,
		},
		{AIR: ""}, // entries with no AIR carry no usable identifier and are skipped
	}
}

// The AIR is the identifier every inference task keys off, so it becomes the Bifrost model ID,
// provider-prefixed like every other provider's listing.
func TestToBifrostListModelsResponse(t *testing.T) {
	out := ToBifrostListModelsResponse(catalogModels(), schemas.Runware, schemas.WhiteList{"*"}, nil, nil, false)

	if len(out.Data) != 2 {
		t.Fatalf("expected 2 models (the AIR-less entry skipped), got %d", len(out.Data))
	}

	grok := out.Data[0]
	if grok.ID != "runware/xai:grok-imagine@image-2.0" {
		t.Fatalf("id = %q, want the provider-prefixed AIR", grok.ID)
	}
	if grok.Name == nil || *grok.Name != "Grok Imagine Image 2.0" {
		t.Fatalf("name not carried: %v", grok.Name)
	}
	if grok.OwnedBy == nil || *grok.OwnedBy != "xAI" {
		t.Fatalf("owned_by should come from the creator name, got %v", grok.OwnedBy)
	}
	if grok.Description == nil || *grok.Description == "" {
		t.Fatalf("description not carried from comment")
	}
	// io: tags are the only modality signal Runware gives, so they drive the architecture block.
	if grok.Architecture == nil {
		t.Fatalf("expected architecture derived from io: capabilities")
	}
	if got := grok.Architecture.InputModalities; len(got) != 2 || got[0] != "image" || got[1] != "text" {
		t.Fatalf("input modalities = %v, want [image text]", got)
	}
	if got := grok.Architecture.OutputModalities; len(got) != 1 || got[0] != "image" {
		t.Fatalf("output modalities = %v, want [image]", got)
	}
	if grok.Architecture.Tokenizer == nil || *grok.Architecture.Tokenizer != "grok_imagine_image_2_0" {
		t.Fatalf("architecture name not carried: %v", grok.Architecture.Tokenizer)
	}

	tripo := out.Data[1]
	if tripo.Created == nil || *tripo.Created != 1778112000 {
		t.Fatalf("created not carried: %v", tripo.Created)
	}
	if got := tripo.Architecture.OutputModalities; len(got) != 1 || got[0] != "3d" {
		t.Fatalf("3d output modality = %v", got)
	}
}

// A key's allowlist scopes the listing the same way it does for every other provider.
func TestToBifrostListModelsResponse_RespectsKeyAllowlist(t *testing.T) {
	out := ToBifrostListModelsResponse(catalogModels(), schemas.Runware, schemas.WhiteList{"tripo:v3.1@0"}, nil, nil, false)
	if len(out.Data) != 1 || out.Data[0].ID != "runware/tripo:v3.1@0" {
		t.Fatalf("allowlist not applied, got %+v", out.Data)
	}
}

// The two Runware catalogues name the same model differently, so the slug bridge is what makes the
// /v1/models price and context limits reachable from a modelSearch entry.
func TestAirToModelSlug(t *testing.T) {
	cases := map[string]string{
		"google:gemini@3.1-pro":                "google-gemini-3-1-pro",
		"minimax:m2.7@0":                       "minimax-m2-7", // version 0 is omitted by /v1/models
		"anthropic:claude@haiku-4.5":           "anthropic-claude-haiku-4-5",
		"deepseek:v4.1@flash":                  "deepseek-v4-1-flash",
		"xai:grok-imagine@image-2.0":           "xai-grok-imagine-image-2-0",
		"runware:llama-3-1-8b@prompt-enhancer": "runware-llama-3-1-8b-prompt-enhancer",
	}
	for air, want := range cases {
		if got := airToModelSlug(air); got != want {
			t.Errorf("airToModelSlug(%q) = %q, want %q", air, got, want)
		}
	}
}

// /v1/models is the only Runware source for per-token price and context limits: the modelSearch
// catalog carries neither. Both must land on the model whose AIR slugs to the offer's id.
func TestToBifrostListModelsResponseWithOffers(t *testing.T) {
	models := []RunwareModel{{AIR: "google:gemini@3.1-pro", Name: "Gemini 3.1 Pro", Category: "text"}}
	offers := map[string]RunwareModelEnvelope{
		"google-gemini-3-1-pro": {
			ID:              "google-gemini-3-1-pro",
			ContextLength:   schemas.Ptr(1048576),
			MaxOutputTokens: schemas.Ptr(65536),
			Pricing: &RunwarePricing{
				Prompt:         schemas.Ptr("0.000002"),
				Completion:     schemas.Ptr("0.000012"),
				InputCacheRead: schemas.Ptr("0.0000002"),
			},
		},
	}

	out := ToBifrostListModelsResponseWithOffers(models, offers, schemas.Runware, schemas.WhiteList{"*"}, nil, nil, false)
	if len(out.Data) != 1 {
		t.Fatalf("expected 1 model, got %d", len(out.Data))
	}

	model := out.Data[0]
	if model.ContextLength == nil || *model.ContextLength != 1048576 {
		t.Fatalf("context_length not carried: %v", model.ContextLength)
	}
	if model.MaxOutputTokens == nil || *model.MaxOutputTokens != 65536 {
		t.Fatalf("max_output_tokens not carried: %v", model.MaxOutputTokens)
	}
	if model.Pricing == nil {
		t.Fatal("pricing not carried: /v1/models is the only source for it")
	}
	if model.Pricing.Prompt == nil || *model.Pricing.Prompt != "0.000002" {
		t.Fatalf("prompt price = %v, want 0.000002", model.Pricing.Prompt)
	}
	if model.Pricing.Completion == nil || *model.Pricing.Completion != "0.000012" {
		t.Fatalf("completion price = %v, want 0.000012", model.Pricing.Completion)
	}
	if model.Pricing.InputCacheRead == nil || *model.Pricing.InputCacheRead != "0.0000002" {
		t.Fatalf("cache read price = %v, want 0.0000002", model.Pricing.InputCacheRead)
	}
}

// Models outside /v1/models (the image, video and audio side of the catalog) list without the
// extras rather than failing.
func TestToBifrostListModelsResponseWithOffers_MissingOffer(t *testing.T) {
	models := []RunwareModel{{AIR: "runware:101@1", Name: "FLUX.1 Dev", Category: "checkpoint"}}
	offers := map[string]RunwareModelEnvelope{
		"google-gemini-3-1-pro": {ID: "google-gemini-3-1-pro", ContextLength: schemas.Ptr(1048576)},
	}

	out := ToBifrostListModelsResponseWithOffers(models, offers, schemas.Runware, schemas.WhiteList{"*"}, nil, nil, false)
	if len(out.Data) != 1 {
		t.Fatalf("expected the model to survive a missing offer, got %d entries", len(out.Data))
	}
	if out.Data[0].ContextLength != nil || out.Data[0].Pricing != nil {
		t.Fatalf("expected no offer fields on an unmatched model, got %+v", out.Data[0])
	}
}

// An offer whose price block is entirely empty must not attach an empty Pricing: modelcatalog
// treats a non-nil Pricing as authoritative and would stop filling it from the datasheet.
func TestApplyRunwareOffer_EmptyPricingIgnored(t *testing.T) {
	model := &schemas.Model{}
	applyRunwareOffer(model, RunwareModelEnvelope{ID: "x", Pricing: &RunwarePricing{}})

	if model.Pricing != nil {
		t.Fatalf("empty pricing block should not attach, got %+v", model.Pricing)
	}
	if model.Architecture != nil {
		t.Fatalf("no modalities on the offer should not create an architecture, got %+v", model.Architecture)
	}
}

// A catalog entry with no io: capability carries no modalities of its own, so the offer's are the
// only ones available and must land on the entry.
func TestApplyRunwareOffer_FillsMissingModalities(t *testing.T) {
	// runwareModelArchitecture returns a non-nil Architecture with nil slices for an entry that
	// declares an architecture string but no io: tags.
	model := &schemas.Model{Architecture: &schemas.Architecture{Tokenizer: schemas.Ptr("flux_dev")}}
	applyRunwareOffer(model, RunwareModelEnvelope{
		InputModalities:  []string{"text", "image"},
		OutputModalities: []string{"image"},
	})

	if got := model.Architecture.InputModalities; len(got) != 2 || got[0] != "text" || got[1] != "image" {
		t.Fatalf("input modalities = %v, want [text image]", got)
	}
	if got := model.Architecture.OutputModalities; len(got) != 1 || got[0] != "image" {
		t.Fatalf("output modalities = %v, want [image]", got)
	}
	// The architecture the catalog already gave must survive.
	if model.Architecture.Tokenizer == nil || *model.Architecture.Tokenizer != "flux_dev" {
		t.Fatalf("existing architecture fields were dropped: %+v", model.Architecture)
	}
}

// Modalities the catalog derived from io: tags win; the offer only fills what is missing.
func TestApplyRunwareOffer_KeepsCatalogModalities(t *testing.T) {
	model := &schemas.Model{Architecture: &schemas.Architecture{
		InputModalities:  []string{"text"},
		OutputModalities: []string{"text"},
	}}
	applyRunwareOffer(model, RunwareModelEnvelope{
		InputModalities:  []string{"text", "image", "video"},
		OutputModalities: []string{"text", "image"},
	})

	if got := model.Architecture.InputModalities; len(got) != 1 || got[0] != "text" {
		t.Fatalf("catalog input modalities overwritten: %v", got)
	}
	if got := model.Architecture.OutputModalities; len(got) != 1 || got[0] != "text" {
		t.Fatalf("catalog output modalities overwritten: %v", got)
	}
}

// The offer's slices are cloned, not aliased: a caller reusing the offers map must not be able to
// mutate a listed model through it.
func TestApplyRunwareOffer_ClonesModalities(t *testing.T) {
	offer := RunwareModelEnvelope{InputModalities: []string{"text"}, OutputModalities: []string{"text"}}
	model := &schemas.Model{}
	applyRunwareOffer(model, offer)

	offer.InputModalities[0] = "mutated"
	if model.Architecture.InputModalities[0] != "text" {
		t.Fatalf("input modalities alias the offer slice: %v", model.Architecture.InputModalities)
	}
}

// A datasheet-filled field is never overwritten by the live offer.
func TestApplyRunwareOffer_DoesNotOverwriteExisting(t *testing.T) {
	model := &schemas.Model{
		ContextLength: schemas.Ptr(4096),
		Pricing:       &schemas.Pricing{Prompt: schemas.Ptr("0.000001")},
	}
	applyRunwareOffer(model, RunwareModelEnvelope{
		ContextLength: schemas.Ptr(999999),
		Pricing:       &RunwarePricing{Prompt: schemas.Ptr("9.9")},
	})

	if *model.ContextLength != 4096 {
		t.Fatalf("context_length overwritten: %d", *model.ContextLength)
	}
	if *model.Pricing.Prompt != "0.000001" {
		t.Fatalf("pricing overwritten: %s", *model.Pricing.Prompt)
	}
}
