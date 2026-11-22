package requestguard

import (
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/modelcatalog"
)

func TestEstimateTokensCountsResponsesToolOutput(t *testing.T) {
	output := strings.Repeat("large tool result ", 200)
	blockText := strings.Repeat("block text ", 50)
	fileData := strings.Repeat("encoded-file-data", 40)
	fileURL := "https://example.test/tool-output.pdf"
	filename := "tool-output.pdf"
	req := &schemas.BifrostRequest{
		RequestType: schemas.ResponsesRequest,
		ResponsesRequest: &schemas.BifrostResponsesRequest{
			Provider: schemas.OpenAI,
			Model:    "model",
			Input: []schemas.ResponsesMessage{{
				ResponsesToolMessage: &schemas.ResponsesToolMessage{
					Output: &schemas.ResponsesToolMessageOutputStruct{
						ResponsesToolCallOutputStr: &output,
						ResponsesFunctionToolCallOutputBlocks: []schemas.ResponsesMessageContentBlock{
							{Type: schemas.ResponsesOutputMessageContentTypeText, Text: &blockText},
							{Type: schemas.ResponsesInputMessageContentBlockTypeFile, ResponsesInputMessageContentBlockFile: &schemas.ResponsesInputMessageContentBlockFile{
								FileData: &fileData, FileURL: &fileURL, Filename: &filename,
							}},
						},
					},
				},
			}},
		},
	}
	features := ExtractRequestFeatures(req)
	cfg := DefaultConfig().BudgetGuard
	cfg.CharsPerToken = 1
	cfg.SafetyMarginMultiplier = 1
	cfg.DefaultOutputTokens = 0
	estimate, _ := EstimateTokens(requestGuardTestContext(), req, features, cfg, nil)
	wantMinimum := len(output) + len(blockText) + len(fileData) + len(fileURL) + len(filename)
	if estimate.InputTokens < wantMinimum {
		t.Fatalf("estimated input tokens = %d, want at least tool output text/file length %d", estimate.InputTokens, wantMinimum)
	}
}

func TestBuildPricingScopesReadsUserFromGrantIdentity(t *testing.T) {
	ctx := requestGuardTestContextForIdentity("pricing-user", "pricing-key", "pricing-customer")
	base := requestGuardTestCatalog()
	catalog := &pricingScopesCaptureCatalog{fakeCatalog: base}
	req := requestGuardTestRequest(schemas.ChatCompletionRequest, schemas.OpenAI, "expensive", false)
	features := ExtractRequestFeatures(req)
	_, _ = EstimateTokens(ctx, req, features, DefaultConfig().BudgetGuard, catalog)
	if catalog.scopes == nil || catalog.scopes.UserID != "pricing-user" {
		t.Fatalf("pricing scopes = %+v, want Grant user id rather than customer id", catalog.scopes)
	}
	if catalog.scopes.UserID == "pricing-customer" {
		t.Fatal("customer identifier was incorrectly used as the user-scoped pricing key")
	}
}

type pricingScopesCaptureCatalog struct {
	*fakeCatalog
	scopes *modelcatalog.PricingLookupScopes
}

func (c *pricingScopesCaptureCatalog) CalculateCostForUsage(usage *schemas.BifrostLLMUsage, provider schemas.ModelProvider, model string, requestType schemas.RequestType, scopes *modelcatalog.PricingLookupScopes) float64 {
	c.scopes = scopes
	return c.fakeCatalog.CalculateCostForUsage(usage, provider, model, requestType, scopes)
}
