package openapimcp

import (
	"fmt"
	"strconv"
	"strings"
)

// Version is the detected spec version.
type Version struct {
	Raw      string // the declared version string
	Swagger2 bool   // Swagger 2.0 document
	Major    int    // 2 or 3
	Minor    int    // 0, 1, 2 (clamped to 2 for newer 3.x minors)
}

// String returns the declared version string.
func (v Version) String() string { return v.Raw }

// Feature flags consumed by the parser so version differences live here.

// HasRequestBody reports whether bodies are declared via requestBody (3.x) rather
// than body/formData parameters (2.0).
func (v Version) HasRequestBody() bool { return !v.Swagger2 }

// HasNullableKeyword reports whether `nullable: true` is the null idiom (2.0 via
// x-nullable, 3.0) as opposed to `type: [..., "null"]` (3.1+).
func (v Version) HasNullableKeyword() bool { return v.Swagger2 || v.Minor == 0 }

// HasQueryMethod reports whether `query` is a first-class path item method (3.2+).
func (v Version) HasQueryMethod() bool { return !v.Swagger2 && v.Minor >= 2 }

// HasAdditionalOperations reports whether path items may carry additionalOperations (3.2+).
func (v Version) HasAdditionalOperations() bool { return v.HasQueryMethod() }

// HasQuerystringParam reports whether `in: querystring` parameters exist (3.2+).
func (v Version) HasQuerystringParam() bool { return v.HasQueryMethod() }

// HasSelf reports whether the document may declare $self (3.2+).
func (v Version) HasSelf() bool { return v.HasQueryMethod() }

// latestSupportedMinor is the newest 3.x minor this package knows the shape of.
const latestSupportedMinor = 2

// DetectVersion reads the swagger/openapi field of a decoded document. It
// returns the version, any best-effort warnings, or an error for documents that
// are not OpenAPI or declare a major this package does not understand.
func DetectVersion(tree map[string]any) (Version, []string, error) {
	if raw, ok := tree["swagger"]; ok {
		s := strings.TrimSpace(fmt.Sprint(raw))
		if s != "2.0" {
			return Version{}, nil, fmt.Errorf("unsupported swagger version %q (only 2.0 is supported)", s)
		}
		return Version{Raw: s, Swagger2: true, Major: 2}, nil, nil
	}
	raw, ok := tree["openapi"]
	if !ok {
		return Version{}, nil, fmt.Errorf("not an OpenAPI document: missing top-level \"openapi\" (3.x) or \"swagger\" (2.0) field")
	}
	s := strings.TrimSpace(fmt.Sprint(raw))
	parts := strings.Split(s, ".")
	if len(parts) < 2 {
		return Version{}, nil, fmt.Errorf("malformed openapi version %q", s)
	}
	major, err := strconv.Atoi(parts[0])
	if err != nil {
		return Version{}, nil, fmt.Errorf("malformed openapi version %q", s)
	}
	minor, err := strconv.Atoi(parts[1])
	if err != nil {
		return Version{}, nil, fmt.Errorf("malformed openapi version %q", s)
	}
	if major != 3 {
		return Version{}, nil, fmt.Errorf("unsupported openapi version %q (supported: Swagger 2.0, OpenAPI 3.0 to 3.%d)", s, latestSupportedMinor)
	}
	var warnings []string
	if minor > latestSupportedMinor {
		warnings = append(warnings, fmt.Sprintf("openapi %s is newer than the latest supported minor (3.%d); parsed best-effort as 3.%d", s, latestSupportedMinor, latestSupportedMinor))
		minor = latestSupportedMinor
	}
	return Version{Raw: s, Major: 3, Minor: minor}, warnings, nil
}
