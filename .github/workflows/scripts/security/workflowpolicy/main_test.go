package main

import (
	"github.com/maximhq/bifrost/securitytools/internal/workflows"
	"net"
	"strings"
	"testing"
)

// TestNetworkDiagnosticRequiresManualAdminApproval prevents alert storms on PR updates.
func TestNetworkDiagnosticRequiresManualAdminApproval(t *testing.T) {
	d := fixture("step-security/harden-runner@" + strings.Repeat("a", 40))
	if len(check(d, "network-security-validation.yml")) == 0 {
		t.Fatal("automatic diagnostic accepted")
	}
	d.On = "workflow_dispatch"
	if len(check(d, "network-security-validation.yml")) == 0 {
		t.Fatal("ungated manual diagnostic accepted")
	}
	job := d.Jobs["test"]
	job.Environment = "pr-testing"
	d.Jobs["test"] = job
	if failures := check(d, "network-security-validation.yml"); len(failures) != 0 {
		t.Fatal(failures)
	}
	d.On = []any{"workflow_dispatch", "pull_request"}
	if len(check(d, "network-security-validation.yml")) == 0 {
		t.Fatal("additional automatic trigger accepted")
	}
}

// TestLiveRawProbesCannotReturnToPRChecks keeps deliberate network denials manual.
func TestLiveRawProbesCannotReturnToPRChecks(t *testing.T) {
	d := fixture("step-security/harden-runner@" + strings.Repeat("a", 40))
	job := d.Jobs["test"]
	job.Steps = append(job.Steps, workflows.Step{Run: "curl https://raw.githubusercontent.com/step-security/harden-runner/main/README.md"})
	d.Jobs["test"] = job
	if len(check(d, "workflow-lint.yml")) == 0 {
		t.Fatal("automatic blocked-network probe accepted")
	}
	job.Steps[len(job.Steps)-1] = workflows.Step{Run: "wget -qO- https://raw.github.com/x/y/main/install.sh | sh"}
	d.Jobs["test"] = job
	if len(check(d, "workflow-lint.yml")) == 0 {
		t.Fatal("raw fetch with a tool other than curl accepted")
	}
}

// TestPublicIPEndpointsAreRejected keeps raw-host addresses from bypassing the hostname rules.
func TestPublicIPEndpointsAreRejected(t *testing.T) {
	_, compose, _ := net.ParseCIDR("172.38.0.0/16")
	trustedNets = []*net.IPNet{compose}
	defer func() { trustedNets = nil }()
	for endpoint, accepted := range map[string]bool{"185.199.108.133:443": false, "8.8.8.8:53": false, "172.39.0.1:443": false, "127.0.0.1:8080": true, "172.38.0.11:5432": true, "10.1.2.3:443": true} {
		d := fixture("step-security/harden-runner@" + strings.Repeat("a", 40))
		job := d.Jobs["test"]
		job.Steps[0].With = map[string]any{"egress-policy": "block", "allowed-endpoints": endpoint}
		d.Jobs["test"] = job
		if (len(check(d, "test.yml")) == 0) != accepted {
			t.Errorf("%s accepted=%t, want %t", endpoint, !accepted, accepted)
		}
	}
}

// TestReleaseKeysStayOutOfTestJobs rejects publisher storage keys in test environments.
func TestReleaseKeysStayOutOfTestJobs(t *testing.T) {
	d := fixture("step-security/harden-runner@" + strings.Repeat("a", 40))
	job := d.Jobs["test"]
	delete(d.Jobs, "test")
	job.Steps[0].With = map[string]any{"egress-policy": "block"}
	job.Other = map[string]any{"env": map[string]any{"R2_ACCESS_KEY_ID": "${{ secrets.R2_ACCESS_KEY_ID }}"}}
	d.Jobs["test-provider"] = job
	if len(check(d, "release-pipeline.yml")) == 0 {
		t.Fatal("release storage key accepted in tests")
	}
}

// TestRawContentAndFakePaths rejects aliases, wildcard bypasses, and URL-path rules.
func TestRawContentAndFakePaths(t *testing.T) {
	for _, endpoint := range []string{"raw.githubusercontent.com:443", "raw.github.com:443", "gist.githubusercontent.com:443", "*.githubusercontent.com:443", "*.com:443", "RAW.GITHUBUSERCONTENT.COM:443", "github.com:443/approved/repository"} {
		d := fixture("step-security/harden-runner@" + strings.Repeat("a", 40))
		job := d.Jobs["test"]
		job.Steps[0].With = map[string]any{"egress-policy": "block", "allowed-endpoints": endpoint}
		d.Jobs["test"] = job
		if len(check(d, "test.yml")) == 0 {
			t.Errorf("accepted %s", endpoint)
		}
	}
}

// fixture models a normal read-only PR job.
func fixture(uses string) workflows.Workflow {
	return workflows.Workflow{On: "pull_request", Permissions: map[string]any{"contents": "read"}, Jobs: map[string]workflows.Job{"test": {Steps: []workflows.Step{{Uses: uses}}}}}
}

