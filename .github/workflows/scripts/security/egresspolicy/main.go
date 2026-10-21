// Command egresspolicy checks package mirrors and provider-harness destinations.
package main

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/maximhq/bifrost/securitytools/internal/workflows"
)

var hostPattern = regexp.MustCompile(`https?://([A-Za-z0-9._-]+\.[A-Za-z]{2,})`)
var templatedAuthority = regexp.MustCompile(`https?://[^\s"'/]*\{\{[^\s"']*`)

// notDialled records reviewed documentation, provider-side fetches, and negative fixtures.
var notDialled = map[string]string{
	"example.com":                            "provider-side MCP and web-fetch targets",
	"www.youtube.com":                        "Google fetches video inputs server-side",
	"en.wikipedia.org":                       "Google search exclusion filter",
	"www.wikipedia.org":                      "provider-side Gemini URL context",
	"mcp.deepwiki.com":                       "provider-side MCP connector",
	"placeholder.search.windows.net":         "Azure placeholder",
	"replace-me.search.windows.net":          "Azure placeholder",
	"does-not-exist-h146.search.windows.net": "synthetic negative endpoint fixture",
	"anthropic.com":                          "documentation", "www.anthropic.com": "documentation",
	"platform.claude.com": "documentation", "developers.openai.com": "documentation",
	"docs.aws.amazon.com": "documentation", "docs.x.ai": "documentation",
	"ai.google.dev": "documentation", "code.claude.com": "documentation",
	"learn.microsoft.com": "documentation", "www.ietf.org": "SDP extension identifier",
	"schema.getpostman.com": "Postman schema identifier",
	"s3.amazonaws.com":      "XML namespace identifier",
	"www.googleapis.com":    "OAuth scope identifier; token exchange uses oauth2.googleapis.com",
}

// hosts extracts literal authorities without losing templated paths on real hosts.
func hosts(data string) map[string]bool {
	result := map[string]bool{}
	data = templatedAuthority.ReplaceAllString(data, " ")
	for _, match := range hostPattern.FindAllStringSubmatch(data, -1) {
		result[match[1]] = true
	}
	return result
}

// requiredHosts inventories concrete harness endpoints and constructed cloud endpoints.
func requiredHosts(root string) (map[string]bool, error) {
	result := map[string]bool{}
	for _, name := range []string{"tests/e2e/api/collections/provider-harness.json", "tests/integrations/python/config.json"} {
		data, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			return nil, err
		}
		for host := range hosts(string(data)) {
			result[host] = true
		}
	}
	err := filepath.WalkDir(filepath.Join(root, "core/providers"), func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for host := range hosts(string(data)) {
			for _, suffix := range []string{".googleapis.com", ".amazonaws.com", ".api.aws"} {
				if strings.HasSuffix(host, suffix) {
					result[host] = true
				}
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	for host := range result {
		// Reserved invalid/example names cannot identify a public egress destination.
		if notDialled[host] != "" || host == "localhost" || strings.HasSuffix(host, ".invalid") || strings.HasSuffix(host, ".example") {
			delete(result, host)
		}
	}
	return result, nil
}

// check rejects missing APT mirrors and provider hosts on blocked-egress jobs.
func check(d workflows.Workflow, required map[string]bool) []string {
	var failures []string
	for name, job := range d.Jobs {
		var harden workflows.Step
		usesAPT, runsHarness := false, false
		for _, step := range job.Steps {
			usesAPT = usesAPT || strings.Contains(step.Run, "apt-get")
			runsHarness = runsHarness || strings.Contains(step.Run, "test-core.sh") || strings.Contains(step.Run, "test-provider-harness.sh")
			if harden.Uses == "" && strings.Contains(step.Uses, "step-security/harden-runner") {
				harden = step
			}
		}
		if workflows.String(harden.With, "egress-policy") != "block" {
			continue
		}
		endpoints, allowed := map[string]bool{}, map[string]bool{}
		for _, endpoint := range strings.Fields(workflows.String(harden.With, "allowed-endpoints")) {
			endpoints[endpoint] = true
			if index := strings.LastIndex(endpoint, ":"); index >= 0 {
				allowed[endpoint[:index]] = true
			}
		}
		if usesAPT {
			for _, host := range []string{"azure.archive.ubuntu.com:80", "esm.ubuntu.com:443"} {
				if !endpoints[host] {
					failures = append(failures, name+": APT mirror missing: "+host)
				}
			}
		}
		if runsHarness {
			for host := range required {
				if !allowed[host] {
					failures = append(failures, name+": harness host missing: "+host)
				}
			}
		}
	}
	sort.Strings(failures)
	return failures
}

// run requires real source inputs so a wrong checkout cannot silently pass.
func run(root string, paths []string) error {
	required, err := requiredHosts(root)
	if err != nil {
		return err
	}
	if len(paths) == 0 {
		paths, err = workflows.Paths(filepath.Join(root, ".github/workflows"))
		if err != nil {
			return err
		}
	}
	count := 0
	for _, path := range paths {
		doc, err := workflows.Read(path)
		if err != nil {
			return err
		}
		for _, failure := range check(doc, required) {
			fmt.Fprintln(os.Stderr, path+": "+failure)
			count++
		}
	}
	if count > 0 {
		return fmt.Errorf("%d egress policy violations", count)
	}
	fmt.Printf("Egress policy passed for %d workflows and %d harness hosts\n", len(paths), len(required))
	return nil
}

// main accepts the repository root followed by optional workflow paths.
func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: egresspolicy REPOSITORY [WORKFLOW...]")
		os.Exit(2)
	}
	if err := run(os.Args[1], os.Args[2:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
