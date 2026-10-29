package openapimcp

import (
	"net/http"
	"net/url"
	"sort"
	"strings"

	"github.com/maximhq/bifrost/core/schemas"
)

// SelectSecurity picks the schemes to apply to a call: the operation's own
// requirements when it declares any, else the document's. The first requirement
// whose schemes are all supported and all have a configured credential wins; an
// empty requirement means the operation accepts anonymous calls and selects
// nothing. A header-carried scheme also counts as met when supplied already
// carries its header (one each caller forwards, with no stored credential); it
// is then left to that header and not returned. oauth2 and openIdConnect
// schemes never have a stored credential: they are met only by an
// Authorization header in supplied, which is the client's OAuth bearer when its
// auth type is oauth or per_user_oauth. When no requirement is
// satisfiable the call is sent without spec-mapped credentials (static headers
// from the admin still apply).
func SelectSecurity(op Operation, doc *Document, creds map[string]schemas.MCPOpenAPICredential, supplied http.Header) []SecurityScheme {
	if doc == nil {
		return nil
	}
	reqs := doc.GlobalSecurity
	if op.HasSecurity {
		reqs = op.Security
	}
	for _, req := range reqs {
		if len(req) == 0 {
			return nil
		}
		var chosen []SecurityScheme
		satisfiable := true
		for name := range req {
			scheme, ok := doc.SecuritySchemes[name]
			if !ok || !scheme.Supported {
				satisfiable = false
				break
			}
			if !hasCredential(scheme, creds[name]) {
				if header := schemeHeader(scheme); header != "" && supplied.Get(header) != "" {
					continue
				}
				satisfiable = false
				break
			}
			chosen = append(chosen, scheme)
		}
		if satisfiable {
			sortSchemes(chosen)
			return chosen
		}
	}
	return nil
}

// schemeHeader names the request header a scheme's credential travels in, or
// "" when it is sent in the query or a cookie.
func schemeHeader(scheme SecurityScheme) string {
	switch {
	case scheme.Type == "apiKey" && scheme.In == InHeader:
		return scheme.ParamName
	case scheme.Type == "http", isOAuthScheme(scheme):
		return "Authorization"
	}
	return ""
}

func sortSchemes(s []SecurityScheme) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j-1].Name > s[j].Name; j-- {
			s[j-1], s[j] = s[j], s[j-1]
		}
	}
}

// isOAuthScheme reports whether scheme's bearer comes from the client's OAuth
// token rather than a stored credential.
func isOAuthScheme(scheme SecurityScheme) bool {
	return scheme.Type == "oauth2" || scheme.Type == "openIdConnect"
}

func hasCredential(scheme SecurityScheme, cred schemas.MCPOpenAPICredential) bool {
	if isOAuthScheme(scheme) {
		return false
	}
	if scheme.Type == "http" && scheme.Scheme == "basic" {
		return secretValue(cred.Username) != ""
	}
	return secretValue(cred.Value) != ""
}

func secretValue(v *schemas.SecretVar) string {
	if v == nil {
		return ""
	}
	return v.GetValue()
}

// InjectCredentials applies the chosen schemes to the outgoing request: apiKey
// header/query/cookie, http bearer and http basic. Query credentials are added
// to q (the caller encodes it into the URL afterwards); cookies are appended to
// cookies as one name and one value each. Spec-mapped credentials win over same-named headers from the
// resolver, since they are the explicit instruction for this upstream.
func InjectCredentials(req *http.Request, q url.Values, cookies *[]*http.Cookie, schemes []SecurityScheme, creds map[string]schemas.MCPOpenAPICredential) {
	for _, scheme := range schemes {
		cred := creds[scheme.Name]
		switch {
		case scheme.Type == "apiKey":
			value := secretValue(cred.Value)
			switch scheme.In {
			case InHeader:
				req.Header.Set(scheme.ParamName, value)
			case InQuery:
				q.Set(scheme.ParamName, value)
			case InCookie:
				*cookies = append(*cookies, &http.Cookie{Name: scheme.ParamName, Value: value})
			}
		case scheme.Type == "http" && scheme.Scheme == "bearer":
			token := secretValue(cred.Value)
			if !strings.HasPrefix(strings.ToLower(token), "bearer ") {
				token = "Bearer " + token
			}
			req.Header.Set("Authorization", token)
		case scheme.Type == "http" && scheme.Scheme == "basic":
			req.SetBasicAuth(secretValue(cred.Username), secretValue(cred.Password))
		}
	}
}

// OAuthEndpoints are the authorization code endpoints and scopes a document's
// oauth2 scheme declares, used to default an OAuth client's oauth_config.
type OAuthEndpoints struct {
	SchemeName   string
	AuthorizeURL string
	TokenURL     string
	Scopes       []string
}

// SpecOAuthEndpoints picks the first oauth2 scheme, by name, whose
// authorization code flow declares both endpoints. Scopes are the ones the
// document's security requirements ask of that scheme (globally and per
// operation), not every scope it declares, so consent asks for what the
// operations use. ok is false when no scheme qualifies.
func SpecOAuthEndpoints(doc *Document) (OAuthEndpoints, bool) {
	if doc == nil {
		return OAuthEndpoints{}, false
	}
	for _, name := range sortedSchemeNames(doc.SecuritySchemes) {
		scheme := doc.SecuritySchemes[name]
		if scheme.Type != "oauth2" || scheme.OAuth == nil || scheme.OAuth.AuthorizationURL == "" || scheme.OAuth.TokenURL == "" {
			continue
		}
		seen := map[string]bool{}
		var scopes []string
		collect := func(reqs []SecurityRequirement) {
			for _, req := range reqs {
				for _, scope := range req[name] {
					if scope = strings.TrimSpace(scope); scope != "" && !seen[scope] {
						seen[scope] = true
						scopes = append(scopes, scope)
					}
				}
			}
		}
		collect(doc.GlobalSecurity)
		for _, op := range doc.Operations {
			collect(op.Security)
		}
		sort.Strings(scopes)
		return OAuthEndpoints{SchemeName: name, AuthorizeURL: scheme.OAuth.AuthorizationURL, TokenURL: scheme.OAuth.TokenURL, Scopes: scopes}, true
	}
	return OAuthEndpoints{}, false
}

// Fill sets each of authorizeURL, tokenURL and scopes that is empty from e, the
// way OAuth discovery fills only the endpoints an MCP server's admin left out.
func (e OAuthEndpoints) Fill(authorizeURL, tokenURL *string, scopes *[]string) {
	if strings.TrimSpace(*authorizeURL) == "" {
		*authorizeURL = e.AuthorizeURL
	}
	if strings.TrimSpace(*tokenURL) == "" {
		*tokenURL = e.TokenURL
	}
	if len(*scopes) == 0 && len(e.Scopes) > 0 {
		*scopes = append([]string(nil), e.Scopes...)
	}
}
