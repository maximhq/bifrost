// Command releaseapproval audits the committed release environment configuration and, with --live, the server.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"

	"github.com/maximhq/bifrost/securitytools/internal/ghapi"
)

const adminTeamID int64 = 16787162
const repository = "repos/maximhq/bifrost"
const releaseAppSlug = "bifrost-release"
const adminTeamSlug = "bifrost-admin"

var releaseBranches = []string{"main", "v2.0.0"}

// approvalEnvironments gate a human decision; the override environments let an admin accept a
// red live-provider or memory run, which feeds the release gate, so they need the same reviewers.
var approvalEnvironments = []string{"oss-release-approval", "oss-npm-release", "oss-native-release", "flaky-test-override", "memory-regression-override"}
var lockedEnvironments = []string{"oss-release", "oss-release-approval", "oss-npm-release", "oss-native-release", "oss-test-reports", "flaky-test-override", "memory-regression-override"}
var repositoryLevelForbidden = []string{"DOCKER_PASSWORD", "R2_ACCESS_KEY_ID", "R2_SECRET_ACCESS_KEY", "R2_REPORTS_ACCESS_KEY_ID", "R2_REPORTS_SECRET_ACCESS_KEY", "GH_TOKEN", "NPM_TOKEN", "RELEASE_APP_CLIENT_ID", "RELEASE_APP_PRIVATE_KEY"}
var releaseEnvironmentSecrets = map[string][]string{
	"oss-release":        {"DOCKER_PASSWORD", "R2_ACCESS_KEY_ID", "R2_SECRET_ACCESS_KEY", "RELEASE_APP_CLIENT_ID", "RELEASE_APP_PRIVATE_KEY"},
	"oss-native-release": {"R2_ACCESS_KEY_ID", "R2_SECRET_ACCESS_KEY"},
	"oss-test-reports":   {"R2_REPORTS_ACCESS_KEY_ID", "R2_REPORTS_SECRET_ACCESS_KEY"},
}

// expectedRulesets pins the refs and rule types each ruleset must carry; the app is the only bypass actor.
var expectedRulesets = map[string]struct {
	target  string
	include []string
	rules   []string
}{
	"release-branches": {"branch", []string{"refs/heads/main", "refs/heads/v2.0.0"}, []string{"deletion", "non_fast_forward", "pull_request", "required_status_checks"}},
	"release-tags":     {"tag", []string{"refs/tags/core/**", "refs/tags/framework/**", "refs/tags/plugins/**", "refs/tags/transports/**"}, []string{"creation", "deletion", "update"}},
}

// reviewer identifies immutable GitHub team or user IDs, not mutable display names.
type reviewer struct {
	Type string
	ID   int64
}

// settings is the shape both the committed JSON and the live API reduce to.
type settings struct {
	Reviewers          []reviewer
	PreventSelfReview  bool
	CanAdminsBypass    bool
	CustomBranchPolicy bool
}

// configFile models the PUT body stored under .github/security.
type configFile struct {
	PreventSelfReview *bool `json:"prevent_self_review"`
	Reviewers         []struct {
		Type string `json:"type"`
		ID   int64  `json:"id"`
	} `json:"reviewers"`
	CanAdminsBypass        *bool `json:"can_admins_bypass"`
	DeploymentBranchPolicy *struct {
		ProtectedBranches    bool `json:"protected_branches"`
		CustomBranchPolicies bool `json:"custom_branch_policies"`
	} `json:"deployment_branch_policy"`
}

// toSettings treats absent fields as the unsafe GitHub defaults.
func (c configFile) toSettings() settings {
	s := settings{PreventSelfReview: c.PreventSelfReview != nil && *c.PreventSelfReview, CanAdminsBypass: c.CanAdminsBypass == nil || *c.CanAdminsBypass}
	for _, r := range c.Reviewers {
		s.Reviewers = append(s.Reviewers, reviewer{Type: r.Type, ID: r.ID})
	}
	s.CustomBranchPolicy = c.DeploymentBranchPolicy != nil && c.DeploymentBranchPolicy.CustomBranchPolicies && !c.DeploymentBranchPolicy.ProtectedBranches
	return s
}

// liveEnvironment models the GET response fields consumed by this policy.
type liveEnvironment struct {
	Name            string `json:"name"`
	CanAdminsBypass *bool  `json:"can_admins_bypass"`
	ProtectionRules []struct {
		Type              string `json:"type"`
		PreventSelfReview bool   `json:"prevent_self_review"`
		Reviewers         []struct {
			Type     string `json:"type"`
			Reviewer struct {
				ID int64 `json:"id"`
			} `json:"reviewer"`
		} `json:"reviewers"`
	} `json:"protection_rules"`
	DeploymentBranchPolicy *struct {
		ProtectedBranches    bool `json:"protected_branches"`
		CustomBranchPolicies bool `json:"custom_branch_policies"`
	} `json:"deployment_branch_policy"`
}

