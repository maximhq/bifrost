package openapimcp

import (
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/maximhq/bifrost/core/schemas"
)

// RelayImport is what a parsed document resolves into for a relay: its operations as routes,
// the security scheme they share as an auth suggestion, and the first server as a base URL hint.
type RelayImport struct {
	Title          string                     `json:"title,omitempty"`
	Version        string                     `json:"version,omitempty"`
	OpenAPIVersion string                     `json:"openapi_version"`
	BaseURL        string                     `json:"base_url,omitempty"`
	Auth           *schemas.RelayAuthConfig   `json:"auth,omitempty"`
	Routes         []schemas.RelayRouteConfig `json:"routes"`
	Warnings       []RelayImportWarning       `json:"warnings"`
	Partial        bool                       `json:"partial,omitempty"` // some path items could not be read and were left out
}

// RelayImportWarning is one thing the import could not carry over. Code and the fields
// beside it let the dashboard show it in the operator's language; Message is the English text.
type RelayImportWarning struct {
	Code    string   `json:"code"`
	Message string   `json:"message"`
	Name    string   `json:"name,omitempty"`   // the security scheme it is about
	Detail  string   `json:"detail,omitempty"` // the apiKey location, HTTP scheme, scheme type or server query it names
	Paths   []string `json:"paths,omitempty"`  // ref_paths: the path items left out
}

const (
	relayImportWarnSchemeUndeclared = "scheme_undeclared"
	relayImportWarnAPIKeyLocation   = "api_key_location"
	relayImportWarnHTTPScheme       = "http_scheme"
	relayImportWarnSchemeType       = "scheme_type"
	relayImportWarnAuthOptional     = "auth_optional"
	relayImportWarnNoSingleScheme   = "no_single_scheme"
	relayImportWarnSeveralSchemes   = "several_schemes"
	relayImportWarnDifferentSchemes = "different_schemes"
	relayImportWarnRefPaths         = "ref_paths"
	relayImportWarnNoMethods        = "no_methods"
	relayImportWarnServerQuery      = "server_query"
)

// relayAuthSuggestion is the auth one security requirement maps onto, or why it maps onto none.
type relayAuthSuggestion struct {
	auth    *schemas.RelayAuthConfig
	warning *RelayImportWarning
}

// ImportRelay maps a parsed document onto relay fields. Every operation whose method a relay
// route serves becomes a route, whether or not it could become an MCP tool: a relay forwards the
// call as sent, so parameters and bodies the tool synthesizer refuses do not matter here.
func ImportRelay(doc *Document, specURL string) RelayImport {
	result := RelayImport{
		Title:          doc.Title,
		Version:        doc.Version,
		OpenAPIVersion: doc.OpenAPIVersion,
		Routes:         []schemas.RelayRouteConfig{},
		Warnings:       []RelayImportWarning{},
	}
	seen := make(map[string]struct{}, len(doc.Operations))
	var suggestions []relayAuthSuggestion
	for _, op := range doc.Operations {
		if !schemas.IsRelayRouteMethod(op.Method) {
			continue
		}
		route := relayRouteFromTemplate(op.Method, op.Path)
		key := relayRouteKey(route)
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		result.Routes = append(result.Routes, route)
		// The requirements the operation is under: its own when it declares any, else the
		// document's. A public operation (an empty list, or an empty alternative) needs no key.
		reqs, declared := doc.GlobalSecurity, doc.HasGlobalSecurity
		if op.HasSecurity {
			reqs, declared = op.Security, true
		}
		if declared && (len(reqs) == 0 || slices.ContainsFunc(reqs, func(r SecurityRequirement) bool { return len(r) == 0 })) {
			continue
		}
		if declared {
			suggestions = append(suggestions, relayAuthFromRequirements(doc, reqs))
		} else {
			suggestions = append(suggestions, relayAuthFromLoneScheme(doc))
		}
	}
	if len(doc.SkippedPaths) > 0 {
		result.Partial = true
		result.Warnings = append(result.Warnings, RelayImportWarning{
			Code:    relayImportWarnRefPaths,
			Message: fmt.Sprintf("%s could not be resolved, so those operations were not imported; add their routes by hand", strings.Join(doc.SkippedPaths, ", ")),
			Paths:   doc.SkippedPaths,
		})
	} else if len(result.Routes) == 0 {
		result.Warnings = append(result.Warnings, RelayImportWarning{Code: relayImportWarnNoMethods, Message: "No operation in the document uses a method the relay serves"})
	}
	// One relay injects one auth, so it is suggested only when every operation that needs a key
	// agrees. When routes were read and none needs a key, nothing is inferred.
	if len(suggestions) == 0 && len(result.Routes) == 0 {
		if doc.HasGlobalSecurity {
			suggestions = append(suggestions, relayAuthFromRequirements(doc, doc.GlobalSecurity))
		} else {
			suggestions = append(suggestions, relayAuthFromLoneScheme(doc))
		}
	}
	if len(suggestions) > 0 {
		first := suggestions[0]
		agree := !slices.ContainsFunc(suggestions[1:], func(s relayAuthSuggestion) bool { return !sameRelayAuth(s.auth, first.auth) })
		switch {
		case !agree:
			result.Warnings = append(result.Warnings, RelayImportWarning{Code: relayImportWarnDifferentSchemes, Message: "The operations require different security schemes, and one relay applies one auth; set the auth type by hand"})
		default:
			result.Auth = first.auth
			if first.warning != nil {
				result.Warnings = append(result.Warnings, *first.warning)
			}
		}
	}
	// The base URL is resolved as for an MCP OpenAPI server; a relay base URL cannot carry a query,
	// so one the server declares is left out and named.
	if base, _, err := ResolveBaseURL(doc, nil, specURL); err == nil {
		if u, err := url.Parse(base); err == nil {
			if u.RawQuery != "" {
				query := "?" + u.RawQuery
				result.Warnings = append(result.Warnings, RelayImportWarning{
					Code:    relayImportWarnServerQuery,
					Message: fmt.Sprintf("The server URL's query (%s) is left out of the suggested base URL, since a relay base URL cannot carry one; callers must send it themselves.", query),
					Detail:  query,
				})
			}
			u.RawQuery, u.ForceQuery, u.Fragment, u.RawFragment = "", false, "", ""
			result.BaseURL = strings.TrimSuffix(u.String(), "/")
		}
	}
	return result
}

