package relay

import (
	"errors"
	"math"
	"net/url"
	"strings"
	"unicode/utf8"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/tidwall/gjson"
)

// isJSONContentType reports whether a body is worth scanning for usage. Any JSON-derived media
// type counts, including application/x-amz-json-1.1 (Textract, Translate) and +json suffixes.
func isJSONContentType(contentType string) bool {
	mediaType, _, _ := strings.Cut(contentType, ";")
	return strings.Contains(strings.ToLower(mediaType), "json")
}

// looksJSON reports whether a body is worth scanning as JSON: a JSON media type, or no media type
// and bytes that parse as JSON.
func looksJSON(contentType string, body []byte) bool {
	if isJSONContentType(contentType) {
		return true
	}
	return contentType == "" && gjson.ValidBytes(body)
}

// tokensConfigured reports whether the mapping names any token path. Once it does, the token
// fields are read from the mapping alone: a half-mapped response must not be topped up from a
// usage block the operator chose not to trust.
func (u resolvedUsage) tokensConfigured() bool {
	return u.inputTokensPath != "" || u.outputTokensPath != "" || u.totalTokensPath != ""
}

// tokenPaths returns the configured path for a token field, or nothing: only mapped fields are
// read, since a non-LLM API's reply can carry token-like names that mean something else.
func (u resolvedUsage) tokenPaths(configured string) []string {
	if configured != "" {
		return []string{configured}
	}
	return nil
}

// costPaths returns the configured cost path, or nothing when none is set or the endpoint ignores
// upstream-reported cost.
func (u resolvedUsage) costPaths() []string {
	if u.ignoreUpstreamCost || u.costPath == "" {
		return nil
	}
	return []string{u.costPath}
}

// effectiveModelPath is where the model is read from: the mapping's path, or nothing when unset.
func (u resolvedUsage) effectiveModelPath() string {
	return u.modelPath
}

// firstNumber reads the first non-negative number found at paths. Negative values are treated
// as absent: a count below zero is never a measurement.
func firstNumber(body []byte, paths []string) (float64, bool) {
	for _, p := range paths {
		if v := providerUtils.GetJSONField(body, p); v.Exists() && v.Type == gjson.Number && finiteNonNegative(v.Float()) {
			return v.Float(), true
		}
	}
	return 0, false
}

// firstCost reads a cost that is either a number or an array of numbers (summed), ignoring
// negative entries.
func firstCost(body []byte, paths []string) (float64, bool) {
	for _, p := range paths {
		v := providerUtils.GetJSONField(body, p)
		if !v.Exists() {
			continue
		}
		if v.Type == gjson.Number {
			if !finiteNonNegative(v.Float()) {
				continue
			}
			return v.Float(), true
		}
		if v.IsArray() {
			var total float64
			found := false
			for _, item := range v.Array() {
				if item.Type == gjson.Number && finiteNonNegative(item.Float()) {
					total += item.Float()
					found = true
				}
			}
			// Entries that are each finite can still sum to +Inf, which is not a price.
			if found && finiteNonNegative(total) {
				return total, true
			}
		}
	}
	return 0, false
}

// usageFields is what one body (or one stream event) said about usage. Presence is tracked per
// field so events that each carry part of the picture can be merged.
type usageFields struct {
	input, output, total, cost, units                float64
	hasInput, hasOutput, hasTotal, hasCost, hasUnits bool
}

func (f usageFields) any() bool {
	return f.hasInput || f.hasOutput || f.hasTotal || f.hasCost || f.hasUnits
}

// billsUnits reports whether the mapping prices a quantity.
func (u resolvedUsage) billsUnits() bool {
	return u.costPerUnit > 0 && (u.unitsPath != "" || u.unitsMeasure == schemas.RelayUnitsBodyBytes || u.unitsMeasure == schemas.RelayUnitsBodyChars)
}

// measureUnits reads the billable quantity from a body per the mapping's measure.
func (u resolvedUsage) measureUnits(contentType string, body []byte) (float64, bool) {
	switch u.unitsMeasure {
	case schemas.RelayUnitsBodyBytes:
		// An empty body is a measured zero, so units_minimum still applies to it.
		return float64(len(body)), true
	case schemas.RelayUnitsBodyChars:
		// What per-character APIs bill (Azure Speech counts characters of the SSML): runes, not
		// bytes, so multi-byte text is not over-counted. Any content type, invalid UTF-8 counts
		// byte-wise as Go does.
		return float64(providerUtils.RuneCount(body)), true
	default:
		if !looksJSON(contentType, body) {
			return 0, false
		}
		return sumQuantity(providerUtils.GetJSONField(body, u.unitsPath), u.unitsMeasure == schemas.RelayUnitsChars)
	}
}

