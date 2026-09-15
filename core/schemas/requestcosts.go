package schemas

// CostCalculation is the result of one pricing calculation. A nil amount means
// no cost could be established; a non-nil zero is a calculated zero cost.
// An incomplete calculation may still contain a known partial amount.
type CostCalculation struct {
	AmountUSD  *float64
	IsComplete bool
}

// RequestCost describes the cost recorded for one logged request attempt.
type RequestCost struct {
	RequestID       string   `json:"request_id"`
	ParentRequestID string   `json:"parent_request_id,omitempty"`
	Provider        string   `json:"provider"`
	Model           string   `json:"model"`
	AmountUSD       *float64 `json:"amount_usd"`
	IsComplete      bool     `json:"is_complete"`
}

// RequestCosts is an immutable snapshot of costs logged in the current request
// scope. It includes earlier fallback attempts, even when they failed.
type RequestCosts struct {
	Version    int           `json:"version"`
	Currency   string        `json:"currency"`
	IsComplete bool          `json:"is_complete"`
	Requests   []RequestCost `json:"requests"`
}
