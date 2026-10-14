package typesafe

import (
	"fmt"
	"math/big"
	"net/url"
	"regexp"
	"strings"

	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

const (
	// creditsHeader carries the credits a hosted System One decision consumed,
	// as an exact decimal string.
	creditsHeader = "X-System-One-Credits"
	// hostedDomain is the hosted System One domain. Only it and its subdomains
	// bill in credits.
	hostedDomain = "system-one.dev"
)

// decimalPattern is a plain non-negative decimal: no sign, exponent, or
// fraction notation, so the value is exactly what the digits say.
var decimalPattern = regexp.MustCompile(`^[0-9]+(\.[0-9]+)?$`)

// parseDecimal reads a plain non-negative decimal into an exact rational and
// returns the number of fractional digits.
func parseDecimal(s string) (*big.Rat, int, error) {
	if !decimalPattern.MatchString(s) {
		return nil, 0, fmt.Errorf("%q is not a non-negative decimal", s)
	}
	r, ok := new(big.Rat).SetString(s)
	if !ok {
		return nil, 0, fmt.Errorf("%q is not a decimal", s)
	}
	scale := 0
	if i := strings.IndexByte(s, '.'); i >= 0 {
		scale = len(s) - i - 1
	}
	return r, scale, nil
}

// creditPrice is a validated USD-per-credit price.
type creditPrice struct {
	rat   *big.Rat
	scale int
}

// parseCreditPrice validates the configured price. An empty string is unset.
func parseCreditPrice(s string) (*creditPrice, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	r, scale, err := parseDecimal(s)
	if err != nil {
		return nil, fmt.Errorf("typesafe_config.usd_per_credit: %w", err)
	}
	if r.Sign() <= 0 {
		return nil, fmt.Errorf("typesafe_config.usd_per_credit must be greater than zero")
	}
	return &creditPrice{rat: r, scale: scale}, nil
}

// cost multiplies credits (an exact decimal string) by the price in exact
// arithmetic. It returns the exact decimal USD string and its float64
// conversion, made only at the end.
func (p *creditPrice) cost(credits string) (string, float64, error) {
	c, cScale, err := parseDecimal(strings.TrimSpace(credits))
	if err != nil {
		return "", 0, err
	}
	total := new(big.Rat).Mul(c, p.rat)
	// A product of finite decimals is finite: its scale is the sum of scales.
	exact := total.FloatString(cScale + p.scale)
	if strings.Contains(exact, ".") {
		exact = strings.TrimRight(strings.TrimRight(exact, "0"), ".")
	}
	f, _ := total.Float64()
	return exact, f, nil
}

// isHostedEndpoint reports whether rawURL targets hosted System One: the
// parsed host is system-one.dev or a subdomain of it. A substring match never
// qualifies (evil-system-one.dev.example.com does not).
func isHostedEndpoint(rawURL string) bool {
	u, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	host := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	return host == hostedDomain || strings.HasSuffix(host, "."+hostedDomain)
}

// applyCredits prices a hosted decision from the credits header. Without the
// header the native token usage stands. A malformed header is an error: the
// request was charged upstream, so guessing a price is not safe.
func applyCredits(resp *fasthttp.Response, price *creditPrice, out *schemas.BifrostDecisionResponse) error {
	raw := resp.Header.Peek(creditsHeader)
	if len(raw) == 0 {
		return nil
	}
	exact, f, err := price.cost(string(raw))
	if err != nil {
		return fmt.Errorf("invalid %s header: %w", creditsHeader, err)
	}
	if out.Usage == nil {
		out.Usage = &schemas.BifrostLLMUsage{}
	}
	out.Usage.Cost = &schemas.BifrostCost{RequestCost: f, TotalCost: f}
	out.Usage.CreditCostUSD = exact
	return nil
}
