package schemas

// A2ARequestType discriminates the Agent-to-Agent protocol operation carried by
// BifrostA2ARequest/BifrostA2AResponse. It exists so a plugin can decide which
// operations it cares about without sniffing pointer fields on the envelope,
// exactly as MCPRequestType does for the MCP pipeline.
type A2ARequestType string

const (
	// A2ARequestTypeGetAgentCard is a discovery read of an agent's public card.
	A2ARequestTypeGetAgentCard A2ARequestType = "get_agent_card"
	// A2ARequestTypeGetExtendedAgentCard is a read of an agent's authenticated
	// extended card. Unlike the public card it is a protocol operation that is
	// never served anonymously, so it always carries a Bifrost identity.
	A2ARequestTypeGetExtendedAgentCard A2ARequestType = "get_extended_agent_card"
	// A2ARequestTypeSendMessage is a non-streaming message send.
	A2ARequestTypeSendMessage A2ARequestType = "send_message"
	// A2ARequestTypeSendStreamingMessage is a streaming message send.
	A2ARequestTypeSendStreamingMessage A2ARequestType = "send_streaming_message"
	// A2ARequestTypeGetTask is a single task lookup.
	A2ARequestTypeGetTask A2ARequestType = "get_task"
	// A2ARequestTypeListTasks is a task listing.
	A2ARequestTypeListTasks A2ARequestType = "list_tasks"
	// A2ARequestTypeCancelTask is an explicit task cancellation.
	A2ARequestTypeCancelTask A2ARequestType = "cancel_task"
	// A2ARequestTypeSubscribeToTask is a task event subscription.
	A2ARequestTypeSubscribeToTask A2ARequestType = "subscribe_to_task"
	// A2ARequestTypeCreateTaskPushConfig registers a downstream push callback.
	A2ARequestTypeCreateTaskPushConfig A2ARequestType = "create_task_push_config"
	// A2ARequestTypeGetTaskPushConfig reads one stored push configuration.
	A2ARequestTypeGetTaskPushConfig A2ARequestType = "get_task_push_config"
	// A2ARequestTypeListTaskPushConfigs lists stored push configurations for a task.
	A2ARequestTypeListTaskPushConfigs A2ARequestType = "list_task_push_configs"
	// A2ARequestTypeDeleteTaskPushConfig removes one push configuration.
	A2ARequestTypeDeleteTaskPushConfig A2ARequestType = "delete_task_push_config"
	// A2ARequestTypePushNotification is one verified push received from the
	// upstream agent on Bifrost's push ingress and accepted into the outbox.
	A2ARequestTypePushNotification A2ARequestType = "push_notification"
	// A2ARequestTypePushDelivery is one re-originated delivery attempt of a
	// queued push to the downstream callback.
	A2ARequestTypePushDelivery A2ARequestType = "push_delivery"
)

// IsStreaming reports whether the operation produces a stream of events rather
// than a single result. Plugins use it to know that the post-hook they receive
// describes the whole stream's outcome, not one event.
func (t A2ARequestType) IsStreaming() bool {
	switch t {
	case A2ARequestTypeSendStreamingMessage, A2ARequestTypeSubscribeToTask:
		return true
	}
	return false
}

// OperationName returns the externally meaningful operation name recorded in
// Agent logs and telemetry. Protocol operations use their A2A v1 names, while
// gateway-owned operations retain their Bifrost names.
func (t A2ARequestType) OperationName() string {
	switch t {
	case A2ARequestTypeGetAgentCard:
		return "GetAgentCard"
	case A2ARequestTypeGetExtendedAgentCard:
		return "GetExtendedAgentCard"
	case A2ARequestTypeSendMessage:
		return "SendMessage"
	case A2ARequestTypeSendStreamingMessage:
		return "SendStreamingMessage"
	case A2ARequestTypeGetTask:
		return "GetTask"
	case A2ARequestTypeListTasks:
		return "ListTasks"
	case A2ARequestTypeCancelTask:
		return "CancelTask"
	case A2ARequestTypeSubscribeToTask:
		return "SubscribeToTask"
	case A2ARequestTypeCreateTaskPushConfig:
		return "CreateTaskPushNotificationConfig"
	case A2ARequestTypeGetTaskPushConfig:
		return "GetTaskPushNotificationConfig"
	case A2ARequestTypeListTaskPushConfigs:
		return "ListTaskPushNotificationConfigs"
	case A2ARequestTypeDeleteTaskPushConfig:
		return "DeleteTaskPushNotificationConfig"
	default:
		return string(t)
	}
}