// relayAuthFromRequirements maps a requirement list onto relay auth. The alternatives are tried in
// order and the first that is one supported scheme wins; schemes inside one alternative are required
// together, which one injected credential cannot satisfy.
func relayAuthFromRequirements(doc *Document, reqs []SecurityRequirement) relayAuthSuggestion {
	if len(doc.SecuritySchemes) == 0 {
		return relayAuthSuggestion{}
	}
	for _, req := range reqs {
		if len(req) == 0 {
			return relayAuthSuggestion{warning: &RelayImportWarning{Code: relayImportWarnAuthOptional, Message: "The document makes authentication optional; set the auth type by hand if the API needs a key"}}
		}
	}
	var firstWarning *RelayImportWarning
	for _, req := range reqs {
		if len(req) != 1 {
			continue
		}
		for name := range req {
			suggestion := relayAuthFromScheme(doc, name)
			if suggestion.auth != nil {
				return suggestion
			}
			if firstWarning == nil {
				firstWarning = suggestion.warning
			}
		}
	}
	if firstWarning == nil {
		firstWarning = &RelayImportWarning{Code: relayImportWarnNoSingleScheme, Message: "No security alternative in the document is a single scheme the relay can inject; set the auth type by hand"}
	}
	return relayAuthSuggestion{warning: firstWarning}
}

// relayAuthFromLoneScheme is the suggestion for an operation under no declared requirement: a lone
// declared scheme is clear, several are not.
func relayAuthFromLoneScheme(doc *Document) relayAuthSuggestion {
	switch len(doc.SecuritySchemes) {
	case 0:
		return relayAuthSuggestion{}
	case 1:
		for name := range doc.SecuritySchemes {
			return relayAuthFromScheme(doc, name)
		}
	}
	return relayAuthSuggestion{warning: &RelayImportWarning{Code: relayImportWarnSeveralSchemes, Message: "The document declares several security schemes and requires none; set the auth type by hand"}}
}

