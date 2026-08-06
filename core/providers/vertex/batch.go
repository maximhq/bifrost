package vertex

import (
	"bytes"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/providers/anthropic"
	"github.com/maximhq/bifrost/core/providers/gemini"
	"github.com/maximhq/bifrost/core/providers/openai"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
)

// vertexBatchCustomIDLabel is the request label used to carry the Bifrost custom_id
// through a batch prediction job (Vertex JSONL has no native custom_id field; the
// request — labels included — is echoed back in each output line).
const vertexBatchCustomIDLabel = "bifrost_custom_id"

// vertexAnthropicBatchVersion is the anthropic_version required by Claude-on-Vertex
// batch instances (same value the chat path injects via the Vertex build config).
const vertexAnthropicBatchVersion = "vertex-2023-10-16"

// vertexJobStateToBatchStatus maps Vertex JOB_STATE_* values to Bifrost batch statuses.
func vertexJobStateToBatchStatus(state string) schemas.BatchStatus {
	switch state {
	case "JOB_STATE_QUEUED", "JOB_STATE_PENDING":
		return schemas.BatchStatusValidating
	case "JOB_STATE_RUNNING", "JOB_STATE_UPDATING":
		return schemas.BatchStatusInProgress
	case "JOB_STATE_SUCCEEDED", "JOB_STATE_PARTIALLY_SUCCEEDED":
		return schemas.BatchStatusCompleted
	case "JOB_STATE_FAILED":
		return schemas.BatchStatusFailed
	case "JOB_STATE_CANCELLING":
		return schemas.BatchStatusCancelling
	case "JOB_STATE_CANCELLED":
		return schemas.BatchStatusCancelled
	case "JOB_STATE_EXPIRED":
		return schemas.BatchStatusExpired
	default:
		return schemas.BatchStatus(state)
	}
}

// vertexBatchJobsBaseURL returns ".../v1/projects/{project}/locations/{region}" for the
// key's configured project and region. Batch prediction requires a regional endpoint.
func vertexBatchJobsBaseURL(key schemas.Key) (string, *schemas.BifrostError) {
	if key.VertexKeyConfig == nil {
		return "", providerUtils.NewConfigurationError("vertex key config is not set")
	}
	projectID := strings.TrimSpace(key.VertexKeyConfig.ProjectID.GetValue())
	if projectID == "" {
		return "", providerUtils.NewConfigurationError("project ID is not set")
	}
	region := key.VertexKeyConfig.Region.GetValue()
	if region == "" {
		return "", providerUtils.NewConfigurationError("region is required for batch prediction")
	}
	return getVertexProjectLocationURL(region, "v1", projectID), nil
}

// vertexBatchJobURL resolves a Bifrost batch ID (bare job ID or full resource name)
// to the job's REST URL.
func vertexBatchJobURL(key schemas.Key, batchID string) (string, *schemas.BifrostError) {
	if strings.HasPrefix(batchID, "projects/") {
		// Full resource name: projects/{p}/locations/{r}/batchPredictionJobs/{id}
		parts := strings.Split(batchID, "/")
		if len(parts) >= 6 && parts[2] == "locations" {
			return getVertexAPIBaseURL(parts[3], "v1") + "/" + batchID, nil
		}
		return "", providerUtils.NewBifrostOperationError(fmt.Sprintf("invalid Vertex batch ID %q", batchID), nil)
	}
	base, cfgErr := vertexBatchJobsBaseURL(key)
	if cfgErr != nil {
		return "", cfgErr
	}
	return base + "/batchPredictionJobs/" + batchID, nil
}

