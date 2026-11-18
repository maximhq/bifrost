package gemini

import (
	"bytes"
	"encoding/json"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/bytedance/sonic"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
)

// Built-in tool steps. Google runs these tools itself and reports each run as a call step and a
// result step that names the call by call_id. Each pair maps onto the Responses item for the same
// tool, so a Responses caller sees a search, a code run, a fetched page or a remote MCP call the
// way it would from any other provider. Steps with no Responses item (google_maps, processing)
// stay raw under provider_extra_fields.steps.
const (
	GeminiInteractionsStepGoogleSearchCall    = "google_search_call"
	GeminiInteractionsStepGoogleSearchResult  = "google_search_result"
	GeminiInteractionsStepCodeExecutionCall   = "code_execution_call"
	GeminiInteractionsStepCodeExecutionResult = "code_execution_result"
	GeminiInteractionsStepURLContextCall      = "url_context_call"
	GeminiInteractionsStepURLContextResult    = "url_context_result"
	GeminiInteractionsStepFileSearchCall      = "file_search_call"
	GeminiInteractionsStepFileSearchResult    = "file_search_result"
	GeminiInteractionsStepMCPCall             = "mcp_server_tool_call"
	GeminiInteractionsStepMCPResult           = "mcp_server_tool_result"
)

// geminiInteractionsDeltaAnnotations carries citations for the text of a model_output step.
const geminiInteractionsDeltaAnnotations = "text_annotation_delta"

// geminiInteractionsGoogleSearch is the grounding_tool_count type of a Google Search run.
const geminiInteractionsGoogleSearch = "google_search"

// geminiInteractionsToolArguments is the arguments object of a built-in call step.
type geminiInteractionsToolArguments struct {
	Queries  []string `json:"queries,omitempty"`  // google_search_call, file_search_call
	Code     string   `json:"code,omitempty"`     // code_execution_call
	Language string   `json:"language,omitempty"` // code_execution_call
	URLs     []string `json:"urls,omitempty"`     // url_context_call
}

// geminiInteractionsURLResult is one entry of a url_context_result.
type geminiInteractionsURLResult struct {
	URL    string `json:"url"`
	Status string `json:"status"`
}

// geminiInteractionsSearchResult is the part of a google_search_result entry Bifrost reads.
type geminiInteractionsSearchResult struct {
	URL               string `json:"url,omitempty"`
	Title             string `json:"title,omitempty"`
	SearchSuggestions string `json:"search_suggestions,omitempty"` // markup of Google's search widget
}

// geminiSearchSuggestions is the search widget markup a google_search_result step carries.
func geminiSearchSuggestions(step GeminiInteractionsItem) []string {
	if step.Type != GeminiInteractionsStepGoogleSearchResult || len(step.Result) == 0 {
		return nil
	}
	var results []geminiInteractionsSearchResult
	if sonic.Unmarshal(step.Result, &results) != nil {
		return nil
	}
	var out []string
	for _, r := range results {
		if r.SearchSuggestions != "" {
			out = append(out, r.SearchSuggestions)
		}
	}
	return out
}

// geminiInteractionsAnnotation is a citation on a text block: url_citation or file_citation.
// The other documented kinds (place_citation, speech and word annotations) have no Responses
// annotation and are not mapped.
type geminiInteractionsAnnotation struct {
	Type        string  `json:"type"`
	URL         *string `json:"url,omitempty"`
	Title       *string `json:"title,omitempty"`
	DocumentURI *string `json:"document_uri,omitempty"`
	FileName    *string `json:"file_name,omitempty"`
	StartIndex  *int    `json:"start_index,omitempty"`
	EndIndex    *int    `json:"end_index,omitempty"`
}

func isGeminiServerToolCall(stepType string) bool {
	switch stepType {
	case GeminiInteractionsStepGoogleSearchCall, GeminiInteractionsStepCodeExecutionCall, GeminiInteractionsStepURLContextCall,
		GeminiInteractionsStepFileSearchCall, GeminiInteractionsStepMCPCall:
		return true
	}
	return false
}

func isGeminiServerToolResult(stepType string) bool {
	switch stepType {
	case GeminiInteractionsStepGoogleSearchResult, GeminiInteractionsStepCodeExecutionResult, GeminiInteractionsStepURLContextResult,
		GeminiInteractionsStepFileSearchResult, GeminiInteractionsStepMCPResult:
		return true
	}
	return false
}

