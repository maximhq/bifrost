package schemas

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

func statusPtr(code int) *int { return &code }

// bifrostErr builds an error Bifrost produced, typed in the nested field core uses.
func bifrostErr(errType string, status *int) *BifrostError {
	return &BifrostError{
		IsBifrostError: true,
		StatusCode:     status,
		Error:          &ErrorField{Type: &errType, Message: "test"},
	}
}

// declared builds an error whose producer stated its category.
func declared(errorType ErrorType, status *int) *BifrostError {
	e := &BifrostError{StatusCode: status}
	e.ExtraFields.ErrorType = errorType
	return e
}

// providerErr builds an upstream failure: not a Bifrost error, status only.
func providerErr(status int) *BifrostError {
	return &BifrostError{IsBifrostError: false, StatusCode: statusPtr(status)}
}

func TestClassifyErrorType(t *testing.T) {
	cases := []struct {
		name        string
		err         *BifrostError
		requestType RequestType
		want        ErrorType
		explanation string
	}{
		// --- nil ---
		{
			name: "nil error has no type",
			err:  nil, requestType: ChatCompletionRequest,
			want:        "",
			explanation: "a success must never be labelled",
		},

		// --- caller ---
		{
			name: "no key serves the model",
			err: func() *BifrostError {
				e := declared(ErrorTypeCallerModelNotAvailable, statusPtr(404))
				e.Type = Ptr(NoKeySupportsModel) // wire contract, unchanged
				return e
			}(),
			requestType: ChatCompletionRequest,
			want:        ErrorTypeCallerModelNotAvailable,
			explanation: "declared, so it is not confused with the provider's own 404 verdict",
		},
		{
			name: "provider rejects an unknown model",
			err:  providerErr(404), requestType: EmbeddingRequest,
			want: ErrorTypeCallerModelUnknown,
		},
		{
			name: "provider 404 with a misleading error type",
			err: &BifrostError{
				StatusCode: statusPtr(404),
				Error:      &ErrorField{Type: Ptr("invalid_request_error"), Message: "The model 'gpt-9' does not exist"},
			},
			requestType: EmbeddingRequest,
			want:        ErrorTypeCallerModelUnknown,
			explanation: "OpenAI labels this invalid_request_error; a provider's use of that string is not trusted",
		},
		{
			name:        "404 on a chat completion is not a model problem",
			err:         providerErr(404),
			requestType: ChatCompletionRequest,
			want:        ErrorTypeCallerInvalidRequest,
			explanation: "chat carries file ids, so a 404 may be a referenced resource",
		},
		{
			name: "404 on a file retrieval is not a model problem",
			err:  providerErr(404), requestType: FileRetrieveRequest,
			want:        ErrorTypeCallerInvalidRequest,
			explanation: "the missing resource is the file; it is still a caller fault, just not a model one",
		},
		{
			name: "404 on passthrough is not a model problem",
			err:  providerErr(404), requestType: PassthroughRequest,
			want: ErrorTypeCallerInvalidRequest,
		},
		{
			name:        "bifrost rejects a malformed request",
			err:         declared(ErrorTypeCallerInvalidRequest, statusPtr(400)),
			requestType: ChatCompletionRequest,
			want:        ErrorTypeCallerInvalidRequest,
		},
		{
			name: "a provider's invalid_request_error is NOT taken at face value",
			err: &BifrostError{
				StatusCode: statusPtr(401),
				Error:      &ErrorField{Type: Ptr("invalid_request_error"), Message: "invalid_api_key"},
			},
			requestType: ChatCompletionRequest,
			want:        ErrorTypeProviderAuthFailed,
			explanation: "OpenAI sends invalid_request_error for a bad key; only a declaration is trusted",
		},
		{
			name: "caller hung up",
			err:  bifrostErr(RequestCancelled, statusPtr(499)), requestType: ChatCompletionRequest,
			want: ErrorTypeCallerCancelled,
		},

		// --- declared by the producer: wins over every inference below ---
		{
			name:        "declared policy refusal beats the 403 it carries",
			err:         declared(ErrorTypePolicyModelBlocked, statusPtr(403)),
			requestType: ChatCompletionRequest,
			want:        ErrorTypePolicyModelBlocked,
			explanation: "a 403 would otherwise read as provider_auth_failed",
		},
		{
			name:        "declared budget refusal beats the 402 it carries",
			err:         declared(ErrorTypePolicyBudgetExceeded, statusPtr(402)),
			requestType: ChatCompletionRequest,
			want:        ErrorTypePolicyBudgetExceeded,
			explanation: "a 402 would otherwise read as provider_billing",
		},
		{
			name:        "declared policy rate limit beats the 429 it carries",
			err:         declared(ErrorTypePolicyRateLimited, statusPtr(429)),
			requestType: ChatCompletionRequest,
			want:        ErrorTypePolicyRateLimited,
			explanation: "our limit, not the upstream's — the distinction the 429 alone destroys",
		},
		{
			name:        "declared queue shed beats the 503 it carries",
			err:         declared(ErrorTypeBifrostDropped, statusPtr(503)),
			requestType: ChatCompletionRequest,
			want:        ErrorTypeBifrostDropped,
			explanation: "our capacity problem, not the upstream's",
		},
		{
			name: "declaration beats a core marker too",
			err: func() *BifrostError {
				e := declared(ErrorTypeCallerModelNotAvailable, statusPtr(404))
				e.Error = &ErrorField{Type: Ptr(RequestTimedOut)}
				return e
			}(),
			requestType: ChatCompletionRequest,
			want:        ErrorTypeCallerModelNotAvailable,
			explanation: "the producer is authoritative; nothing downstream may override it",
		},

		// --- provider ---
		{
			name: "upstream rejected the credential",
			err:  providerErr(401), requestType: ChatCompletionRequest,
			want: ErrorTypeProviderAuthFailed,
		},
		{
			name: "upstream rate limited us",
			err:  providerErr(429), requestType: ChatCompletionRequest,
			want: ErrorTypeProviderRateLimited,
		},
		{
			name: "upstream overloaded (anthropic 529)",
			err:  providerErr(529), requestType: ChatCompletionRequest,
			want: ErrorTypeProviderOverloaded,
		},
		{
			name: "upstream server error",
			err:  providerErr(500), requestType: ChatCompletionRequest,
			want: ErrorTypeProviderServerError,
		},
		{
			name:        "every key dead",
			err:         declared(ErrorTypeProviderCredentialsExhausted, statusPtr(502)),
			requestType: ChatCompletionRequest,
			want:        ErrorTypeProviderCredentialsExhausted,
			explanation: "more specific than the 502 it also carries",
		},
		{
			name: "connection never established",
			err:  bifrostErr(ProviderConnectionFailed, statusPtr(502)), requestType: ChatCompletionRequest,
			want: ErrorTypeProviderConnectionFailed,
		},
		{
			name: "upstream did not answer in time",
			err:  bifrostErr(RequestTimedOut, statusPtr(504)), requestType: ChatCompletionRequest,
			want: ErrorTypeProviderTimeout,
		},

		// --- bifrost ---
		{
			name: "queue full, request shed",
			err:  bifrostErr(RequestDropped, statusPtr(503)), requestType: ChatCompletionRequest,
			want:        ErrorTypeBifrostDropped,
			explanation: "our capacity problem, not the upstream's — must not read as provider_overloaded off the 503",
		},
		{
			name:        "bifrost failed with no type and no status",
			err:         &BifrostError{IsBifrostError: true, Error: &ErrorField{Message: "conversion failed"}},
			requestType: ChatCompletionRequest,
			want:        ErrorTypeBifrostInternal,
		},

		// --- fallback ---
		{
			name:        "unclassifiable failure",
			err:         &BifrostError{IsBifrostError: false, Error: &ErrorField{Message: "something odd"}},
			requestType: ChatCompletionRequest,
			want:        ErrorTypeOther,
			explanation: "never silently dropped; `other` is the signal a value is missing",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ClassifyErrorType(tc.err, tc.requestType); got != tc.want {
				t.Errorf("ClassifyErrorType() = %q, want %q (%s)", got, tc.want, tc.explanation)
			}
		})
	}
}