// vertexBatchJobToBifrost maps a BatchPredictionJob resource to the Bifrost retrieve response.
func vertexBatchJobToBifrost(job *VertexBatchPredictionJob) schemas.BifrostBatchRetrieveResponse {
	status := vertexJobStateToBatchStatus(job.State)
	resp := schemas.BifrostBatchRetrieveResponse{
		ID:        job.Name,
		Object:    "batch",
		Status:    status,
		CreatedAt: gcsParseTime(job.CreateTime),
	}
	if job.DisplayName != "" {
		resp.DisplayName = schemas.Ptr(job.DisplayName)
	}
	if job.InputConfig.GcsSource != nil && len(job.InputConfig.GcsSource.Uris) > 0 {
		resp.InputFileID = job.InputConfig.GcsSource.Uris[0]
	}
	if job.OutputInfo != nil && job.OutputInfo.GcsOutputDirectory != "" {
		resp.OutputFileID = schemas.Ptr(job.OutputInfo.GcsOutputDirectory)
	}
	if job.StartTime != "" {
		resp.InProgressAt = schemas.Ptr(gcsParseTime(job.StartTime))
	}
	if job.EndTime != "" {
		endTime := gcsParseTime(job.EndTime)
		switch status {
		case schemas.BatchStatusCompleted:
			resp.CompletedAt = &endTime
		case schemas.BatchStatusFailed:
			resp.FailedAt = &endTime
		case schemas.BatchStatusCancelled:
			resp.CancelledAt = &endTime
		case schemas.BatchStatusExpired:
			resp.ExpiredAt = &endTime
		}
	}
	if job.CompletionStats != nil {
		succeeded := gcsParseSize(job.CompletionStats.SuccessfulCount)
		failed := gcsParseSize(job.CompletionStats.FailedCount)
		incomplete := gcsParseSize(job.CompletionStats.IncompleteCount)
		resp.RequestCounts = schemas.BatchRequestCounts{
			Total:     int(succeeded + failed + incomplete),
			Completed: int(succeeded),
			Failed:    int(failed),
		}
	}
	if job.Error != nil && job.Error.Message != "" {
		resp.Errors = &schemas.BatchErrors{
			Data: []schemas.BatchError{{Code: fmt.Sprintf("%d", job.Error.Code), Message: job.Error.Message}},
		}
	}
	return resp
}

// parseVertexJobAPIError parses a Vertex AI error response (same envelope as GCS).
func parseVertexJobAPIError(body []byte, statusCode int, op string) *schemas.BifrostError {
	var apiErr gcsErrorBody
	_ = sonic.Unmarshal(body, &apiErr)
	msg := apiErr.Error.Message
	if msg == "" {
		msg = fmt.Sprintf("Vertex %s failed with HTTP %d", op, statusCode)
	}
	return providerUtils.NewProviderAPIError(msg, nil, statusCode, nil, nil)
}

// ToVertexBatchCreateRequest maps a Bifrost batch create request to a Vertex
// BatchPredictionJob request. The model, display name and input/output GCS config are
// mapped explicitly; every other field is taken from extra_params (e.g. modelParameters,
// labels, modelVersionId, encryptionSpec) and merged verbatim into the job body.
func ToVertexBatchCreateRequest(ctx *schemas.BifrostContext, request *schemas.BifrostBatchCreateRequest, displayName, inputURI, outputURI string) *VertexBatchCreateRequest {
	model := ""
	if request.Model != nil {
		model = *request.Model
	}
	if model != "" && !strings.Contains(model, "/") {
		// Bare model names are resolved to a publisher path; Claude models live under the
		// Anthropic publisher (publishers/anthropic/models/...), everything else under Google.
		publisher := "google"
		if schemas.IsAnthropicModelFamily(ctx, model) {
			publisher = "anthropic"
		}
		model = "publishers/" + publisher + "/models/" + model
	}

	req := &VertexBatchCreateRequest{
		DisplayName: displayName,
		Model:       model,
		InputConfig: VertexBatchInputConfig{
			InstancesFormat: "jsonl",
			GcsSource:       &VertexGcsSource{Uris: []string{inputURI}},
		},
		OutputConfig: VertexBatchOutputConfig{
			PredictionsFormat: "jsonl",
			GcsDestination:    &VertexGcsDestination{OutputUriPrefix: outputURI},
		},
		ExtraParams: request.ExtraParams,
	}

	return req
}

