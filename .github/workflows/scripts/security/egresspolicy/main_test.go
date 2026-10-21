package main

import (
	"github.com/maximhq/bifrost/securitytools/internal/workflows"
	"testing"
)

// TestHostTemplates retains a known host even when its path is a runtime template.
func TestHostTemplates(t *testing.T) {
	result := hosts(`https://{{endpoint}}/path https://files.example.com/{{file}} https://api.example.com/value`)
	if len(result) != 2 || !result["files.example.com"] || !result["api.example.com"] {
		t.Fatal(result)
	}
}

// TestMissingEgress checks both mirrors and literal provider hosts.
func TestMissingEgress(t *testing.T) {
	job := workflows.Job{Steps: []workflows.Step{
		{Uses: "step-security/harden-runner@fixture", With: map[string]any{"egress-policy": "block", "allowed-endpoints": "github.com:443"}},
		{Run: "apt-get update\nbash test-provider-harness.sh"},
	}}
	d := workflows.Workflow{Jobs: map[string]workflows.Job{"test": job}}
	if failures := check(d, map[string]bool{"api.example.com": true}); len(failures) != 3 {
		t.Fatal(failures)
	}
	job.Steps[0].With["allowed-endpoints"] = "azure.archive.ubuntu.com:80 esm.ubuntu.com:443 api.example.com:443"
	if failures := check(d, map[string]bool{"api.example.com": true}); len(failures) != 0 {
		t.Fatal(failures)
	}
}

// TestMissingCheckoutFails ensures absent harness inputs never count as successful validation.
func TestMissingCheckoutFails(t *testing.T) {
	if _, err := requiredHosts(t.TempDir()); err == nil {
		t.Fatal("missing inputs accepted")
	}
}