// An empty error_type would look like a category but mean "we did not look".
func TestClassifyErrorTypeAlwaysLabelsAFailure(t *testing.T) {
	for status := 400; status <= 599; status++ {
		if got := ClassifyErrorType(providerErr(status), ChatCompletionRequest); got == "" {
			t.Fatalf("status %d classified as the empty string", status)
		}
	}
	if got := ClassifyErrorType(&BifrostError{}, ChatCompletionRequest); got == "" {
		t.Error("an error with no status and no type classified as the empty string")
	}
}

// A value outside ErrorTypes means a dashboard built from it misses traffic.
func TestClassifierOnlyReturnsPublishedVocabulary(t *testing.T) {
	published := map[ErrorType]bool{}
	for _, v := range ErrorTypes {
		published[v] = true
	}
	check := func(e *BifrostError, rt RequestType) {
		if got := ClassifyErrorType(e, rt); got != "" && !published[got] {
			t.Errorf("classifier returned %q, which is not in ErrorTypes", got)
		}
	}
	for status := 100; status <= 599; status++ {
		check(providerErr(status), ChatCompletionRequest)
		check(providerErr(status), FileRetrieveRequest)
	}
	for _, marker := range []string{
		RequestCancelled, RequestTimedOut, RequestDropped, ProviderConnectionFailed,
		NoKeySupportsModel, "invalid_request_error", "something_unknown",
	} {
		check(bifrostErr(marker, nil), ChatCompletionRequest)
		check(&BifrostError{Type: &marker}, ChatCompletionRequest)
	}
	check(&BifrostError{IsBifrostError: true}, ChatCompletionRequest)
	check(&BifrostError{}, ChatCompletionRequest)
}

