// Only generated deltas count: initial metadata and terminal snapshots must not
// satisfy the cancellation threshold or double-count content.
export function generatedTextChars(event) {
  let count = 0;
  for (const choice of event.choices || []) {
    const delta = choice.delta || {};
    count += (delta.content || delta.reasoning_content || delta.reasoning || "").length;
    for (const call of delta.tool_calls || []) count += (call.function?.arguments || "").length;
  }
  if (event.type === "content_block_delta") {
    count += (event.delta?.text || event.delta?.thinking || event.delta?.partial_json || "").length;
  } else if (/^response\.(output_text|reasoning_text|reasoning_summary_text|function_call_arguments)\.delta$/.test(event.type || "")) {
    count += (event.delta || "").length;
  }
  for (const candidate of event.candidates || []) {
    for (const part of candidate.content?.parts || []) count += (part.text || "").length;
  }
  return count;
}

export function cancelledUsageFailure(row, provider) {
  const input = Number(row.prompt_tokens || 0);
  const output = Number(row.completion_tokens || 0);
  const total = Number(row.total_tokens || 0);
  if (!(Number(row.cost) > 0 && input > 0 && output > 0)) {
    return `cancelled ${provider} stream lacks cost/input/output usage: ${row.cost}/${input}/${output}`;
  }
  if (total !== input + output) return `inconsistent total tokens: ${total} != ${input} + ${output}`;
  // Probes consume at least 128 generated characters of a counting response.
  // A lone Anthropic message_start cursor (commonly 1, 4, or 8) is insufficient.
  if (provider === "anthropic" && output <= 8) return `stale initial Anthropic output count: ${output}`;
  return undefined;
}