// isResponsesServerToolItem reports whether a Responses item is a tool the provider ran itself.
func isResponsesServerToolItem(itemType schemas.ResponsesMessageType) bool {
	switch itemType {
	case schemas.ResponsesMessageTypeWebSearchCall, schemas.ResponsesMessageTypeCodeInterpreterCall, schemas.ResponsesMessageTypeWebFetchCall,
		schemas.ResponsesMessageTypeFileSearchCall, schemas.ResponsesMessageTypeMCPCall:
		return true
	}
	return false
}

// geminiServerToolCallToResponses renders a built-in call step as its Responses items, in
// progress until the result step arrives. url_context reads several URLs in one call; each is
// its own web_fetch_call, as a Responses web fetch names one URL.
func geminiServerToolCallToResponses(step GeminiInteractionsItem) []schemas.ResponsesMessage {
	id := ""
	if step.ID != nil {
		id = *step.ID
	}
	var args geminiInteractionsToolArguments
	if len(step.Arguments) > 0 {
		_ = sonic.Unmarshal(step.Arguments, &args)
	}
	item := func(itemType schemas.ResponsesMessageType, itemID string, tool *schemas.ResponsesToolMessage) schemas.ResponsesMessage {
		return schemas.ResponsesMessage{ID: schemas.Ptr(itemID), Type: schemas.Ptr(itemType), Status: schemas.Ptr("in_progress"), ResponsesToolMessage: tool}
	}
	switch step.Type {
	case GeminiInteractionsStepGoogleSearchCall:
		action := &schemas.ResponsesWebSearchToolCallAction{Type: "search"}
		if step.SearchType != nil && *step.SearchType == "image_search" {
			action.ImageQueries = args.Queries
		} else {
			action.Queries = args.Queries
			if len(args.Queries) > 0 {
				action.Query = schemas.Ptr(args.Queries[0])
			}
		}
		return []schemas.ResponsesMessage{item(schemas.ResponsesMessageTypeWebSearchCall, id, &schemas.ResponsesToolMessage{
			Action: &schemas.ResponsesToolMessageActionStruct{ResponsesWebSearchToolCallAction: action},
		})}
	case GeminiInteractionsStepCodeExecutionCall:
		code := args.Code
		return []schemas.ResponsesMessage{item(schemas.ResponsesMessageTypeCodeInterpreterCall, id, &schemas.ResponsesToolMessage{
			CallID:                           schemas.Ptr(id),
			ResponsesCodeInterpreterToolCall: &schemas.ResponsesCodeInterpreterToolCall{Code: &code, Outputs: []schemas.ResponsesCodeInterpreterOutput{}},
		})}
	case GeminiInteractionsStepURLContextCall:
		urls := args.URLs
		if len(urls) == 0 {
			urls = []string{""}
		}
		items := make([]schemas.ResponsesMessage, 0, len(urls))
		for i, u := range urls {
			itemID := id
			if len(urls) > 1 {
				itemID = id + "_" + strconv.Itoa(i)
			}
			items = append(items, item(schemas.ResponsesMessageTypeWebFetchCall, itemID, &schemas.ResponsesToolMessage{
				Action: &schemas.ResponsesToolMessageActionStruct{ResponsesWebFetchToolCallAction: &schemas.ResponsesWebFetchToolCallAction{Type: "fetch", URL: u}},
			}))
		}
		return items
	case GeminiInteractionsStepFileSearchCall:
		queries := args.Queries
		if queries == nil {
			queries = []string{}
		}
		return []schemas.ResponsesMessage{item(schemas.ResponsesMessageTypeFileSearchCall, id, &schemas.ResponsesToolMessage{
			ResponsesFileSearchToolCall: &schemas.ResponsesFileSearchToolCall{Queries: queries},
		})}
	case GeminiInteractionsStepMCPCall:
		arguments := "{}"
		if len(step.Arguments) > 0 && !bytes.Equal(step.Arguments, []byte("null")) {
			arguments = string(step.Arguments)
		}
		return []schemas.ResponsesMessage{item(schemas.ResponsesMessageTypeMCPCall, id, &schemas.ResponsesToolMessage{
			Name: geminiMCPToolName(step), ServerLabel: step.ServerName, Arguments: &arguments,
		})}
	}
	return nil
}

