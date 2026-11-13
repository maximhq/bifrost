package handlers

import (
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/bytedance/sonic"
	"github.com/fasthttp/router"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/maximhq/bifrost/framework/traceingest"
	"github.com/maximhq/bifrost/transports/v2/bifrost-http/lib"
	"github.com/valyala/fasthttp"
	collectortrace "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/protobuf/proto"
)

func init() {
	traceIngestAdapters = append(traceIngestAdapters, func(h *TraceIngestHandler, rt *router.Router, middlewares ...schemas.BifrostHTTPMiddleware) {
		// Arize AX and Phoenix. Their exporters post to the endpoint as given or append /v1/traces.
		arize := lib.ChainMiddlewares(func(ctx *fasthttp.RequestCtx) { h.receiveOTLP(ctx, logstore.TraceSourceArize) }, middlewares...)
		rt.POST(tracingPrefix+"/arize", arize)
		rt.POST(tracingPrefix+"/arize/v1/traces", arize)
		// Any other OTLP/HTTP exporter: OpenLLMetry, the Vercel AI SDK, MLflow, LangSmith's OTel export.
		otel := lib.ChainMiddlewares(func(ctx *fasthttp.RequestCtx) { h.receiveOTLP(ctx, logstore.TraceSourceOTel) }, middlewares...)
		rt.POST(tracingPrefix+"/otel", otel)
		rt.POST(tracingPrefix+"/otel/v1/traces", otel)
	})
}

// otlpResource is the spans of one OTLP resource, decoded.
type otlpResource struct {
	Attributes map[string]any
	Spans      []*traceingest.OTelSpan
}

// otlpCredential reads the tracing key from the headers OTLP exporters are configured with:
// Authorization (bare or Bearer), Arize's api_key, Phoenix's and Arize's alternatives.
func otlpCredential(ctx *fasthttp.RequestCtx) string {
	if credential := bearerCredential(string(ctx.Request.Header.Peek("Authorization"))); credential != "" {
		return credential
	}
	for _, name := range []string{"api_key", "arize-api-key", "x-api-key", "x-bf-tracing-key"} {
		if value := strings.TrimSpace(string(ctx.Request.Header.Peek(name))); value != "" {
			return value
		}
	}
	return ""
}

// receiveOTLP handles an OTLP/HTTP trace export, protobuf or JSON, for the given source.
func (h *TraceIngestHandler) receiveOTLP(ctx *fasthttp.RequestCtx, source string) {
	principal, ok := h.authenticate(ctx, otlpCredential(ctx))
	if !ok {
		return
	}
	isJSON := strings.HasPrefix(strings.ToLower(string(ctx.Request.Header.ContentType())), "application/json")
	resources, err := decodeOTLP(ctx.PostBody(), isJSON)
	if err != nil {
		// Only a body that cannot be decoded is refused. A span the mappers do not understand is
		// stored as it is: a client must not retry after a partial success, so nothing decodable
		// is ever rejected.
		SendError(ctx, fasthttp.StatusBadRequest, "malformed OTLP trace export: "+err.Error())
		return
	}
	if err := h.ingestOTLP(ctx, principal, source, resources, string(ctx.Request.Header.Peek("x-project-name"))); err != nil {
		h.fail(ctx, err)
		return
	}
	writeOTLPSuccess(ctx, isJSON)
}

// writeOTLPSuccess answers with an empty ExportTraceServiceResponse in the request's encoding.
func writeOTLPSuccess(ctx *fasthttp.RequestCtx, isJSON bool) {
	ctx.SetStatusCode(fasthttp.StatusOK)
	if isJSON {
		ctx.SetContentType("application/json")
		ctx.SetBodyString("{}")
		return
	}
	ctx.SetContentType("application/x-protobuf")
	body, _ := proto.Marshal(&collectortrace.ExportTraceServiceResponse{})
	ctx.SetBody(body)
}

// otlpProjectAttributes are the resource attributes that name a project, strongest first.
var otlpProjectAttributes = []string{"openinference.project.name", "arize.project.name", "model_id", "service.name"}