// toSettings flattens protection rules; a second required_reviewers rule is reported as extra reviewers.
func (e liveEnvironment) toSettings() settings {
	s := settings{CanAdminsBypass: e.CanAdminsBypass == nil || *e.CanAdminsBypass}
	for _, rule := range e.ProtectionRules {
		if rule.Type != "required_reviewers" {
			continue
		}
		s.PreventSelfReview = rule.PreventSelfReview
		for _, r := range rule.Reviewers {
			s.Reviewers = append(s.Reviewers, reviewer{Type: r.Type, ID: r.Reviewer.ID})
		}
	}
	s.CustomBranchPolicy = e.DeploymentBranchPolicy != nil && e.DeploymentBranchPolicy.CustomBranchPolicies && !e.DeploymentBranchPolicy.ProtectedBranches
	return s
}

// checkSettings rejects bypasses, missing branch locks, and any reviewer other than the admin team.
func checkSettings(name string, s settings) []string {
	var failures []string
	if s.CanAdminsBypass {
		failures = append(failures, name+" must disallow administrator bypass")
	}
	if !s.CustomBranchPolicy {
		failures = append(failures, name+" must restrict deployments to named release branches")
	}
	if slices.Contains(approvalEnvironments, name) {
		if len(s.Reviewers) != 1 || s.Reviewers[0].Type != "Team" || s.Reviewers[0].ID != adminTeamID {
			failures = append(failures, "only maximhq/bifrost-admin may approve "+name)
		}
		if !s.PreventSelfReview {
			failures = append(failures, name+" must prevent self-review")
		}
	} else if len(s.Reviewers) != 0 {
		failures = append(failures, name+" holds credentials and must not be an approval surface")
	}
	return failures
}

// branchPolicies is shared by the committed file and the live listing.
type branchPolicies struct {
	Policies []struct {
		Name string `json:"name"`
		Type string `json:"type"`
	} `json:"branch_policies"`
}

// checkBranches requires exactly the release branches, by exact name, with no patterns or tags.
func checkBranches(name string, p branchPolicies) []string {
	var names []string
	for _, policy := range p.Policies {
		if policy.Type != "branch" {
			return []string{name + " must only lock to branches, not " + policy.Type}
		}
		names = append(names, policy.Name)
	}
	sort.Strings(names)
	if !slices.Equal(names, releaseBranches) {
		return []string{fmt.Sprintf("%s must lock to exactly %v, found %v", name, releaseBranches, names)}
	}
	return nil
}

// checkSecretNames keeps publish credentials out of repository scope and inside their environment.
func checkSecretNames(scope string, names, forbidden, required []string) []string {
	var failures []string
	for _, secret := range forbidden {
		if slices.Contains(names, secret) {
			failures = append(failures, scope+" must not hold "+secret)
		}
	}
	for _, secret := range required {
		if !slices.Contains(names, secret) {
			failures = append(failures, scope+" must hold "+secret)
		}
	}
	return failures
}

// ruleset models the fields shared by the committed POST body and the live GET response.
type ruleset struct {
	Name         string `json:"name"`
	Target       string `json:"target"`
	Enforcement  string `json:"enforcement"`
	BypassActors []struct {
		ActorID    int64  `json:"actor_id"`
		ActorType  string `json:"actor_type"`
		BypassMode string `json:"bypass_mode"`
	} `json:"bypass_actors"`
	Conditions struct {
		RefName struct {
			Include []string `json:"include"`
			Exclude []string `json:"exclude"`
		} `json:"ref_name"`
	} `json:"conditions"`
	Rules []struct {
		Type       string         `json:"type"`
		Parameters map[string]any `json:"parameters"`
	} `json:"rules"`
}