// geminiMCPToolName is an MCP call's tool name without the "server:" prefix Google adds; server_name
// already holds the server.
func geminiMCPToolName(step GeminiInteractionsItem) *string {
	if step.Name == nil || step.ServerName == nil || *step.ServerName == "" {
		return step.Name
	}
	if name, ok := strings.CutPrefix(*step.Name, *step.ServerName+":"); ok && name != "" {
		return &name
	}
	return step.Name
}

// geminiFileSearchResults maps file_search_result entries onto Responses results. Google documents
// no fixed entry shape, so each entry is kept whole in Attributes (and sent back from there), with
// its text and title also surfaced as the result's text and filename.
func geminiFileSearchResults(raw json.RawMessage) []schemas.ResponsesFileSearchToolCallResult {
	if len(raw) == 0 {
		return nil
	}
	var entries []map[string]any
	if sonic.Unmarshal(raw, &entries) != nil {
		return nil
	}
	out := make([]schemas.ResponsesFileSearchToolCallResult, 0, len(entries))
	for _, entry := range entries {
		attrs := entry
		result := schemas.ResponsesFileSearchToolCallResult{Attributes: &attrs}
		if text, ok := entry["text"].(string); ok {
			result.Text = &text
		}
		if title, ok := entry["title"].(string); ok {
			result.Filename = &title
		} else if name, ok := entry["file_name"].(string); ok {
			result.Filename = &name
		}
		out = append(out, result)
	}
	return out
}

// applyGeminiServerToolResult folds a result step onto the items its call produced.
func applyGeminiServerToolResult(items []*schemas.ResponsesMessage, step GeminiInteractionsItem) {
	status := "completed"
	if step.IsError != nil && *step.IsError {
		status = "failed"
	}
	for _, it := range items {
		itemStatus := status
		tool := it.ResponsesToolMessage
		switch step.Type {
		case GeminiInteractionsStepGoogleSearchResult:
			var results []geminiInteractionsSearchResult
			if len(step.Result) > 0 && sonic.Unmarshal(step.Result, &results) == nil && tool != nil && tool.Action != nil && tool.Action.ResponsesWebSearchToolCallAction != nil {
				for _, r := range results {
					if r.URL == "" {
						continue
					}
					source := schemas.ResponsesWebSearchToolCallActionSearchSource{Type: "url", URL: r.URL}
					if r.Title != "" {
						source.Title = schemas.Ptr(r.Title)
					}
					tool.Action.ResponsesWebSearchToolCallAction.Sources = append(tool.Action.ResponsesWebSearchToolCallAction.Sources, source)
				}
			}
		case GeminiInteractionsStepCodeExecutionResult:
			if tool != nil && tool.ResponsesCodeInterpreterToolCall != nil {
				tool.ResponsesCodeInterpreterToolCall.Outputs = append(tool.ResponsesCodeInterpreterToolCall.Outputs, geminiCodeExecutionResultOutputs(step.Result)...)
			}
		case GeminiInteractionsStepURLContextResult:
			var results []geminiInteractionsURLResult
			if len(step.Result) > 0 && sonic.Unmarshal(step.Result, &results) == nil && tool != nil && tool.Action != nil && tool.Action.ResponsesWebFetchToolCallAction != nil {
				for _, r := range results {
					if r.URL == tool.Action.ResponsesWebFetchToolCallAction.URL && r.Status != "" && r.Status != "success" {
						itemStatus = "failed"
					}
				}
			}
		case GeminiInteractionsStepFileSearchResult:
			if tool != nil && tool.ResponsesFileSearchToolCall != nil {
				tool.ResponsesFileSearchToolCall.Results = append(tool.ResponsesFileSearchToolCall.Results, geminiFileSearchResults(step.Result)...)
			}
		case GeminiInteractionsStepMCPResult:
			if tool != nil {
				output := geminiInteractionsResultText(step.Result)
				if (output == "" && geminiInteractionsResultHasContent(step.Result)) || geminiInteractionsResultHasMedia(step.Result) {
					// Rich content (an image, with or without a caption) is kept as its native JSON
					// rather than reduced to its text or reported as an empty success.
					output = string(bytes.TrimSpace(step.Result))
				}
				tool.Output = &schemas.ResponsesToolMessageOutputStruct{ResponsesToolCallOutputStr: &output}
			}
		}
		it.Status = &itemStatus
	}
}

