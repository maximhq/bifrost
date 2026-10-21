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

var releaseBranches = []string{"main", "v2.0.0"}

// approvalEnvironments gate a human decision; the override environments let an admin accept a
// red live-provider or memory run, which feeds the release gate, so they need the same reviewers.
var approvalEnvironments = []string{"oss-release-approval", "oss-npm-release", "oss-native-release", "flaky-test-override", "memory-regression-override"}
var lockedEnvironments = []string{"oss-release", "oss-release-approval", "oss-npm-release", "oss-native-release", "oss-test-reports", "flaky-test-override", "memory-regression-override"}
var repositoryLevelForbidden = []string{"DOCKER_PASSWORD", "R2_ACCESS_KEY_ID", "R2_SECRET_ACCESS_KEY", "R2_REPORTS_ACCESS_KEY_ID", "R2_REPORTS_SECRET_ACCESS_KEY"}
var releaseEnvironmentSecrets = map[string][]string{
	"oss-release":        {"DOCKER_PASSWORD", "R2_ACCESS_KEY_ID", "R2_SECRET_ACCESS_KEY"},
	"oss-native-release": {"R2_ACCESS_KEY_ID", "R2_SECRET_ACCESS_KEY"},
	"oss-test-reports":   {"R2_REPORTS_ACCESS_KEY_ID", "R2_REPORTS_SECRET_ACCESS_KEY"},
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
	return append(failures, checkBranches("release-branch-policies.json", policies)...), nil
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
	return failures, nil
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
