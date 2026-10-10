package openapimcp

import (
	"net/http"
	"net/url"
	"strings"

	"github.com/maximhq/bifrost/core/schemas"
)

// SelectSecurity picks the schemes to apply to a call: the operation's own
// requirements when it declares any, else the document's. The first requirement
// whose schemes are all supported and all have a configured credential wins; an
// empty requirement means the operation accepts anonymous calls and selects
// nothing. When no requirement is satisfiable the call is sent without
// spec-mapped credentials (static headers from the admin still apply).
func SelectSecurity(op Operation, doc *Document, creds map[string]schemas.MCPOpenAPICredential) []SecurityScheme {
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
			if !ok || !scheme.Supported || !hasCredential(scheme, creds[name]) {
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

func sortSchemes(s []SecurityScheme) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j-1].Name > s[j].Name; j-- {
			s[j-1], s[j] = s[j], s[j-1]
		}
	}
}

func hasCredential(scheme SecurityScheme, cred schemas.MCPOpenAPICredential) bool {
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
// cookies. Spec-mapped credentials win over same-named headers from the
// resolver, since they are the explicit instruction for this upstream.
func InjectCredentials(req *http.Request, q url.Values, cookies *[]string, schemes []SecurityScheme, creds map[string]schemas.MCPOpenAPICredential) {
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
				*cookies = append(*cookies, scheme.ParamName+"="+url.QueryEscape(value))
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
