package main

import (
	"github.com/maximhq/bifrost/securitytools/internal/workflows"
	"strings"
	"testing"
)

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