// relayAuthFromScheme maps one parsed security scheme onto a relay auth type. An apiKey is sent
// verbatim, even in Authorization; only an http bearer scheme adds the "Bearer " prefix.
func relayAuthFromScheme(doc *Document, name string) relayAuthSuggestion {
	scheme, ok := doc.SecuritySchemes[name]
	if !ok {
		return relayAuthSuggestion{warning: &RelayImportWarning{Code: relayImportWarnSchemeUndeclared, Name: name, Message: fmt.Sprintf("Security scheme %s is required but not declared; set the auth type by hand", name)}}
	}
	switch {
	case scheme.Type == "apiKey" && scheme.Supported && scheme.In == InHeader:
		return relayAuthSuggestion{auth: &schemas.RelayAuthConfig{Type: schemas.RelayAuthHeader, HeaderName: scheme.ParamName}}
	case scheme.Type == "apiKey" && scheme.Supported && scheme.In == InQuery:
		return relayAuthSuggestion{auth: &schemas.RelayAuthConfig{Type: schemas.RelayAuthQuery, QueryParam: scheme.ParamName}}
	case scheme.Type == "apiKey":
		return relayAuthSuggestion{warning: &RelayImportWarning{Code: relayImportWarnAPIKeyLocation, Name: name, Detail: scheme.In, Message: fmt.Sprintf("Security scheme %s uses an apiKey in %q, which the relay cannot inject", name, scheme.In)}}
	case scheme.Type == "http" && scheme.Scheme == "bearer":
		return relayAuthSuggestion{auth: &schemas.RelayAuthConfig{Type: schemas.RelayAuthBearer}}
	case scheme.Type == "http" && scheme.Scheme == "basic":
		return relayAuthSuggestion{auth: &schemas.RelayAuthConfig{Type: schemas.RelayAuthBasic}}
	case scheme.Type == "http":
		return relayAuthSuggestion{warning: &RelayImportWarning{Code: relayImportWarnHTTPScheme, Name: name, Detail: scheme.Scheme, Message: fmt.Sprintf("Security scheme %s uses HTTP %s auth, which the relay cannot inject", name, scheme.Scheme)}}
	}
	return relayAuthSuggestion{warning: &RelayImportWarning{Code: relayImportWarnSchemeType, Name: name, Detail: scheme.Type, Message: fmt.Sprintf("Security scheme %s is %s; set the auth type by hand", name, scheme.Type)}}
}

func sameRelayAuth(a, b *schemas.RelayAuthConfig) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// relayRouteKey is a route's identity for de-duplication: two templates can become one glob
// (/a/{x} and /a/{y}).
func relayRouteKey(route schemas.RelayRouteConfig) string {
	key := route.Method + " " + route.Path
	for name, value := range route.Headers {
		key += "\n" + strings.ToLower(name) + ": " + value
	}
	return key
}

// relayRouteFromTemplate turns an OpenAPI path key into a route. AWS specifications key every
// operation of a JSON-protocol API as "/#X-Amz-Target=Service.Op": the operation is a header, not a
// path, so such a key becomes a route on the path before the "#" with a header matcher.
func relayRouteFromTemplate(method, template string) schemas.RelayRouteConfig {
	route := schemas.RelayRouteConfig{Method: method}
	base, fragment, hasFragment := strings.Cut(template, "#")
	if !hasFragment {
		route.Path = relayPathGlob(template)
		return route
	}
	if base == "" {
		base = "/"
	}
	route.Path = relayPathGlob(base)
	if name, value, ok := strings.Cut(fragment, "="); ok && name != "" {
		route.Headers = map[string]string{name: value}
	}
	return route
}

// relayTemplateParam matches one {param} of a path template.
var relayTemplateParam = regexp.MustCompile(`\{[^}]*\}`)

// relayGlobMeta escapes the characters the relay's segment matcher (path.Match) treats specially,
// so literal text in a template matches only itself.
var relayGlobMeta = strings.NewReplacer(`\`, `\\`, `*`, `\*`, `?`, `\?`, `[`, `\[`, `]`, `\]`)

// relayPathGlob turns an OpenAPI path template into a route glob: every {param}, whether it is the
// whole segment ({id}) or part of one ({id}.json, {model}:predict), becomes *. Literal text is
// matched against the decoded request path, so it is percent-decoded (text that is not valid
// encoding is kept as written) and then escaped.
func relayPathGlob(template string) string {
	if !strings.HasPrefix(template, "/") {
		template = "/" + template
	}
	segments := strings.Split(template, "/")
	for i, segment := range segments {
		literals := relayTemplateParam.Split(segment, -1)
		kept := literals[:0]
		for j, literal := range literals {
			// Adjacent parameters ({name}{ext}) leave an empty literal between them; dropping it makes
			// them one *, since ** is refused mid-path and matches every path below at the end.
			if literal == "" && j != 0 && j != len(literals)-1 {
				continue
			}
			if decoded, err := url.PathUnescape(literal); err == nil && utf8.ValidString(decoded) {
				literal = decoded
			}
			kept = append(kept, relayGlobMeta.Replace(literal))
		}
		segments[i] = strings.Join(kept, "*")
	}
	return strings.Join(segments, "/")
}
