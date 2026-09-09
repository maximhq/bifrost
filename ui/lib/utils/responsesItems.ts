import type { ResponsesMessage } from "@/lib/types/logs";
import { applyRedactionMapping } from "@/lib/utils/redaction";

// Item fields the log timeline renders on its own: the role label, meta line, message text,
// tool output, and declared tool list.
const RENDERED_ITEM_FIELDS = new Set(["id", "type", "status", "role", "phase", "content", "call_id", "name", "output", "tools"]);

// Rendered by the timeline's reasoning branch, which runs only for reasoning items — a
// compaction item carries encrypted_content that nothing else on the row would show.
const REASONING_ONLY_FIELDS = new Set(["summary", "encrypted_content"]);

const SUMMARY_MAX_LENGTH = 72;

// isRenderedField reports whether the timeline already shows a field elsewhere on the row.
function isRenderedField(msg: ResponsesMessage, field: string, value: unknown): boolean {
	if (RENDERED_ITEM_FIELDS.has(field)) return true;
	if (REASONING_ONLY_FIELDS.has(field)) return msg.type === "reasoning";
	// `arguments` is a JSON string on function_call, which the timeline shows as the row text,
	// but a JSON object on codex tool_search_call items, which it cannot.
	return field === "arguments" && typeof value === "string";
}

// isEmptyValue reports the zero values a provider emits for a field it did not use. `false` and
// `0` are meaningful (mcp_approval_responses carries `approve: false`) and are kept.
function isEmptyValue(value: unknown): boolean {
	if (value == null) return true;
	if (typeof value === "string") return value.trim().length === 0;
	return Array.isArray(value) && value.length === 0;
}

// truncateSummary reveals redacted placeholders and keeps the result short enough to sit on a
// single meta line. Revealing first is what makes it work: a `[TYPE-N]` placeholder straddling
// the cut is no longer a whole token, so a later reveal would silently leave the row redacted.
function truncateSummary(value: string, mapping?: Record<string, string>): string {
	const revealed = applyRedactionMapping(value, mapping).trim();
	return revealed.length > SUMMARY_MAX_LENGTH ? `${revealed.slice(0, SUMMARY_MAX_LENGTH - 1)}…` : revealed;
}

// summarizeQueries renders a query list as its first entry plus a count of the rest.
function summarizeQueries(value: unknown, mapping?: Record<string, string>): string | undefined {
	if (!Array.isArray(value)) return undefined;
	const queries = value.filter((query): query is string => typeof query === "string" && query.trim().length > 0);
	if (queries.length === 0) return undefined;
	const first = truncateSummary(queries[0], mapping);
	return queries.length > 1 ? `${first} +${queries.length - 1} more` : first;
}

// firstDetail returns the first usable string among candidate fields, truncated for display.
function firstDetail(values: unknown[], mapping?: Record<string, string>): string | undefined {
	const found = values.find((value): value is string => typeof value === "string" && value.trim().length > 0);
	return found ? truncateSummary(found, mapping) : undefined;
}

// extractResponsesItemPayload collects the part of a Responses item that lives outside the
// fields the timeline renders itself — `action` on web_search_call, `input` on custom_tool_call,
// `code` on code_interpreter_call, `encrypted_content` on compaction, and so on. Providers keep
// adding item types, so it works off what the item actually carries rather than a per-type
// switch, which leaves new types rendering correctly with no change here.
export function extractResponsesItemPayload(msg: ResponsesMessage): Record<string, unknown> | undefined {
	const payload = Object.fromEntries(
		Object.entries(msg as Record<string, unknown>).filter(([field, value]) => !isRenderedField(msg, field, value) && !isEmptyValue(value)),
	);
	return Object.keys(payload).length > 0 ? payload : undefined;
}

// summarizeResponsesToolCall pulls the one identifying detail off a server-side tool item — what
// was searched, which page was fetched, which MCP server answered — so the timeline's meta line
// says more than the bare item type. Returns undefined for items that carry no such detail. The
// mapping reveals redacted values when the row does, keeping the meta line and the payload below
// it in agreement; `action.type` is an enum and is never redacted.
export function summarizeResponsesToolCall(msg: ResponsesMessage, mapping?: Record<string, string>): string | undefined {
	const item = msg as Record<string, unknown>;
	const action = item.action as Record<string, unknown> | undefined;
	if (action && typeof action === "object" && !Array.isArray(action)) {
		const detail =
			summarizeQueries(action.queries, mapping) ??
			firstDetail(
				[action.query, action.url, action.pattern, Array.isArray(action.command) ? action.command.join(" ") : undefined, action.name],
				mapping,
			);
		const actionType = typeof action.type === "string" ? action.type : undefined;
		return [actionType, detail].filter(Boolean).join(" · ") || undefined;
	}
	return summarizeQueries(item.queries, mapping) ?? firstDetail([item.server_label, item.container_id], mapping);
}