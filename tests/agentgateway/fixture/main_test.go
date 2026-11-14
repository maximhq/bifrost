package fixture

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2asrv"
)

// TestInputRequiredContinuationAndTerminalStates verifies continuation and terminal state permutations.
func TestInputRequiredContinuationAndTerminalStates(t *testing.T) {
	handler := a2asrv.NewHandler(newExecutor())
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()

	first, err := handler.SendMessage(ctx, sendRequest("start lifecycle", "", ""))
	if err != nil {
		t.Fatalf("SendMessage(start lifecycle) error = %v", err)
	}
	primary, ok := first.(*a2a.Task)
	if !ok {
		t.Fatalf("SendMessage(start lifecycle) result type = %T, want *a2a.Task", first)
	}
	if primary.Status.State != a2a.TaskStateInputRequired {
		t.Fatalf("initial state = %s, want %s", primary.Status.State, a2a.TaskStateInputRequired)
	}

	continued, err := handler.SendMessage(ctx, sendRequest("continue: 3 days", primary.ContextID, primary.ID))
	if err != nil {
		t.Fatalf("SendMessage(continue) error = %v", err)
	}
	continuedTask, ok := continued.(*a2a.Task)
	if !ok {
		t.Fatalf("SendMessage(continue) result type = %T, want *a2a.Task", continued)
	}
	if continuedTask.ID != primary.ID || continuedTask.ContextID != primary.ContextID {
		t.Fatalf("continuation IDs = (%s, %s), want (%s, %s)", continuedTask.ID, continuedTask.ContextID, primary.ID, primary.ContextID)
	}
	if continuedTask.Status.State != a2a.TaskStateCompleted {
		t.Fatalf("continuation state = %s, want %s", continuedTask.Status.State, a2a.TaskStateCompleted)
	}
	if err := validateFixtureTravelPack(continuedTask.Artifacts); err != nil {
		t.Fatalf("continuation travel pack = %v", err)
	}

	for _, test := range []struct {
		name  string
		text  string
		state a2a.TaskState
	}{
		{name: "failed", text: "fail task", state: a2a.TaskStateFailed},
		{name: "rejected", text: "reject task", state: a2a.TaskStateRejected},
	} {
		t.Run(test.name, func(t *testing.T) {
			result, err := handler.SendMessage(ctx, sendRequest(test.text, primary.ContextID, ""))
			if err != nil {
				t.Fatalf("SendMessage() error = %v", err)
			}
			task, ok := result.(*a2a.Task)
			if !ok {
				t.Fatalf("SendMessage() result type = %T, want *a2a.Task", result)
			}
			if task.ContextID != primary.ContextID {
				t.Fatalf("task context = %s, want %s", task.ContextID, primary.ContextID)
			}
			if task.Status.State != test.state {
				t.Fatalf("task state = %s, want %s", task.Status.State, test.state)
			}
			if task.Status.Message == nil || messageText(task.Status.Message) == "" {
				t.Fatal("terminal task status has no Agent message")
			}
		})
	}
}

// sendRequest creates a task request with optional continuation identifiers.
func sendRequest(text, contextID string, taskID a2a.TaskID) *a2a.SendMessageRequest {
	message := a2a.NewMessage(a2a.MessageRoleUser, a2a.NewTextPart(text))
	message.ContextID = contextID
	message.TaskID = taskID
	return &a2a.SendMessageRequest{Message: message}
}

// validateFixtureTravelPack verifies the continuation produced each intended fixture medium.
func validateFixtureTravelPack(artifacts []*a2a.Artifact) error {
	if len(artifacts) < 4 {
		return fmt.Errorf("artifact count %d, want at least 4", len(artifacts))
	}
	media := map[string]bool{}
	for _, artifact := range artifacts {
		for _, part := range artifact.Parts {
			media[part.MediaType] = true
			if part.Filename == "lisbon-itinerary.txt" && !strings.Contains(string(part.Raw()), "Day 3") {
				return fmt.Errorf("itinerary file is incomplete")
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

// TestStreamExecutionSurvivesSubscriberDisconnect verifies future events remain available after disconnect.
func TestStreamExecutionSurvivesSubscriberDisconnect(t *testing.T) {
	executor := &executor{
		streamStepDelay:   100 * time.Millisecond,
		cancelWindowDelay: time.Second,
	}
	handler := a2asrv.NewHandler(executor)
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()

	message := a2a.NewMessage(a2a.MessageRoleUser, a2a.NewTextPart("interruptible stream"))
	message.ContextID = a2a.NewContextID()
	request := &a2a.SendMessageRequest{Message: message}

	var taskID a2a.TaskID
	for event, err := range handler.SendStreamingMessage(ctx, request) {
		if err != nil {
			t.Fatalf("SendStreamingMessage() error = %v", err)
		}
		if artifact, ok := event.(*a2a.TaskArtifactUpdateEvent); ok {
			taskID = artifact.TaskID
			break
		}
	}
	if taskID == "" {
		t.Fatal("stream ended before the first artifact")
	}

	ongoing, err := handler.GetTask(ctx, &a2a.GetTaskRequest{ID: taskID})
	if err != nil {
		t.Fatalf("GetTask() error = %v", err)
	}
	if ongoing.Status.State != a2a.TaskStateWorking {
		t.Fatalf("task state after disconnect = %s, want %s", ongoing.Status.State, a2a.TaskStateWorking)
	}

	var sawCheckpointTwo, sawCompleted bool
	for event, err := range handler.SubscribeToTask(ctx, &a2a.SubscribeToTaskRequest{ID: taskID}) {
		if err != nil {
			t.Fatalf("SubscribeToTask() error = %v", err)
		}
		switch value := event.(type) {
		case *a2a.TaskArtifactUpdateEvent:
			if value.Append && messageTextFromParts(value.Artifact.Parts) == "; checkpoint 2" {
				sawCheckpointTwo = true
			}
		case *a2a.TaskStatusUpdateEvent:
			if value.Status.State == a2a.TaskStateCompleted {
				sawCompleted = true
			}
		}
	}
	if !sawCheckpointTwo || !sawCompleted {
		t.Fatalf("resubscription future events: checkpoint2=%t completed=%t", sawCheckpointTwo, sawCompleted)
	}
}

// messageTextFromParts returns the first text part for stream assertions.
func messageTextFromParts(parts a2a.ContentParts) string {
	if len(parts) == 0 {
		return ""
	}
	return parts[0].Text()
}