// JSONRPCMethodName returns the strict A2A v1 JSON-RPC method name when this
// request is a JSON-RPC operation. Public Agent Card discovery and gateway-owned
// push operations are not JSON-RPC methods.
func (t A2ARequestType) JSONRPCMethodName() (string, bool) {
	switch t {
	case A2ARequestTypeGetExtendedAgentCard,
		A2ARequestTypeSendMessage,
		A2ARequestTypeSendStreamingMessage,
		A2ARequestTypeGetTask,
		A2ARequestTypeListTasks,
		A2ARequestTypeCancelTask,
		A2ARequestTypeSubscribeToTask,
		A2ARequestTypeCreateTaskPushConfig,
		A2ARequestTypeGetTaskPushConfig,
		A2ARequestTypeListTaskPushConfigs,
		A2ARequestTypeDeleteTaskPushConfig:
		return t.OperationName(), true
	default:
		return "", false
	}
}

// BifrostA2ARequest is the envelope for A2A operations that flow through the
// PreA2AHook/PostA2AHook pipeline. RequestType identifies the operation while
// operation-specific projections carry correlation data when available.
// AgentName is always set because it is the resource key agent authorization is
// decided against. The envelope carries protocol-neutral identifiers rather
// than SDK types so plugins in other modules need no A2A SDK dependency.
type BifrostA2ARequest struct {
	RequestType A2ARequestType
	AgentName   string // Agent this request targets (always set, regardless of request type)
	RequestBody *string

	*BifrostA2ASendMessageRequest
	*BifrostA2ATaskRequest
	*BifrostA2APushRequest
}

// BifrostA2ASendMessageRequest carries correlation identifiers for policy and
// indexing; the parent envelope carries the strict-v1 typed request serialization.
type BifrostA2ASendMessageRequest struct {
	MessageID string
	ContextID string
	TaskID    string
}

// BifrostA2ATaskRequest identifies the task a lookup, cancellation, or
// subscription targets.
type BifrostA2ATaskRequest struct {
	TaskID string
}

// BifrostA2APushRequest carries push-relay correlation identifiers for the push
// configuration operations, ingress accepts, and delivery attempts. Fields are
// populated as they become known: a create has no PushConfigID until the
// upstream assigns one, and only delivery attempts have an AttemptID.
type BifrostA2APushRequest struct {
	PushConfigID string
	DeliveryID   string
	AttemptID    string
}

// BifrostA2AResponse is the envelope handed to PostA2AHook. Exactly one embedded
// sub-response pointer is populated, matching the request type recorded on
// ExtraFields.
type BifrostA2AResponse struct {
	*BifrostA2AGetAgentCardResponse
	*BifrostA2ASendMessageResponse
	*BifrostA2ATaskResponse
	*BifrostA2AListTasksResponse
	*BifrostA2APushResponse

	ResponseBody *string
	ExtraFields  BifrostA2AResponseExtraFields
}

// BifrostA2AGetAgentCardResponse reports that a card was served. ResponseBody on
// the parent envelope carries the bounded, typed JSON serialization so logging
// and post-hooks can observe or replace the public-card response without taking
// a dependency on the A2A SDK.
type BifrostA2AGetAgentCardResponse struct{}

