package databricks

import "github.com/maximhq/bifrost/core/providers/typesafe"

// DatabricksDecisionRequest is the ai_decide body: Typesafe's systemone request without
// model, which ai_decide rejects, plus the function options.
type DatabricksDecisionRequest struct {
	State       interface{}                          `json:"state"`
	Questions   map[string]typesafe.TypesafeQuestion `json:"questions"`
	Options     *DatabricksDecisionOptions           `json:"options,omitempty"`
	ExtraParams map[string]interface{}               `json:"-"`
}

// DatabricksDecisionOptions selects the ai_decide function API version.
type DatabricksDecisionOptions struct {
	Version string `json:"version"`
}

// GetExtraParams implements providerUtils.RequestBodyWithExtraParams.
func (r *DatabricksDecisionRequest) GetExtraParams() map[string]interface{} {
	return r.ExtraParams
}

// DatabricksDecisionResponse is the ai_decide envelope around the answers. The body names
// no model and reports no usage.
type DatabricksDecisionResponse struct {
	Response *DatabricksDecisionResult `json:"response"`
}

// DatabricksDecisionResult carries ai_decide's answers, keyed by question ID.
type DatabricksDecisionResult struct {
	Answers map[string]DatabricksDecisionAnswer `json:"answers"`
}

// DatabricksDecisionAnswer is a Typesafe answer whose noul value arrives as probability.
type DatabricksDecisionAnswer struct {
	typesafe.TypesafeAnswer
	Probability *float64 `json:"probability,omitempty"`
}
