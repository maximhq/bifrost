package handlers

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/bytedance/sonic"
	"github.com/valyala/fasthttp"
)

// fakeGithubDeviceFlow stands in for github.com. It answers both device flow endpoints with
// the configured bodies and records the form values it received.
type fakeGithubDeviceFlow struct {
	codeBody  string
	tokenBody string
	forms     []map[string]string
}

func (f *fakeGithubDeviceFlow) start(t *testing.T) {
	t.Helper()
	SetLogger(&mockLogger{})

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Errorf("unreadable form: %v", err)
		}
		form := map[string]string{"path": r.URL.Path, "accept": r.Header.Get("Accept")}
		for name := range r.PostForm {
			form[name] = r.PostForm.Get(name)
		}
		f.forms = append(f.forms, form)

		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/login/device/code":
			_, _ = fmt.Fprint(w, f.codeBody)
		case "/login/oauth/access_token":
			_, _ = fmt.Fprint(w, f.tokenBody)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))

	codeURL, tokenURL, client := githubDeviceCodeURL, githubAccessTokenURL, githubDeviceLoginClient
	githubDeviceCodeURL = server.URL + "/login/device/code"
	githubAccessTokenURL = server.URL + "/login/oauth/access_token"
	githubDeviceLoginClient = server.Client()
	t.Cleanup(func() {
		githubDeviceCodeURL, githubAccessTokenURL, githubDeviceLoginClient = codeURL, tokenURL, client
		server.Close()
	})
}

func deviceLoginCtx(provider, body string) *fasthttp.RequestCtx {
	ctx := newTestRequestCtx(body)
	ctx.SetUserValue("provider", provider)
	return ctx
}

func TestInitiateGithubDeviceLogin(t *testing.T) {
	h := &ProviderHandler{}

	t.Run("returns the user code from GitHub", func(t *testing.T) {
		fake := &fakeGithubDeviceFlow{codeBody: `{"device_code":"dev-1","user_code":"ABCD-1234","verification_uri":"https://github.com/login/device","expires_in":900,"interval":5}`}
		fake.start(t)

		ctx := deviceLoginCtx("github-copilot", `{"client_id":" Ov23example "}`)
		h.initiateGithubDeviceLogin(ctx)

		if ctx.Response.StatusCode() != fasthttp.StatusOK {
			t.Fatalf("status got %d, want 200; body=%s", ctx.Response.StatusCode(), ctx.Response.Body())
		}
		var got GithubDeviceLoginResponse
		if err := sonic.Unmarshal(ctx.Response.Body(), &got); err != nil {
			t.Fatalf("unreadable response: %v", err)
		}
		want := GithubDeviceLoginResponse{DeviceCode: "dev-1", UserCode: "ABCD-1234", VerificationURI: "https://github.com/login/device", ExpiresIn: 900, Interval: 5}
		if got != want {
			t.Errorf("response got %+v, want %+v", got, want)
		}
		if len(fake.forms) != 1 || fake.forms[0]["client_id"] != "Ov23example" || fake.forms[0]["scope"] != githubDeviceLoginScope || fake.forms[0]["accept"] != "application/json" {
			t.Errorf("request to GitHub got %v", fake.forms)
		}
	})

	t.Run("reports why GitHub refused", func(t *testing.T) {
		cases := []struct{ name, codeBody, want string }{
			{
				"device flow is off for the app",
				`{"error":"device_flow_disabled","error_description":"Device Flow must be explicitly enabled for this App"}`,
				"Device Flow must be explicitly enabled",
			},
			// GitHub answers an unknown client ID with this body and a 404.
			{"unknown client ID", `{"error":"Not Found"}`, "Not Found"},
		}
		for _, tc := range cases {
			fake := &fakeGithubDeviceFlow{codeBody: tc.codeBody}
			fake.start(t)

			ctx := deviceLoginCtx("github-copilot", `{"client_id":"Ov23example"}`)
			h.initiateGithubDeviceLogin(ctx)

			if ctx.Response.StatusCode() != fasthttp.StatusBadRequest {
				t.Fatalf("%s: status got %d, want 400; body=%s", tc.name, ctx.Response.StatusCode(), ctx.Response.Body())
			}
			if body := string(ctx.Response.Body()); !contains(body, tc.want) {
				t.Errorf("%s: body got %s, want GitHub's reason", tc.name, body)
			}
		}
	})

	t.Run("rejects a request that cannot start a login", func(t *testing.T) {
		fake := &fakeGithubDeviceFlow{}
		fake.start(t)

		cases := []struct{ name, provider, body string }{
			{"missing client_id", "github-copilot", `{}`},
			{"blank client_id", "github-copilot", `{"client_id":"  "}`},
			{"malformed body", "github-copilot", `{`},
			{"another provider", "openai", `{"client_id":"Ov23example"}`},
		}
		for _, tc := range cases {
			ctx := deviceLoginCtx(tc.provider, tc.body)
			h.initiateGithubDeviceLogin(ctx)
			if ctx.Response.StatusCode() != fasthttp.StatusBadRequest {
				t.Errorf("%s: status got %d, want 400", tc.name, ctx.Response.StatusCode())
			}
		}
		if len(fake.forms) != 0 {
			t.Errorf("GitHub was called %d times, want 0", len(fake.forms))
		}
	})
}