// ingestOTLP stores decoded resources. Each resource goes to the project it names. explicitProject,
// from a request header, overrides every resource and must resolve. A name taken from a resource
// attribute is a hint: when the key cannot use it, the next hint is tried, then the key's default.
func (h *TraceIngestHandler) ingestOTLP(ctx *fasthttp.RequestCtx, principal *traceingest.Principal, source string, resources []otlpResource, explicitProject string) error {
	storeCtx, cancel := ingestContext()
	defer cancel()
	byProject := make(map[string][]*traceingest.SpanPart)
	for _, resource := range resources {
		if len(resource.Spans) == 0 {
			continue
		}
		candidates := []string{strings.TrimSpace(explicitProject)}
		if candidates[0] == "" {
			candidates = candidates[:0]
			for _, name := range otlpProjectAttributes {
				if value, ok := resource.Attributes[name].(string); ok && strings.TrimSpace(value) != "" {
					candidates = append(candidates, value)
				}
			}
			candidates = append(candidates, "")
		}
		var projectID string
		var lastErr error
		for _, candidate := range candidates {
			project, err := h.service.ResolveProject(storeCtx, principal, candidate)
			if err == nil {
				projectID = project.ID
				break
			}
			lastErr = err
		}
		if projectID == "" {
			return lastErr
		}
		for _, span := range resource.Spans {
			span.Resource = resource.Attributes
			byProject[projectID] = append(byProject[projectID], traceingest.MapOTelSpan(span))
		}
	}
	for projectID, parts := range byProject {
		project, err := h.service.ResolveProject(storeCtx, principal, projectID)
		if err != nil {
			return err
		}
		if err := h.service.Ingest(storeCtx, principal, project, source, parts); err != nil {
			return err
		}
	}
	return nil
}

// decodeOTLP decodes an ExportTraceServiceRequest. OTLP/JSON is not plain protobuf JSON: ids are
// hex, not base64, and enums may be numbers, so it is decoded by hand.
func decodeOTLP(body []byte, isJSON bool) ([]otlpResource, error) {
	if isJSON {
		return decodeOTLPJSON(body)
	}
	var request collectortrace.ExportTraceServiceRequest
	if err := proto.Unmarshal(body, &request); err != nil {
		return nil, err
	}
	resources := make([]otlpResource, 0, len(request.ResourceSpans))
	for _, resourceSpans := range request.ResourceSpans {
		resource := otlpResource{Attributes: otlpAttributes(resourceSpans.GetResource().GetAttributes())}
		for _, scopeSpans := range resourceSpans.ScopeSpans {
			scope := scopeSpans.GetScope().GetName()
			for _, span := range scopeSpans.Spans {
				if len(span.TraceId) == 0 || len(span.SpanId) == 0 {
					continue
				}
				decoded := &traceingest.OTelSpan{
					TraceID: hex.EncodeToString(span.TraceId), SpanID: hex.EncodeToString(span.SpanId), ParentSpanID: hex.EncodeToString(span.ParentSpanId),
					Name: span.Name, Kind: otlpSpanKind(int(span.Kind)), Scope: scope, Attributes: otlpAttributes(span.Attributes),
					StartTime: otlpTime(span.StartTimeUnixNano), EndTime: otlpTime(span.EndTimeUnixNano),
					StatusError: span.GetStatus().GetCode() == tracepb.Status_STATUS_CODE_ERROR, StatusMessage: span.GetStatus().GetMessage(),
				}
				for _, event := range span.Events {
					decoded.Events = append(decoded.Events, traceingest.OTelEvent{Name: event.Name, Time: otlpTime(event.TimeUnixNano), Attributes: otlpAttributes(event.Attributes)})
				}
				resource.Spans = append(resource.Spans, decoded)
			}
		}
		resources = append(resources, resource)
	}
	return resources, nil
}

func otlpTime(unixNano uint64) time.Time {
	if unixNano == 0 {
		return time.Time{}
	}
	return time.Unix(0, int64(unixNano)).UTC()
}

var otlpSpanKinds = map[int]string{1: "internal", 2: "server", 3: "client", 4: "producer", 5: "consumer"}