// sumQuantity reads the quantity at a path: the number there, or with chars the characters of
// the string there. An array, which a path such as #.Text yields on a batch body, is summed so
// every element is billed rather than none.
func sumQuantity(v gjson.Result, chars bool) (float64, bool) {
	if v.IsArray() {
		var total float64
		found := false
		for _, item := range v.Array() {
			if q, ok := sumQuantity(item, chars); ok {
				total += q
				found = true
			}
		}
		return total, found && !math.IsInf(total, 0)
	}
	if chars {
		if v.Type == gjson.String {
			return float64(utf8.RuneCountInString(v.Str)), true
		}
		return 0, false
	}
	if v.Type == gjson.Number && finiteNonNegative(v.Float()) {
		return v.Float(), true
	}
	return 0, false
}

// finiteNonNegative is the test every measured number passes: a negative value is not a count,
// and an overflowing one (1e400 parses as +Inf) would make the cost unserialisable.
func finiteNonNegative(f float64) bool {
	return f >= 0 && !math.IsInf(f, 0) && !math.IsNaN(f)
}

// requestUnits reads the quantity from the caller's body when the mapping bills request-side units
// (Translate text, TTS markup). It is known before the upstream answers, so it seeds both the
// buffered and the streaming paths. The caller's Content-Type is not consulted: a caller could
// otherwise label a JSON body text/plain and be billed for nothing.
func (u resolvedUsage) requestUnits(body []byte) usageFields {
	var f usageFields
	if u.billsUnits() && u.unitsSource == schemas.RelayUnitsFromRequest {
		f.units, f.hasUnits = u.measureUnits("", body)
	}
	return f
}

// maxRequestJSONDepth bounds the duplicate-key walk over a billed request body.
const maxRequestJSONDepth = 64

// requestBodyError refuses a request that a billable route reading its quantity at a path in the
// caller's body (Translate's Text) cannot measure unambiguously: a body that is not JSON, which
// would otherwise bill nothing, and a body naming one key twice in an object, case-insensitively,
// which the relay would read first-wins while most upstream decoders read last-wins or fold case.
// Whole-body measures need no check.
func (u resolvedUsage) requestBodyError(body []byte) error {
	if !u.billsUnits() || u.unitsSource != schemas.RelayUnitsFromRequest ||
		u.unitsMeasure == schemas.RelayUnitsBodyBytes || u.unitsMeasure == schemas.RelayUnitsBodyChars {
		return nil
	}
	if !gjson.ValidBytes(body) {
		return errors.New("this route bills on " + u.unitsPath + " in the request body, which must be JSON")
	}
	if hasDuplicateKey(gjson.ParseBytes(body), 0) {
		return errors.New("the request body names a key more than once (case-insensitively) or nests deeper than 64 levels, so the billed quantity is ambiguous")
	}
	if caseVariantOnly(body, u.unitsPath) {
		// gjson matches keys exactly while most upstream decoders fold case, so a lone "text" for a
		// "Text" path would be processed upstream and billed nothing here.
		return errors.New("the request body names " + u.unitsPath + " with different letter case; use the exact key so the billed quantity can be read")
	}
	if !finiteNonNegative(u.unitCharge(u.requestUnits(body))) {
		return errors.New("the billed quantity in the request body is too large to price")
	}
	return nil
}