// BifrostA2ASendMessageResponse carries the identity the upstream agent assigned
// to a non-streaming send, so a plugin can correlate the outcome without
// re-parsing the SDK result.
type BifrostA2ASendMessageResponse struct {
	TaskID    string
	ContextID string
	MessageID string
}

// BifrostA2ATaskResponse describes the task returned by a lookup or
// cancellation.
type BifrostA2ATaskResponse struct {
	TaskID    string
	ContextID string
	State     string
}

// BifrostA2AListTasksResponse reports how many tasks the upstream returned.
type BifrostA2AListTasksResponse struct {
	Count int
}

// BifrostA2APushResponse reports the outcome correlation of a push-relay
// operation: the configuration it acted on and, for ingress accepts and
// delivery attempts, the durable delivery it produced or attempted.
type BifrostA2APushResponse struct {
	PushConfigID string
	DeliveryID   string
	AttemptID    string
}

// A2AEventType identifies the strict-v1 event shape observed from an upstream
// stream without exposing SDK types to plugins.
type A2AEventType string

const (
	A2AEventTypeMessage        A2AEventType = "message"
	A2AEventTypeTask           A2AEventType = "task"
	A2AEventTypeStatusUpdate   A2AEventType = "status_update"
	A2AEventTypeArtifactUpdate A2AEventType = "artifact_update"
)

// BifrostA2AEvent is a bounded, protocol-neutral observation of one event that
// was accepted by the downstream iterator. Sequence is gateway observation
// order within the operation; Body is the typed strict-v1 event serialization.
type BifrostA2AEvent struct {
	RequestType A2ARequestType
	AgentName   string
	EventType   A2AEventType
	Sequence    int64
	TaskID      string
	ContextID   string
	MessageID   string
	ArtifactID  string
	TaskState   string
	ContentType string
	Body        *string
}

// A2AEventObserver is an optional, observation-only extension implemented by
// plugins that need per-event telemetry. It cannot mutate or short-circuit the
// stream and is invoked only for plugins whose operation pre-hook already ran.
type A2AEventObserver interface {
	ObserveA2AEvent(ctx *BifrostContext, event *BifrostA2AEvent)
}

// BifrostA2AResponseExtraFields lets PostA2AHook discriminate the operation from
// the response alone, exactly as BifrostMCPResponseExtraFields does for MCP.
type BifrostA2AResponseExtraFields struct {
	A2ARequestType A2ARequestType `json:"a2a_request_type"`
	AgentName      string         `json:"agent_name"`
	Latency        int64          `json:"latency"` // in milliseconds
	// UpstreamLatency is the total time spent blocked on upstream agent sockets
	// across every candidate attempt, in milliseconds. Nil when the operation
	// never accumulated one; nil means unknown, not zero. Mirrors
	// BifrostResponseExtraFields.UpstreamLatency.
	UpstreamLatency *int64 `json:"upstream_latency,omitempty"`
	// OverheadLatency is Bifrost's own cost (total minus UpstreamLatency), in ms.
	// Not serialized (json:"-"): at response time it can only be an estimate; the
	// authoritative value is stamped on the trace and logged at completion.
	// Nil means unknown. Mirrors BifrostResponseExtraFields.OverheadLatency.
	OverheadLatency *int64 `json:"-"`
}

// PopulateExtraFields backfills the request type and agent name on a response
// when a plugin's short-circuit did not set them, so PostA2AHook can always
// discriminate. Mirrors BifrostMCPResponse.PopulateExtraFields.
func (r *BifrostA2AResponse) PopulateExtraFields(a2aRequestType A2ARequestType, agentName string) {
	if r == nil {
		return
	}
	if r.ExtraFields.A2ARequestType == "" {
		r.ExtraFields.A2ARequestType = a2aRequestType
	}
	if r.ExtraFields.AgentName == "" {
		r.ExtraFields.AgentName = agentName
	}
}