func otlpSpanKind(kind int) string { return otlpSpanKinds[kind] }

func otlpAttributes(attributes []*commonpb.KeyValue) map[string]any {
	if len(attributes) == 0 {
		return nil
	}
	values := make(map[string]any, len(attributes))
	for _, attribute := range attributes {
		values[attribute.Key] = otlpValue(attribute.Value)
	}
	return values
}

func otlpValue(value *commonpb.AnyValue) any {
	switch v := value.GetValue().(type) {
	case *commonpb.AnyValue_StringValue:
		return v.StringValue
	case *commonpb.AnyValue_BoolValue:
		return v.BoolValue
	case *commonpb.AnyValue_IntValue:
		return v.IntValue
	case *commonpb.AnyValue_DoubleValue:
		return v.DoubleValue
	case *commonpb.AnyValue_ArrayValue:
		list := make([]any, len(v.ArrayValue.GetValues()))
		for i, item := range v.ArrayValue.GetValues() {
			list[i] = otlpValue(item)
		}
		return list
	case *commonpb.AnyValue_KvlistValue:
		return otlpAttributes(v.KvlistValue.GetValues())
	case *commonpb.AnyValue_BytesValue:
		return base64.StdEncoding.EncodeToString(v.BytesValue)
	}
	return nil
}

// OTLP/JSON shapes. Field names are lowerCamelCase per the OTLP spec; 64-bit integers arrive as
// strings or numbers, and enums as numbers or names.
type otlpJSONRequest struct {
	ResourceSpans []struct {
		Resource struct {
			Attributes []otlpJSONKeyValue `json:"attributes"`
		} `json:"resource"`
		ScopeSpans []struct {
			Scope struct {
				Name string `json:"name"`
			} `json:"scope"`
			Spans []otlpJSONSpan `json:"spans"`
		} `json:"scopeSpans"`
	} `json:"resourceSpans"`
}

type otlpJSONSpan struct {
	TraceID           string             `json:"traceId"`
	SpanID            string             `json:"spanId"`
	ParentSpanID      string             `json:"parentSpanId"`
	Name              string             `json:"name"`
	Kind              any                `json:"kind"`
	StartTimeUnixNano any                `json:"startTimeUnixNano"`
	EndTimeUnixNano   any                `json:"endTimeUnixNano"`
	Attributes        []otlpJSONKeyValue `json:"attributes"`
	Status            struct {
		Code    any    `json:"code"`
		Message string `json:"message"`
	} `json:"status"`
	Events []struct {
		Name         string             `json:"name"`
		TimeUnixNano any                `json:"timeUnixNano"`
		Attributes   []otlpJSONKeyValue `json:"attributes"`
	} `json:"events"`
}

type otlpJSONKeyValue struct {
	Key   string        `json:"key"`
	Value otlpJSONValue `json:"value"`
}

type otlpJSONValue struct {
	StringValue *string  `json:"stringValue"`
	BoolValue   *bool    `json:"boolValue"`
	IntValue    any      `json:"intValue"`
	DoubleValue *float64 `json:"doubleValue"`
	BytesValue  *string  `json:"bytesValue"`
	ArrayValue  *struct {
		Values []otlpJSONValue `json:"values"`
	} `json:"arrayValue"`
	KvlistValue *struct {
		Values []otlpJSONKeyValue `json:"values"`
	} `json:"kvlistValue"`
}

func (v otlpJSONValue) value() any {
	switch {
	case v.StringValue != nil:
		return *v.StringValue
	case v.BoolValue != nil:
		return *v.BoolValue
	case v.IntValue != nil:
		return int64(otlpJSONUint(v.IntValue))
	case v.DoubleValue != nil:
		return *v.DoubleValue
	case v.BytesValue != nil:
		return *v.BytesValue
	case v.ArrayValue != nil:
		list := make([]any, len(v.ArrayValue.Values))
		for i, item := range v.ArrayValue.Values {
			list[i] = item.value()
		}
		return list
	case v.KvlistValue != nil:
		return otlpJSONAttributes(v.KvlistValue.Values)
	}
	return nil
}