// A declaration must survive verbatim, or declaring is pointless.
func TestDeclarationIsReturnedVerbatim(t *testing.T) {
	// Contradicting statuses, so a classifier preferring inference fails rather than
	// coincidentally passes.
	for _, contradicting := range []*int{nil, statusPtr(403), statusPtr(429), statusPtr(500), statusPtr(404)} {
		for _, want := range ErrorTypes {
			e := declared(want, contradicting)
			// Also plant a conflicting core marker, the other thing that could win.
			e.Error = &ErrorField{Type: Ptr(RequestDropped)}
			if got := ClassifyErrorType(e, ChatCompletionRequest); got != want {
				t.Errorf("declared %q with status %v classified as %q", want, contradicting, got)
			}
		}
	}
}

// A producer typo must not become an undocumented label. It surfaces in _OTHER, which
// is alarmed on, rather than creating a plausible-looking series nobody queries.
func TestInvalidDeclarationFallsBackToOther(t *testing.T) {
	for _, bogus := range []ErrorType{"polciy_model_blocked", "caller_", "made_up", "CALLER_CANCELLED"} {
		e := declared(bogus, statusPtr(429))
		if got := ClassifyErrorType(e, ChatCompletionRequest); got != ErrorTypeOther {
			t.Errorf("declared %q classified as %q, want %q", bogus, got, ErrorTypeOther)
		}
	}
}

// A value nothing declares and no rule infers is dead weight.
func TestEveryVocabularyValueIsReachable(t *testing.T) {
	// Declaration reaches all of them (asserted above); this checks the inference side
	// has not lost one it used to produce.
	inferable := map[ErrorType]bool{}
	record := func(e *BifrostError, rt RequestType) {
		if got := ClassifyErrorType(e, rt); got != "" {
			inferable[got] = true
		}
	}
	for status := 400; status <= 599; status++ {
		record(providerErr(status), EmbeddingRequest)
		record(providerErr(status), ChatCompletionRequest)
		record(providerErr(status), FileRetrieveRequest)
	}
	for _, marker := range []string{RequestCancelled, RequestTimedOut, RequestDropped, ProviderConnectionFailed} {
		record(bifrostErr(marker, nil), ChatCompletionRequest)
	}
	record(&BifrostError{IsBifrostError: true}, ChatCompletionRequest)
	record(&BifrostError{}, ChatCompletionRequest)

	// Declaration-only: no status or marker infers these, because inference could not
	// tell them from a superficially identical upstream failure.
	declarationOnly := map[ErrorType]bool{
		ErrorTypeCallerModelNotAvailable:      true,
		ErrorTypePolicyModelBlocked:           true,
		ErrorTypePolicyProviderBlocked:        true,
		ErrorTypePolicyAccessDenied:           true,
		ErrorTypePolicyBudgetExceeded:         true,
		ErrorTypePolicyRateLimited:            true,
		ErrorTypePolicyToolBlocked:            true,
		ErrorTypeProviderCredentialsExhausted: true,
	}
	for _, v := range ErrorTypes {
		if declarationOnly[v] || inferable[v] {
			continue
		}
		t.Errorf("vocabulary value %q is neither inferable nor marked declaration-only", v)
	}
}

