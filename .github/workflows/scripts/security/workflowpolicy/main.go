// Command workflowpolicy enforces pinned actions and release runner isolation.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/maximhq/bifrost/securitytools/internal/workflows"
)

// check returns every policy violation without executing workflow contents.
func check(document workflows.Workflow, filename string) []string {
	var failures []string
	if _, found := workflows.Triggers(document.On)["pull_request_target"]; found {
		failures = append(failures, "pull_request_target requires a separate security design")
	}
	permissions, ok := document.Permissions.(map[string]any)
	if document.Permissions != "read-all" && (!ok || (workflows.String(permissions, "contents") != "" && workflows.String(permissions, "contents") != "read" && workflows.String(permissions, "contents") != "none")) {
		failures = append(failures, "workflow must not default to repository write access")
	}
	for name, job := range document.Jobs {
		references := append([]workflows.Step{{Uses: job.Uses}}, job.Steps...)
		for _, step := range references {
			uses := step.Uses
			if uses != "" && !strings.HasPrefix(uses, "./") {
				pattern := `^.+@[0-9a-f]{40}$`
				if strings.HasPrefix(uses, "docker://") {
					pattern = `^.+@sha256:[0-9a-f]{64}$`
				}
				if !regexp.MustCompile(pattern).MatchString(uses) {
					failures = append(failures, name+": mutable action reference "+uses)
				}
			}
		}
		if filename == "release-pipeline.yml" {
			first := workflows.Step{}
			if len(job.Steps) > 0 {
				first = job.Steps[0]
			}
			if !strings.HasPrefix(first.Uses, "step-security/harden-runner@") {
				failures = append(failures, name+": harden-runner must be the first step")
			}
			if workflows.String(first.With, "egress-policy") != "block" {
				failures = append(failures, name+": release egress must block")
			}
		}
	}
	return failures
}

// run scans the full workflow directory and reports all violations together.
func run(root string) error {
	paths, err := workflows.Paths(root)
	if err != nil {
		return err
	}
	count := 0
	for _, p := range paths {
		doc, err := workflows.Read(p)
		if err != nil {
			return err
		}
		for _, failure := range check(doc, filepath.Base(p)) {
			fmt.Fprintln(os.Stderr, filepath.Base(p)+": "+failure)
			count++
		}
	}
	if count > 0 {
		return fmt.Errorf("%d workflow policy violations", count)
	}
	fmt.Printf("Release policy passed for %d workflows\n", len(paths))
	return nil
}

// main requires an explicit input directory to avoid checking an unintended checkout.
func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: workflowpolicy WORKFLOW_DIRECTORY")
		os.Exit(2)
	}
	if err := run(os.Args[1]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
