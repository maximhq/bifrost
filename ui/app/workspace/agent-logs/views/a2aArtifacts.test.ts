import type { AgentLogDetail } from "@/lib/types/agentLogs";
import { describe, expect, it } from "vitest";
import { collectA2AArtifacts } from "./a2aArtifacts";

const base: AgentLogDetail = {
	id: "op-1",
	timestamp: "2026-11-11T10:42:18.000Z",
	record_kind: "request",
	operation: "SendMessage",
	status: "success",
	agent_name: "report-writer",
	request_id: "req-1",
};

function event(id: string, body: unknown, artifactId?: string): AgentLogDetail {
	return { ...base, id, record_kind: "event", event_type: "artifact_update", artifact_id: artifactId, event_body: JSON.stringify(body) };
}

describe("collectA2AArtifacts", () => {
	it("reads v1 artifactUpdate events inside a JSON-RPC result", () => {
		const records = collectA2AArtifacts(base, [
			event("e1", {
				result: { artifactUpdate: { taskId: "t", artifact: { artifactId: "a1", name: "report.pdf", parts: [{ text: "x" }] } } },
			}),
		]);
		expect(records).toHaveLength(1);
		expect(records[0]).toMatchObject({ key: "a1", name: "report.pdf", eventId: "e1" });
	});

	it("reads v0.3 artifact-update events", () => {
		const records = collectA2AArtifacts(base, [
			event("e1", { kind: "artifact-update", taskId: "t", artifact: { artifactId: "a1", parts: [] } }),
		]);
		expect(records.map((record) => record.key)).toEqual(["a1"]);
	});

	it("appends parts for append=true chunks and replaces otherwise", () => {
		const records = collectA2AArtifacts(base, [
			event("e1", { artifactUpdate: { artifact: { artifactId: "a1", name: "notes", parts: [{ text: "one" }] } } }),
			event("e2", { artifactUpdate: { append: true, artifact: { artifactId: "a1", parts: [{ text: "two" }] } } }),
			event("e3", { artifactUpdate: { artifact: { artifactId: "a2", parts: [{ text: "old" }] } } }),
			event("e4", { artifactUpdate: { artifact: { artifactId: "a2", parts: [{ text: "new" }] } } }),
		]);
		expect(records.find((record) => record.key === "a1")?.artifact.parts).toEqual([{ text: "one" }, { text: "two" }]);
		expect(records.find((record) => record.key === "a1")?.name).toBe("notes");
		expect(records.find((record) => record.key === "a2")?.artifact.parts).toEqual([{ text: "new" }]);
	});

	it("adds the final Task's artifacts from a non-streaming response without duplicating streamed ones", () => {
		const operation = {
			...base,
			response_body: JSON.stringify({
				result: {
					task: {
						id: "t",
						artifacts: [
							{ artifactId: "a1", name: "streamed" },
							{ artifactId: "a2", name: "final-only" },
						],
					},
				},
			}),
		};
		const records = collectA2AArtifacts(operation, [event("e1", { artifactUpdate: { artifact: { artifactId: "a1", name: "streamed" } } })]);
		expect(records.map((record) => [record.key, record.name])).toEqual([
			["a1", "streamed"],
			["a2", "final-only"],
		]);
	});

	it("ignores other events and unparseable bodies", () => {
		const status = { ...event("s1", { statusUpdate: {} }), event_type: "status_update" };
		const broken = { ...event("b1", {}), event_body: "{not json" };
		expect(collectA2AArtifacts({ ...base, response_body: "nope" }, [status, broken])).toEqual([]);
	});
});