// Alarms match on the prefix, so a value outside the four families is invisible.
func TestVocabularyIsPrefixedByFaultDomain(t *testing.T) {
	for _, v := range ErrorTypes {
		if v == ErrorTypeOther {
			continue
		}
		name := string(v)
		if !strings.HasPrefix(name, "caller_") && !strings.HasPrefix(name, "policy_") &&
			!strings.HasPrefix(name, "provider_") && !strings.HasPrefix(name, "bifrost_") {
			t.Errorf("error type %q has no fault-domain prefix; alarms matching caller_/policy_/provider_/bifrost_ would miss it", v)
		}
	}
}

// validErrorTypes is derived from ErrorTypes, so a value added to one but not the other
// would be rejected as a typo at runtime.
func TestValidErrorTypesCoversTheVocabulary(t *testing.T) {
	for _, v := range ErrorTypes {
		if _, ok := validErrorTypes[v]; !ok {
			t.Errorf("published error type %q is not accepted as a declaration", v)
		}
	}
	if len(validErrorTypes) != len(ErrorTypes) {
		t.Errorf("validErrorTypes has %d entries, ErrorTypes has %d", len(validErrorTypes), len(ErrorTypes))
	}
}

// A duplicate would silently merge two conditions into one series.
func TestVocabularyValuesAreDistinct(t *testing.T) {
	seen := map[ErrorType]bool{}
	for _, v := range ErrorTypes {
		if v == "" {
			t.Error("the vocabulary contains the empty string, which means \"not classified\"")
		}
		if seen[v] {
			t.Errorf("duplicate error type %q", v)
		}
		seen[v] = true
	}
}

// The types most likely to be added to the inclusion list by mistake.
func TestModelAddressedRequestTypesExcludeResourceAddressed(t *testing.T) {
	for _, rt := range []RequestType{
		ListModelsRequest, PassthroughRequest,
		ResponsesRetrieveRequest, ResponsesDeleteRequest, ResponsesCancelRequest,
		BatchCreateRequest, BatchRetrieveRequest, BatchResultsRequest,
		FileUploadRequest, FileRetrieveRequest, FileContentRequest,
		VideoRetrieveRequest, VideoEditRequest, VideoRemixRequest,
		ContainerRetrieveRequest, CachedContentRetrieveRequest, MCPToolExecutionRequest,
	} {
		if RequestTypeAddressesModel(rt) {
			t.Errorf("request type %q addresses a resource other than the model; a 404 on it must not classify as %q", rt, ErrorTypeCallerModelUnknown)
		}
	}
}

