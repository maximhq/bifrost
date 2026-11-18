package config

import (
	"flag"
	"fmt"
	"net/http"
	"strings"
)

const ProjectIDHeader = "x-bf-project-id"
const ProjectNameHeader = "x-bf-project-name"

// Project selects one governance project without changing the caller's credential.
type Project struct {
	ID   string
	Name string
}

// BindFlags registers an explicit per-command project selection.
func (p *Project) BindFlags(fs *flag.FlagSet) {
	fs.StringVar(&p.ID, "project-id", "", "Bifrost Enterprise project ID (exclusive with --project-name)")
	fs.StringVar(&p.Name, "project-name", "", "Bifrost Enterprise project name (exclusive with --project-id)")
}

// ValidateFlags rejects explicit empty selections instead of silently leaving spend unscoped.
func (p Project) ValidateFlags(fs *flag.FlagSet) error {
	var empty bool
	fs.Visit(func(f *flag.Flag) {
		if (f.Name == "project-id" && strings.TrimSpace(p.ID) == "") || (f.Name == "project-name" && strings.TrimSpace(p.Name) == "") {
			empty = true
		}
	})
	if empty {
		return fmt.Errorf("project selection cannot be empty")
	}
	return p.Validate()
}

// Validate prevents ambiguous selectors and HTTP header injection.
func (p Project) Validate() error {
	if p.ID != "" && p.Name != "" {
		return fmt.Errorf("use only one of --project-id or --project-name")
	}
	value := p.ID + p.Name
	if value != "" && strings.TrimSpace(value) == "" {
		return fmt.Errorf("project selection cannot be empty")
	}
	for _, c := range []byte(value) {
		if c < 32 || c == 127 {
			return fmt.Errorf("project selection must not contain control characters")
		}
	}
	return nil
}

// Header returns the single selected project header, or an empty pair.
func (p Project) Header() (string, string) {
	if p.ID != "" {
		return ProjectIDHeader, strings.TrimSpace(p.ID)
	}
	if p.Name != "" {
		return ProjectNameHeader, strings.TrimSpace(p.Name)
	}
	return "", ""
}

// Apply replaces both project selectors while preserving unrelated request headers.
func (p Project) Apply(headers http.Header) http.Header {
	name, value := p.Header()
	if name == "" {
		return headers
	}
	if headers == nil {
		headers = make(http.Header)
	}
	for key := range headers {
		if IsProjectHeader(key) {
			delete(headers, key)
		}
	}
	headers.Set(name, value)
	return headers
}

// IsProjectHeader compares project header names case-insensitively.
func IsProjectHeader(name string) bool {
	return strings.EqualFold(strings.TrimSpace(name), ProjectIDHeader) || strings.EqualFold(strings.TrimSpace(name), ProjectNameHeader)
}

// MergeHeaders replaces project selectors in a harness's delimited header string.
func (p Project) MergeHeaders(value, separator string) string {
	name, selected := p.Header()
	if name == "" {
		return value
	}
	kept := []string{}
	for _, line := range strings.Split(value, separator) {
		key, _, _ := strings.Cut(line, ":")
		if strings.TrimSpace(line) != "" && !IsProjectHeader(key) {
			kept = append(kept, line)
		}
	}
	return strings.Join(append(kept, name+": "+selected), separator)
}
