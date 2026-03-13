import { Message, type CompletionUsage, type ToolCall, type VariableMap, replaceVariablesInMessages } from "@/lib/message";
import { getErrorMessage } from "@/lib/store";
import type { ModelParams } from "@/lib/types/prompts";

export interface ExecutionConfig {
	provider: string;
	model: string;
	modelParams: ModelParams;
	apiKeyId: string;
	variables?: VariableMap;
}

function getBaseUrl() {
	if(process.env.NODE_ENV === "development") {
		return "http://localhost:8080";
	} else {
		return "";
	}
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
	const resolvedMessages = config.variables
		? replaceVariablesInMessages(allMessages, config.variables)
		: allMessages;

	try {
		const headers: Record<string, string> = { "Content-Type": "application/json" };
		if (config.apiKeyId && config.apiKeyId !== "__auto__") {
			if (config.apiKeyId.startsWith("sk-bf-")) {
				headers["Authorization"] = `Bearer ${config.apiKeyId}`;
			} else {
				headers["x-bf-api-key-id"] = config.apiKeyId;
			}
		}

		const { api_key_id: _, ...requestParams } = config.modelParams;
		const response = await fetch(`${getBaseUrl()}/v1/chat/completions`, {
			method: "POST",
			headers,
			body: JSON.stringify({
				model: `${config.provider}/${config.model}`,
				messages: Message.toAPIMessages(resolvedMessages),
				...requestParams,
				stream: requestParams.stream ?? true,
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
		callbacks.onError(getErrorMessage(err));
	} finally {
		callbacks.onFinally();
	}
}