// geminiInteractionsResultText reads a built-in tool result as text: the string itself, the
// text of a content list, or the raw JSON of anything else.
func geminiInteractionsResultText(raw json.RawMessage) string {
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return ""
	}
	var text string
	if sonic.Unmarshal(raw, &text) == nil {
		return text
	}
	var blocks []GeminiInteractionsItem
	if sonic.Unmarshal(raw, &blocks) == nil {
		var b strings.Builder
		for _, block := range blocks {
			if block.Text != nil {
				b.WriteString(*block.Text)
			}
		}
		return b.String()
	}
	return string(raw)
}

// geminiInteractionsContentBlockArray reports whether text is a JSON array of typed content
// blocks, and returns it trimmed. Any other text, including a JSON array of plain values, is not.
func geminiInteractionsContentBlockArray(text string) (json.RawMessage, bool) {
	trimmed := strings.TrimSpace(text)
	if !strings.HasPrefix(trimmed, "[") {
		return nil, false
	}
	var blocks []GeminiInteractionsItem
	if sonic.UnmarshalString(trimmed, &blocks) != nil || len(blocks) == 0 {
		return nil, false
	}
	for _, b := range blocks {
		if b.Type == "" {
			return nil, false
		}
	}
	return json.RawMessage(trimmed), true
}

// geminiCodeExecutionResultOutputs maps a code_execution_result onto code interpreter outputs: a
// string is logs; a content list keeps each text block as logs and each image as an image output
// (inline data as a data: URL).
func geminiCodeExecutionResultOutputs(raw json.RawMessage) []schemas.ResponsesCodeInterpreterOutput {
	var blocks []GeminiInteractionsItem
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '[' || sonic.Unmarshal(trimmed, &blocks) != nil {
		logs := geminiInteractionsResultText(raw)
		return []schemas.ResponsesCodeInterpreterOutput{{ResponsesCodeInterpreterOutputLogs: &schemas.ResponsesCodeInterpreterOutputLogs{Type: "logs", Logs: logs}}}
	}
	out := make([]schemas.ResponsesCodeInterpreterOutput, 0, len(blocks))
	for _, b := range blocks {
		switch {
		case b.Type == GeminiContentTypeText && b.Text != nil:
			out = append(out, schemas.ResponsesCodeInterpreterOutput{ResponsesCodeInterpreterOutputLogs: &schemas.ResponsesCodeInterpreterOutputLogs{Type: "logs", Logs: *b.Text}})
		case b.Type == "image":
			url := ""
			switch {
			case b.Data != nil && b.MimeType != nil:
				url = "data:" + *b.MimeType + ";base64," + *b.Data
			case b.URI != nil:
				url = *b.URI
			}
			if url != "" {
				out = append(out, schemas.ResponsesCodeInterpreterOutput{ResponsesCodeInterpreterOutputImage: &schemas.ResponsesCodeInterpreterOutputImage{Type: "image", URL: url}})
			}
		}
	}
	if len(out) == 0 {
		empty := ""
		out = append(out, schemas.ResponsesCodeInterpreterOutput{ResponsesCodeInterpreterOutputLogs: &schemas.ResponsesCodeInterpreterOutputLogs{Type: "logs", Logs: empty}})
	}
	return out
}

// geminiCodeExecutionHasArtifact reports whether a code run's result lists anything but the text and images
// geminiCodeExecutionResultOutputs maps.
func geminiCodeExecutionHasArtifact(raw json.RawMessage) bool {
	var blocks []GeminiInteractionsItem
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '[' || sonic.Unmarshal(trimmed, &blocks) != nil {
		return false
	}
	return slices.ContainsFunc(blocks, func(b GeminiInteractionsItem) bool {
		return b.Type != GeminiContentTypeText && b.Type != GeminiContentTypeImage
	})
}

// geminiInteractionsImageBlockFromURL turns an image URL back into an image block: a base64 data:
// URL becomes inline data, anything else a URI.
func geminiInteractionsImageBlockFromURL(url string) GeminiInteractionsItem {
	if rest, ok := strings.CutPrefix(url, "data:"); ok {
		if mime, data, found := strings.Cut(rest, ";base64,"); found {
			return GeminiInteractionsItem{Type: "image", Data: &data, MimeType: &mime}
		}
	}
	return GeminiInteractionsItem{Type: "image", URI: &url}
}