// caseVariantOnly reports whether a plain dotted path finds nothing in body by exact keys but would
// by case-insensitive ones. A batch path (one "#" segment, as in "#.Text") is checked element by
// element, so a single case-variant element in the array is caught; paths using other gjson syntax
// are left to gjson.
func caseVariantOnly(body []byte, path string) bool {
	if path == "" || strings.ContainsAny(path, `\*?|@!`) {
		return false
	}
	if strings.Contains(path, "#") {
		prefix, rest, _ := strings.Cut(path, "#")
		rest = strings.TrimPrefix(rest, ".")
		if strings.Contains(rest, "#") || strings.HasSuffix(prefix, "..") {
			return false
		}
		array := gjson.ParseBytes(body)
		if prefix = strings.TrimSuffix(prefix, "."); prefix != "" {
			array = gjson.GetBytes(body, prefix)
			// The path to the array is a plain dotted path, so a case variant there hides the
			// whole batch the same way; it cannot contain "#", so this takes the non-batch branch.
			if !array.Exists() {
				return caseVariantOnly(body, prefix)
			}
		}
		if !array.IsArray() || rest == "" {
			return false
		}
		variant := false
		array.ForEach(func(_, element gjson.Result) bool {
			if element.IsObject() && caseVariantOnly([]byte(element.Raw), rest) {
				variant = true
				return false
			}
			return true
		})
		return variant
	}
	if gjson.GetBytes(body, path).Exists() {
		return false
	}
	obj := gjson.ParseBytes(body)
	for _, segment := range strings.Split(path, ".") {
		if !obj.IsObject() {
			return false
		}
		var next gjson.Result
		found := false
		obj.ForEach(func(key, value gjson.Result) bool {
			if strings.EqualFold(key.Str, segment) {
				next, found = value, true
				return false
			}
			return true
		})
		if !found {
			return false
		}
		obj = next
	}
	return true
}

// unitCharge is the unit part of a call's cost: billed units times the rate, 0 with no quantity.
func (u resolvedUsage) unitCharge(f usageFields) float64 {
	if !f.hasUnits || u.costPerUnit <= 0 {
		return 0
	}
	return u.billedUnits(f.units) * u.costPerUnit
}

// hasDuplicateKey reports whether any object in v names a key twice, case-insensitively, or v
// nests deeper than maxRequestJSONDepth.
func hasDuplicateKey(v gjson.Result, depth int) bool {
	if depth > maxRequestJSONDepth {
		return true
	}
	dup := false
	if v.IsObject() {
		seen := make(map[string]struct{})
		v.ForEach(func(k, val gjson.Result) bool {
			name := strings.ToLower(k.Str)
			if _, ok := seen[name]; ok {
				dup = true
				return false
			}
			seen[name] = struct{}{}
			dup = hasDuplicateKey(val, depth+1)
			return !dup
		})
	} else if v.IsArray() {
		v.ForEach(func(_, val gjson.Result) bool {
			dup = hasDuplicateKey(val, depth+1)
			return !dup
		})
	}
	return dup
}

// merge overlays the fields o carries onto f.
func (f *usageFields) merge(o usageFields) {
	if o.hasInput {
		f.input, f.hasInput = o.input, true
	}
	if o.hasOutput {
		f.output, f.hasOutput = o.output, true
	}
	if o.hasTotal {
		f.total, f.hasTotal = o.total, true
	}
	if o.hasCost {
		f.cost, f.hasCost = o.cost, true
	}
	if o.hasUnits {
		f.units, f.hasUnits = o.units, true
	}
}

// parse reads the mapped fields from a response body (or one stream event). Non-JSON bodies yield
// nothing, except a response-side body_bytes quantity.
func (u resolvedUsage) parse(contentType string, body []byte) usageFields {
	var f usageFields
	if u.billsUnits() && u.unitsSource == schemas.RelayUnitsFromResponse {
		f.units, f.hasUnits = u.measureUnits(contentType, body)
	}
	if len(body) == 0 || !looksJSON(contentType, body) {
		return f
	}
	f.input, f.hasInput = firstNumber(body, u.tokenPaths(u.inputTokensPath))
	f.output, f.hasOutput = firstNumber(body, u.tokenPaths(u.outputTokensPath))
	f.total, f.hasTotal = firstNumber(body, u.tokenPaths(u.totalTokensPath))
	f.cost, f.hasCost = firstCost(body, u.costPaths())
	return f
}

