package typesafe

import (
	"context"
	"net"
	"net/http"
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

func TestCreditPricingExactArithmetic(t *testing.T) {
	t.Parallel()
	price, err := parseCreditPrice("0.0025")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ name, credits, want string }{
		{"fractional", "12.4", "0.031"},
		{"large", "1000000000000.125", "2500000000.0003125"},
		{"integer", "4", "0.01"},
		{"zero", "0", "0"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, _, err := price.cost(tc.credits)
			if err != nil || got != tc.want {
				t.Fatalf("cost=%q err=%v want %q", got, err, tc.want)
			}
		})
	}
	// 0.1 * 3 is exactly 0.3 in decimal, which float arithmetic cannot give.
	p2, _ := parseCreditPrice("0.1")
	if got, f, _ := p2.cost("3"); got != "0.3" || f != 0.3 {
		t.Fatalf("0.1*3 = %q / %v", got, f)
	}
}

func TestCreditPricingRejectsMalformed(t *testing.T) {
	t.Parallel()
	price, _ := parseCreditPrice("1.5")
	for _, s := range []string{"", "-1", "1e3", "abc", "1.", ".5", "NaN", "1/2"} {
		if _, _, err := price.cost(s); err == nil {
			t.Errorf("credits %q accepted", s)
		}
	}
	for _, s := range []string{"-1", "0", "x", "1e-3"} {
		if _, err := parseCreditPrice(s); err == nil {
			t.Errorf("price %q accepted", s)
		}
	}
	if p, err := parseCreditPrice(""); p != nil || err != nil {
		t.Errorf("unset price = %v, %v", p, err)
	}
}

func TestIsHostedEndpointMatchesParsedHostOnly(t *testing.T) {
	t.Parallel()
	for u, want := range map[string]bool{
		"https://system-one.dev/v1/systemone":        true,
		"https://api.system-one.dev/v1/systemone":    true,
		"https://API.System-One.dev.:443/v1":         true,
		"https://evil-system-one.dev/v1":             false,
		"https://system-one.dev.example.com/v1":      false,
		"https://example.com/system-one.dev":         false,
		"https://user@example.com/?h=system-one.dev": false,
		"https://api.typesafe.ai/v1/systemone":       false,
		"http://127.0.0.1:8080/v1/systemone":         false,
	} {
		if got := isHostedEndpoint(u); got != want {
			t.Errorf("isHostedEndpoint(%q)=%v want %v", u, got, want)
		}
	}
}

// hostedProvider points a provider configured for a hosted URL at a loopback
// fixture by redirecting the dial.
func hostedProvider(t *testing.T, usd string, handler http.HandlerFunc) *TypesafeProvider {
	t.Helper()
	f, p := newTypesafeFixture(t, func(w http.ResponseWriter, r *http.Request, _ []byte) { handler(w, r) })
	cfg := &schemas.ProviderConfig{NetworkConfig: schemas.NetworkConfig{BaseURL: "http://api.system-one.dev", DefaultRequestTimeoutInSeconds: 10}}
	if usd != "" {
		cfg.TypesafeConfig = &schemas.TypesafeConfig{USDPerCredit: usd}
	}
	hp, err := NewTypesafeProvider(cfg, noopLogger{})
	if err != nil {
		t.Fatal(err)
	}
	addr := strings.TrimPrefix(f.URL, "http://")
	hp.client.Dial = func(string) (net.Conn, error) { return net.Dial("tcp", addr) }
	_ = p
	return hp
}

func hostedDecision(t *testing.T, p *TypesafeProvider) (*schemas.BifrostDecisionResponse, *schemas.BifrostError) {
	t.Helper()
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	return p.Decision(ctx, fixtureKey(), decisionRequest(t, "fixture", map[string]TypesafeQuestion{
		"q": {Type: TypesafeQuestionTypeNoul, Instructions: "Evaluate this state."},
	}))
}

func creditsHandler(credits string, calls *int) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		*calls++
		if credits != "" {
			w.Header().Set(creditsHeader, credits)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(fixtureSuccessBody))
	}
}

func TestHostedDecisionPricedFromCredits(t *testing.T) {
	calls := 0
	p := hostedProvider(t, "0.0025", creditsHandler("12.4", &calls))
	resp, err := hostedDecision(t, p)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	u := resp.Usage
	if u == nil || u.CreditCostUSD != "0.031" || u.Cost == nil || u.Cost.TotalCost != 0.031 {
		t.Fatalf("usage = %+v", u)
	}
	if u.PromptTokens != 3 {
		t.Errorf("token usage lost: %+v", u)
	}
}

func TestHostedDecisionWithoutPriceRejectedBeforeSending(t *testing.T) {
	calls := 0
	p := hostedProvider(t, "", creditsHandler("12.4", &calls))
	_, err := hostedDecision(t, p)
	if err == nil || !strings.Contains(err.Error.Message, "usd_per_credit") {
		t.Fatalf("err = %+v", err)
	}
	if calls != 0 {
		t.Fatalf("request was sent %d times", calls)
	}
}

func TestHostedDecisionMalformedCreditsFails(t *testing.T) {
	for _, bad := range []string{"abc", "-3", "1e2"} {
		calls := 0
		p := hostedProvider(t, "0.01", creditsHandler(bad, &calls))
		if resp, err := hostedDecision(t, p); err == nil || resp != nil || !strings.Contains(err.Error.Message, creditsHeader) {
			t.Errorf("%q: resp=%v err=%+v", bad, resp, err)
		}
	}
}

func TestHostedDecisionWithoutCreditsHeaderKeepsTokenUsage(t *testing.T) {
	calls := 0
	p := hostedProvider(t, "0.01", creditsHandler("", &calls))
	resp, err := hostedDecision(t, p)
	if err != nil || resp.Usage == nil || resp.Usage.CreditCostUSD != "" || resp.Usage.Cost != nil {
		t.Fatalf("resp=%+v err=%v", resp, err)
	}
}

func TestNonHostedIgnoresCreditsHeaderAndPrice(t *testing.T) {
	_, p := newTypesafeFixture(t, func(w http.ResponseWriter, r *http.Request, _ []byte) {
		w.Header().Set(creditsHeader, "99")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(fixtureSuccessBody))
	})
	p.creditPrice, _ = parseCreditPrice("1")
	resp, err := hostedDecision(t, p)
	if err != nil || resp.Usage.CreditCostUSD != "" || resp.Usage.Cost != nil {
		t.Fatalf("resp=%+v err=%v", resp.Usage, err)
	}
}

func TestNewTypesafeProviderRejectsInvalidPrice(t *testing.T) {
	_, err := NewTypesafeProvider(&schemas.ProviderConfig{TypesafeConfig: &schemas.TypesafeConfig{USDPerCredit: "cheap"}}, noopLogger{})
	if err == nil {
		t.Fatal("invalid price accepted")
	}
}