// checkRuleset requires active enforcement, the exact refs, every rule, and only the release app as bypass.
func checkRuleset(r ruleset, remote bool) []string {
	expected, ok := expectedRulesets[r.Name]
	if !ok {
		return []string{"unexpected ruleset " + r.Name}
	}
	var failures []string
	if r.Target != expected.target {
		failures = append(failures, r.Name+" must target "+expected.target)
	}
	if r.Enforcement != "active" {
		failures = append(failures, r.Name+" must be actively enforced")
	}
	include := slices.Clone(r.Conditions.RefName.Include)
	sort.Strings(include)
	if !slices.Equal(include, expected.include) || len(r.Conditions.RefName.Exclude) != 0 {
		failures = append(failures, fmt.Sprintf("%s must cover exactly %v", r.Name, expected.include))
	}
	if len(r.BypassActors) != 1 || r.BypassActors[0].ActorType != "Integration" || r.BypassActors[0].BypassMode != "always" || (remote && r.BypassActors[0].ActorID <= 0) {
		failures = append(failures, r.Name+" may be bypassed only by the release app")
	}
	parameters := map[string]map[string]any{}
	for _, rule := range r.Rules {
		parameters[rule.Type] = rule.Parameters
	}
	for _, rule := range expected.rules {
		if _, ok := parameters[rule]; !ok {
			failures = append(failures, r.Name+" must include rule "+rule)
		}
	}
	if p, ok := parameters["pull_request"]; ok {
		count, _ := p["required_approving_review_count"].(float64)
		if count < 2 || p["require_code_owner_review"] != true || p["require_last_push_approval"] != true || p["dismiss_stale_reviews_on_push"] != true {
			failures = append(failures, r.Name+" must require two reviews, code owners, last-push approval, and stale dismissal")
		}
	}
	return failures
}

// classicProtection models the legacy branch protection fields that still grant a human bypass.
type classicProtection struct {
	EnforceAdmins struct {
		Enabled bool `json:"enabled"`
	} `json:"enforce_admins"`
	AllowForcePushes struct {
		Enabled bool `json:"enabled"`
	} `json:"allow_force_pushes"`
	RequiredPullRequestReviews struct {
		Bypass struct {
			Users []any `json:"users"`
			Teams []any `json:"teams"`
			Apps  []struct {
				Slug string `json:"slug"`
			} `json:"apps"`
		} `json:"bypass_pull_request_allowances"`
	} `json:"required_pull_request_reviews"`
	Restrictions *struct {
		Users []any `json:"users"`
		Teams []struct {
			Slug string `json:"slug"`
		} `json:"teams"`
		Apps []struct {
			Slug string `json:"slug"`
		} `json:"apps"`
	} `json:"restrictions"`
}

// checkClassic accepts legacy protection only when it mirrors the ruleset: no human bypass, and the
// release app allowed through, since classic is evaluated independently and would otherwise block it.
func checkClassic(branch string, p classicProtection) []string {
	var failures []string
	if !p.EnforceAdmins.Enabled {
		failures = append(failures, branch+" classic protection must apply to administrators")
	}
	if p.AllowForcePushes.Enabled {
		failures = append(failures, branch+" classic protection must not allow force pushes")
	}
	bypass := p.RequiredPullRequestReviews.Bypass
	if len(bypass.Users)+len(bypass.Teams) != 0 {
		failures = append(failures, branch+" classic protection must not let any user or team bypass review")
	}
	if len(bypass.Apps) != 1 || bypass.Apps[0].Slug != releaseAppSlug {
		failures = append(failures, branch+" classic protection must let exactly the "+releaseAppSlug+" app bypass review, or the app cannot push release commits")
	}
	// "Restrict who can push" is a separate gate that applies to administrators once
	// enforce_admins is on; it must name the app (or it blocks every release commit) and
	// the admin team (or nobody can merge a pull request), and never an individual.
	if r := p.Restrictions; r != nil {
		if len(r.Users) != 0 {
			failures = append(failures, branch+" push restriction must not name individual users")
		}
		if len(r.Apps) != 1 || r.Apps[0].Slug != releaseAppSlug {
			failures = append(failures, branch+" push restriction must allow exactly the "+releaseAppSlug+" app")
		}
		for _, team := range r.Teams {
			if team.Slug != adminTeamSlug {
				failures = append(failures, branch+" push restriction must not allow team "+team.Slug)
			}
		}
	}
	return failures
}

// readJSON decodes a committed configuration file.
func readJSON(path string, target any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, target)
}