// vertexConvertRequestsToJSONL converts inline batch request items to Vertex batch JSONL.
// The instance shape depends on the model family (batchPredictionJobs always wraps it under
// "request"):
//   - Gemini/Google: bodies are converted to the native GenerateContentRequest shape (OpenAI
//     "messages" become "contents"), and each custom_id is carried in the request labels.
//   - Anthropic/Claude: the instance is an Anthropic Messages body with a native top-level
//     "custom_id"; OpenAI-style bodies are converted to Anthropic shape, Anthropic-native
//     bodies pass through with "anthropic_version" ensured.
func vertexConvertRequestsToJSONL(ctx *schemas.BifrostContext, requests []schemas.BatchRequestItem, model string) ([]byte, error) {
	isAnthropic := schemas.IsAnthropicModelFamily(ctx, model)

	var buf bytes.Buffer
	for i, item := range requests {
		body := item.Body
		if body == nil {
			body = item.Params
		}
		if body == nil {
			return nil, fmt.Errorf("batch request item %d (custom_id %q) has no body", i, item.CustomID)
		}

		if isAnthropic {
			requestBody, err := vertexAnthropicBatchInstance(ctx, body, model)
			if err != nil {
				return nil, fmt.Errorf("failed to convert batch request item %d (custom_id %q): %w", i, item.CustomID, err)
			}
			// Claude-on-Vertex batch has a native top-level custom_id, not Gemini labels.
			lineObj := map[string]interface{}{"request": requestBody}
			if item.CustomID != "" {
				lineObj["custom_id"] = item.CustomID
			}
			line, err := providerUtils.MarshalSorted(lineObj)
			if err != nil {
				return nil, fmt.Errorf("failed to marshal batch request item %d (custom_id %q): %w", i, item.CustomID, err)
			}
			buf.Write(line)
			buf.WriteByte('\n')
			continue
		}

		// OpenAI-style bodies (with "messages") are converted to Gemini's request shape; native
		// Gemini/Vertex bodies pass through verbatim so caller-supplied fields (tools, toolConfig,
		// labels, cachedContent, ...) are not dropped by the lossy struct conversion.
		if _, isOpenAI := body["messages"]; isOpenAI {
			geminiReq, err := gemini.ToGeminiBatchGenerateContentRequest(body)
			if err != nil {
				return nil, fmt.Errorf("failed to convert batch request item %d (custom_id %q): %w", i, item.CustomID, err)
			}
			reqBytes, err := providerUtils.MarshalSorted(geminiReq)
			if err != nil {
				return nil, fmt.Errorf("failed to marshal batch request item %d (custom_id %q): %w", i, item.CustomID, err)
			}
			converted := map[string]interface{}{}
			if err := sonic.Unmarshal(reqBytes, &converted); err != nil {
				return nil, fmt.Errorf("failed to unmarshal batch request item %d (custom_id %q): %w", i, item.CustomID, err)
			}
			body = converted
		}

		if item.CustomID != "" {
			// Shallow-copy before injecting labels so the caller's map is not mutated.
			withLabels := make(map[string]interface{}, len(body)+1)
			for k, v := range body {
				withLabels[k] = v
			}
			labels := map[string]interface{}{}
			if existing, ok := withLabels["labels"].(map[string]interface{}); ok {
				for k, v := range existing {
					labels[k] = v
				}
			}
			labels[vertexBatchCustomIDLabel] = item.CustomID
			withLabels["labels"] = labels
			body = withLabels
		}

		line, err := providerUtils.MarshalSorted(map[string]interface{}{"request": body})
		if err != nil {
			return nil, fmt.Errorf("failed to marshal batch request item %d (custom_id %q): %w", i, item.CustomID, err)
		}
		buf.Write(line)
		buf.WriteByte('\n')
	}
	return buf.Bytes(), nil
}

// vertexAnthropicBatchInstance builds the per-line "request" body for a Claude-on-Vertex batch
// instance. OpenAI-shaped bodies are converted to the Anthropic Messages shape via the shared
// converters; bodies already in Anthropic shape pass through with the job-level model dropped
// and anthropic_version ensured.
func vertexAnthropicBatchInstance(ctx *schemas.BifrostContext, body map[string]interface{}, model string) (map[string]interface{}, error) {
	if bodyLooksLikeOpenAIChat(body) {
		reqBytes, err := sonic.Marshal(body)
		if err != nil {
			return nil, err
		}
		var oaiReq openai.OpenAIChatRequest
		if err := sonic.Unmarshal(reqBytes, &oaiReq); err != nil {
			return nil, err
		}
		bifrostReq := oaiReq.ToBifrostChatRequest(ctx)
		if bifrostReq == nil {
			return nil, fmt.Errorf("could not parse OpenAI-style batch body")
		}
		if model != "" {
			bifrostReq.Model = model
		}
		// The Vertex build config injects anthropic_version, strips the model field (model is at
		// the job level), remaps tool versions and strips cache_control scope — same shaping the
		// chat path uses for Claude-on-Vertex.
		anthropicBytes, bErr := anthropic.BuildAnthropicChatRequestBody(ctx, bifrostReq, anthropic.AnthropicRequestBuildConfig{
			Provider: schemas.Vertex,
			Model:    model,
		})
		if bErr != nil {
			msg := "anthropic request build failed"
			if bErr.Error != nil && bErr.Error.Message != "" {
				msg = bErr.Error.Message
			}
			return nil, fmt.Errorf("%s", msg)
		}
		out := map[string]interface{}{}
		if err := sonic.Unmarshal(anthropicBytes, &out); err != nil {
			return nil, err
		}
		return out, nil
	}

	// Already Anthropic-shaped: shallow-copy, drop the job-level model, ensure anthropic_version.
	out := make(map[string]interface{}, len(body)+1)
	for k, v := range body {
		out[k] = v
	}
	delete(out, "model")
	if _, ok := out["anthropic_version"]; !ok {
		out["anthropic_version"] = vertexAnthropicBatchVersion
	}
	return out, nil
}

