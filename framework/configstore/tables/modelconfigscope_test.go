package tables

import "testing"

// TestDeclaredModelConfigScopesAreValidByDefault pins every scope this package
// declares as writable without a registration call.
//
// "user" previously was not: it was declared here and enforced by the governance
// plugin, but left out of the seed, so it only became writable once the governance
// plugin's Init called RegisterModelConfigScope. Anything writing a user-scoped
// row before that — enterprise config.json reconciliation materializing an access
// profile's per-model budgets during LoadConfig — failed with
// `invalid scope "user" for model config`, aborting the profile assignment
// half-way through.
func TestDeclaredModelConfigScopesAreValidByDefault(t *testing.T) {
	for _, scope := range []string{
		ModelConfigScopeGlobal,
		ModelConfigScopeVirtualKey,
		ModelConfigScopeUser,
	} {
		if !IsValidModelConfigScope(scope) {
			t.Errorf("scope %q is declared by this package but is not valid without RegisterModelConfigScope", scope)
		}
	}
}

// TestUndeclaredModelConfigScopeStillNeedsRegistration keeps the registry doing
// the job it exists for: scopes this module does not declare stay rejected until
// a downstream build registers them.
func TestUndeclaredModelConfigScopeStillNeedsRegistration(t *testing.T) {
	const scope = "test-downstream-scope"
	if IsValidModelConfigScope(scope) {
		t.Fatalf("scope %q should not be valid before registration", scope)
	}
	RegisterModelConfigScope(scope)
	if !IsValidModelConfigScope(scope) {
		t.Errorf("scope %q should be valid after registration", scope)
	}
}
