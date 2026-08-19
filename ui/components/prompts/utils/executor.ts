import { Message, type CompletionUsage, type ToolCall, type VariableMap, replaceVariablesInMessages } from "@/lib/message";
import { getErrorMessage } from "@/lib/store";
import type { ModelParams } from "@/lib/types/prompts";

export interface ExecutionConfig {
	provider: string;
	model: string;
	modelParams: ModelParams;
	apiKeyId: string;
	variables?: VariableMap;
	customHeaders?: Record<string, string>;
}

function getBaseUrl() {
	if (process.env.NODE_ENV === "development") {
		return "http://localhost:8080";
	} else {
		return "";
	}
}

function buildHeaders(config: Pick<ExecutionConfig, "apiKeyId" | "customHeaders">): Record<string, string> {
	const headers: Record<string, string> = { "Content-Type": "application/json" };
	if (config.apiKeyId && config.apiKeyId !== "__auto__") {
		if (config.apiKeyId.startsWith("sk-bf-")) {
			headers["Authorization"] = `Bearer ${config.apiKeyId}`;
		} else {
			headers["x-bf-api-key-id"] = config.apiKeyId;
		}
	}
	if (config.customHeaders) {
		const reserved = new Set(["content-type", "authorization", "x-bf-api-key-id"]);
		for (const [name, value] of Object.entries(config.customHeaders)) {
			const trimmedName = name.trim();
			const trimmedValue = value.trim();
			if (!trimmedName || !trimmedValue) continue;
			if (reserved.has(trimmedName.toLowerCase())) {
				console.warn(`Ignoring custom header "${trimmedName}" — reserved by the playground.`);
				continue;
			}
			headers[trimmedName] = trimmedValue;
		}
	}
	return headers;
}

export interface ExecutionCallbacks {
	onStreamingStart: (allMessages: Message[], placeholder: Message) => void;
	onStreamChunk: (content: string) => void;
	onComplete: (content: string, usage?: CompletionUsage) => void;
	onToolCallComplete: (content: string, toolCalls: ToolCall[], usage?: CompletionUsage) => void;
	onEmptyResponse: () => void;
	onError: (error: string) => void;
	onFinally: () => void;
}