// bodyLooksLikeOpenAIChat reports whether a batch request body is an OpenAI chat-completions
// body that must be converted to Anthropic Messages shape. OpenAI and Anthropic both key on
// "messages", so detection relies on OpenAI-only markers; a plain messages+max_tokens body is
// valid as-is for Anthropic and is left to pass through. Bodies already declaring
// anthropic_version are treated as Anthropic-native.
func bodyLooksLikeOpenAIChat(body map[string]interface{}) bool {
	if _, ok := body["anthropic_version"]; ok {
		return false
	}
	if _, ok := body["messages"].([]interface{}); !ok {
		return false
	}
	for _, k := range []string{"max_completion_tokens", "frequency_penalty", "presence_penalty", "logit_bias", "response_format", "parallel_tool_calls", "logprobs", "top_logprobs", "n"} {
		if _, ok := body[k]; ok {
			return true
		}
	}
	// OpenAI tool wrapper: {"type": "function", "function": {...}} (Anthropic tools are flat).
	if tools, ok := body["tools"].([]interface{}); ok {
		for _, t := range tools {
			if tm, ok := t.(map[string]interface{}); ok {
				if tm["type"] == "function" || tm["function"] != nil {
					return true
				}
			}
		}
	}
	messages, _ := body["messages"].([]interface{})
	for _, m := range messages {
		mm, ok := m.(map[string]interface{})
		if !ok {
			continue
		}
		// OpenAI carries system/developer prompts and tool results as message roles; Anthropic
		// uses a top-level "system" field and tool_result content blocks.
		switch mm["role"] {
		case "system", "developer", "tool":
			return true
		}
		if mm["tool_calls"] != nil {
			return true
		}
		if parts, ok := mm["content"].([]interface{}); ok {
			for _, p := range parts {
				if pm, ok := p.(map[string]interface{}); ok && pm["type"] == "image_url" {
					return true
				}
			}
		}
	}
	return false
}

// ============================ Integration Converters ============================
// Convert between the native Vertex BatchPredictionJob wire shape (used by the aiplatform
// JobServiceClient) and Bifrost's neutral batch types, for the genai HTTP integration.
// Key/project selection happens in Bifrost from the vertex key config, so the project and
// location in the inbound request path are placeholders — only the job body is converted.

// batchStatusToVertexJobState is the inverse of vertexJobStateToBatchStatus.
func batchStatusToVertexJobState(status schemas.BatchStatus) string {
	switch status {
	case schemas.BatchStatusValidating:
		return "JOB_STATE_PENDING"
	case schemas.BatchStatusInProgress, schemas.BatchStatusFinalizing:
		return "JOB_STATE_RUNNING"
	case schemas.BatchStatusCompleted, schemas.BatchStatusEnded:
		return "JOB_STATE_SUCCEEDED"
	case schemas.BatchStatusFailed:
		return "JOB_STATE_FAILED"
	case schemas.BatchStatusCancelling:
		return "JOB_STATE_CANCELLING"
	case schemas.BatchStatusCancelled:
		return "JOB_STATE_CANCELLED"
	case schemas.BatchStatusExpired:
		return "JOB_STATE_EXPIRED"
	default:
		return "JOB_STATE_UNSPECIFIED"
	}
}

// formatVertexBatchTime renders a Unix timestamp as an RFC3339 string, empty when zero.
func formatVertexBatchTime(unix int64) string {
	if unix <= 0 {
		return ""
	}
	return time.Unix(unix, 0).UTC().Format(time.RFC3339)
}

