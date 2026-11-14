// This file contains the endpoint that classifies statements with a model
// router, so its configuration can be checked against a dataset.

package handlers

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/maximhq/bifrost/plugins/routing"
	"github.com/maximhq/bifrost/plugins/routing/complexity"
	"github.com/valyala/fasthttp"
)

// ModelRouterClassifier classifies statements with a served model router. The
// routing manager implements it when the routing plugin is loaded; the
// endpoint answers 503 otherwise.
type ModelRouterClassifier interface {
	ClassifyWithModelRouter(ctx context.Context, routerID string, inputs []routing.ModelRouterClassifyInput) (routing.ModelRouterClassifyResult, error)
}

// unclassifiedTier is the summary key for statements that got no tier.
const unclassifiedTier = "UNCLASSIFIED"

// modelRouterClassifyRequest is a page of statements to classify.
type modelRouterClassifyRequest struct {
	Inputs []modelRouterClassifyInputRequest `json:"inputs"`
}

// modelRouterClassifyInputRequest is one statement, optionally labelled with
// the tier it should get and an id the caller tracks it by.
type modelRouterClassifyInputRequest struct {
	ID                   string   `json:"id,omitempty"`
	Text                 string   `json:"text"`
	PreviousUserMessages []string `json:"previous_user_messages,omitempty"`
	ExpectedTier         string   `json:"expected_tier,omitempty"`
}

// modelRouterClassifyResponse is the router's answer for every statement, in
// input order, and a summary across them.
type modelRouterClassifyResponse struct {
	RouterID   string                          `json:"router_id"`
	RouterName string                          `json:"router_name"`
	Classifier string                          `json:"classifier"`
	Results    []modelRouterClassifyResultView `json:"results"`
	Summary    modelRouterClassifySummary      `json:"summary"`
}

// modelRouterClassifyResultView is one statement's classification with the
// caller's id, text and label echoed back.
type modelRouterClassifyResultView struct {
	Index        int    `json:"index"`
	ID           string `json:"id,omitempty"`
	Text         string `json:"text"`
	ExpectedTier string `json:"expected_tier,omitempty"`
	Correct      *bool  `json:"correct,omitempty"` // set only for a labelled statement
	routing.ModelRouterClassification
}

// modelRouterClassifySummary counts tiers across the page and, for labelled
// statements, how many got the expected tier. Confusion is keyed by expected
// tier, then by the tier given; UNCLASSIFIED stands for no tier.
type modelRouterClassifySummary struct {
	Total     int                       `json:"total"`
	Tiers     map[string]int            `json:"tiers"`
	Labeled   int                       `json:"labeled"`
	Correct   int                       `json:"correct"`
	Accuracy  *float64                  `json:"accuracy,omitempty"`
	Confusion map[string]map[string]int `json:"confusion,omitempty"`
}

// classifyWithModelRouter classifies a page of statements with one router as
// it is served now. Nothing is saved, no session is read or moved, and the
// statements are not captured for phrase suggestions. A decision-model or LLM
// router makes one model call per statement.
func (h *RoutingHandler) classifyWithModelRouter(ctx *fasthttp.RequestCtx) {
	classifier, ok := h.routingManager.(ModelRouterClassifier)
	if !ok {
		SendError(ctx, fasthttp.StatusServiceUnavailable, "model router classification is not available on this deployment")
		return
	}
	id := pathID(ctx, "router_id")
	var req modelRouterClassifyRequest
	if !decodeStrictJSON(ctx, &req) {
		return
	}
	inputs := make([]routing.ModelRouterClassifyInput, len(req.Inputs))
	for i, in := range req.Inputs {
		expected := strings.ToUpper(strings.TrimSpace(in.ExpectedTier))
		if expected != "" && expected != complexity.TierSimple && expected != complexity.TierMedium && expected != complexity.TierComplex {
			SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("input %d: expected_tier must be SIMPLE, MEDIUM, or COMPLEX", i))
			return
		}
		req.Inputs[i].ExpectedTier = expected
		inputs[i] = routing.ModelRouterClassifyInput{Text: in.Text, PreviousUserMessages: in.PreviousUserMessages}
	}
	result, err := classifier.ClassifyWithModelRouter(ctx, id, inputs)
	switch {
	case errors.Is(err, routing.ErrUnknownComplexityRouter):
		h.sendModelRouterNotServed(ctx, id)
		return
	case errors.Is(err, routing.ErrInvalidModelRouterClassify):
		SendError(ctx, fasthttp.StatusBadRequest, err.Error())
		return
	case err != nil:
		SendError(ctx, fasthttp.StatusServiceUnavailable, fmt.Sprintf("failed to classify with model router: %v", err))
		return
	}
	SendJSON(ctx, buildModelRouterClassifyResponse(req.Inputs, result))
}

// buildModelRouterClassifyResponse pairs each classification with its input
// and summarises the page.
func buildModelRouterClassifyResponse(inputs []modelRouterClassifyInputRequest, result routing.ModelRouterClassifyResult) modelRouterClassifyResponse {
	summary := modelRouterClassifySummary{
		Total: len(result.Results),
		Tiers: map[string]int{complexity.TierSimple: 0, complexity.TierMedium: 0, complexity.TierComplex: 0, unclassifiedTier: 0},
	}
	views := make([]modelRouterClassifyResultView, len(result.Results))
	for i, classification := range result.Results {
		in := inputs[i]
		given := classification.Tier
		if given == "" {
			given = unclassifiedTier
		}
		summary.Tiers[given]++
		view := modelRouterClassifyResultView{
			Index:                     i,
			ID:                        in.ID,
			Text:                      in.Text,
			ExpectedTier:              in.ExpectedTier,
			ModelRouterClassification: classification,
		}
		if in.ExpectedTier != "" {
			correct := classification.Tier == in.ExpectedTier
			view.Correct = &correct
			summary.Labeled++
			if correct {
				summary.Correct++
			}
			if summary.Confusion == nil {
				summary.Confusion = map[string]map[string]int{}
			}
			if summary.Confusion[in.ExpectedTier] == nil {
				summary.Confusion[in.ExpectedTier] = map[string]int{}
			}
			summary.Confusion[in.ExpectedTier][given]++
		}
		views[i] = view
	}
	if summary.Labeled > 0 {
		accuracy := float64(summary.Correct) / float64(summary.Labeled)
		summary.Accuracy = &accuracy
	}
	return modelRouterClassifyResponse{
		RouterID:   result.RouterID,
		RouterName: result.RouterName,
		Classifier: result.Classifier,
		Results:    views,
		Summary:    summary,
	}
}
