package driver

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2aclient"
)

// pushConfigID is the client-chosen ID of the lifecycle push configuration.
const pushConfigID = "lifecycle-push"

// Lifecycle configures one scenario run against an already registered Agent.
type Lifecycle struct {
	Gateway    *Gateway
	AgentName  string
	VirtualKey string
	// Bearer, when set, authenticates every call as an identity-provider user.
	Bearer string
	// Sink, when set, enables the push configuration and delivery steps.
	Sink *Sink
	// Release, when set, is called after push setup and just before the
	// resubscription, to let a gated fixture emit its remaining events.
	Release func()
	// CallURL, when set, replaces the scheme and host of every interface URL in
	// the resolved card. Cluster tests use it to send protocol calls to a node
	// other than the one the card advertises.
	CallURL string
}

// Result carries the identifiers produced by a run, for log assertions.
type Result struct {
	ContextID      string
	PrimaryTaskID  a2a.TaskID
	StreamTaskID   a2a.TaskID
	CanceledTaskID a2a.TaskID
	FailedTaskID   a2a.TaskID
	RejectedTaskID a2a.TaskID
	PushConfigID   string
}

// Run executes the full lifecycle: input-required continuation with artifacts,
// streaming with disconnect and resubscribe, push configuration and delivery,
// cancel, failed, rejected, list and extended card.
func (l *Lifecycle) Run(ctx context.Context) (*Result, error) {
	card, err := l.Gateway.WaitForCard(ctx, l.AgentName, l.VirtualKey, l.Bearer)
	if err != nil {
		return nil, fmt.Errorf("get public card: %w", err)
	}
	if l.CallURL != "" {
		target, err := url.Parse(l.CallURL)
		if err != nil {
			return nil, fmt.Errorf("parse call URL: %w", err)
		}
		for _, iface := range card.SupportedInterfaces {
			u, err := url.Parse(iface.URL)
			if err != nil {
				return nil, fmt.Errorf("parse interface URL: %w", err)
			}
			u.Scheme, u.Host = target.Scheme, target.Host
			iface.URL = u.String()
		}
	}
	client, err := a2aclient.NewFromCard(ctx, card, a2aclient.WithCallInterceptors(headerInterceptor{virtualKey: l.VirtualKey, bearer: l.Bearer}))
	if err != nil {
		return nil, fmt.Errorf("create A2A client: %w", err)
	}
	defer client.Destroy()

	res := &Result{}
	first, err := client.SendMessage(ctx, request("start lifecycle", "", "", false))
	if err != nil {
		return nil, fmt.Errorf("start lifecycle: %w", err)
	}
	primary, ok := first.(*a2a.Task)
	if !ok {
		return nil, fmt.Errorf("start lifecycle returned %T, want task", first)
	}
	if primary.Status.State != a2a.TaskStateInputRequired {
		return nil, fmt.Errorf("start lifecycle state %s, want input-required", primary.Status.State)
	}
	res.ContextID, res.PrimaryTaskID = primary.ContextID, primary.ID

	continued, err := client.SendMessage(ctx, request("continue: 3 days", primary.ContextID, primary.ID, false))
	if err != nil {
		return nil, fmt.Errorf("continue lifecycle: %w", err)
	}
	continuedTask, ok := continued.(*a2a.Task)
	if !ok || continuedTask.ID != primary.ID || continuedTask.ContextID != primary.ContextID {
		return nil, errors.New("continuation did not preserve task/context IDs")
	}
	if continuedTask.Status.State != a2a.TaskStateCompleted {
		return nil, fmt.Errorf("continuation state %s, want completed", continuedTask.Status.State)
	}
	if err := validateTravelPack(continuedTask.Artifacts); err != nil {
		return nil, fmt.Errorf("continuation travel pack: %w", err)
	}

	got, err := client.GetTask(ctx, &a2a.GetTaskRequest{ID: primary.ID})
	if err != nil {
		return nil, fmt.Errorf("tasks/get: %w", err)
	}
	if got.Status.State != a2a.TaskStateCompleted {
		return nil, fmt.Errorf("continued task state %s, want completed", got.Status.State)
	}

	streamTask, err := interruptStream(ctx, client, primary.ContextID)
	if err != nil {
		return nil, err
	}
	res.StreamTaskID = streamTask.ID
	ongoing, err := client.GetTask(ctx, &a2a.GetTaskRequest{ID: streamTask.ID})
	if err != nil {
		return nil, fmt.Errorf("get interrupted task: %w", err)
	}
	if ongoing.Status.State != a2a.TaskStateWorking {
		return nil, fmt.Errorf("interrupted task state %s, want working", ongoing.Status.State)
	}

	if l.Sink != nil {
		cfg, err := client.CreateTaskPushConfig(ctx, &a2a.PushConfig{TaskID: streamTask.ID, ID: pushConfigID, URL: l.Sink.URL, Token: l.Sink.Token})
		if err != nil {
			return nil, fmt.Errorf("push config create: %w", err)
		}
		res.PushConfigID = cfg.ID
		if _, err = client.GetTaskPushConfig(ctx, &a2a.GetTaskPushConfigRequest{TaskID: streamTask.ID, ID: cfg.ID}); err != nil {
			return nil, fmt.Errorf("push config get: %w", err)
		}
		configs, err := client.ListTaskPushConfigs(ctx, &a2a.ListTaskPushConfigRequest{TaskID: streamTask.ID})
		if err != nil || len(configs) == 0 {
			return nil, fmt.Errorf("push config list: count=%d error=%v", len(configs), err)
		}
	}

	if l.Release != nil {
		l.Release()
	}
	sawCheckpointTwo, sawCompleted := false, false
	for event, eventErr := range client.SubscribeToTask(ctx, &a2a.SubscribeToTaskRequest{ID: streamTask.ID}) {
		if eventErr != nil {
			return nil, fmt.Errorf("tasks/resubscribe: %w", eventErr)
		}
		switch v := event.(type) {
		case *a2a.TaskArtifactUpdateEvent:
			if v.Append && partText(v.Artifact.Parts) == "; checkpoint 2" {
				sawCheckpointTwo = true
			}
		case *a2a.TaskStatusUpdateEvent:
			sawCompleted = sawCompleted || v.Status.State == a2a.TaskStateCompleted
		}
		if sawCompleted {
			break
		}
	}
	if !sawCheckpointTwo || !sawCompleted {
		return nil, fmt.Errorf("tasks/resubscribe missed future events: checkpoint2=%t completed=%t", sawCheckpointTwo, sawCompleted)
	}

	if l.Sink != nil {
		if err := l.Sink.WaitFor(ctx, streamTask.ID); err != nil {
			return nil, err
		}
		if err := client.DeleteTaskPushConfig(ctx, &a2a.DeleteTaskPushConfigRequest{TaskID: streamTask.ID, ID: pushConfigID}); err != nil {
			return nil, fmt.Errorf("push config delete: %w", err)
		}
	}

	cancelResult, err := client.SendMessage(ctx, request("cancel me", primary.ContextID, "", true))
	if err != nil {
		return nil, fmt.Errorf("create cancelable task: %w", err)
	}
	cancelTask, ok := cancelResult.(*a2a.Task)
	if !ok || cancelTask.ID == primary.ID || cancelTask.ContextID != primary.ContextID {
		return nil, errors.New("cancelable task was not a second task in the same context")
	}
	canceled, err := client.CancelTask(ctx, &a2a.CancelTaskRequest{ID: cancelTask.ID})
	if err != nil {
		return nil, fmt.Errorf("tasks/cancel: %w", err)
	}
	if canceled.Status.State != a2a.TaskStateCanceled {
		return nil, fmt.Errorf("canceled task state %s, want canceled", canceled.Status.State)
	}
	res.CanceledTaskID = cancelTask.ID

	failed, err := sendTaskWithExpectedState(ctx, client, primary.ContextID, "fail task", a2a.TaskStateFailed)
	if err != nil {
		return nil, err
	}
	res.FailedTaskID = failed.ID
	rejected, err := sendTaskWithExpectedState(ctx, client, primary.ContextID, "reject task", a2a.TaskStateRejected)
	if err != nil {
		return nil, err
	}
	res.RejectedTaskID = rejected.ID

	listed, err := client.ListTasks(ctx, &a2a.ListTasksRequest{ContextID: primary.ContextID, PageSize: 20, IncludeArtifacts: true})
	if err != nil {
		return nil, fmt.Errorf("tasks/list: %w", err)
	}
	if len(listed.Tasks) < 5 {
		return nil, fmt.Errorf("tasks/list returned %d same-context tasks, want at least 5", len(listed.Tasks))
	}
	if card.Capabilities.ExtendedAgentCard {
		if _, err = client.GetExtendedAgentCard(ctx, &a2a.GetExtendedAgentCardRequest{}); err != nil {
			return nil, fmt.Errorf("extended card: %w", err)
		}
	}
	return res, nil
}