func TestPollGithubDeviceLogin(t *testing.T) {
	h := &ProviderHandler{}
	const body = `{"client_id":"Ov23example","device_code":"dev-1"}`

	cases := []struct {
		name      string
		tokenBody string
		want      GithubDeviceLoginPollResponse
	}{
		{
			"approved",
			`{"access_token":"gho_abc","token_type":"bearer","scope":"read:user"}`,
			GithubDeviceLoginPollResponse{Status: GithubDeviceLoginComplete, AccessToken: "gho_abc"},
		},
		{
			"not approved yet",
			`{"error":"authorization_pending","error_description":"The authorization request is still pending."}`,
			GithubDeviceLoginPollResponse{Status: GithubDeviceLoginPending},
		},
		{
			"polling too fast",
			`{"error":"slow_down","error_description":"Too many requests.","interval":10}`,
			GithubDeviceLoginPollResponse{Status: GithubDeviceLoginPending, Interval: 10},
		},
		{
			"code expired",
			`{"error":"expired_token","error_description":"The device code has expired."}`,
			GithubDeviceLoginPollResponse{Status: GithubDeviceLoginExpired},
		},
		{
			"user refused",
			`{"error":"access_denied","error_description":"The user has denied your application access."}`,
			GithubDeviceLoginPollResponse{Status: GithubDeviceLoginError, Error: "The user has denied your application access."},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake := &fakeGithubDeviceFlow{tokenBody: tc.tokenBody}
			fake.start(t)

			ctx := deviceLoginCtx("github-copilot", body)
			h.pollGithubDeviceLogin(ctx)

			if ctx.Response.StatusCode() != fasthttp.StatusOK {
				t.Fatalf("status got %d, want 200; body=%s", ctx.Response.StatusCode(), ctx.Response.Body())
			}
			var got GithubDeviceLoginPollResponse
			if err := sonic.Unmarshal(ctx.Response.Body(), &got); err != nil {
				t.Fatalf("unreadable response: %v", err)
			}
			if got != tc.want {
				t.Errorf("response got %+v, want %+v", got, tc.want)
			}
			if len(fake.forms) != 1 || fake.forms[0]["device_code"] != "dev-1" || fake.forms[0]["grant_type"] != githubDeviceGrantType {
				t.Errorf("request to GitHub got %v", fake.forms)
			}
		})
	}

	t.Run("requires client_id and device_code", func(t *testing.T) {
		fake := &fakeGithubDeviceFlow{}
		fake.start(t)

		for _, body := range []string{`{"client_id":"Ov23example"}`, `{"device_code":"dev-1"}`, `{`} {
			ctx := deviceLoginCtx("github-copilot", body)
			h.pollGithubDeviceLogin(ctx)
			if ctx.Response.StatusCode() != fasthttp.StatusBadRequest {
				t.Errorf("body %s: status got %d, want 400", body, ctx.Response.StatusCode())
			}
		}
		if len(fake.forms) != 0 {
			t.Errorf("GitHub was called %d times, want 0", len(fake.forms))
		}
	})

	t.Run("an unreachable GitHub is a 502", func(t *testing.T) {
		fake := &fakeGithubDeviceFlow{}
		fake.start(t)
		githubAccessTokenURL = "http://127.0.0.1:1/login/oauth/access_token"

		ctx := deviceLoginCtx("github-copilot", body)
		h.pollGithubDeviceLogin(ctx)

		if ctx.Response.StatusCode() != fasthttp.StatusBadGateway {
			t.Fatalf("status got %d, want 502; body=%s", ctx.Response.StatusCode(), ctx.Response.Body())
		}
	})
}