// geminiInteractionsResultHasMedia reports whether a result is a content list with any block that
// is not text.
func geminiInteractionsResultHasMedia(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '[' {
		return false
	}
	var blocks []GeminiInteractionsItem
	if sonic.Unmarshal(trimmed, &blocks) != nil {
		return false
	}
	for _, b := range blocks {
		if b.Type != "" && b.Type != GeminiContentTypeText {
			return true
		}
	}
	return false
}

// geminiInteractionsResultHasContent reports whether a tool result carries anything at all.
func geminiInteractionsResultHasContent(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) > 0 && !bytes.Equal(trimmed, []byte("null")) && !bytes.Equal(trimmed, []byte("[]")) && !bytes.Equal(trimmed, []byte(`""`))
}

// geminiInteractionsAnnotationsToResponses maps the citations on a text block.
func geminiInteractionsAnnotationsToResponses(raw json.RawMessage) []schemas.ResponsesOutputMessageContentTextAnnotation {
	out := []schemas.ResponsesOutputMessageContentTextAnnotation{}
	if len(raw) == 0 {
		return out
	}
	var annotations []geminiInteractionsAnnotation
	if sonic.Unmarshal(raw, &annotations) != nil {
		return out
	}
	for _, a := range annotations {
		switch a.Type {
		case "url_citation":
			out = append(out, schemas.ResponsesOutputMessageContentTextAnnotation{Type: "url_citation", URL: a.URL, Title: a.Title, StartIndex: a.StartIndex, EndIndex: a.EndIndex})
		case "file_citation":
			out = append(out, schemas.ResponsesOutputMessageContentTextAnnotation{Type: "file_citation", FileID: a.DocumentURI, Filename: a.FileName, StartIndex: a.StartIndex, EndIndex: a.EndIndex})
		}
	}
	return out
}

// responsesAnnotationsToGeminiInteractions maps Responses citations back to Interactions ones.
func responsesAnnotationsToGeminiInteractions(annotations []schemas.ResponsesOutputMessageContentTextAnnotation) json.RawMessage {
	out := make([]geminiInteractionsAnnotation, 0, len(annotations))
	for _, a := range annotations {
		switch a.Type {
		case "url_citation":
			out = append(out, geminiInteractionsAnnotation{Type: "url_citation", URL: a.URL, Title: a.Title, StartIndex: a.StartIndex, EndIndex: a.EndIndex})
		case "file_citation":
			out = append(out, geminiInteractionsAnnotation{Type: "file_citation", DocumentURI: a.FileID, FileName: a.Filename, StartIndex: a.StartIndex, EndIndex: a.EndIndex})
		}
	}
	if len(out) == 0 {
		return nil
	}
	raw, err := providerUtils.MarshalSorted(out)
	if err != nil {
		return nil
	}
	return raw
}

// geminiInteractionsSearchUnits is the billable Google Search units of a run: one per query on
// Gemini 3 and later, one per grounded request before that, the same rule generateContent
// follows. Other grounding tools (google_maps) are not search units.
func geminiInteractionsSearchUnits(counts []GeminiInteractionsGroundingToolCount, model string) int {
	total := 0
	for _, c := range counts {
		if c.Type == geminiInteractionsGoogleSearch {
			total += c.Count
		}
	}
	if total <= 0 {
		return 0
	}
	if !isGemini3Plus(model) {
		return 1
	}
	return total
}