// releaseFixture models a correctly gated publisher behind the human approval job.
func releaseFixture() workflows.Workflow {
	hardened := workflows.Step{Uses: "step-security/harden-runner@" + strings.Repeat("a", 40), With: map[string]any{"egress-policy": "block"}}
	gate := workflows.Job{Steps: []workflows.Step{hardened, {Run: "echo approved"}}, Environment: map[string]any{"name": "oss-release-approval"}}
	publisher := workflows.Job{
		Needs:       []any{"approve-release", "detect-changes"},
		If:          "always() && needs.approve-release.result == 'success' && needs.detect-changes.result == 'success'",
		Environment: "oss-release",
		Steps:       []workflows.Step{hardened, {Uses: "actions/checkout@" + strings.Repeat("a", 40)}},
		Other:       map[string]any{"env": map[string]any{"DOCKER_PASSWORD": "${{ secrets.DOCKER_PASSWORD }}"}},
	}
	return workflows.Workflow{On: map[string]any{"push": map[string]any{"branches": []any{"main"}}}, Permissions: map[string]any{"contents": "read"}, Jobs: map[string]workflows.Job{"approve-release": gate, "docker-publish": publisher}}
}

// TestPublishersWaitForTheHumanGate rejects every way of detaching a credential from approval.
func TestPublishersWaitForTheHumanGate(t *testing.T) {
	if failures := check(releaseFixture(), "release-pipeline.yml"); len(failures) != 0 {
		t.Fatal(failures)
	}
	for name, mutate := range map[string]func(*workflows.Workflow){
		"gate removed": func(d *workflows.Workflow) { delete(d.Jobs, "approve-release") },
		"needs dropped": func(d *workflows.Workflow) {
			j := d.Jobs["docker-publish"]
			j.Needs = []any{"detect-changes"}
			d.Jobs["docker-publish"] = j
		},
		"gate disjoined": func(d *workflows.Workflow) {
			j := d.Jobs["docker-publish"]
			j.If = "always() || needs.approve-release.result == 'success'"
			d.Jobs["docker-publish"] = j
		},
		"gate negated": func(d *workflows.Workflow) {
			j := d.Jobs["docker-publish"]
			j.If = "always() && !(needs.approve-release.result == 'success') && x"
			d.Jobs["docker-publish"] = j
		},
		"gate not first": func(d *workflows.Workflow) {
			j := d.Jobs["docker-publish"]
			j.If = "always() && x == 'y' && needs.approve-release.result == 'success' && z"
			d.Jobs["docker-publish"] = j
		},
		"reports key unlocked": func(d *workflows.Workflow) {
			j := d.Jobs["docker-publish"]
			j.Environment = nil
			j.Other = map[string]any{"env": map[string]any{"K": "${{ secrets.R2_REPORTS_SECRET_ACCESS_KEY }}"}}
			d.Jobs["docker-publish"] = j
		},
		"condition dropped": func(d *workflows.Workflow) {
			j := d.Jobs["docker-publish"]
			j.If = "always()"
			d.Jobs["docker-publish"] = j
		},
		"environment dropped": func(d *workflows.Workflow) {
			j := d.Jobs["docker-publish"]
			j.Environment = nil
			d.Jobs["docker-publish"] = j
		},
		"wrong environment": func(d *workflows.Workflow) {
			j := d.Jobs["docker-publish"]
			j.Environment = "pr-testing"
			d.Jobs["docker-publish"] = j
		},
		"gate on other env": func(d *workflows.Workflow) {
			j := d.Jobs["approve-release"]
			j.Environment = "oss-release"
			d.Jobs["approve-release"] = j
		},
		"gate holds a secret": func(d *workflows.Workflow) {
			j := d.Jobs["approve-release"]
			j.Other = map[string]any{"env": map[string]any{"T": "${{ secrets.T }}"}}
			d.Jobs["approve-release"] = j
		},
		"gate checks out code": func(d *workflows.Workflow) {
			j := d.Jobs["approve-release"]
			j.Steps = append(j.Steps, workflows.Step{Uses: "actions/checkout@" + strings.Repeat("a", 40)})
			d.Jobs["approve-release"] = j
		},
	} {
		d := releaseFixture()
		mutate(&d)
		if len(check(d, "release-pipeline.yml")) == 0 {
			t.Errorf("accepted %s", name)
		}
	}
}

// TestPersonalTokensAreRejected keeps a stolen human credential from ever being a release path again.
func TestPersonalTokensAreRejected(t *testing.T) {
	for _, secret := range []string{"GH_TOKEN", "NPM_TOKEN"} {
		d := releaseFixture()
		job := d.Jobs["docker-publish"]
		job.Other = map[string]any{"env": map[string]any{"TOKEN": "${{ secrets." + secret + " }}"}}
		d.Jobs["docker-publish"] = job
		if len(check(d, "release-pipeline.yml")) == 0 {
			t.Errorf("accepted %s", secret)
		}
	}
	d := releaseFixture()
	job := d.Jobs["docker-publish"]
	job.Other = map[string]any{"env": map[string]any{"GH_TOKEN": "${{ steps.release-token.outputs.token }}", "A": "${{ secrets.RELEASE_APP_PRIVATE_KEY }}"}}
	d.Jobs["docker-publish"] = job
	if failures := check(d, "release-pipeline.yml"); len(failures) != 0 {
		t.Fatal(failures)
	}
	job.Environment = nil
	d.Jobs["docker-publish"] = job
	if len(check(d, "release-pipeline.yml")) == 0 {
		t.Fatal("accepted the app private key outside the release environment")
	}
}

