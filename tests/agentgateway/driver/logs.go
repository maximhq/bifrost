package driver

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// LogOperation mirrors the hydrated Agent history operation: one request row
// plus the event rows it produced. Only fields the assertions read are decoded.
type LogOperation struct {
	LogRow
	Events []LogRow `json:"events"`
}

// LogRow is one Agent log row.
type LogRow struct {
	ID               string   `json:"id"`
	RecordKind       string   `json:"record_kind"`
	Operation        string   `json:"operation"`
	Status           string   `json:"status"`
	AgentName        string   `json:"agent_name"`
	RequestID        string   `json:"request_id"`
	TaskID           string   `json:"task_id"`
	ContextID        string   `json:"context_id"`
	EventType        string   `json:"event_type"`
	TaskState        string   `json:"task_state"`
	DeliveryID       string   `json:"delivery_id"`
	PushConfigID     string   `json:"push_config_id"`
	UserID           string   `json:"user_id"`
	VirtualKeyID     string   `json:"virtual_key_id"`
	TeamID           string   `json:"team_id"`
	CustomerID       string   `json:"customer_id"`
	BusinessUnitID   string   `json:"business_unit_id"`
	ProjectID        string   `json:"project_id"`
	TeamIDs          []string `json:"team_ids"`
	BusinessUnitIDs  []string `json:"business_unit_ids"`
	CustomerIDs      []string `json:"customer_ids"`
	EventSequenceRaw *int64   `json:"event_sequence"`
	ResponseBody     string   `json:"response_body"`
}

// Tags are the identity annotations every request row for the run must carry.
// Empty fields are not checked.
type Tags struct {
	UserID, VirtualKeyID, TeamID, CustomerID, BusinessUnitID, ProjectID string
}

