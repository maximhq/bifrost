// Command prapproval audits workflow and server-side admin approval coverage.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"slices"

	"github.com/maximhq/bifrost/securitytools/internal/ghapi"
	"github.com/maximhq/bifrost/securitytools/internal/workflows"
)

const adminTeamID int64 = 16787162
const conditionalEnvironment = "${{ (github.event_name == 'schedule' || (github.event_name == 'push' && (github.ref == 'refs/heads/dev' || github.ref == 'refs/heads/main' || github.ref == 'refs/heads/v2.0.0'))) && 'ci-trusted-branch' || 'pr-testing' }}"

var trustedBranches = []string{"dev", "main", "v2.0.0"}
var manualTests = []string{"pr-tests.yml", "run-core-tests.yml", "live-tests.yml", "e2e-tests.yml", "network-security-validation.yml"}
var prEvents = []string{"pull_request", "pull_request_target", "pull_request_review", "pull_request_review_comment", "merge_group", "issue_comment", "workflow_run", "repository_dispatch", "check_run", "check_suite"}

// checkWorkflow requires the gate on preparatory jobs as well as test execution.
func checkWorkflow(document workflows.Workflow, filename string) []string {
	triggers := workflows.Triggers(document.On)
	needsApproval := slices.Contains(manualTests, filename)
	for _, event := range prEvents {
		if _, ok := triggers[event]; ok {
			needsApproval = true
		}
	}
	if raw, ok := triggers["push"]; ok {
		push, _ := raw.(map[string]any)
		branches, _ := push["branches"].([]any)
		if len(branches) == 0 {
			needsApproval = true
		}
		for _, branch := range branches {
			if !slices.Contains(trustedBranches, fmt.Sprint(branch)) {
				needsApproval = true
			}
		}
		if _, ok := push["tags"]; ok {
			needsApproval = true
		}
		if _, ok := push["tags-ignore"]; ok {
			needsApproval = true
		}
	}
	if !needsApproval {
		return nil
	}
	var failures []string
	for name, job := range document.Jobs {
		environment := job.Environment
		if mapping, ok := environment.(map[string]any); ok {
			environment = mapping["name"]
		}
		if environment != "pr-testing" && environment != conditionalEnvironment {
			failures = append(failures, filename+": "+name+" must wait for admin approval before any runner starts")
		}
	}
	return failures
}

// reviewer identifies immutable GitHub team or user IDs, not mutable display names.
type reviewer struct {
	Type     string `json:"type"`
	Reviewer struct {
		ID int64 `json:"id"`
	} `json:"reviewer"`
}

// protectionRule models only the approval constraints consumed by this policy.
type protectionRule struct {
	Type              string     `json:"type"`
	PreventSelfReview bool       `json:"prevent_self_review"`
	Reviewers         []reviewer `json:"reviewers"`
}

// environmentSettings distinguishes absent bypass settings from explicitly disabled bypass.
type environmentSettings struct {
	Name            string           `json:"name"`
	CanAdminsBypass *bool            `json:"can_admins_bypass"`
	ProtectionRules []protectionRule `json:"protection_rules"`
}

// checkEnvironment rejects empty environments, additional approvers, and bypasses.
func checkEnvironment(environment environmentSettings) []string {
	var failures []string
	if environment.Name != "pr-testing" {
		failures = append(failures, "wrong approval environment")
	}
	if environment.CanAdminsBypass == nil || *environment.CanAdminsBypass {
		failures = append(failures, "pr-testing must disallow administrator bypass")
	}
	var rules []protectionRule
	for _, rule := range environment.ProtectionRules {
		if rule.Type == "required_reviewers" {
			rules = append(rules, rule)
		}
	}
	if len(rules) != 1 {
		return append(failures, "pr-testing must have required reviewers")
	}
	rule := rules[0]
	if len(rule.Reviewers) != 1 || rule.Reviewers[0].Type != "Team" || rule.Reviewers[0].Reviewer.ID != adminTeamID {
		failures = append(failures, "only maximhq/bifrost-admin may approve pr-testing")
	}
	if !rule.PreventSelfReview {
		failures = append(failures, "pr-testing must prevent self-review")
	}
	return failures
}