// The deliberate answer for every request type: can a 404 on it only mean the model?
// TestEveryRequestTypeIsCategorized fails on a declared RequestType missing here.
var modelAddressingByRequestType = map[string]bool{
	// Only the model is addressable: a 404 can mean nothing else.
	"ModelRetrieveRequest":         true,
	"TextCompletionRequest":        true,
	"TextCompletionStreamRequest":  true,
	"ChatCompletionRequest":        false,
	"ChatCompletionStreamRequest":  false,
	"ResponsesRequest":             false,
	"ResponsesStreamRequest":       false,
	"EmbeddingRequest":             true,
	"SpeechRequest":                true,
	"SpeechStreamRequest":          true,
	"TranscriptionRequest":         true,
	"TranscriptionStreamRequest":   true,
	"ImageGenerationRequest":       true,
	"ImageGenerationStreamRequest": true,
	"ImageEditRequest":             false,
	"ImageEditStreamRequest":       false,
	"ImageVariationRequest":        false,
	"VideoGenerationRequest":       false,
	"RerankRequest":                true,
	"DecisionRequest":              true,
	"OCRRequest":                   false,
	"CountTokensRequest":           true,

	// Addresses a resource id too, so a 404 is ambiguous.
	"ResponsesRetrieveRequest":       false,
	"ResponsesRetrieveStreamRequest": false,
	"ResponsesDeleteRequest":         false,
	"ResponsesCancelRequest":         false,
	"ResponsesInputItemsRequest":     false,
	"VideoEditRequest":               false,
	"VideoRetrieveRequest":           false,
	"VideoDownloadRequest":           false,
	"VideoDeleteRequest":             false,
	"VideoListRequest":               false,
	"VideoRemixRequest":              false,
	"BatchCreateRequest":             false,
	"BatchListRequest":               false,
	"BatchRetrieveRequest":           false,
	"BatchCancelRequest":             false,
	"BatchResultsRequest":            false,
	"BatchDeleteRequest":             false,
	"FileUploadRequest":              false,
	"FileListRequest":                false,
	"FileRetrieveRequest":            false,
	"FileDeleteRequest":              false,
	"FileContentRequest":             false,
	"CachedContentCreateRequest":     false,
	"CachedContentListRequest":       false,
	"CachedContentRetrieveRequest":   false,
	"CachedContentUpdateRequest":     false,
	"CachedContentDeleteRequest":     false,
	"ContainerCreateRequest":         false,
	"ContainerListRequest":           false,
	"ContainerRetrieveRequest":       false,
	"ContainerDeleteRequest":         false,
	"ContainerFileCreateRequest":     false,
	"ContainerFileListRequest":       false,
	"ContainerFileRetrieveRequest":   false,
	"ContainerFileContentRequest":    false,
	"ContainerFileDeleteRequest":     false,

	// Addresses no model at all, or an arbitrary upstream path.
	"ListModelsRequest":        false,
	"PassthroughRequest":       false,
	"PassthroughStreamRequest": false,
	"MCPToolExecutionRequest":  false,
	"UnknownRequest":           false,

	// Internal, or session-oriented surfaces that do not 404 per call.
	"CompactionRequest":         false,
	"WebSocketResponsesRequest": false,
	"RealtimeRequest":           false,
}

// Parses the RequestType constants out of bifrost.go, so the guard reflects what the
// package declares rather than a hand-kept list.
func requestTypeConstantsFromSource(t *testing.T) map[string]string {
	t.Helper()

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "bifrost.go", nil, 0)
	if err != nil {
		t.Fatalf("parsing bifrost.go: %v", err)
	}

	found := map[string]string{}
	ast.Inspect(file, func(n ast.Node) bool {
		spec, ok := n.(*ast.ValueSpec)
		if !ok {
			return true
		}
		ident, ok := spec.Type.(*ast.Ident)
		if !ok || ident.Name != "RequestType" {
			return true
		}
		for i, name := range spec.Names {
			value := ""
			if i < len(spec.Values) {
				if lit, ok := spec.Values[i].(*ast.BasicLit); ok {
					value = lit.Value
				}
			}
			found[name.Name] = value
		}
		return true
	})

	if len(found) == 0 {
		t.Fatal("parsed no RequestType constants; the guard would pass vacuously")
	}
	return found
}

// Adding a RequestType must force a decision: the inclusion list answers false for
// anything absent, and silence is not a decision.
func TestEveryRequestTypeIsCategorized(t *testing.T) {
	declared := requestTypeConstantsFromSource(t)

	for name := range declared {
		if _, ok := modelAddressingByRequestType[name]; !ok {
			t.Errorf("RequestType %q is uncategorized. Can a 404 on it only mean the model?\n"+
				"  true  -> add to modelAddressedRequestTypes in errors.go AND here\n"+
				"  false -> add here only", name)
		}
	}

	// A stale entry would hide the disappearance of a type the classifier still lists.
	for name := range modelAddressingByRequestType {
		if _, ok := declared[name]; !ok {
			t.Errorf("modelAddressingByRequestType has an entry for %q, which is no longer a declared RequestType", name)
		}
	}
}

// The golden answers must match the classifier, or the guard documents nothing.
func TestClassifierMatchesCategorization(t *testing.T) {
	declared := requestTypeConstantsFromSource(t)

	for name, literal := range declared {
		want, ok := modelAddressingByRequestType[name]
		if !ok {
			continue // already reported by TestEveryRequestTypeIsCategorized
		}
		if len(literal) < 2 {
			t.Errorf("RequestType %q has no string value", name)
			continue
		}
		requestType := RequestType(literal[1 : len(literal)-1])

		if got := RequestTypeAddressesModel(requestType); got != want {
			t.Errorf("RequestTypeAddressesModel(%q) = %v, want %v (per modelAddressingByRequestType[%q])",
				requestType, got, want, name)
		}
	}
}
