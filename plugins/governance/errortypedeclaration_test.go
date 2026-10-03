package governance

import (
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

// Every Decision that refuses a request. Hand-kept: Go cannot enumerate constants.
// A missed entry surfaces at runtime as schemas.ErrorTypeOther, which is alarmed on.
var allRefusalDecisions = []Decision{
	DecisionAccessNotFound,
	DecisionAccessBlocked,
	DecisionAccessUnresolved,
	DecisionModelBlocked,
	DecisionProviderBlocked,
	DecisionRateLimited,
	DecisionTokenLimited,
	DecisionRequestLimited,
	DecisionBudgetExceeded,
	DecisionMCPToolBlocked,
}

// The status code cannot tell these apart: three decisions answer 403, and our 429
// differs from an upstream's.
func TestEveryRefusalDeclaresAnErrorType(t *testing.T) {
	for _, decision := range allRefusalDecisions {
		t.Run(string(decision), func(t *testing.T) {
			// decide is nil-safe on ctx and touches no plugin state when refusing.
			_, bifrostErr := (&GovernancePlugin{}).decide(nil, &EvaluationResult{
				Decision: decision,
				Reason:   "refused for test",
			})
			if bifrostErr == nil {
				t.Fatalf("decision %q must refuse the request with an error", decision)
			}
			if bifrostErr.ExtraFields.ErrorType == "" {
				t.Fatalf("decision %q declared no ErrorType; it will be counted as %q",
					decision, schemas.ErrorTypeOther)
			}
			got := schemas.ClassifyErrorType(bifrostErr, schemas.ChatCompletionRequest)
			if got == schemas.ErrorTypeOther {
				t.Errorf("decision %q classified as %q", decision, schemas.ErrorTypeOther)
			}
			if got != bifrostErr.ExtraFields.ErrorType {
				t.Errorf("declared %q but classified as %q; the declaration must win",
					bifrostErr.ExtraFields.ErrorType, got)
			}
		})
	}
}

// Not merely non-empty: a copy-paste declaring everything policy_access_denied would
// pass the test above while making the metric useless.
func TestRefusalsDeclareTheIntendedErrorType(t *testing.T) {
	for decision, want := range map[Decision]schemas.ErrorType{
		DecisionModelBlocked:    schemas.ErrorTypePolicyModelBlocked,
		DecisionProviderBlocked: schemas.ErrorTypePolicyProviderBlocked,
		DecisionAccessNotFound:  schemas.ErrorTypePolicyAccessDenied,
		DecisionAccessBlocked:   schemas.ErrorTypePolicyAccessDenied,
		DecisionBudgetExceeded:  schemas.ErrorTypePolicyBudgetExceeded,
		DecisionRateLimited:     schemas.ErrorTypePolicyRateLimited,
		DecisionTokenLimited:    schemas.ErrorTypePolicyRateLimited,
		DecisionRequestLimited:  schemas.ErrorTypePolicyRateLimited,
		DecisionMCPToolBlocked:  schemas.ErrorTypePolicyToolBlocked,
		// Not a policy refusal: the deployment is misassembled, not the caller.
		DecisionAccessUnresolved: schemas.ErrorTypeBifrostInternal,
	} {
		_, bifrostErr := (&GovernancePlugin{}).decide(nil, &EvaluationResult{Decision: decision})
		if bifrostErr == nil {
			t.Fatalf("decision %q must refuse the request with an error", decision)
		}
		if got := bifrostErr.ExtraFields.ErrorType; got != want {
			t.Errorf("decision %q declared %q, want %q", decision, got, want)
		}
	}
}

// Type still carries the raw decision, so clients matching on it keep working.
func TestRefusalStillCarriesRawDecisionInType(t *testing.T) {
	for _, decision := range allRefusalDecisions {
		_, bifrostErr := (&GovernancePlugin{}).decide(nil, &EvaluationResult{Decision: decision})
		if bifrostErr == nil || bifrostErr.Type == nil {
			t.Fatalf("decision %q lost its wire-level Type", decision)
		}
		if *bifrostErr.Type != string(decision) {
			t.Errorf("decision %q surfaced as Type %q", decision, *bifrostErr.Type)
		}
	}
}
