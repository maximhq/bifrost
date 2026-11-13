// Synthetic Task snapshots keep these UI regressions independent of gateway traffic.
export function createTaskHistoryOperation() {
  const suffix = Date.now();
  const question = {
    messageId: `question-${suffix}`,
    role: "ROLE_USER",
    parts: [{ text: "Count yesterday's support issues" }],
  };
  const task = {
    id: `task-${suffix}`,
    contextId: `context-${suffix}`,
    status: { state: "TASK_STATE_COMPLETED" },
    history: [
      question,
      {
        messageId: `progress-${suffix}`,
        role: "ROLE_AGENT",
        parts: [{ text: "Checked all support issues." }],
      },
    ],
    artifacts: [
      {
        artifactId: `answer-${suffix}`,
        name: "Support issue count",
        parts: [{ text: "A total of 8 issues were raised yesterday." }],
      },
    ],
  };
  return {
    id: `request-${suffix}`,
    timestamp: new Date().toISOString(),
    record_kind: "request",
    operation: "SendStreamingMessage",
    status: "success",
    agent_name: "history-test-agent",
    request_id: `request-${suffix}`,
    task_id: task.id,
    context_id: task.contextId,
    input: "Count yesterday's support issues",
    request_body: JSON.stringify({ message: question }),
    events: [
      {
        id: `event-${suffix}`,
        timestamp: new Date().toISOString(),
        record_kind: "event",
        operation: "SendStreamingMessage",
        status: "success",
        agent_name: "history-test-agent",
        request_id: `request-${suffix}`,
        task_id: task.id,
        context_id: task.contextId,
        event_type: "task",
        event_body: JSON.stringify(task),
      },
    ],
  };
}