// liveSettings verifies that the YAML environment references actually require admins.
func liveSettings() ([]string, error) {
	var environment environmentSettings
	if err := ghapi.Read("repos/maximhq/bifrost/environments/pr-testing", &environment); err != nil {
		return nil, err
	}
	failures := checkEnvironment(environment)
	var policy struct {
		ApprovalPolicy string `json:"approval_policy"`
	}
	if err := ghapi.Read("repos/maximhq/bifrost/actions/permissions/fork-pr-contributor-approval", &policy); err != nil {
		return nil, err
	}
	if policy.ApprovalPolicy != "all_external_contributors" {
		failures = append(failures, "all external contributors must require GitHub fork approval")
	}
	var team struct {
		Permissions struct {
			Admin bool `json:"admin"`
		} `json:"permissions"`
	}
	if err := ghapi.Read("orgs/maximhq/teams/bifrost-admin/repos/maximhq/bifrost", &team, "Accept: application/vnd.github.v3.repository+json"); err != nil {
		return nil, err
	}
	if !team.Permissions.Admin {
		failures = append(failures, "bifrost-admin must have repository administrator access")
	}
	return failures, nil
}

// liveInventory fails on every active workflow outside reviewed repository YAML.
func liveInventory(paths []string) ([]string, error) {
	known := map[string]bool{}
	for _, path := range paths {
		known[".github/workflows/"+filepath.Base(path)] = true
	}
	var failures []string
	for page := 1; ; page++ {
		var inventory struct {
			Workflows []struct{ Name, Path, State string } `json:"workflows"`
		}
		if err := ghapi.Read(fmt.Sprintf("repos/maximhq/bifrost/actions/workflows?per_page=100&page=%d", page), &inventory); err != nil {
			return nil, err
		}
		for _, workflow := range inventory.Workflows {
			if workflow.State == "active" && !known[workflow.Path] {
				failures = append(failures, fmt.Sprintf("approval coverage not verified outside repository YAML: %s (%s)", workflow.Name, workflow.Path))
			}
		}
		if len(inventory.Workflows) < 100 {
			break
		}
	}
	return failures, nil
}

// run checks the complete local inventory and optionally reads live settings.
func run(root string, live, inventory bool) error {
	paths, err := workflows.Paths(root)
	if err != nil {
		return err
	}
	var failures []string
	for _, path := range paths {
		doc, err := workflows.Read(path)
		if err != nil {
			return err
		}
		failures = append(failures, checkWorkflow(doc, filepath.Base(path))...)
	}
	if live {
		errors, err := liveSettings()
		if err != nil {
			return err
		}
		failures = append(failures, errors...)
	}
	if inventory {
		errors, err := liveInventory(paths)
		if err != nil {
			return err
		}
		failures = append(failures, errors...)
	}
	for _, failure := range failures {
		fmt.Fprintln(os.Stderr, failure)
	}
	if len(failures) > 0 {
		return fmt.Errorf("%d approval policy violations", len(failures))
	}
	fmt.Printf("PR approval policy passed for %d workflows (live settings checked: %t)\n", len(paths), live)
	return nil
}

// main keeps remote verification explicit and read-only.
func main() {
	root := flag.String("workflows", "../..", "workflow directory; default assumes run-security-tool.sh")
	live := flag.Bool("live", false, "read live GitHub settings without modifying them")
	inventory := flag.Bool("inventory", false, "also reject unaccounted active workflows; requires --live")
	flag.Parse()
	if *inventory && !*live {
		fmt.Fprintln(os.Stderr, "--inventory requires --live")
		os.Exit(2)
	}
	if err := run(*root, *live, *inventory); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
