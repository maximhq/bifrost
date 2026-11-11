import type { AgentLogDetail } from "@/lib/types/agentLogs";

// An A2A artifact recovered from an operation, with where it came from.
export interface A2AArtifactRecord {
	key: string; // artifactId, or a positional fallback when the agent sent none
	artifact: Record<string, unknown>;
	name?: string;
	timestamp?: string; // when the carrying event or response was recorded
	eventId?: string; // the artifact_update event row, when it came from one
}

function asRecord(value: unknown): Record<string, unknown> | undefined {
	return value && typeof value === "object" && !Array.isArray(value) ? (value as Record<string, unknown>) : undefined;
}

function parseBody(body?: string): Record<string, unknown> | undefined {
	if (!body) return undefined;
	try {
		return asRecord(JSON.parse(body));
	} catch {
		return undefined;
	}
}

// unwrapEnvelope strips the JSON-RPC result/params layers the gateway records around A2A objects.
function unwrapEnvelope(root: Record<string, unknown> | undefined): Record<string, unknown> | undefined {
	let current = root;
	for (let depth = 0; current && depth < 3; depth++) {
		const next = asRecord(current.result) ?? asRecord(current.params);
		if (!next) break;
		current = next;
	}
	return current;
}

// artifactFromEvent reads one artifact_update event body (v1 { artifactUpdate } or v0.3 { kind, artifact }).
function artifactFromEvent(body?: string): { artifact: Record<string, unknown>; append: boolean } | undefined {
	const root = unwrapEnvelope(parseBody(body));
	if (!root) return undefined;
	const update = asRecord(root.artifactUpdate) ?? root;
	const artifact = asRecord(update.artifact);
	return artifact ? { artifact, append: update.append === true } : undefined;
}

// artifactsFromResponse reads the final Task's artifacts from a non-streaming response ({ task } or a bare Task).
function artifactsFromResponse(body?: string): Record<string, unknown>[] {
	const root = unwrapEnvelope(parseBody(body));
	if (!root) return [];
	const task = asRecord(root.task) ?? root;
	return Array.isArray(task.artifacts) ? task.artifacts.map(asRecord).filter((a): a is Record<string, unknown> => Boolean(a)) : [];
}

function artifactKey(artifact: Record<string, unknown>, fallback: string): string {
	return typeof artifact.artifactId === "string" && artifact.artifactId ? artifact.artifactId : fallback;
}

/**
 * Collects the artifacts an operation produced: streamed artifact_update events first, then the final Task's
 * artifacts from the response body. A chunk sent with append=true adds its parts to the artifact already seen
 * under that id; any other repeat replaces it, as an A2A client would.
 */
export function collectA2AArtifacts(operation: AgentLogDetail | undefined, events: AgentLogDetail[] = []): A2AArtifactRecord[] {
	const byKey = new Map<string, A2AArtifactRecord>();
	events.forEach((event, index) => {
		if (event.event_type !== "artifact_update") return;
		const update = artifactFromEvent(event.event_body);
		if (!update) return;
		const key = artifactKey(update.artifact, event.artifact_id || `event-${index}`);
		const previous = byKey.get(key);
		const artifact =
			update.append && previous
				? {
						...previous.artifact,
						...update.artifact,
						parts: [
							...(Array.isArray(previous.artifact.parts) ? previous.artifact.parts : []),
							...(Array.isArray(update.artifact.parts) ? update.artifact.parts : []),
						],
					}
				: update.artifact;
		byKey.set(key, {
			key,
			artifact,
			name: typeof artifact.name === "string" ? artifact.name : previous?.name,
			timestamp: event.timestamp,
			eventId: event.id,
		});
	});
	artifactsFromResponse(operation?.response_body).forEach((artifact, index) => {
		const key = artifactKey(artifact, `response-${index}`);
		if (byKey.has(key)) return;
		byKey.set(key, { key, artifact, name: typeof artifact.name === "string" ? artifact.name : undefined, timestamp: operation?.timestamp });
	});
	return [...byKey.values()];
}