package schemas

type PassthroughRequest struct {
	Method      string
	Path        string // stripped path, e.g. "/v1/fine-tuning/jobs"
	RawQuery    string // raw query string, no "?"
	Body        []byte
	SafeHeaders map[string]string // client headers, auth already stripped
}