// committed verifies the files an operator applies, so a wrong config fails before it is applied.
func committed(root string) ([]string, error) {
	var failures []string
	for _, name := range lockedEnvironments {
		file := "release-approval-environment.json"
		if !slices.Contains(approvalEnvironments, name) {
			file = "oss-release-environment.json"
		}
		var config configFile
		if err := readJSON(filepath.Join(root, file), &config); err != nil {
			return nil, err
		}
		failures = append(failures, checkSettings(name, config.toSettings())...)
	}
	var policies branchPolicies
	if err := readJSON(filepath.Join(root, "release-branch-policies.json"), &policies); err != nil {
		return nil, err
	}
	failures = append(failures, checkBranches("release-branch-policies.json", policies)...)
	for name := range expectedRulesets {
		var r ruleset
		if err := readJSON(filepath.Join(root, name+"-ruleset.json"), &r); err != nil {
			return nil, err
		}
		if r.Name != name {
			failures = append(failures, name+"-ruleset.json names a different ruleset")
		}
		failures = append(failures, checkRuleset(r, false)...)
	}
	return failures, nil
}

// secretNames lists names only; values are never readable through the API.
func secretNames(endpoint string) ([]string, error) {
	var listing struct {
		Secrets []struct {
			Name string `json:"name"`
		} `json:"secrets"`
	}
	if err := ghapi.Read(endpoint+"?per_page=100", &listing); err != nil {
		return nil, err
	}
	var names []string
	for _, secret := range listing.Secrets {
		names = append(names, secret.Name)
	}
	return names, nil
}

// liveRulesets requires both rulesets to exist and reads each by immutable ID.
func liveRulesets() ([]string, error) {
	var listing []struct {
		ID   int64  `json:"id"`
		Name string `json:"name"`
	}
	if err := ghapi.Read(repository+"/rulesets?per_page=100", &listing); err != nil {
		return nil, err
	}
	var failures []string
	for name := range expectedRulesets {
		index := slices.IndexFunc(listing, func(r struct {
			ID   int64  `json:"id"`
			Name string `json:"name"`
		}) bool {
			return r.Name == name
		})
		if index < 0 {
			failures = append(failures, "ruleset "+name+" is not configured")
			continue
		}
		var r ruleset
		if err := ghapi.Read(fmt.Sprintf("%s/rulesets/%d", repository, listing[index].ID), &r); err != nil {
			return nil, err
		}
		failures = append(failures, checkRuleset(r, true)...)
	}
	for _, branch := range releaseBranches {
		var p classicProtection
		err := ghapi.Read(repository+"/branches/"+branch+"/protection", &p)
		if errors.Is(err, ghapi.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		failures = append(failures, checkClassic(branch, p)...)
	}
	return failures, nil
}

// live reads the server state without modifying it.
func live() ([]string, error) {
	var failures []string
	for _, name := range lockedEnvironments {
		var environment liveEnvironment
		err := ghapi.Read(repository+"/environments/"+name, &environment)
		if errors.Is(err, ghapi.ErrNotFound) {
			// Report and keep going: an operator wants the whole drift list, and GitHub
			// would otherwise create this environment unprotected on first use.
			failures = append(failures, "environment "+name+" does not exist; create it before merging the workflows")
			continue
		}
		if err != nil {
			return nil, err
		}
		failures = append(failures, checkSettings(name, environment.toSettings())...)
		var policies branchPolicies
		if err := ghapi.Read(repository+"/environments/"+name+"/deployment-branch-policies", &policies); err != nil {
			return nil, err
		}
		failures = append(failures, checkBranches(name, policies)...)
	}
	names, err := secretNames(repository + "/actions/secrets")
	if err != nil {
		return nil, err
	}
	failures = append(failures, checkSecretNames("repository secrets", names, repositoryLevelForbidden, nil)...)
	for name, required := range releaseEnvironmentSecrets {
		names, err := secretNames(repository + "/environments/" + name + "/secrets")
		if err != nil {
			return nil, err
		}
		failures = append(failures, checkSecretNames(name+" secrets", names, nil, required)...)
	}
	more, err := liveRulesets()
	if err != nil {
		return nil, err
	}
	return append(failures, more...), nil
}

// run always checks the committed files and optionally the live settings.
func run(root string, remote bool) error {
	failures, err := committed(root)
	if err != nil {
		return err
	}
	if remote {
		more, err := live()
		if err != nil {
			return err
		}
		failures = append(failures, more...)
	}
	for _, failure := range failures {
		fmt.Fprintln(os.Stderr, failure)
	}
	if len(failures) > 0 {
		return fmt.Errorf("%d release approval violations", len(failures))
	}
	fmt.Printf("Release approval policy passed (live settings checked: %t)\n", remote)
	return nil
}

// main keeps remote verification explicit and read-only.
func main() {
	root := flag.String("security", "../../../security", "directory holding the committed environment configuration")
	remote := flag.Bool("live", false, "read live GitHub settings without modifying them")
	flag.Parse()
	if err := run(*root, *remote); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