// FetchOperations returns every hydrated operation logged for the Agent.
func (g *Gateway) FetchOperations(ctx context.Context, agent string) ([]LogOperation, error) {
	q := url.Values{"hydrate": {"true"}, "agent_name": {agent}, "limit": {"200"}, "record_kind": {"request"}}
	status, body, err := g.Do(ctx, http.MethodGet, "/api/agents/history?"+q.Encode(), nil, nil)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("agent history: HTTP %d: %s", status, truncate(body))
	}
	var out struct {
		Logs []LogOperation `json:"logs"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("decode agent history: %w", err)
	}
	return out.Logs, nil
}

// AssertLogs polls until the run's Agent logs are ingested, then checks the
// operation counts, request/event correlation, task and context IDs, terminal
// states, uniqueness per logical operation, and identity tags. pushed says
// whether push delivery was part of the run.
func AssertLogs(ctx context.Context, g *Gateway, agent string, res *Result, pushed bool, tags Tags) error {
	var last error
	var ops []LogOperation
	for {
		var err error
		ops, err = g.FetchOperations(ctx, agent)
		if err == nil {
			err = checkOperations(ops, agent, res, pushed, tags)
		}
		if err == nil {
			return nil
		}
		last = err
		select {
		case <-ctx.Done():
			return fmt.Errorf("agent logs never converged: %w\n%s", last, summarize(ops))
		case <-time.After(500 * time.Millisecond):
		}
	}
}

// checkOperations validates one snapshot of the Agent's operations.
func checkOperations(ops []LogOperation, agent string, res *Result, pushed bool, tags Tags) error {
	byOp := map[string][]LogOperation{}
	seen := map[string]bool{}
	for _, op := range ops {
		if op.AgentName != agent {
			return fmt.Errorf("operation %s has agent %q, want %q", op.Operation, op.AgentName, agent)
		}
		if op.RecordKind != "request" {
			return fmt.Errorf("operation %s has record kind %q", op.Operation, op.RecordKind)
		}
		if op.RequestID == "" || seen[op.RequestID] {
			return fmt.Errorf("operation %s has empty or duplicate request_id %q", op.Operation, op.RequestID)
		}
		seen[op.RequestID] = true
		for _, ev := range op.Events {
			if ev.RequestID != op.RequestID {
				return fmt.Errorf("event %s of %s is correlated under %q, want %q", ev.EventType, op.Operation, ev.RequestID, op.RequestID)
			}
		}
		// Push ingress comes from the upstream Agent and delivery runs in the
		// background relay, so neither carries the calling client's identity.
		// Card fetches are skipped because callers may probe with other keys.
		if op.Operation != "push_notification" && op.Operation != "push_delivery" && op.Operation != "GetAgentCard" {
			if err := checkTags(op.LogRow, tags); err != nil {
				return fmt.Errorf("%s: %w", op.Operation, err)
			}
		}
		byOp[op.Operation] = append(byOp[op.Operation], op)
	}

	// SendMessage: start, continue, cancel-create, fail, reject.
	if n := len(byOp["SendMessage"]); n != 5 {
		return fmt.Errorf("SendMessage operations = %d, want 5", n)
	}
	var continued bool
	for _, op := range byOp["SendMessage"] {
		if op.TaskID == string(res.PrimaryTaskID) && hasState(op.LogRow, "COMPLETED") {
			continued = op.ContextID == res.ContextID
		}
	}
	if !continued {
		return fmt.Errorf("no completed SendMessage for task %s in context %s", res.PrimaryTaskID, res.ContextID)
	}
	for _, want := range []struct{ id, state string }{
		{string(res.FailedTaskID), "FAILED"}, {string(res.RejectedTaskID), "REJECTED"}, {string(res.CanceledTaskID), ""},
	} {
		if !hasTaskState(byOp["SendMessage"], want.id, want.state) {
			return fmt.Errorf("no SendMessage for task %s with state %s", want.id, want.state)
		}
	}

	streams := byOp["SendStreamingMessage"]
	if len(streams) != 1 {
		return fmt.Errorf("SendStreamingMessage operations = %d, want 1", len(streams))
	}
	if streams[0].TaskID != string(res.StreamTaskID) || streams[0].ContextID != res.ContextID {
		return fmt.Errorf("streaming operation task/context = %s/%s, want %s/%s", streams[0].TaskID, streams[0].ContextID, res.StreamTaskID, res.ContextID)
	}
	if !hasEvent(streams[0].Events, "artifact_update") || !hasEvent(streams[0].Events, "status_update") {
		return fmt.Errorf("streaming operation lacks artifact_update and status_update events")
	}

	subs := byOp["SubscribeToTask"]
	if len(subs) != 1 || !hasEvent(subs[0].Events, "artifact_update") || !hasEvent(subs[0].Events, "status_update") {
		return fmt.Errorf("SubscribeToTask operations = %d, want 1 with artifact and status events", len(subs))
	}
	if subs[0].TaskID != string(res.StreamTaskID) || subs[0].ContextID != res.ContextID {
		return fmt.Errorf("subscription task/context = %s/%s, want %s/%s", subs[0].TaskID, subs[0].ContextID, res.StreamTaskID, res.ContextID)
	}
	for _, ev := range subs[0].Events {
		if ev.TaskID != string(res.StreamTaskID) || ev.ContextID != res.ContextID {
			return fmt.Errorf("subscription event %s task/context = %s/%s, want %s/%s", ev.EventType, ev.TaskID, ev.ContextID, res.StreamTaskID, res.ContextID)
		}
	}
	if !terminal(subs[0].Events, "COMPLETED") {
		return fmt.Errorf("SubscribeToTask has no terminal COMPLETED status event")
	}

	for _, name := range []string{"GetTask", "ListTasks", "CancelTask", "GetExtendedAgentCard"} {
		if len(byOp[name]) == 0 {
			return fmt.Errorf("no %s operation logged", name)
		}
	}
	if pushed {
		for _, name := range []string{"CreateTaskPushNotificationConfig", "GetTaskPushNotificationConfig", "ListTaskPushNotificationConfigs", "DeleteTaskPushNotificationConfig"} {
			if len(byOp[name]) == 0 {
				return fmt.Errorf("no %s operation logged", name)
			}
		}
		if n := len(byOp["push_notification"]); n < 2 {
			return fmt.Errorf("push_notification ingress operations = %d, want at least 2", n)
		}
		for _, n := range byOp["push_notification"] {
			if n.Status != "success" {
				return fmt.Errorf("push_notification ingress for task %s has status %q, want success", n.TaskID, n.Status)
			}
		}
		deliveries := byOp["push_delivery"]
		if len(deliveries) < 2 {
			return fmt.Errorf("push_delivery operations = %d, want at least 2", len(deliveries))
		}
		for _, d := range deliveries {
			if d.DeliveryID == "" || d.TaskID != string(res.StreamTaskID) {
				return fmt.Errorf("push_delivery row has delivery_id %q task %q", d.DeliveryID, d.TaskID)
			}
		}
	}
	return nil
}

// checkTags verifies the populated expected tags against a row.
func checkTags(row LogRow, t Tags) error {
	for _, c := range []struct{ name, got, want string }{
		{"user_id", row.UserID, t.UserID}, {"virtual_key_id", row.VirtualKeyID, t.VirtualKeyID},
		{"team_id", row.TeamID, t.TeamID}, {"customer_id", row.CustomerID, t.CustomerID},
		{"business_unit_id", row.BusinessUnitID, t.BusinessUnitID}, {"project_id", row.ProjectID, t.ProjectID},
	} {
		if c.want != "" && c.got != c.want {
			return fmt.Errorf("%s = %q, want %q", c.name, c.got, c.want)
		}
	}
	return nil
}

// hasTaskState reports whether any operation targets the task with the state.
func hasTaskState(ops []LogOperation, taskID, state string) bool {
	for _, op := range ops {
		if op.TaskID == taskID && hasState(op.LogRow, state) {
			return true
		}
	}
	return false
}

// hasState reports whether a row's task state, or the task state in its
// response body, names the state. SendMessage request rows carry no task_state
// column, so the response body is the only place their outcome is recorded.
func hasState(row LogRow, state string) bool {
	return strings.Contains(strings.ToUpper(row.TaskState), state) || strings.Contains(row.ResponseBody, "TASK_STATE_"+state)
}

// hasEvent reports whether an event of the type exists.
func hasEvent(events []LogRow, eventType string) bool {
	for _, ev := range events {
		if ev.EventType == eventType {
			return true
		}
	}
	return false
}

// terminal reports whether a status event carries the terminal state.
func terminal(events []LogRow, state string) bool {
	for _, ev := range events {
		if ev.EventType == "status_update" && strings.Contains(strings.ToUpper(ev.TaskState), state) {
			return true
		}
	}
	return false
}

// summarize renders one line per operation and event for failure messages.
func summarize(ops []LogOperation) string {
	var b strings.Builder
	for _, op := range ops {
		fmt.Fprintf(&b, "%s status=%s task=%s ctx=%s state=%s req=%s events=%d\n", op.Operation, op.Status, op.TaskID, op.ContextID, op.TaskState, op.RequestID, len(op.Events))
		for _, ev := range op.Events {
			fmt.Fprintf(&b, "    event %s state=%s req=%s\n", ev.EventType, ev.TaskState, ev.RequestID)
		}
	}
	return b.String()
}
