package schemas

type BifrostPassthroughRequest struct {
	Provider    ModelProvider // provider extracted from path or body, used for key selection when non-empty
	Model       string        // model extracted from path or body, used for key selection when non-empty
	Method      string
	Path        string // stripped path, e.g. "/v1/fine-tuning/jobs"
	RawQuery    string // raw query string, no "?"
	Body        []byte
	SafeHeaders map[string]string // client headers, auth already stripped
}

type BifrostPassthroughResponse struct {
	StatusCode  int
	Headers     map[string]string
	Body        []byte
	ExtraFields BifrostResponseExtraFields
}

type PassthroughLogParams struct {
	Method     string `json:"method"`
	Path       string `json:"path"`      // stripped path, e.g. "/v1/fine-tuning/jobs"
	RawQuery   string `json:"raw_query"` // raw query string, no "?"
	StatusCode int    `json:"status_code"`
}
