import { describe, expect, it } from "vitest";

import type { LogEntry } from "@/lib/types/logs";

import i18n from "@/lib/i18n";

import { createColumns, getMessage } from "./columns";

describe("getMessage", () => {
	it("translates realtime labels after a language change while preserving message and tool content", async () => {
		const log = {
			object: "realtime.turn",
			input_history: [
				{ role: "tool", content: '{"result":"unchanged"}' },
				{ role: "user", content: "hello <world>" },
			],
			output_message: {
				role: "assistant",
				content: "Hello!",
				tool_calls: [{ function: { name: "get_weather", arguments: '{"city":"Paris"}' } }],
			},
		} as unknown as LogEntry;
		try {
			await i18n.changeLanguage("zh-CN");
			expect(getMessage(log)).toBe(
				'工具结果：{"result":"unchanged"}\n用户：hello <world>\n助手工具调用：get_weather({"city":"Paris"})\n助手：Hello!',
			);
			await i18n.changeLanguage("en");
			expect(getMessage(log)).toBe(
				'Tool Result: {"result":"unchanged"}\nUser: hello <world>\nAssistant Tool Call: get_weather({"city":"Paris"})\nAssistant: Hello!',
			);
		} finally {
			await i18n.changeLanguage("en");
		}
	});

	it("returns EI realtime text from input history", () => {
		const log = {
			object: "realtime.turn",
			input_history: [
				{
					role: "user",
					content: [{ type: "text", text: "hello from the browser" }],
				},
			],
		} as unknown as LogEntry;

		expect(getMessage(log)).toBe("User: hello from the browser");
	});

	it("returns LM realtime text from output message", () => {
		const log = {
			object: "realtime.turn",
			input_history: [],
			responses_input_history: [],
			output_message: {
				role: "assistant",
				content: [{ type: "text", text: "hello from the model" }],
			},
		} as unknown as LogEntry;

		expect(getMessage(log)).toBe("Assistant: hello from the model");
	});

	it("returns split realtime text when both user and assistant are present", () => {
		const log = {
			object: "realtime.turn",
			input_history: [
				{
					role: "user",
					content: [{ type: "text", text: "who are you?" }],
				},
			],
			output_message: {
				role: "assistant",
				content: [{ type: "text", text: "I am the assistant." }],
			},
		} as unknown as LogEntry;

		expect(getMessage(log)).toBe("User: who are you?\nAssistant: I am the assistant.");
	});

	it("returns split realtime text including tool output", () => {
		const log = {
			object: "realtime.turn",
			input_history: [
				{
					role: "tool",
					content: [{ type: "text", text: '{"nextResponse":"tool result"}' }],
				},
				{
					role: "user",
					content: [{ type: "text", text: "who are you?" }],
				},
			],
			output_message: {
				role: "assistant",
				content: [{ type: "text", text: "I am the assistant." }],
			},
		} as unknown as LogEntry;

		expect(getMessage(log)).toBe('Tool Result: {"nextResponse":"tool result"}\nUser: who are you?\nAssistant: I am the assistant.');
	});

	it("returns realtime assistant tool calls from output message", () => {
		const log = {
			object: "realtime.turn",
			input_history: [
				{
					role: "user",
					content: [{ type: "text", text: "show me a pastel palette" }],
				},
			],
			output_message: {
				role: "assistant",
				tool_calls: [
					{
						function: {
							name: "display_color_palette",
							arguments: '{"theme":"pastel"}',
						},
					},
				],
			},
		} as unknown as LogEntry;

		expect(getMessage(log)).toBe('User: show me a pastel palette\nAssistant Tool Call: display_color_palette({"theme":"pastel"})');
	});
});

describe("createColumns translations", () => {
	it("uses the current locale when no translator is supplied", async () => {
		try {
			for (const [locale, project] of [
				["en", "Project"],
				["zh-CN", "项目"],
			]) {
				await i18n.changeLanguage(locale);
				const columns = createColumns(() => {});
				expect(columns.find((column) => column.id === "project")?.header).toBe(project);
				expect(columns.some((column) => typeof column.header === "string" && /^(logs|labels)\./.test(column.header))).toBe(false);
			}
		} finally {
			await i18n.changeLanguage("en");
		}
	});
});