package schemas

import "encoding/json"

// BifrostExtractionRequest asks a model to read typed fields out of some text
// (Input) and optional images: an invoice number, a due date, a total, an
// estimate. Fields are keyed by name, and the response answers each one under
// the same name. The shape follows Cloudflare's Clairic, the first model that
// serves extraction.
type BifrostExtractionRequest struct {
	Provider       ModelProvider                  `json:"provider"`
	Model          string                         `json:"model"`
	Input          string                         `json:"input"`
	Fields         map[string]ExtractionFieldSpec `json:"fields"`
	Images         []ExtractionImage              `json:"images,omitempty"`
	Params         *ExtractionParameters          `json:"params,omitempty"`
	Fallbacks      []Fallback                     `json:"fallbacks,omitempty"`
	RawRequestBody []byte                         `json:"-"`
	ExtraParams    map[string]interface{}         `json:"-"` // native extensions; sent only under the passthrough-extra-params flag
}

// GetRawRequestBody returns the raw request body for the extraction request.
func (r *BifrostExtractionRequest) GetRawRequestBody() []byte {
	return r.RawRequestBody
}

// ExtractionFieldType is the kind of value a field holds.
type ExtractionFieldType string

const (
	ExtractionFieldTypeText     ExtractionFieldType = "text"     // a span of the input
	ExtractionFieldTypeNumber   ExtractionFieldType = "number"   // a number, optionally with a unit
	ExtractionFieldTypeDate     ExtractionFieldType = "date"     // an ISO date
	ExtractionFieldTypeTime     ExtractionFieldType = "time"     // a time of day
	ExtractionFieldTypeDatetime ExtractionFieldType = "datetime" // an ISO date and time
	ExtractionFieldTypeDuration ExtractionFieldType = "duration" // a length of time in Unit
	ExtractionFieldTypeList     ExtractionFieldType = "list"     // a list of Items values
	ExtractionFieldTypeEstimate ExtractionFieldType = "estimate" // a distribution over a Min..Max grid
	ExtractionFieldTypeForecast ExtractionFieldType = "forecast" // one estimate per step over Horizon steps
)

// ExtractionFieldSpec describes one field to extract. Only Type is required;
// the rest apply to the field types noted on each member. Min, Max, and Step
// are a number, or an ISO date for date estimates ("month" or "year" for
// Step), so they are kept as the JSON the caller sent.
type ExtractionFieldSpec struct {
	Type          ExtractionFieldType `json:"type"`
	Instructions  *string             `json:"instructions,omitempty"`   // what the field should contain
	Unit          *string             `json:"unit,omitempty"`           // unit label; for durations, seconds through years
	Integer       *bool               `json:"integer,omitempty"`        // numbers and durations
	Decimals      *int                `json:"decimals,omitempty"`       // numbers, durations, and number lists
	Items         *string             `json:"items,omitempty"`          // list item type: text, number, or date
	Min           json.RawMessage     `json:"min,omitempty"`            // estimates and forecasts
	Max           json.RawMessage     `json:"max,omitempty"`            // estimates and forecasts
	Step          json.RawMessage     `json:"step,omitempty"`           // estimate grid step
	Scale         *string             `json:"scale,omitempty"`          // estimate grid: linear or log
	NullThreshold *float64            `json:"null_threshold,omitempty"` // probability of null above which the value is null
	Horizon       *int                `json:"horizon,omitempty"`        // forecasts: number of steps
	Interval      *string             `json:"interval,omitempty"`       // forecasts: step length
}

// ExtractionImage is one image read before the input: a base64 data URL, or
// base64 data with its content type. Exactly one form is set.
type ExtractionImage struct {
	URL         *string
	ContentType string
	Base64      string
}

// MarshalJSON emits the data URL as a string, or the base64 form as an object.
func (img ExtractionImage) MarshalJSON() ([]byte, error) {
	if img.URL != nil {
		return Marshal(*img.URL)
	}
	return Marshal(struct {
		ContentType string `json:"content_type"`
		Base64      string `json:"base64"`
	}{img.ContentType, img.Base64})
}

// UnmarshalJSON reads a string as a data URL and an object as base64 data
// with its content type.
func (img *ExtractionImage) UnmarshalJSON(data []byte) error {
	*img = ExtractionImage{}
	var url string
	if err := Unmarshal(data, &url); err == nil {
		img.URL = &url
		return nil
	}
	var object struct {
		ContentType string `json:"content_type"`
		Base64      string `json:"base64"`
	}
	if err := Unmarshal(data, &object); err != nil {
		return err
	}
	img.ContentType = object.ContentType
	img.Base64 = object.Base64
	return nil
}

// ExtractionParameters holds the optional request settings.
type ExtractionParameters struct {
	ReferenceDate    *string `json:"reference_date,omitempty"`    // ISO date that anchors dates written without a year
	IncludeQuantiles *bool   `json:"include_quantiles,omitempty"` // add 99 percentiles to estimates
	ReturnStateText  *bool   `json:"return_state_text,omitempty"` // return the text that spans index into
}

// ExtractionAnswer is the answer for one field. Value is null when the input
// does not determine it; Guess is the best value regardless. Value, Guess, and
// the distribution members are a string, number, list, or date depending on
// the field type, so they are kept as the JSON the provider sent.
type ExtractionAnswer struct {
	Type       ExtractionFieldType `json:"type"`
	Value      json.RawMessage     `json:"value"`
	Guess      json.RawMessage     `json:"guess,omitempty"`
	PNull      *float64            `json:"p_null,omitempty"`     // probability the input does not determine the value
	Confidence *float64            `json:"confidence,omitempty"` // probability of the reported value
	Span       []int               `json:"span,omitempty"`       // [start, end) of a text answer in the input
	Spans      [][]int             `json:"spans,omitempty"`      // one span per list item
	Mean       json.RawMessage     `json:"mean,omitempty"`
	Median     json.RawMessage     `json:"median,omitempty"`
	P10        json.RawMessage     `json:"p10,omitempty"`
	P90        json.RawMessage     `json:"p90,omitempty"`
	PBelow     *float64            `json:"p_below,omitempty"` // probability mass below the estimate grid
	PAbove     *float64            `json:"p_above,omitempty"` // probability mass above the estimate grid
	Quantiles  json.RawMessage     `json:"quantiles,omitempty"`
	Steps      json.RawMessage     `json:"steps,omitempty"` // forecasts: one estimate per step
}

// BifrostExtractionResponse holds one answer per requested field, keyed by the
// field's name. Truncated reports that the input did not fit and was cut;
// StateText is the text that spans index into, when it was asked for.
type BifrostExtractionResponse struct {
	ID          string                      `json:"id,omitempty"`
	Model       string                      `json:"model"`
	Answers     map[string]ExtractionAnswer `json:"answers"`
	Truncated   bool                        `json:"truncated"`
	StateText   *string                     `json:"state_text,omitempty"`
	Usage       *BifrostLLMUsage            `json:"usage,omitempty"`
	ExtraFields BifrostResponseExtraFields  `json:"extra_fields"`
}