// validateTravelPack verifies that the completed itinerary carries text, image and video artifacts.
func validateTravelPack(artifacts []*a2a.Artifact) error {
	if len(artifacts) < 4 {
		return fmt.Errorf("artifact count %d, want at least 4", len(artifacts))
	}
	media := map[string]bool{}
	for _, artifact := range artifacts {
		for _, part := range artifact.Parts {
			media[part.MediaType] = true
			if part.Filename == "lisbon-itinerary.txt" && !strings.Contains(string(part.Raw()), "Day 3") {
				return errors.New("itinerary file is missing the complete three-day plan")
			}
		}
	}
	for _, mediaType := range []string{"text/plain", "image/jpeg", "video/mp4"} {
		if !media[mediaType] {
			return fmt.Errorf("missing %s artifact", mediaType)
		}
	}
	return nil
}

// sendTaskWithExpectedState sends a same-context task and validates its terminal status and message.
func sendTaskWithExpectedState(ctx context.Context, client *a2aclient.Client, contextID, text string, expected a2a.TaskState) (*a2a.Task, error) {
	result, err := client.SendMessage(ctx, request(text, contextID, "", false))
	if err != nil {
		return nil, fmt.Errorf("send %s task: %w", expected, err)
	}
	task, ok := result.(*a2a.Task)
	if !ok {
		return nil, fmt.Errorf("send %s task returned %T, want task", expected, result)
	}
	if task.ContextID != contextID {
		return nil, fmt.Errorf("send %s task context %s, want %s", expected, task.ContextID, contextID)
	}
	if task.Status.State != expected {
		return nil, fmt.Errorf("send task state %s, want %s", task.Status.State, expected)
	}
	if task.Status.Message == nil || partText(task.Status.Message.Parts) == "" {
		return nil, fmt.Errorf("send %s task returned no status message", expected)
	}
	return task, nil
}

