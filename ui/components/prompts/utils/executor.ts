import { Message, type MessageContent } from "@/lib/message";
import { getErrorMessage } from "@/lib/store";
import type { ModelParams } from "@/lib/types/prompts";

export interface ExecutionConfig {
	provider: string;
	model: string;
	modelParams: ModelParams;
	apiKeyId: string;
}

export interface ExecutionCallbacks {
	onStreamingStart: (allMessages: Message[], placeholder: Message) => void;
	onStreamChunk: (content: string) => void;
	onComplete: (content: string) => void;
	onEmptyResponse: () => void;
	onError: (error: string) => void;
	onFinally: () => void;
}

export async function executePrompt(
	currentMessages: Message[],
	userInput: string,
	attachments: MessageContent[] | undefined,
	config: ExecutionConfig,
	callbacks: ExecutionCallbacks,
) {
	const hasInput = userInput.trim() || (attachments && attachments.length > 0);
	let allMessages: Message[];
	if (hasInput) {
		const userMessage = Message.request(userInput, 0, attachments);
		allMessages = [...currentMessages, userMessage];
	} else {
		allMessages = [...currentMessages];
	}

	const placeholder = Message.response("");
	callbacks.onStreamingStart(allMessages, placeholder);

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
		const response = await fetch("http://localhost:8080/v1/chat/completions", {
			method: "POST",
			headers,
			body: JSON.stringify({
				model: `${config.provider}/${config.model}`,
				messages: Message.toAPIMessages(allMessages),
				...requestParams,
				stream: true,
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
			if (content) {
				callbacks.onComplete(content);
			} else {
				callbacks.onEmptyResponse();
			}
		} else {
			const reader = response.body?.getReader();
			if (!reader) throw new Error("No response body");

			const decoder = new TextDecoder();
			let assistantContent = "";

			while (true) {
				const { done, value } = await reader.read();
				if (done) break;

				const chunk = decoder.decode(value);
				const lines = chunk.split("\n");

				for (const line of lines) {
					if (line.startsWith("data: ")) {
						const data = line.slice(6);
						if (data === "[DONE]") continue;

						try {
							const parsed = JSON.parse(data);
							const content = parsed.choices?.[0]?.delta?.content;
							if (content) {
								assistantContent += content;
								callbacks.onStreamChunk(assistantContent);
							}
						} catch {
							// Ignore parse errors for incomplete chunks
						}
					}
				}
			}

			if (assistantContent) {
				callbacks.onComplete(assistantContent);
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