// responsesServerToolItemToGeminiInteractionSteps renders a built-in tool item another provider
// ran as the call and result steps the Interactions API documents for that tool.
func responsesServerToolItemToGeminiInteractionSteps(item schemas.ResponsesMessage) []GeminiInteractionsItem {
	tool := item.ResponsesToolMessage
	if tool == nil || item.Type == nil {
		return nil
	}
	id := ""
	switch {
	case item.ID != nil:
		id = *item.ID
	case tool.CallID != nil:
		id = *tool.CallID
	}
	var isError *bool
	pending := false // a call still running has no result yet
	if item.Status != nil {
		switch *item.Status {
		case "failed":
			isError = schemas.Ptr(true)
		case "in_progress", "searching", "interpreting":
			pending = true
		}
	}
	marshal := func(v interface{}) json.RawMessage {
		raw, err := providerUtils.MarshalSorted(v)
		if err != nil {
			return nil
		}
		return raw
	}
	call := GeminiInteractionsItem{ID: schemas.Ptr(id)}
	result := GeminiInteractionsItem{CallID: schemas.Ptr(id), IsError: isError}
	switch *item.Type {
	case schemas.ResponsesMessageTypeWebSearchCall:
		var args geminiInteractionsToolArguments
		if tool.Action != nil && tool.Action.ResponsesWebSearchToolCallAction != nil {
			action := tool.Action.ResponsesWebSearchToolCallAction
			switch action.Type {
			case "open_page":
				// Opening a page is a URL fetch, not a search.
				if action.URL == nil || *action.URL == "" {
					return nil
				}
				status := "success"
				if isError != nil {
					status, result.IsError = "error", nil
				}
				call.Type, call.Arguments = GeminiInteractionsStepURLContextCall, marshal(geminiInteractionsToolArguments{URLs: []string{*action.URL}})
				if pending {
					return []GeminiInteractionsItem{call}
				}
				result.Type, result.Result = GeminiInteractionsStepURLContextResult, marshal([]geminiInteractionsURLResult{{URL: *action.URL, Status: status}})
				return []GeminiInteractionsItem{call, result}
			case "find":
				// An in-page find has no Interactions step; reporting it as a search would be false.
				return nil
			}
			args.Queries = action.Queries
			if len(args.Queries) == 0 && action.Query != nil {
				args.Queries = []string{*action.Query}
			}
			if len(action.ImageQueries) > 0 {
				call.SearchType = schemas.Ptr("image_search")
				args.Queries = action.ImageQueries
			}
		}
		call.Type, call.Arguments = GeminiInteractionsStepGoogleSearchCall, marshal(args)
		result.Type, result.Result = GeminiInteractionsStepGoogleSearchResult, json.RawMessage("[]")
		if tool.Action != nil && tool.Action.ResponsesWebSearchToolCallAction != nil {
			var sources []geminiInteractionsSearchResult
			for _, src := range tool.Action.ResponsesWebSearchToolCallAction.Sources {
				if src.URL == "" {
					continue
				}
				entry := geminiInteractionsSearchResult{URL: src.URL}
				if src.Title != nil {
					entry.Title = *src.Title
				}
				sources = append(sources, entry)
			}
			if len(sources) > 0 {
				result.Result = marshal(sources)
			}
		}
	case schemas.ResponsesMessageTypeCodeInterpreterCall:
		args := geminiInteractionsToolArguments{Language: "python"}
		if tool.ResponsesCodeExecutionCall != nil && tool.ResponsesCodeExecutionCall.ToolName == "bash_code_execution" {
			args.Language = "bash" // Anthropic's shell, which carries its command as the code
		}
		var logs strings.Builder
		var blocks []GeminiInteractionsItem
		hasImage := false
		if ci := tool.ResponsesCodeInterpreterToolCall; ci != nil {
			if ci.Code != nil {
				args.Code = *ci.Code
			}
			for _, o := range ci.Outputs {
				switch {
				case o.ResponsesCodeInterpreterOutputLogs != nil:
					text := o.ResponsesCodeInterpreterOutputLogs.Logs
					logs.WriteString(text)
					blocks = append(blocks, GeminiInteractionsItem{Type: GeminiContentTypeText, Text: &text})
				case o.ResponsesCodeInterpreterOutputImage != nil:
					hasImage = true
					blocks = append(blocks, geminiInteractionsImageBlockFromURL(o.ResponsesCodeInterpreterOutputImage.URL))
				}
			}
		}
		call.Type, call.Arguments = GeminiInteractionsStepCodeExecutionCall, marshal(args)
		// A run with an image is a content list; text alone stays the plain string Google sends.
		result.Type, result.Result = GeminiInteractionsStepCodeExecutionResult, marshal(logs.String())
		if hasImage {
			result.Result = marshal(blocks)
		}
	case schemas.ResponsesMessageTypeWebFetchCall:
		url := ""
		if tool.Action != nil && tool.Action.ResponsesWebFetchToolCallAction != nil {
			url = tool.Action.ResponsesWebFetchToolCallAction.URL
		}
		status := "success"
		if isError != nil || (tool.ResponsesWebFetchCall != nil && tool.ResponsesWebFetchCall.ErrorCode != nil && *tool.ResponsesWebFetchCall.ErrorCode != "") {
			status, isError, result.IsError = "error", nil, nil
		}
		call.Type, call.Arguments = GeminiInteractionsStepURLContextCall, marshal(geminiInteractionsToolArguments{URLs: []string{url}})
		result.Type, result.Result = GeminiInteractionsStepURLContextResult, marshal([]geminiInteractionsURLResult{{URL: url, Status: status}})
	case schemas.ResponsesMessageTypeFileSearchCall:
		call.Type = GeminiInteractionsStepFileSearchCall
		result.Type = GeminiInteractionsStepFileSearchResult
		if fs := tool.ResponsesFileSearchToolCall; fs != nil && len(fs.Queries) > 0 {
			call.Arguments = marshal(geminiInteractionsToolArguments{Queries: fs.Queries})
		}
		if fs := tool.ResponsesFileSearchToolCall; fs != nil && len(fs.Results) > 0 {
			entries := make([]map[string]any, 0, len(fs.Results))
			for _, r := range fs.Results {
				entry := map[string]any{}
				if r.Attributes != nil {
					maps.Copy(entry, *r.Attributes)
				}
				if r.Text != nil {
					entry["text"] = *r.Text
				}
				if r.Filename != nil {
					if _, ok := entry["file_name"]; !ok {
						entry["title"] = *r.Filename
					}
				}
				entries = append(entries, entry)
			}
			result.Result = marshal(entries)
		}
	case schemas.ResponsesMessageTypeMCPCall:
		args := json.RawMessage("{}")
		if tool.Arguments != nil && json.Valid([]byte(*tool.Arguments)) {
			args = json.RawMessage(*tool.Arguments)
		}
		call.Type, call.Name, call.ServerName, call.Arguments = GeminiInteractionsStepMCPCall, tool.Name, tool.ServerLabel, args
		if tool.Error.IsError() {
			isError = schemas.Ptr(true)
		}
		if tool.Output == nil {
			if isError == nil {
				// A call still pending has no result yet.
				return []GeminiInteractionsItem{call}
			}
			// A call that failed without output still reports the failure and its error.
			result.Type, result.Name, result.ServerName, result.Result, result.IsError = GeminiInteractionsStepMCPResult, tool.Name, tool.ServerLabel, marshal(tool.Error.Text()), schemas.Ptr(true)
			return []GeminiInteractionsItem{call, result}
		}
		output := ""
		if tool.Output.ResponsesToolCallOutputStr != nil {
			output = *tool.Output.ResponsesToolCallOutputStr
		}
		resultJSON := marshal(output)
		if blocks, ok := geminiInteractionsContentBlockArray(output); ok {
			// A content-block array kept from a rich native result goes back out as one.
			resultJSON = blocks
		}
		if typed := tool.Output.ResponsesFunctionToolCallOutputBlocks; len(typed) > 0 {
			if blocks, err := toGeminiInteractionsContentBlocks(&schemas.ResponsesMessageContent{ContentBlocks: typed}); err == nil {
				resultJSON = marshal(blocks)
			}
		}
		result.Type, result.Name, result.ServerName, result.Result, result.IsError = GeminiInteractionsStepMCPResult, tool.Name, tool.ServerLabel, resultJSON, isError
		if tool.Name == nil && tool.CallID != nil && *tool.CallID != "" {
			// Anthropic's result is an item of its own, which names the call it answers and no tool.
			result.CallID = tool.CallID
			return []GeminiInteractionsItem{result}
		}
	default:
		return nil
	}
	if pending {
		return []GeminiInteractionsItem{call}
	}
	return []GeminiInteractionsItem{call, result}
}

// geminiInteractionsComputerEnvironment maps a Responses computer environment onto the three
// the Interactions computer_use tool takes. Desktop operating systems are "desktop"; anything
// unset or unknown is "browser", the tool's own default.
func geminiInteractionsComputerEnvironment(environment string) string {
	switch strings.ToLower(environment) {
	case "mobile":
		return "mobile"
	case "desktop", "mac", "windows", "ubuntu", "linux":
		return "desktop"
	}
	return "browser"
}
