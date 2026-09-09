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
