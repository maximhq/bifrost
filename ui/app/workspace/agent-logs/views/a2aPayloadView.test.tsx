import "@/lib/i18n";
import { mapAgentCard } from "@/lib/utils/agentCard";
import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import { A2AConversationMessage, A2APayloadView } from "./a2aPayloadView";

const statusMessage = { messageId: "agent-message", role: "ROLE_AGENT", parts: [{ text: "Finished researching" }] };
const response = {
	tasks: [
		{
			id: "task-1",
			contextId: "context-1",
			status: { state: "TASK_STATE_COMPLETED", timestamp: "2026-10-12T01:00:00Z", message: statusMessage },
			history: [{ messageId: "user-message", role: "ROLE_USER", parts: [{ text: "Research this" }] }, statusMessage],
			artifacts: [{ artifactId: "artifact-1", name: "Findings", description: "Research summary", parts: [{ text: "The findings" }] }],
		},
	],
	totalSize: 3,
	pageSize: 20,
	nextPageToken: "next-page",
};

function renderTasks() {
	return renderToStaticMarkup(<A2APayloadView operation="ListTasks" responseBody={JSON.stringify(response)} />);
}

describe("task list conversations", () => {
	it("renders history and artifacts without repeating the final status message", () => {
		const html = renderTasks();
		expect(html).toContain("Research this");
		expect(html.match(/Finished researching/g)).toHaveLength(1);
		expect(html).toContain("Research summary");
		expect(html).toContain("The findings");
	});
	it("keeps task and pagination fields available without nested message detail panels", () => {
		const html = renderTasks();
		expect(html).toContain("context-1");
		expect(html).toContain("Task details");
		expect(html).toContain("Response details");
		expect(html).toContain("next-page");
		expect(html).not.toContain("Message 1 details");
		expect(html).not.toContain("Status message details");
		expect(html).not.toContain("20 on this page");
	});
	it("makes each message or artifact title a copy control", () => {
		const html = renderTasks();
		expect(html.match(/aria-label="Copy message ID"/g)).toHaveLength(2);
		expect(html.match(/aria-label="Copy artifact ID"/g)).toHaveLength(1);
	});
});

describe("artifact metadata", () => {
	it.each(["image/png", "audio/mpeg", "video/mp4"])("keeps file details for %s previews", (mediaType) => {
		const html = renderToStaticMarkup(
			<A2AConversationMessage
				item={{ role: "agent", kind: "artifact", parts: [{ url: "https://example.com/media", filename: "preview-file", mediaType }] }}
			/>,
		);
		expect(html).toContain("agent-artifact-metadata");
		expect(html).toContain(">preview-file</span>");
		expect(html).toContain(`>${mediaType}</span>`);
		expect(html).toContain("Part 1");
	});
	it("groups extra fields without repeating the copyable artifact ID or file details", () => {
		const html = renderToStaticMarkup(
			<A2AConversationMessage
				item={{
					id: "artifact-id",
					role: "agent",
					kind: "artifact",
					artifactFields: { extensions: ["extension-uri"] },
					parts: [{ url: "/fixture.txt", filename: "fixture.txt", mediaType: "text/plain" }],
				}}
			/>,
		);
		expect(html).toContain("agent-artifact-metadata");
		expect(html).not.toContain("artifact-id");
		expect(html).toContain('aria-label="Copy artifact ID"');
		expect(html).toContain("extension-uri");
		expect(html).not.toContain("Part details");
		expect(html).not.toContain("Part 1");
	});
	it("omits metadata when the artifact has no additional fields", () => {
		const html = renderToStaticMarkup(
			<A2AConversationMessage
				item={{
					id: "artifact-id",
					role: "agent",
					kind: "artifact",
					artifactFields: { metadata: {}, extensions: [] },
					parts: [{ text: "Output" }],
				}}
			/>,
		);
		expect(html).not.toContain("agent-artifact-metadata");
	});
	it("preserves extra part fields grouped by part", () => {
		const html = renderToStaticMarkup(
			<A2AConversationMessage
				item={{
					id: "artifact-id",
					role: "agent",
					kind: "artifact",
					parts: [
						{ text: "Output", metadata: { source: "research" } },
						{ text: "More output", partId: "part-2" },
					],
				}}
			/>,
		);
		expect(html).toContain("Part 1");
		expect(html).toContain(">Metadata</dt>");
		expect(html).toContain("Part 2");
		expect(html).toContain("part-2");
	});
});

