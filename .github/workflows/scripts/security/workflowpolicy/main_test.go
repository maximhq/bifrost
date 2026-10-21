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
