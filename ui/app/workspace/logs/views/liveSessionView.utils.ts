import { LiveTranscriptLine, ResponsesMessage } from "@/lib/types/logs";

import { getResponsesRole, type MessageRole } from "./responsesItemRow";

// The live panel honours the sheet's Messages filter the way the Responses view does: an absent
// set shows everything, a set keeps only the roles it names.

export function visibleTranscript(lines: LiveTranscriptLine[], roles?: Set<MessageRole>): LiveTranscriptLine[] {
	if (!roles) return lines;
	return lines.filter((line) => roles.has(line.role === "assistant" ? "assistant" : "user"));
}

export function visibleDelegationItems(items: ResponsesMessage[], roles?: Set<MessageRole>): ResponsesMessage[] {
	if (!roles) return items;
	return items.filter((item) => roles.has(getResponsesRole(item)));
}

// shortId keeps the head and tail of a provider id so a row of them fits on one line; the full
// id stays one hover away.
export function shortId(id: string): string {
	return id.length <= 20 ? id : `${id.slice(0, 9)}…${id.slice(-4)}`;
}

// toolNamesByCall maps each function call's id to the tool it invoked, so its result row can
// carry the tool's name instead of the bare call id.
export function toolNamesByCall(items: ResponsesMessage[]): Map<string, string> {
	const names = new Map<string, string>();
	for (const item of items) {
		if (item.type === "function_call" && item.call_id && item.name) names.set(item.call_id, item.name);
	}
	return names;
}