// build turns parsed fields into the usage a billable call reports. It never returns nil: an
// empty usage still counts as one request. The cost is the endpoint's formula:
// cost_per_request + the reported cost (cost_path) + billed units * cost_per_unit. Token counts
// are carried for the log row and never priced.
func (u resolvedUsage) build(f usageFields) *schemas.RelayUsage {
	usage := &schemas.RelayUsage{CostPerRequest: u.costPerRequest, CostPerUnit: u.costPerUnit}
	total, hasTotal := f.total, f.hasTotal
	if !hasTotal && (f.hasInput || f.hasOutput) {
		total, hasTotal = f.input+f.output, true
	}
	if hasTotal && total > 0 {
		usage.InputTokens, usage.OutputTokens, usage.TotalTokens = int(f.input), int(f.output), int(total)
	}
	cost := u.costPerRequest
	if f.hasCost && finiteNonNegative(cost+f.cost) {
		cost += f.cost
		usage.ExtractedCost = schemas.Ptr(f.cost)
	}
	if f.hasUnits {
		usage.Units = schemas.Ptr(f.units)
		// A quantity that overflows the block rounding (a huge count over a tiny block) is not a
		// number of units; the row keeps what was read and bills none.
		if billed := u.billedUnits(f.units); finiteNonNegative(billed) {
			usage.BilledUnits = schemas.Ptr(billed)
		}
	}
	// A charge that overflows (a huge quantity, or a tiny block size) is not a price; the request
	// side refuses such a body before relaying it (requestBodyError), so only an upstream-reported
	// quantity can reach this, and it bills no unit charge rather than +Inf. The sum is checked
	// too: two finite charges can still overflow together, and a cost must be representable.
	if charge := u.unitCharge(f); finiteNonNegative(charge) && finiteNonNegative(cost+charge) {
		cost += charge
	}
	usage.Cost = cost
	return usage
}

// billedUnits applies the mapping's rounding rules to a raw quantity: per started block of
// unitsBlockSize (Comprehend Medical bills 100-character units), and never fewer than
// unitsMinimum. With the defaults (no block, no minimum) the raw quantity is billed exactly.
func (u resolvedUsage) billedUnits(raw float64) float64 {
	units := raw
	if u.unitsBlockSize > 0 {
		units = math.Ceil(raw / u.unitsBlockSize)
	}
	if units < u.unitsMinimum {
		units = u.unitsMinimum
	}
	return units
}

// extractUsage reads tokens, cost and a response-side quantity from a buffered response body and
// merges them over whatever the request already established (request-side units).
func extractUsage(u resolvedUsage, contentType string, body []byte, initial usageFields) *schemas.RelayUsage {
	f := initial
	f.merge(u.parse(contentType, body))
	return u.build(f)
}

// hasUsageEvent is the cheap gate for streams: only events that carry one of the mapped fields are
// fully parsed.
func (u resolvedUsage) hasUsageEvent(event []byte) bool {
	for _, p := range u.eventPaths {
		if providerUtils.GetJSONField(event, p).Exists() {
			return true
		}
	}
	return false
}

// collectEventPaths lists every response path hasUsageEvent probes. It depends only on the
// mapping, so it is built once per resolved usage (and again when ignoreUpstreamCost changes)
// rather than per SSE event.
func (u resolvedUsage) collectEventPaths() []string {
	var paths []string
	paths = append(paths, u.tokenPaths(u.totalTokensPath)...)
	paths = append(paths, u.tokenPaths(u.inputTokensPath)...)
	paths = append(paths, u.tokenPaths(u.outputTokensPath)...)
	paths = append(paths, u.costPaths()...)
	if u.billsUnits() && u.unitsSource == schemas.RelayUnitsFromResponse && u.unitsPath != "" {
		paths = append(paths, u.unitsPath)
	}
	return paths
}

// streamObserver returns the per-event observer for one stream. Each field keeps the last value
// any event reported (running totals, the OpenAI and Anthropic shape), never a sum of per-event
// increments; a stream that reports input tokens on its first event and output tokens on its last
// is billed for both, and an event that carries nothing leaves what was seen alone. initial
// carries what the request already established (request-side units).
func (u resolvedUsage) streamObserver(initial usageFields) func(event []byte) *schemas.RelayUsage {
	observe, _ := u.streamUsage(initial)
	return observe
}

// wholeBodyResponse reports a mapping priced by the size of the reply (body_bytes or body_chars
// read from the response), the one quantity a stream can only know once it has ended.
func (u resolvedUsage) wholeBodyResponse() bool {
	return u.billsUnits() && u.unitsSource == schemas.RelayUnitsFromResponse &&
		(u.unitsMeasure == schemas.RelayUnitsBodyBytes || u.unitsMeasure == schemas.RelayUnitsBodyChars)
}