// vertexCompletionStatsFromCounts maps Bifrost request counts to Vertex completion stats.
func vertexCompletionStatsFromCounts(c schemas.BatchRequestCounts) *VertexBatchCompletionStats {
	if c.Total == 0 && c.Completed == 0 && c.Failed == 0 {
		return nil
	}
	incomplete := c.Total - c.Completed - c.Failed
	if incomplete < 0 {
		incomplete = 0
	}
	return &VertexBatchCompletionStats{
		SuccessfulCount: strconv.Itoa(c.Completed),
		FailedCount:     strconv.Itoa(c.Failed),
		IncompleteCount: strconv.Itoa(incomplete),
	}
}

// ToBifrostBatchCreateRequest maps an inbound native Vertex BatchPredictionJob (as sent by
// the aiplatform JobServiceClient) to a Bifrost batch create request. The model, GCS input
// URI and display name are mapped to typed Bifrost fields; the GCS output prefix and every
// other Vertex-native create-input field (modelParameters, labels, modelVersionId,
// encryptionSpec, instanceConfig, ...) are carried through ExtraParams keyed by their Vertex
// JSON names, so ToVertexBatchCreateRequest can merge them back into the job body verbatim
// for a lossless round trip. Server-populated, output-only fields (state, outputInfo, error,
// timestamps, completionStats, partialFailures, satisfiesPz*, ...) are intentionally omitted.
func ToBifrostBatchCreateRequest(job *VertexBatchPredictionJob) *schemas.BifrostBatchCreateRequest {
	req := &schemas.BifrostBatchCreateRequest{Provider: schemas.Vertex}
	if job == nil {
		return req
	}
	if job.Model != "" {
		req.Model = schemas.Ptr(job.Model)
	}
	if job.InputConfig.GcsSource != nil && len(job.InputConfig.GcsSource.Uris) > 0 {
		req.InputFileID = job.InputConfig.GcsSource.Uris[0]
	}
	// Display name maps to the typed DisplayName field (read back by BatchCreate to set
	// the outbound Vertex displayName) so it survives a full round trip.
	if job.DisplayName != "" {
		req.DisplayName = schemas.Ptr(job.DisplayName)
	}

	// Output destination maps to the typed OutputFolder (read back by BatchCreate as the
	// gs:// output prefix), so it survives a full round trip without going through extra_params.
	if job.OutputConfig.GcsDestination != nil && job.OutputConfig.GcsDestination.OutputUriPrefix != "" {
		req.OutputFolder = &schemas.BatchOutputFolder{URL: job.OutputConfig.GcsDestination.OutputUriPrefix}
	}

	// Remaining create-input fields → ExtraParams, keyed by their Vertex JSON names so they
	// merge cleanly into the outbound BatchPredictionJob body. Each is guarded so zero values
	// are not forwarded (mirroring the native struct's omitempty tags).
	extra := map[string]interface{}{}
	if job.ModelVersionID != "" {
		extra["modelVersionId"] = job.ModelVersionID
	}
	if job.UnmanagedContainerModel != nil {
		extra["unmanagedContainerModel"] = job.UnmanagedContainerModel
	}
	if job.InstanceConfig != nil {
		extra["instanceConfig"] = job.InstanceConfig
	}
	if job.ModelParameters != nil {
		extra["modelParameters"] = job.ModelParameters
	}
	if job.DedicatedResources != nil {
		extra["dedicatedResources"] = job.DedicatedResources
	}
	if job.ServiceAccount != "" {
		extra["serviceAccount"] = job.ServiceAccount
	}
	if job.ManualBatchTuningParameters != nil {
		extra["manualBatchTuningParameters"] = job.ManualBatchTuningParameters
	}
	if job.GenerateExplanation {
		extra["generateExplanation"] = job.GenerateExplanation
	}
	if len(job.ExplanationSpec) > 0 {
		extra["explanationSpec"] = job.ExplanationSpec
	}
	if len(job.Labels) > 0 {
		extra["labels"] = job.Labels
	}
	if job.EncryptionSpec != nil {
		extra["encryptionSpec"] = job.EncryptionSpec
	}
	if len(job.ModelMonitoringConfig) > 0 {
		extra["modelMonitoringConfig"] = job.ModelMonitoringConfig
	}
	if job.DisableContainerLogging {
		extra["disableContainerLogging"] = job.DisableContainerLogging
	}
	if len(extra) > 0 {
		req.ExtraParams = extra
	}
	return req
}

