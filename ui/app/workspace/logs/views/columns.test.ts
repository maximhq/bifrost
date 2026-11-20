import { describe, expect, it } from "vitest";
import { renderToStaticMarkup } from "react-dom/server";
import { createTable, flexRender, getCoreRowModel } from "@tanstack/react-table";

import type { LogEntry } from "@/lib/types/logs";

import i18n from "@/lib/i18n";
import { formatCompactNumber } from "@/lib/utils/numbers";

import { cachedInputTooltip, createColumns, getMessage } from "./columns";

describe("metadata cells", () => {
	it.each([
		[[{ id: "item_audio", index: 0 }], "item_audio"],
		[{ nested: { id: "item_audio" } }, "nested"],
		[false, "false"],
		[0, "0"],
		["custom value", "custom value"],
		[null, "-"],
		[undefined, "-"],
	])("renders JSON metadata safely: %j", (value, expected) => {
		const log = { id: "log", metadata: { realtime_input_items: value } } as unknown as LogEntry;
		const table = createTable({
			data: [log],
			columns: createColumns(() => {}, false, ["realtime_input_items"]),
			getCoreRowModel: getCoreRowModel(),
			state: {},
			onStateChange: () => {},
			renderFallbackValue: null,
		});
		const cell = table
			.getRowModel()
			.rows[0].getAllCells()
			.find((cell) => cell.column.id === "metadata_realtime_input_items")!;
		expect(renderToStaticMarkup(flexRender(cell.column.columnDef.cell, cell.getContext()))).toContain(expected);
	});
});

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

	it("shows N/A for a successful model retrieve, which has no message body", () => {
		const log = { object: "model_retrieve", status: "success" } as unknown as LogEntry;

		expect(getMessage(log)).toBe("N/A");
	});

	it("keeps the error summary for a failed model retrieve", () => {
		const log = {
			object: "model_retrieve",
			status: "error",
			content_summary: "invalid model: path delimiters and percent-encoding are not allowed",
		} as unknown as LogEntry;

		expect(getMessage(log)).toBe("invalid model: path delimiters and percent-encoding are not allowed");
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

	it("shows the tool calls when the last input is an assistant tool call", () => {
		const log = {
			object: "chat.completion",
			input_history: [
				{ role: "system", content: "You are an agent." },
				{
					role: "assistant",
					tool_calls: [{ function: { name: "AskUserQuestion", arguments: '{"q":"amend?"}' } }],
				},
			],
		} as unknown as LogEntry;

		expect(getMessage(log)).toBe('AskUserQuestion({"q":"amend?"})');
	});

	it("falls back to the content summary when the last input has no text", () => {
		const log = {
			object: "chat.completion",
			input_history: [{ role: "assistant", content: [{ type: "image_url", image_url: { url: "x" } }] }],
			content_summary: "25\nexit=0",
		} as unknown as LogEntry;

		expect(getMessage(log)).toBe("25\nexit=0");
	});

	it("shows a responses function_call as name(args)", () => {
		const log = {
			object: "response",
			responses_input_history: [{ type: "function_call", call_id: "c1", name: "Bash", arguments: '{"cmd":"ls"}' }],
		} as unknown as LogEntry;

		expect(getMessage(log)).toBe('Bash({"cmd":"ls"})');
	});

	it("shows a responses function_call with empty string content as name(args)", () => {
		const log = {
			object: "response",
			responses_input_history: [{ type: "function_call", call_id: "c1", content: "", name: "Bash", arguments: "{}" }],
		} as unknown as LogEntry;

		expect(getMessage(log)).toBe("Bash({})");
	});

	it("falls back to the content summary when a responses item has empty string content", () => {
		const log = {
			object: "response",
			responses_input_history: [{ type: "message", role: "user", content: "" }],
			content_summary: "latest tool result",
		} as unknown as LogEntry;

		expect(getMessage(log)).toBe("latest tool result");
	});

	it("falls back to the content summary when a function_call_output array has no text", () => {
		const log = {
			object: "response",
			responses_input_history: [
				{ type: "function_call_output", call_id: "c1", output: [{ type: "input_image", image_url: "data:image/png;base64,AA" }] },
			],
			content_summary: "screenshot taken",
		} as unknown as LogEntry;

		expect(getMessage(log)).toBe("screenshot taken");
	});

	// The cases below pin the same precedence as BuildInputContentSummary in
	// framework/logstore/payload.go: last user text, then the latest tool result
	// or tool call, so full-history rows match hybrid content_summary rows.
	it("prefers the last user text over a trailing assistant tool call", () => {
		const log = {
			object: "chat.completion",
			input_history: [
				{ role: "user", content: "what's the weather?" },
				{ role: "assistant", tool_calls: [{ function: { name: "get_weather", arguments: '{"city":"Paris"}' } }] },
			],
		} as unknown as LogEntry;

		expect(getMessage(log)).toBe("what's the weather?");
	});

	it("prefers the last user text over a trailing tool result", () => {
		const log = {
			object: "chat.completion",
			input_history: [
				{ role: "user", content: "add 12 and 13" },
				{ role: "assistant", tool_calls: [{ function: { name: "add", arguments: '{"a":12,"b":13}' } }] },
				{ role: "tool", content: "25" },
			],
		} as unknown as LogEntry;

		expect(getMessage(log)).toBe("add 12 and 13");
	});

	it("prefers the last user text over a trailing assistant reply", () => {
		const log = {
			object: "chat.completion",
			input_history: [
				{ role: "user", content: "first question" },
				{ role: "assistant", content: "first answer" },
			],
		} as unknown as LogEntry;

		expect(getMessage(log)).toBe("first question");
	});

	it("shows the latest tool result when the chat history has no user text", () => {
		const log = {
			object: "chat.completion",
			input_history: [
				{ role: "system", content: "You are an agent." },
				{ role: "assistant", tool_calls: [{ function: { name: "add", arguments: '{"a":12,"b":13}' } }] },
				{ role: "tool", content: "25" },
				{ role: "assistant", content: [{ type: "image_url", image_url: { url: "x" } }] },
			],
		} as unknown as LogEntry;

		expect(getMessage(log)).toBe("25");
	});

	it("prefers the last responses user text over a trailing function_call", () => {
		const log = {
			object: "response",
			responses_input_history: [
				{ type: "message", role: "user", content: [{ type: "input_text", text: "list files" }] },
				{ type: "function_call", call_id: "c1", name: "Bash", arguments: '{"cmd":"ls"}' },
			],
		} as unknown as LogEntry;

		expect(getMessage(log)).toBe("list files");
	});

	it("prefers the last responses user text over a trailing function_call_output", () => {
		const log = {
			object: "response",
			responses_input_history: [
				{ type: "message", role: "user", content: "list files" },
				{ type: "function_call", call_id: "c1", name: "Bash", arguments: '{"cmd":"ls"}' },
				{ type: "function_call_output", call_id: "c1", output: "a.txt" },
			],
		} as unknown as LogEntry;

		expect(getMessage(log)).toBe("list files");
	});

	it("shows the last text part of a mixed function_call_output array", () => {
		const log = {
			object: "response",
			responses_input_history: [
				{
					type: "function_call_output",
					call_id: "c1",
					output: [
						{ type: "input_text", text: "before" },
						{ type: "input_image", image_url: "data:image/png;base64,AA" },
						{ type: "input_text", text: "after" },
					],
				},
			],
		} as unknown as LogEntry;

		expect(getMessage(log)).toBe("after");
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

describe("tokens cell", () => {
	const renderTokens = (log: Partial<LogEntry>) => {
		const table = createTable({
			data: [{ id: "log", ...log } as unknown as LogEntry],
			columns: createColumns(() => {}),
			getCoreRowModel: getCoreRowModel(),
			state: {},
			onStateChange: () => {},
			renderFallbackValue: null,
		});
		const cell = table
			.getRowModel()
			.rows[0].getAllCells()
			.find((cell) => cell.column.id === "tokens")!;
		return renderToStaticMarkup(flexRender(cell.column.columnDef.cell, cell.getContext()));
	};
	const cachedReadOf = (html: string) => html.match(/data-testid="logs-tokens-cache-read"[^>]*>([^<]*)</)?.[1];

	it("shows the provider's cache reads next to input and output", () => {
		const html = renderTokens({
			token_usage: { prompt_tokens: 1000, completion_tokens: 100, total_tokens: 1100, prompt_tokens_details: { cached_read_tokens: 400 } },
		});
		expect(cachedReadOf(html)).toBe("400");
		expect(html).toContain("bg-emerald-400");
	});

	it("renders a row without cache reads as before", () => {
		const html = renderTokens({ token_usage: { prompt_tokens: 1000, completion_tokens: 100, total_tokens: 1100 } });
		expect(cachedReadOf(html)).toBeUndefined();
		expect(html).not.toContain("bg-emerald-400");
	});

	it("clamps cache reads to the input they are part of", () => {
		const html = renderTokens({
			token_usage: { prompt_tokens: 1000, completion_tokens: 100, total_tokens: 1100, prompt_tokens_details: { cached_read_tokens: 5000 } },
		});
		expect(cachedReadOf(html)).toBe(formatCompactNumber(1000));
	});

	it("sums cache reads across a settled batch's models", () => {
		const usage = (prompt: number, cached: number) => ({
			prompt_tokens: prompt,
			completion_tokens: 10,
			total_tokens: prompt + 10,
			prompt_tokens_details: { cached_read_tokens: cached },
		});
		const html = renderTokens({
			batch_debug: {
				accounting: {
					model_breakdowns: {
						a: { model: "a", request_count: 1, usage: usage(500, 200) },
						b: { model: "b", request_count: 1, usage: usage(300, 100) },
					},
				},
			},
		} as unknown as Partial<LogEntry>);
		expect(cachedReadOf(html)).toBe("300");
	});

	it("breaks the input down in the tooltip as the log detail does", () => {
		const t = i18n.getFixedT("en", "observability");
		const lines = cachedInputTooltip(
			{
				prompt_tokens: 1000,
				completion_tokens: 100,
				total_tokens: 1100,
				prompt_tokens_details: { cached_read_tokens: 400, cached_write_tokens: 100 },
			},
			400,
			t,
		).split("\n");
		expect(lines).toEqual([
			t("logs.detail.inputTokensIncludeCached"),
			t("logs.detail.uncachedInput", { value: (500).toLocaleString("en") }),
			t("logs.detail.cacheRead", { value: (400).toLocaleString("en") }),
			t("logs.detail.cacheWrite", { value: (100).toLocaleString("en") }),
			t("logs.detail.inputTokensTotal", { value: (1000).toLocaleString("en") }),
		]);
	});
});