// streamUsage returns the per-event observer for one stream and, for a mapping priced by the
// size of the reply, the end-of-stream builder that bills the bytes (or runes) forwarded. Both
// share one accumulator, so tokens seen in events and the final size land in the same usage.
func (u resolvedUsage) streamUsage(initial usageFields) (func(event []byte) *schemas.RelayUsage, func(bodyBytes, bodyRunes int64) *schemas.RelayUsage) {
	acc := initial
	wholeBody := u.wholeBodyResponse()
	observe := func(event []byte) *schemas.RelayUsage {
		f := u.eventFields(event)
		if !f.any() {
			return nil
		}
		acc.merge(f)
		return u.build(acc)
	}
	if !wholeBody {
		return observe, nil
	}
	final := func(bodyBytes, bodyRunes int64) *schemas.RelayUsage {
		quantity := bodyBytes
		if u.unitsMeasure == schemas.RelayUnitsBodyChars {
			quantity = bodyRunes
		}
		acc.merge(usageFields{units: float64(quantity), hasUnits: true})
		return u.build(acc)
	}
	return observe, final
}

// eventFields reads the usage fields one SSE event carries. A whole-body measure counts the whole
// reply, never one event: an event's size is not the quantity, so a response-side body_bytes or
// body_chars is left unread per event and streamUsage bills the stream's total at its end. The
// relay's observer and the usage preview both read events through it.
func (u resolvedUsage) eventFields(event []byte) usageFields {
	f := u.parse("application/json", event)
	if u.wholeBodyResponse() {
		f.units, f.hasUnits = 0, false
	}
	return f
}

// defaultUsage is what a billable call reports when nothing could be read from the response:
// the flat fee plus any request-side units.
func (u resolvedUsage) defaultUsage(initial usageFields) *schemas.RelayUsage {
	return u.build(initial)
}

// ReadModel reads the model a JSON request body names at modelPath; an empty path reads none. It
// is lenient on purpose: a non-LLM API has no model, and a body that is not JSON names none. The
// one refusal is a body that names a key of the path more than once in the same object, counting
// case variants, as every other passthrough route refuses: the relay reads the first while most
// upstream decoders take the last or fold case, so governance would check one model and the
// upstream run another. Every object along a plain dotted path is checked; a path using gjson
// escapes, wildcards or modifiers is checked at no level. A lone case variant ("Model") is no
// model, which a model-restricted virtual key refuses anyway, so PascalCase APIs keep working.
// ReadRequestModel reads the model where the mapping says it is: a query parameter of the raw query
// with source query, else a field of the JSON body (ReadModel). A query naming the parameter twice
// is refused like a body naming the key twice: governance and the upstream could read different
// values.
func ReadRequestModel(source schemas.RelayModelSource, body []byte, contentType, rawQuery, modelPath string) (string, error) {
	if source != schemas.RelayModelFromQuery {
		return ReadModel(body, contentType, modelPath)
	}
	if modelPath == "" || rawQuery == "" {
		return "", nil
	}
	// ParseQuery keeps going past a bad segment, so the values it returns with an error may still
	// name the model; a query that does not parse is refused rather than read as carrying none.
	values, err := url.ParseQuery(rawQuery)
	if err != nil {
		return "", errors.New("relay request query is malformed: " + err.Error())
	}
	switch got := values[modelPath]; len(got) {
	case 0:
		return "", nil
	case 1:
		return strings.TrimSpace(got[0]), nil
	default:
		return "", errors.New("relay request query must carry the model parameter at most once (" + modelPath + ")")
	}
}

func ReadModel(body []byte, contentType, modelPath string) (string, error) {
	if modelPath == "" || len(body) == 0 || !looksJSON(contentType, body) {
		return "", nil
	}
	if !strings.ContainsAny(modelPath, `\*?#|@!`) {
		obj := gjson.ParseBytes(body)
		for _, segment := range strings.Split(modelPath, ".") {
			if !obj.IsObject() {
				break
			}
			var next gjson.Result
			seen := 0
			obj.ForEach(func(key, value gjson.Result) bool {
				if strings.EqualFold(key.Str, segment) {
					seen++
					next = value
				}
				return seen < 2
			})
			if seen > 1 {
				return "", errors.New("relay request body must carry the model key at most once (" + modelPath + ")")
			}
			obj = next
		}
	}
	if v := gjson.GetBytes(body, modelPath); v.Type == gjson.String {
		return strings.TrimSpace(v.Str), nil
	}
	return "", nil
}
