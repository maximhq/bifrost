package main

import (
	"path/filepath"
	"slices"
	"testing"
)

// TestCommittedConfigurationPasses pins the files an operator applies to the policy itself.
func TestCommittedConfigurationPasses(t *testing.T) {
	failures, err := committed(filepath.Join("..", "..", "..", "..", "security"))
	if err != nil {
		t.Fatal(err)
	}
	if len(failures) != 0 {
		t.Fatal(failures)
	}
}

// TestApprovalSurfaceRejectsWeakerSettings covers bypass, self-review, extra reviewers, and open branches.
func TestApprovalSurfaceRejectsWeakerSettings(t *testing.T) {
	good := settings{Reviewers: []reviewer{{Type: "Team", ID: adminTeamID}}, PreventSelfReview: true, CustomBranchPolicy: true}
	if failures := checkSettings("oss-release-approval", good); len(failures) != 0 {
		t.Fatal(failures)
	}
	for name, mutate := range map[string]func(*settings){
		"admin bypass":   func(s *settings) { s.CanAdminsBypass = true },
		"self review":    func(s *settings) { s.PreventSelfReview = false },
		"open branches":  func(s *settings) { s.CustomBranchPolicy = false },
		"extra reviewer": func(s *settings) { s.Reviewers = append(s.Reviewers, reviewer{Type: "User", ID: 1}) },
		"wrong team":     func(s *settings) { s.Reviewers = []reviewer{{Type: "Team", ID: 1}} },
		"user not team":  func(s *settings) { s.Reviewers = []reviewer{{Type: "User", ID: adminTeamID}} },
		"nobody reviews": func(s *settings) { s.Reviewers = nil },
		"no self-review": func(s *settings) { s.PreventSelfReview = false },
	} {
		s := good
		s.Reviewers = append([]reviewer(nil), good.Reviewers...)
		mutate(&s)
		if len(checkSettings("oss-npm-release", s)) == 0 {
			t.Errorf("accepted %s", name)
		}
	}
}

// TestCredentialEnvironmentIsNotAnApprovalSurface keeps reviewers off the environment that holds secrets.
func TestCredentialEnvironmentIsNotAnApprovalSurface(t *testing.T) {
	if failures := checkSettings("oss-release", settings{CustomBranchPolicy: true}); len(failures) != 0 {
		t.Fatal(failures)
	}
	if len(checkSettings("oss-release", settings{CustomBranchPolicy: true, Reviewers: []reviewer{{Type: "Team", ID: adminTeamID}}})) == 0 {
		t.Fatal("accepted reviewers on the credential environment")
	}
	if len(checkSettings("oss-release", settings{})) == 0 {
		t.Fatal("accepted an unlocked credential environment")
	}
}

// TestLiveShapeReducesLikeTheCommittedShape guards the two decoders against drifting apart.
func TestLiveShapeReducesLikeTheCommittedShape(t *testing.T) {
	no := false
	yes := true
	config := configFile{PreventSelfReview: &yes, CanAdminsBypass: &no}
	config.Reviewers = append(config.Reviewers, struct {
		Type string `json:"type"`
		ID   int64  `json:"id"`
	}{"Team", adminTeamID})
	config.DeploymentBranchPolicy = &struct {
		ProtectedBranches    bool `json:"protected_branches"`
		CustomBranchPolicies bool `json:"custom_branch_policies"`
	}{false, true}
	remote := liveEnvironment{CanAdminsBypass: &no, DeploymentBranchPolicy: config.DeploymentBranchPolicy}
	remote.ProtectionRules = append(remote.ProtectionRules, struct {
		Type              string `json:"type"`
		PreventSelfReview bool   `json:"prevent_self_review"`
		Reviewers         []struct {
			Type     string `json:"type"`
			Reviewer struct {
				ID int64 `json:"id"`
			} `json:"reviewer"`
		} `json:"reviewers"`
	}{Type: "required_reviewers", PreventSelfReview: true})
	remote.ProtectionRules[0].Reviewers = append(remote.ProtectionRules[0].Reviewers, struct {
		Type     string `json:"type"`
		Reviewer struct {
			ID int64 `json:"id"`
		} `json:"reviewer"`
	}{Type: "Team"})
	remote.ProtectionRules[0].Reviewers[0].Reviewer.ID = adminTeamID
	for _, s := range []settings{config.toSettings(), remote.toSettings()} {
		if failures := checkSettings("oss-release-approval", s); len(failures) != 0 {
			t.Fatal(failures)
		}
	}
	if !(configFile{}).toSettings().CanAdminsBypass || !(liveEnvironment{}).toSettings().CanAdminsBypass {
		t.Fatal("absent bypass settings must read as the unsafe GitHub default")
	}
}