describe("push configuration operations", () => {
	const config = { id: "push-1", taskId: "task-1", url: "https://example.com/callback" };
	it.each(["CreateTaskPushNotificationConfig", "GetTaskPushNotificationConfig", "DeleteTaskPushNotificationConfig"])(
		"labels configuration and task IDs separately for %s",
		(operation) => {
			const html = renderToStaticMarkup(
				<A2APayloadView
					operation={operation}
					requestBody={JSON.stringify(config)}
					responseBody={JSON.stringify(operation.startsWith("Delete") ? {} : config)}
				/>,
			);
			expect(html).toContain("Request parameters");
			expect(html).toContain("Configuration ID");
			expect(html.match(/>Task ID</g)).toHaveLength(operation.startsWith("Delete") ? 1 : 2);
			if (operation.startsWith("Delete")) expect(html).toContain("Push configuration deleted");
		},
	);
	it("anchors the list count to its configuration header", () => {
		const html = renderToStaticMarkup(
			<A2APayloadView
				operation="ListTaskPushNotificationConfigs"
				requestBody={JSON.stringify({ taskId: "task-1" })}
				responseBody={JSON.stringify({ configs: [config] })}
			/>,
		);
		expect(html).toContain("Push configurations");
		expect(html).toContain("Request parameters");
		expect(html).not.toContain("Callbacks configured for this task.");
	});
});

describe("task subscriptions", () => {
	it("groups the subscription request in the shared parameters panel", () => {
		const html = renderToStaticMarkup(<A2APayloadView operation="SubscribeToTask" requestBody='{"id":"task-1"}' />);
		expect(html).toContain("Request parameters");
		expect(html).toContain("Task ID");
		expect(html).toContain("task-1");
	});
});

describe("task identifiers", () => {
	it.each(["GetTask", "CancelTask", "ListTasks"])("uses filter and copy controls in %s responses", (operation) => {
		const body = operation === "ListTasks" ? response : response.tasks[0];
		const html = renderToStaticMarkup(<A2APayloadView operation={operation} responseBody={JSON.stringify(body)} onFilter={() => {}} />);
		expect(html.match(/data-testid="agent-log-response-task-id"/g)).toHaveLength(2);
		expect(html).toContain('aria-label="Filter by Task"');
	});
});

describe("agent card responses", () => {
	it.each(["GetAgentCard", "GetExtendedAgentCard"])("uses the registration card renderer for %s", (operation) => {
		const html = renderToStaticMarkup(
			<A2APayloadView
				operation={operation}
				responseBody={JSON.stringify({
					name: "Example agent",
					version: "1.0",
					capabilities: { streaming: true, pushNotifications: true },
					supportedInterfaces: [{ protocolBinding: "JSONRPC", protocolVersion: "1.0", url: "https://example.com/rpc" }],
					skills: [{ id: "research", name: "Research" }],
					signatures: [{ signature: "signed" }],
				})}
			/>,
		);
		expect(html).toContain('data-testid="agent-card-preview"');
		expect(html).toContain("Example agent");
		expect(html).toContain("Push Notifications");
		expect(html).toContain("Show card details");
		expect(html).not.toContain("Fetched");
	});
});

describe("complete agent card mapping", () => {
	it("preserves security definitions, required scopes, and full signatures", () => {
		const signature = { header: { kid: "key-1" }, protected: "encoded-header", signature: "encoded-signature" };
		const model = mapAgentCard({
			name: "Full card",
			description: "Full description",
			capabilities: {},
			securitySchemes: {
				oauth: {
					oauth2SecurityScheme: { flows: { clientCredentials: { tokenUrl: "https://example.com/token", scopes: { read: "Read" } } } },
				},
			},
			securityRequirements: [{ schemes: { oauth: ["read"] } }],
			signatures: [signature],
			skills: [{ id: "skill-1", name: "Research", securityRequirements: [{ schemes: { oauth: ["read", "write"] } }] }],
		});
		expect(model.security_schemes[0].definition).toEqual({
			flows: { clientCredentials: { tokenUrl: "https://example.com/token", scopes: { read: "Read" } } },
		});
		expect(model.security_requirement_details).toEqual([{ oauth: ["read"] }]);
		expect(model.skills?.[0].security_requirement_details).toEqual([{ oauth: ["read", "write"] }]);
		expect(model.signatures).toEqual([signature]);
		expect(model.signature_count).toBe(1);
		const html = renderToStaticMarkup(
			<A2APayloadView responseBody={JSON.stringify({ name: "Full card", description: "Full description", skills: [] })} />,
		);
		expect(html).toContain("Full description");
	});
});

describe("task identifiers without filtering", () => {
	it("does not advertise a filter action when no callback is supplied", () => {
		const html = renderToStaticMarkup(
			<A2APayloadView
				responseBody={JSON.stringify({ id: "task-copy-only", contextId: "context", status: { state: "TASK_STATE_COMPLETED" } })}
			/>,
		);
		expect(html).toContain("task-copy-only");
		expect(html).not.toContain('aria-label="Filter by Task"');
		expect(html).toContain("cursor-context-menu");
	});
});