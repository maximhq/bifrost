package main

import (
	"github.com/maximhq/bifrost/securitytools/internal/workflows"
	"path/filepath"
	"testing"
)

// fixture leaves the preparatory runner unprotected to reproduce the bypass.
func fixture(event any) workflows.Workflow {
	return workflows.Workflow{On: event, Jobs: map[string]workflows.Job{"prepare": {}, "test": {Environment: "pr-testing"}}}
}

// TestEveryPRJobRequiresApproval covers comments, reviews, queue, and indirect triggers.
func TestEveryPRJobRequiresApproval(t *testing.T) {
	for _, event := range prEvents {
		if len(checkWorkflow(fixture(event), "test.yml")) == 0 {
			t.Errorf("accepted ungated %s", event)
		}
	}
}

// TestPushAliasesCannotBypass rejects feature pushes, wildcards, tags, and unrestricted pushes.
func TestPushAliasesCannotBypass(t *testing.T) {
	for _, push := range []any{nil, map[string]any{}, map[string]any{"branches": []any{"**/*"}}, map[string]any{"branches-ignore": []any{"main"}}, map[string]any{"branches": []any{"feature/test"}}, map[string]any{"tags": []any{"*"}}} {
		if len(checkWorkflow(fixture(map[string]any{"push": push}), "test.yml")) == 0 {
			t.Errorf("accepted ungated push: %v", push)
		}
	}
	if failures := checkWorkflow(fixture(map[string]any{"push": map[string]any{"branches": []any{"dev", "main", "v2.0.0"}}}), "test.yml"); len(failures) != 0 {
		t.Fatal(failures)
	}
}

// TestGateAndManualEntrypoints rejects actor exemptions and missing setup-job approval.
func TestGateAndManualEntrypoints(t *testing.T) {
	d := fixture("pull_request")
	for _, environment := range []any{nil, "unprotected", "${{ github.actor == 'dependabot[bot]' && 'unprotected' || 'pr-testing' }}", "pr-testing", conditionalEnvironment} {
		d.Jobs["prepare"] = workflows.Job{Environment: environment}
		failures := checkWorkflow(d, "test.yml")
		wantSuccess := environment == "pr-testing" || environment == conditionalEnvironment
		if wantSuccess != (len(failures) == 0) {
			t.Fatalf("environment %v: %v", environment, failures)
		}
	}
	for _, name := range manualTests {
		if len(checkWorkflow(fixture("workflow_dispatch"), name)) == 0 {
			t.Errorf("ungated manual entrypoint: %s", name)
		}
	}
}

// TestRepositoryInventory verifies the actual committed workflows as well as fixtures.
func TestRepositoryInventory(t *testing.T) {
	if err := run(filepath.Join("..", "..", ".."), false, false); err != nil {
		t.Fatal(err)
	}
}

// TestRequiredAdminReview rejects empty rules, wrong approvers, bypass, and self-review.
func TestRequiredAdminReview(t *testing.T) {
	disabled := false
	approved := reviewer{Type: "Team"}
	approved.Reviewer.ID = adminTeamID
	good := environmentSettings{Name: "pr-testing", CanAdminsBypass: &disabled, ProtectionRules: []protectionRule{{Type: "required_reviewers", PreventSelfReview: true, Reviewers: []reviewer{approved}}}}
	if failures := checkEnvironment(good); len(failures) != 0 {
		t.Fatal(failures)
	}
	for _, kind := range []string{"empty", "bypass", "wrong-team", "self-review", "missing-reviewers"} {
		bad := good
		bad.ProtectionRules = append([]protectionRule{}, good.ProtectionRules...)
		bad.ProtectionRules[0].Reviewers = append([]reviewer{}, good.ProtectionRules[0].Reviewers...)
		switch kind {
		case "empty":
			bad.ProtectionRules = nil
		case "bypass":
			enabled := true
			bad.CanAdminsBypass = &enabled
		case "wrong-team":
			bad.ProtectionRules[0].Reviewers[0].Reviewer.ID = 1
		case "self-review":
			bad.ProtectionRules[0].PreventSelfReview = false
		case "missing-reviewers":
			bad.ProtectionRules[0].Reviewers = nil
		}
		if len(checkEnvironment(bad)) == 0 {
			t.Errorf("accepted %s", kind)
		}
	}
}