export async function executePrompt(
	currentMessages: Message[],
	pendingMessage: Message | undefined,
	config: ExecutionConfig,
	callbacks: ExecutionCallbacks,
	signal?: AbortSignal,
) {
	let allMessages: Message[];
	if (pendingMessage) {
		allMessages = [...currentMessages, pendingMessage];
	} else {
		allMessages = [...currentMessages];
	}

	const placeholder = Message.response("");
	callbacks.onStreamingStart(allMessages, placeholder);

	// Replace Jinja2 variables before sending to the API
	const resolvedMessages = config.variables ? replaceVariablesInMessages(allMessages, config.variables) : allMessages;

	try {
		const headers = buildHeaders(config);

		const { api_key_id: _, ...requestParams } = config.modelParams;
		const response = await fetch(`${getBaseUrl()}/v1/chat/completions`, {
			method: "POST",
			headers,
			signal,
			body: JSON.stringify({
				model: `${config.provider}/${config.model}`,
				messages: Message.toAPIMessages(resolvedMessages),
				...requestParams,
				stream: requestParams.stream,
			}),
		});

		if (!response.ok) {
			let errorMessage = `HTTP error! status: ${response.status}`;
			try {
				const data = await response.json();
				errorMessage = data.error?.error || data.error?.message || errorMessage;
			} catch (error) {
				console.error("Failed to parse error response:", error);
			}
			throw new Error(errorMessage);
		}

		const contentType = response.headers.get("content-type") || "";
		const isStreamResponse = contentType.includes("text/event-stream");

		if (!isStreamResponse) {
			const data = await response.json();
			const content = data.choices?.[0]?.message?.content ?? "";
			const toolCalls = data.choices?.[0]?.message?.tool_calls as ToolCall[] | undefined;
			const usage = data.usage as CompletionUsage | undefined;
			if (toolCalls && toolCalls.length > 0) {
				callbacks.onToolCallComplete(content, toolCalls, usage);
			} else if (content) {
				callbacks.onComplete(content, usage);
			} else {
				callbacks.onEmptyResponse();
			}
		} else {
			const reader = response.body?.getReader();
			if (!reader) throw new Error("No response body");

			const decoder = new TextDecoder();
			let assistantContent = "";
			let streamUsage: CompletionUsage | undefined;
			const toolCallsMap = new Map<number, ToolCall>();
			let buffer = "";

			while (true) {
				const { done, value } = await reader.read();
				if (done) break;

				buffer += decoder.decode(value, { stream: true });
				const lines = buffer.split("\n");
				// Keep the last (potentially incomplete) line in the buffer
				buffer = lines.pop() ?? "";

				for (const line of lines) {
					const trimmed = line.trim();
					if (!trimmed.startsWith("data: ")) continue;
					const data = trimmed.slice(6);
					if (data === "[DONE]") continue;

					try {
						const parsed = JSON.parse(data);
						const delta = parsed.choices?.[0]?.delta;

						if (parsed.usage) {
							streamUsage = parsed.usage as CompletionUsage;
						}

						const content = delta?.content;
						if (content) {
							assistantContent += content;
							callbacks.onStreamChunk(assistantContent);
						}

						const deltaToolCalls = delta?.tool_calls as Array<{
							index: number;
							id?: string;
							type?: string;
							function?: { name?: string; arguments?: string };
						}>;
						if (deltaToolCalls) {
							for (const dtc of deltaToolCalls) {
								const idx = dtc.index;
								const existing = toolCallsMap.get(idx);
								if (existing) {
									if (dtc.function?.arguments) {
										existing.function.arguments += dtc.function.arguments;
									}
								} else {
									toolCallsMap.set(idx, {
										type: "function",
										id: dtc.id ?? "",
										function: {
											name: dtc.function?.name ?? "",
											arguments: dtc.function?.arguments ?? "",
										},
									});
								}
							}
						}
					} catch {
						// Ignore parse errors
					}
				}
			}

			const toolCalls = Array.from(toolCallsMap.values());
			if (toolCalls.length > 0) {
				callbacks.onToolCallComplete(assistantContent, toolCalls, streamUsage);
			} else if (assistantContent) {
				callbacks.onComplete(assistantContent, streamUsage);
			} else {
				callbacks.onEmptyResponse();
			}
		}
	} catch (err) {
		if (err instanceof DOMException && err.name === "AbortError") {
			// User cancelled — no error to display
		} else {
			callbacks.onError(getErrorMessage(err));
		}
	} finally {
		callbacks.onFinally();
	}
}

export class MCPAuthRequiredError extends Error {
	kind: "oauth" | "headers";
	mcpClientName: string;
	authorizeUrl: string;

	constructor(opts: { kind: "oauth" | "headers"; mcpClientName: string; authorizeUrl: string; message: string }) {
		super(opts.message);
		this.name = "MCPAuthRequiredError";
		this.kind = opts.kind;
		this.mcpClientName = opts.mcpClientName;
		this.authorizeUrl = opts.authorizeUrl;
	}
}

export async function executeToolCall(toolCall: ToolCall, config: Pick<ExecutionConfig, "apiKeyId" | "customHeaders">): Promise<string> {
	const headers = buildHeaders(config);

	const response = await fetch(`${getBaseUrl()}/v1/mcp/tool/execute`, {
		method: "POST",
		headers,
		body: JSON.stringify({
			id: toolCall.id,
			type: toolCall.type,
			index: 0,
			function: {
				name: toolCall.function.name,
				arguments: toolCall.function.arguments,
			},
		}),
	});

	if (!response.ok) {
		let errorMessage = `HTTP error! status: ${response.status}`;
		try {
			const data = await response.json();
			errorMessage = data.error?.message || data.error?.error || errorMessage;

			const authRequired = data.extra_fields?.mcp_auth_required;
			if (authRequired) {
				throw new MCPAuthRequiredError({
					kind: authRequired.kind,
					mcpClientName: authRequired.mcp_client_name || "MCP server",
					authorizeUrl: authRequired.authorize_url || authRequired.submit_url || "",
					message: authRequired.message || errorMessage,
				});
			}
		} catch (e) {
			if (e instanceof MCPAuthRequiredError) throw e;
		}
		throw new Error(errorMessage);
	}

	const data = await response.json();
	return typeof data.content === "string" ? data.content : JSON.stringify(data.content);
}