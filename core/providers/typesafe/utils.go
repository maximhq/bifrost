package typesafe

import (
	"strings"

	"github.com/bytedance/sonic"

	schemas "github.com/maximhq/bifrost/core/schemas"
)

// typesafeDefaultBaseURL is the default Typesafe API host.
const typesafeDefaultBaseURL = "https://api.typesafe.ai"

// typesafeSystemOnePath is the single evaluation endpoint for the System One
// model class; every jev model is served by it, selected via the request's
// model field.
const typesafeSystemOnePath = "/v1/systemone"

// typesafeModelsPath is the native model-listing endpoint.
const typesafeModelsPath = "/v1/models"

// typesafeModel is one entry of the static model catalog.
type typesafeModel struct {
	ID          string
	Name        string
	Description string
	ReleaseDate string
}

// typesafeModels is the pinned jev catalog: merged into the live listing
// (which carries the aliases only) and served alone when the live call fails
// against the default endpoint. Mirrored in the hosted datasheet. Aliases
// resolve upstream: jev-latest and jev-preview both point at jev-1.13.0 today.
var typesafeModels = []typesafeModel{
	{
		ID:          "jev-1.13.0",
		Name:        "Jev 1.13.0",
		Description: "TypeSafe's System One Model: Jev, version 1.13.0",
		// The SDKs validate release_date on every listed model; 1.13.0 shipped
		// at the same instant its aliases were minted.
		ReleaseDate: "2026-09-10T18:38:01.391457+00:00",
	},
	{
		ID:          "jev-latest",
		Name:        "Jev (latest)",
		Description: "The latest iteration of TypeSafe's System One Model: Jev",
		ReleaseDate: "2026-09-10T18:38:01.391457+00:00",
	},
	{
		ID:          "jev-preview",
		Name:        "Jev (preview)",
		Description: "A preview version of `jev-latest`: should be better in most ways",
		ReleaseDate: "2026-09-10T18:39:06.057655+00:00",
	},
}

// TypesafeNativeModel is one entry of the native model-listing shape.
type TypesafeNativeModel struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	ReleaseDate string `json:"release_date,omitempty"`
}

// TypesafeNativeListModelsResponse is the native model-listing shape of
// GET /v1/models, served on /typesafe/v1/models.
type TypesafeNativeListModelsResponse struct {
	Models []TypesafeNativeModel `json:"models"`
}

// ToTypesafeNativeListModelsResponse converts a Bifrost model listing into the
// native shape, restoring bare upstream model names. Description and
// release_date come from the upstream entry the provider carried on
// ProviderExtra, falling back to the pinned catalog.
func ToTypesafeNativeListModelsResponse(resp *schemas.BifrostListModelsResponse) *TypesafeNativeListModelsResponse {
	native := &TypesafeNativeListModelsResponse{Models: []TypesafeNativeModel{}}
	if resp == nil {
		return native
	}
	catalog := make(map[string]typesafeModel, len(typesafeModels))
	for _, model := range typesafeModels {
		catalog[model.ID] = model
	}
	prefix := string(schemas.Typesafe) + "/"
	for _, model := range resp.Data {
		name := strings.TrimPrefix(model.ID, prefix)
		entry := TypesafeNativeModel{Name: name}
		var upstream TypesafeNativeModel
		if len(model.ProviderExtra) > 0 && sonic.Unmarshal(model.ProviderExtra, &upstream) == nil {
			entry.Description = upstream.Description
			entry.ReleaseDate = upstream.ReleaseDate
		} else if known, ok := catalog[name]; ok {
			entry.Description = known.Description
			entry.ReleaseDate = known.ReleaseDate
		}
		native.Models = append(native.Models, entry)
	}
	return native
}