// interruptStream disconnects after the first artifact so resubscription can verify future events.
func interruptStream(ctx context.Context, client *a2aclient.Client, contextID string) (*a2a.Task, error) {
	var task *a2a.Task
	for event, err := range client.SendStreamingMessage(ctx, request("interruptible stream", contextID, "", false)) {
		if err != nil {
			return nil, fmt.Errorf("message/stream: %w", err)
		}
		switch v := event.(type) {
		case *a2a.Task:
			task = v
		case *a2a.TaskArtifactUpdateEvent:
			if task == nil {
				task = &a2a.Task{ID: v.TaskID, ContextID: v.ContextID}
			}
			return task, nil
		}
	}
	return nil, errors.New("stream ended before the intentional interruption point")
}

// request creates a task request with optional continuation and return behavior.
func request(text, contextID string, taskID a2a.TaskID, returnImmediately bool) *a2a.SendMessageRequest {
	message := a2a.NewMessage(a2a.MessageRoleUser, a2a.NewTextPart(text))
	message.ContextID = contextID
	message.TaskID = taskID
	return &a2a.SendMessageRequest{Message: message, Config: &a2a.SendMessageConfig{ReturnImmediately: returnImmediately}}
}

// partText returns the first text part for assertions.
func partText(parts a2a.ContentParts) string {
	if len(parts) == 0 {
		return ""
	}
	return parts[0].Text()
}

type headerInterceptor struct{ virtualKey, bearer, project string }

// Before adds the configured virtual key to one outbound A2A call.
func (i headerInterceptor) Before(ctx context.Context, req *a2aclient.Request) (context.Context, any, error) {
	if i.virtualKey != "" {
		req.ServiceParams.Append("x-bf-vk", i.virtualKey)
	}
	if i.project != "" {
		req.ServiceParams.Append("x-bf-project-id", i.project)
	}
	if i.bearer != "" {
		req.ServiceParams.Append("Authorization", "Bearer "+i.bearer)
	}
	return ctx, nil, nil
}

// After leaves responses unchanged.
func (headerInterceptor) After(context.Context, *a2aclient.Response) error { return nil }

// Probe sends one message as the given credentials and returns the gateway's
// verdict, so access checks cover the protocol path and not only the card.
func (g *Gateway) Probe(ctx context.Context, agent, virtualKey, bearer string) error {
	card, err := g.FetchCard(ctx, agent, virtualKey, bearer)
	if err != nil {
		return fmt.Errorf("card: %w", err)
	}
	client, err := a2aclient.NewFromCard(ctx, card, a2aclient.WithCallInterceptors(headerInterceptor{virtualKey: virtualKey, bearer: bearer, project: g.ProjectID}))
	if err != nil {
		return err
	}
	defer client.Destroy()
	_, err = client.SendMessage(ctx, request("fail task", "", "", false))
	return err
}