func otlpJSONAttributes(attributes []otlpJSONKeyValue) map[string]any {
	if len(attributes) == 0 {
		return nil
	}
	values := make(map[string]any, len(attributes))
	for _, attribute := range attributes {
		values[attribute.Key] = attribute.Value.value()
	}
	return values
}

// otlpJSONUint reads a 64-bit integer sent as a JSON string or number. Negative ints wrap, which
// int64() of the result undoes.
func otlpJSONUint(value any) uint64 {
	switch v := value.(type) {
	case string:
		if n, err := strconv.ParseUint(v, 10, 64); err == nil {
			return n
		}
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			return uint64(n)
		}
	case float64:
		return uint64(int64(v))
	}
	return 0
}

// otlpJSONEnum reads an enum sent as a number or as its name ("SPAN_KIND_CLIENT", "STATUS_CODE_ERROR").
func otlpJSONEnum(value any, names map[string]int) int {
	switch v := value.(type) {
	case float64:
		return int(v)
	case string:
		return names[v]
	}
	return 0
}

var (
	otlpJSONSpanKindNames   = map[string]int{"SPAN_KIND_INTERNAL": 1, "SPAN_KIND_SERVER": 2, "SPAN_KIND_CLIENT": 3, "SPAN_KIND_PRODUCER": 4, "SPAN_KIND_CONSUMER": 5}
	otlpJSONStatusCodeNames = map[string]int{"STATUS_CODE_OK": 1, "STATUS_CODE_ERROR": 2}
)

// otlpJSONID normalises a trace or span id. The spec says hex; some exporters send base64, the
// plain protobuf JSON form, so an id that is not hex of a valid length is tried as base64.
func otlpJSONID(id string) string {
	id = strings.TrimSpace(id)
	if id == "" {
		return ""
	}
	if (len(id) == 32 || len(id) == 16) && isHex(id) {
		return strings.ToLower(id)
	}
	if raw, err := base64.StdEncoding.DecodeString(id); err == nil && (len(raw) == 16 || len(raw) == 8) {
		return hex.EncodeToString(raw)
	}
	return id
}

func isHex(s string) bool {
	_, err := hex.DecodeString(s)
	return err == nil
}

func decodeOTLPJSON(body []byte) ([]otlpResource, error) {
	var request otlpJSONRequest
	if err := sonic.Unmarshal(body, &request); err != nil {
		return nil, fmt.Errorf("invalid JSON: %w", err)
	}
	resources := make([]otlpResource, 0, len(request.ResourceSpans))
	for _, resourceSpans := range request.ResourceSpans {
		resource := otlpResource{Attributes: otlpJSONAttributes(resourceSpans.Resource.Attributes)}
		for _, scopeSpans := range resourceSpans.ScopeSpans {
			for _, span := range scopeSpans.Spans {
				traceID, spanID := otlpJSONID(span.TraceID), otlpJSONID(span.SpanID)
				if traceID == "" || spanID == "" {
					continue
				}
				decoded := &traceingest.OTelSpan{
					TraceID: traceID, SpanID: spanID, ParentSpanID: otlpJSONID(span.ParentSpanID), Name: span.Name, Scope: scopeSpans.Scope.Name,
					Kind: otlpSpanKind(otlpJSONEnum(span.Kind, otlpJSONSpanKindNames)), Attributes: otlpJSONAttributes(span.Attributes),
					StartTime: otlpTime(otlpJSONUint(span.StartTimeUnixNano)), EndTime: otlpTime(otlpJSONUint(span.EndTimeUnixNano)),
					StatusError: otlpJSONEnum(span.Status.Code, otlpJSONStatusCodeNames) == 2, StatusMessage: span.Status.Message,
				}
				for _, event := range span.Events {
					decoded.Events = append(decoded.Events, traceingest.OTelEvent{Name: event.Name, Time: otlpTime(otlpJSONUint(event.TimeUnixNano)), Attributes: otlpJSONAttributes(event.Attributes)})
				}
				resource.Spans = append(resource.Spans, decoded)
			}
		}
		resources = append(resources, resource)
	}
	return resources, nil
}