// TestBranchLockIsExact rejects extra branches, patterns, tags, and a missing release branch.
func TestBranchLockIsExact(t *testing.T) {
	policies := func(entries ...[2]string) branchPolicies {
		var p branchPolicies
		for _, entry := range entries {
			p.Policies = append(p.Policies, struct {
				Name string `json:"name"`
				Type string `json:"type"`
			}{entry[0], entry[1]})
		}
		return p
	}
	if failures := checkBranches("x", policies([2]string{"v2.0.0", "branch"}, [2]string{"main", "branch"})); len(failures) != 0 {
		t.Fatal(failures)
	}
	for name, p := range map[string]branchPolicies{
		"missing": policies([2]string{"main", "branch"}),
		"extra":   policies([2]string{"main", "branch"}, [2]string{"v2.0.0", "branch"}, [2]string{"dev", "branch"}),
		"pattern": policies([2]string{"main", "branch"}, [2]string{"v2.0.0", "branch"}, [2]string{"release/*", "branch"}),
		"tag":     policies([2]string{"main", "branch"}, [2]string{"v2.0.0", "tag"}),
		"none":    policies(),
	} {
		if len(checkBranches("x", p)) == 0 {
			t.Errorf("accepted %s", name)
		}
	}
}

// TestSecretPlacement keeps publish credentials out of repository scope and inside the release environment.
func TestSecretPlacement(t *testing.T) {
	if len(checkSecretNames("repository secrets", []string{"OPENAI_API_KEY", "DOCKER_PASSWORD"}, repositoryLevelForbidden, nil)) == 0 {
		t.Fatal("accepted a repository-level publish credential")
	}
	if failures := checkSecretNames("repository secrets", []string{"OPENAI_API_KEY", "DOCKER_USERNAME", "R2_BUCKET"}, repositoryLevelForbidden, nil); len(failures) != 0 {
		t.Fatal(failures)
	}
	if len(checkSecretNames("oss-release secrets", []string{"DOCKER_PASSWORD"}, nil, releaseEnvironmentSecrets["oss-release"])) == 0 {
		t.Fatal("accepted an environment missing a required credential")
	}
	if len(checkSecretNames("repository secrets", []string{"R2_REPORTS_SECRET_ACCESS_KEY"}, repositoryLevelForbidden, nil)) == 0 {
		t.Fatal("accepted the reports key at repository scope")
	}
}

// TestOverrideEnvironmentsAreApprovalSurfaces keeps the flaky and memory overrides on the admin team.
func TestOverrideEnvironmentsAreApprovalSurfaces(t *testing.T) {
	for _, name := range []string{"flaky-test-override", "memory-regression-override", "oss-test-reports"} {
		if !slices.Contains(lockedEnvironments, name) {
			t.Errorf("%s is not branch-locked", name)
		}
	}
	single := settings{Reviewers: []reviewer{{Type: "User", ID: 1050029}}, CustomBranchPolicy: true}
	if len(checkSettings("flaky-test-override", single)) == 0 {
		t.Fatal("accepted a single self-reviewing user on the flaky override")
	}
	if len(checkSettings("memory-regression-override", settings{CustomBranchPolicy: true})) == 0 {
		t.Fatal("accepted an auto-approving memory override")
	}
}
