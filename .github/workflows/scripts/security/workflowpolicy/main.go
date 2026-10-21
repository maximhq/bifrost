// Command workflowpolicy enforces pinned actions and release runner isolation.
package main

import (
	"fmt"
	"net"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/maximhq/bifrost/securitytools/internal/workflows"
)

// rawContentURL matches any tool fetching unreviewed bytes from GitHub's raw or gist hosts.
var rawContentURL = regexp.MustCompile(`https?://(?:raw\.githubusercontent\.com|raw\.github\.com|gist\.githubusercontent\.com)(?:/|\s|$)`)

// trustedNets holds the docker-compose subnets CI services listen on; run loads them, tests set them.
var trustedNets []*net.IPNet

// loadTrustedNets reads subnet declarations from the CI compose file, which uses non-RFC1918 ranges.
func loadTrustedNets(root string) error {
	data, err := os.ReadFile(filepath.Join(root, "configs", "docker-compose.yml"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, match := range regexp.MustCompile(`subnet:\s*([0-9./]+)`).FindAllStringSubmatch(string(data), -1) {
		_, network, err := net.ParseCIDR(match[1])
		if err != nil {
			return err
		}
		trustedNets = append(trustedNets, network)
	}
	return nil
}

// addressAllowed accepts loopback, private, and declared compose addresses; anything else may be a raw host.
func addressAllowed(ip net.IP) bool {
	if ip.IsLoopback() || ip.IsPrivate() {
		return true
	}
	for _, network := range trustedNets {
		if network.Contains(ip) {
			return true
		}
	}
	return false
}

// check returns every policy violation without executing workflow contents.
func check(document workflows.Workflow, filename string) []string {
	var failures []string
	triggers := workflows.Triggers(document.On)
	_, manual := triggers["workflow_dispatch"]
	if filename == "network-security-validation.yml" && (!manual || len(triggers) != 1) {
		failures = append(failures, "live network diagnostics must be manual-only")
	}
	if _, found := triggers["pull_request_target"]; found {
		failures = append(failures, "pull_request_target requires a separate security design")
	}
	permissions, ok := document.Permissions.(map[string]any)
	if document.Permissions != "read-all" && (!ok || (workflows.String(permissions, "contents") != "" && workflows.String(permissions, "contents") != "read" && workflows.String(permissions, "contents") != "none")) {
		failures = append(failures, "workflow must not default to repository write access")
	}
	for name, job := range document.Jobs {
		if filename == "network-security-validation.yml" && job.Environment != "pr-testing" {
			failures = append(failures, name+": live network diagnostics require admin approval")
		}
		references := append([]workflows.Step{{Uses: job.Uses}}, job.Steps...)
		for _, step := range references {
			if filename != "network-security-validation.yml" && rawContentURL.MatchString(step.Run) {
				failures = append(failures, name+": raw GitHub content may only be fetched by the manual network diagnostic")
			}
			uses := step.Uses
			if strings.HasPrefix(uses, "step-security/harden-runner@") {
				for _, endpoint := range strings.Fields(workflows.String(step.With, "allowed-endpoints")) {
					if !regexp.MustCompile(`^[a-zA-Z0-9.*_-]+:[0-9]{1,5}$`).MatchString(endpoint) {
						failures = append(failures, name+": invalid host:port endpoint "+endpoint)
						continue
					}
					host := strings.TrimSuffix(strings.ToLower(endpoint[:strings.LastIndex(endpoint, ":")]), ".")
					if ip := net.ParseIP(host); ip != nil && !addressAllowed(ip) {
						failures = append(failures, name+": "+endpoint+" is a public address that the hostname rules cannot see")
						continue
					}
					for _, blocked := range []string{"raw.githubusercontent.com", "raw.github.com", "gist.githubusercontent.com"} {
						if matches, _ := path.Match(host, blocked); matches {
							failures = append(failures, name+": "+endpoint+" opens arbitrary GitHub raw content")
							break
						}
					}
				}
			}
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
	if err := loadTrustedNets(root); err != nil {
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