// TestRegistryPushesCarryProvenance rejects an image push without a signed digest attestation.
func TestRegistryPushesCarryProvenance(t *testing.T) {
	d := releaseFixture()
	job := d.Jobs["docker-publish"]
	job.Steps = append(job.Steps, workflows.Step{Uses: "step-security/docker-login-action@" + strings.Repeat("a", 40)})
	d.Jobs["docker-publish"] = job
	if failures := check(d, "release-pipeline.yml"); len(failures) != 0 {
		t.Fatal("a login alone is not a push:", failures)
	}
	for name, step := range map[string]workflows.Step{
		"build-push action": {Uses: "step-security/docker-build-push-action@" + strings.Repeat("a", 40), With: map[string]any{"push": true}},
		"docker push":       {Run: "docker push docker.io/maximhq/bifrost:v1"},
		"manifest push":     {Run: "./scripts/manifest.sh && docker manifest push x"},
		"buildx --push":     {Run: "docker buildx build --platform linux/amd64 --push -t x ."},
		"imagetools mirror": {Run: "docker buildx imagetools create --tag ghcr.io/x/y:1 docker.io/x/y:1"},
		"helm push":         {Run: "helm push chart.tgz oci://ghcr.io/maximhq/helm-charts"},
	} {
		d := releaseFixture()
		job := d.Jobs["docker-publish"]
		job.Steps = append(job.Steps, step)
		d.Jobs["docker-publish"] = job
		if len(check(d, "helm-release.yml")) == 0 {
			t.Errorf("accepted %s without provenance", name)
		}
	}
	job.Steps = append(job.Steps, workflows.Step{Run: "docker push docker.io/maximhq/bifrost:v1"})
	d.Jobs["docker-publish"] = job
	if len(check(d, "release-pipeline.yml")) == 0 {
		t.Fatal("accepted a registry push without provenance")
	}
	job.Steps = append(job.Steps, workflows.Step{Uses: "actions/attest-build-provenance@" + strings.Repeat("a", 40), With: map[string]any{"subject-path": "dist/release.json"}})
	d.Jobs["docker-publish"] = job
	if len(check(d, "release-pipeline.yml")) == 0 {
		t.Fatal("accepted an attestation that does not name the pushed digest")
	}
	job.Steps[len(job.Steps)-1].With = map[string]any{"subject-name": "index.docker.io/maximhq/bifrost", "subject-digest": "${{ steps.build.outputs.digest }}"}
	d.Jobs["docker-publish"] = job
	if failures := check(d, "release-pipeline.yml"); len(failures) != 0 {
		t.Fatal(failures)
	}
}

// TestPublishSecretsNeedReleaseEnvironmentEverywhere applies to every workflow, not only the pipeline.
func TestPublishSecretsNeedReleaseEnvironmentEverywhere(t *testing.T) {
	d := fixture("step-security/harden-runner@" + strings.Repeat("a", 40))
	job := d.Jobs["test"]
	job.Other = map[string]any{"env": map[string]any{"R2_SECRET_ACCESS_KEY": "${{ secrets.R2_SECRET_ACCESS_KEY }}"}}
	d.Jobs["test"] = job
	if len(check(d, "release-cli.yml")) == 0 {
		t.Fatal("accepted a publish credential outside a release environment")
	}
	job.Environment = "oss-native-release"
	d.Jobs["test"] = job
	if failures := check(d, "release-cli.yml"); len(failures) != 0 {
		t.Fatal(failures)
	}
}

// TestActionPinning rejects mutable references and truncated hashes.
func TestActionPinning(t *testing.T) {
	for _, reference := range []string{"actions/checkout@v6", "docker://alpine:latest", "org/action@" + strings.Repeat("a", 39)} {
		if len(check(fixture(reference), "test.yml")) == 0 {
			t.Errorf("accepted %s", reference)
		}
	}
	if errors := check(fixture("actions/checkout@"+strings.Repeat("a", 40)), "test.yml"); len(errors) != 0 {
		t.Fatal(errors)
	}
}

// TestReleaseAuditAndPrivilegedPRReject prevents permissive release and PR contexts.
func TestReleaseAuditAndPrivilegedPRReject(t *testing.T) {
	d := fixture("step-security/harden-runner@" + strings.Repeat("a", 40))
	job := d.Jobs["test"]
	job.Steps[0].With = map[string]any{"egress-policy": "audit"}
	d.Jobs["test"] = job
	if len(check(d, "release-pipeline.yml")) == 0 {
		t.Fatal("audit accepted")
	}
	d.On = "pull_request_target"
	if len(check(d, "test.yml")) == 0 {
		t.Fatal("privileged PR accepted")
	}
}