// vertexBatchJobShell builds the BatchPredictionJob fields shared by the create and retrieve
// response converters. name is whatever Bifrost returns (bare id or full resource name);
// displayName is the human-readable job name, kept distinct from name.
func vertexBatchJobShell(name, displayName string, status schemas.BatchStatus, createdAt int64, inputFileID string, outputFileID *string) *VertexBatchPredictionJob {
	job := &VertexBatchPredictionJob{
		Name:        name,
		DisplayName: displayName,
		State:       batchStatusToVertexJobState(status),
		CreateTime:  formatVertexBatchTime(createdAt),
	}
	if inputFileID != "" {
		job.InputConfig = VertexBatchInputConfig{
			InstancesFormat: "jsonl",
			GcsSource:       &VertexGcsSource{Uris: []string{inputFileID}},
		}
	}
	if outputFileID != nil && *outputFileID != "" {
		job.OutputConfig = VertexBatchOutputConfig{
			PredictionsFormat: "jsonl",
			GcsDestination:    &VertexGcsDestination{OutputUriPrefix: *outputFileID},
		}
		job.OutputInfo = &VertexBatchOutputInfo{GcsOutputDirectory: *outputFileID}
	}
	return job
}

// ToVertexBatchCreateResponse maps a Bifrost batch create response to a native Vertex
// BatchPredictionJob.
func ToVertexBatchCreateResponse(resp *schemas.BifrostBatchCreateResponse) *VertexBatchPredictionJob {
	if resp == nil {
		return nil
	}
	displayName := ""
	if resp.DisplayName != nil {
		displayName = *resp.DisplayName
	}
	job := vertexBatchJobShell(resp.ID, displayName, resp.Status, resp.CreatedAt, resp.InputFileID, resp.OutputFileID)
	job.CompletionStats = vertexCompletionStatsFromCounts(resp.RequestCounts)
	return job
}

// ToVertexBatchRetrieveResponse maps a Bifrost batch retrieve response to a native Vertex
// BatchPredictionJob, including timestamps, completion stats and any terminal error.
func ToVertexBatchRetrieveResponse(resp *schemas.BifrostBatchRetrieveResponse) *VertexBatchPredictionJob {
	if resp == nil {
		return nil
	}
	displayName := ""
	if resp.DisplayName != nil {
		displayName = *resp.DisplayName
	}
	job := vertexBatchJobShell(resp.ID, displayName, resp.Status, resp.CreatedAt, resp.InputFileID, resp.OutputFileID)
	if resp.InProgressAt != nil {
		job.StartTime = formatVertexBatchTime(*resp.InProgressAt)
	}
	switch {
	case resp.CompletedAt != nil:
		job.EndTime = formatVertexBatchTime(*resp.CompletedAt)
	case resp.FailedAt != nil:
		job.EndTime = formatVertexBatchTime(*resp.FailedAt)
	case resp.CancelledAt != nil:
		job.EndTime = formatVertexBatchTime(*resp.CancelledAt)
	case resp.ExpiredAt != nil:
		job.EndTime = formatVertexBatchTime(*resp.ExpiredAt)
	}
	job.CompletionStats = vertexCompletionStatsFromCounts(resp.RequestCounts)
	if resp.Errors != nil && len(resp.Errors.Data) > 0 {
		code := 0
		if c, err := strconv.Atoi(resp.Errors.Data[0].Code); err == nil {
			code = c
		}
		job.Error = &VertexBatchJobError{Code: code, Message: resp.Errors.Data[0].Message}
	}
	return job
}

// ToVertexBatchListResponse maps a Bifrost batch list response to the native Vertex
// batchPredictionJobs.list response envelope.
func ToVertexBatchListResponse(resp *schemas.BifrostBatchListResponse) *VertexBatchJobListResponse {
	out := &VertexBatchJobListResponse{}
	if resp == nil {
		return out
	}
	for i := range resp.Data {
		if job := ToVertexBatchRetrieveResponse(&resp.Data[i]); job != nil {
			out.BatchPredictionJobs = append(out.BatchPredictionJobs, *job)
		}
	}
	if resp.NextCursor != nil {
		out.NextPageToken = *resp.NextCursor
	}
	return out
}
