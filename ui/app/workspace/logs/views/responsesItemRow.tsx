import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { ResponsesMessage } from "@/lib/types/logs";
import { cn } from "@/lib/utils";
import { applyRedactionMapping, applyRedactionMappingToValue } from "@/lib/utils/redaction";
import { extractResponsesItemPayload, summarizeResponsesToolCall } from "@/lib/utils/responsesItems";
import { ChevronDown } from "lucide-react";
import { useState, type ReactNode } from "react";
import { hasNoToolArguments, isResponsesToolCallItem } from "../sheets/logDetailView.utils";

// One item of a Responses conversation, rendered on the timeline the log sheet draws: a reasoning
// item, a tool call and its result, an assistant message. The live session panel renders a
// delegation's items with the same rows.

export type MessageRole = "system" | "user" | "assistant" | "reasoning" | "tool";
export const messageToneClass: Record<MessageRole, string> = {
	system: "bg-zinc-50 border-zinc-200 dark:bg-zinc-900/40 dark:border-zinc-800",
	user: "bg-blue-50/60 border-blue-200 dark:bg-blue-950/30 dark:border-blue-900",
	assistant: "bg-white border-zinc-200 dark:bg-zinc-900 dark:border-zinc-800",
	reasoning: "bg-violet-50/70 border-violet-200 dark:bg-violet-950/30 dark:border-violet-900",
	tool: "bg-amber-50/70 border-amber-200 dark:bg-amber-950/30 dark:border-amber-900",
};
export const messageDotClass: Record<MessageRole, string> = {
	system: "bg-zinc-400",
	user: "bg-blue-500",
	assistant: "bg-zinc-900 dark:bg-zinc-100",
	reasoning: "bg-violet-500",
	tool: "bg-amber-500",
};
export const messageRoleLabel: Record<MessageRole, string> = {
	system: "System",
	user: "User",
	assistant: "Assistant",
	reasoning: "Reasoning",
	tool: "Tool Result",
};

export const extractResponsesText = (msg: ResponsesMessage, mapping?: Record<string, string>): string => {
	let text: string;
	if (msg.type === "reasoning") {
		const summaryText = (msg.summary ?? [])
			.map((s) => s.text)
			.filter(Boolean)
			.join("\n")
			.trim();
		if (summaryText) text = summaryText;
		else if (msg.encrypted_content) text = msg.encrypted_content;
		else text = "";
	} else if (typeof msg.content === "string") {
		text = msg.content;
	} else if (Array.isArray(msg.content)) {
		text = msg.content
			.filter(
				(b: any) =>
					b &&
					(b.text || b.refusal) &&
					(b.type === "input_text" || b.type === "output_text" || b.type === "reasoning_text" || b.type === "refusal"),
			)
			// Refusal blocks carry their text in `refusal`, not `text`.
			.map((b: any) => (b.text ?? b.refusal) as string)
			.join("\n");
	} else if (typeof (msg as any).arguments === "string") {
		text = (msg as any).arguments as string;
	} else {
		text = "";
	}
	if (mapping && text) {
		for (const [key, value] of Object.entries(mapping)) {
			text = text.replaceAll(`[${key}]`, value);
		}
	}
	return text;
};

export type ReasoningParts = {
	summaries: string[];
	encrypted?: string;
	signatures: string[];
	contentText?: string;
};

export const collectReasoningFromBlocks = (blocks: any[]): { text: string; signatures: string[] } => {
	const texts: string[] = [];
	const signatures: string[] = [];
	for (const b of blocks) {
		if (!b || typeof b !== "object") continue;
		const isReasoningish =
			b.type === "input_text" || b.type === "output_text" || b.type === "reasoning_text" || b.type === "refusal" || !b.type;
		if (isReasoningish && typeof b.text === "string" && b.text.trim()) {
			texts.push(b.text);
		}
		if (typeof b.signature === "string" && b.signature.trim()) {
			signatures.push(b.signature.trim());
		}
	}
	return { text: texts.join("\n"), signatures };
};

export const extractReasoningParts = (msg: ResponsesMessage, mapping?: Record<string, string>): ReasoningParts => {
	let summaries = (msg.summary ?? []).map((s) => (s?.text ?? "").trim()).filter(Boolean);
	const encryptedRaw = (msg as any).encrypted_content?.trim?.();
	let encrypted = encryptedRaw ? encryptedRaw : undefined;
	const signatures: string[] = [];
	let contentText = "";
	if (typeof msg.content === "string") {
		contentText = msg.content;
	} else if (Array.isArray(msg.content)) {
		const fromContent = collectReasoningFromBlocks(msg.content as any[]);
		contentText = fromContent.text;
		signatures.push(...fromContent.signatures);
	}
	// Some providers stash reasoning under `output` instead of `content`
	const out = (msg as any).output;
	if (out !== undefined) {
		if (typeof out === "string" && out.trim() && !contentText) {
			contentText = out;
		} else if (Array.isArray(out)) {
			const fromOutput = collectReasoningFromBlocks(out as any[]);
			if (!contentText && fromOutput.text) contentText = fromOutput.text;
			signatures.push(...fromOutput.signatures);
		}
	}
	// Defensive: top-level text-bearing fields some variants use
	if (!contentText) {
		const topText =
			(typeof (msg as any).text === "string" && (msg as any).text) ||
			(typeof (msg as any).thinking === "string" && (msg as any).thinking) ||
			"";
		if (topText.trim()) contentText = topText;
	}
	if (mapping) {
		summaries = summaries.map((s) => {
			for (const [key, value] of Object.entries(mapping)) {
				s = s.replaceAll(`[${key}]`, value);
			}
			return s;
		});
		if (encrypted) {
			for (const [key, value] of Object.entries(mapping)) {
				encrypted = encrypted.replaceAll(`[${key}]`, value);
			}
		}
		if (contentText) {
			for (const [key, value] of Object.entries(mapping)) {
				contentText = contentText.replaceAll(`[${key}]`, value);
			}
		}
	}
	return {
		summaries,
		encrypted,
		signatures,
		contentText: contentText || undefined,
	};
};

export const getResponsesRole = (msg: ResponsesMessage): MessageRole => {
	if (msg.type === "reasoning") return "reasoning";
	if (
		msg.type &&
		(msg.type.endsWith("_call") ||
			msg.type.endsWith("_call_output") ||
			msg.type === "tool_search_output" ||
			msg.type === "additional_tools" ||
			msg.type === "mcp_list_tools" ||
			msg.type === "mcp_approval_request" ||
			msg.type === "mcp_approval_responses")
	) {
		return "tool";
	}
	const r = msg.role;
	if (r === "user") return "user";
	if (r === "assistant") return "assistant";
	if (r === "system" || r === "developer") return "system";
	return "assistant";
};

// Expands namespace tool declarations into their callable children
// (`namespace.tool` names), leaving plain declarations untouched.
export const flattenDeclaredTools = (tools: any[]): any[] =>
	tools.flatMap((tool) =>
		tool?.type === "namespace" && Array.isArray(tool.tools)
			? (tool.tools as any[]).map((nested) => ({ ...nested, name: `${tool.name ?? "namespace"}.${nested?.name ?? ""}` }))
			: [tool],
	);

export function EncryptedReveal({ text, label }: { text: string; label: string }) {
	const [open, setOpen] = useState(false);
	return (
		<div className="space-y-1">
			<button
				type="button"
				onClick={() => setOpen((o) => !o)}
				className="text-muted-foreground hover:text-foreground inline-flex items-center gap-1 text-[10.5px] font-semibold tracking-wider uppercase"
			>
				<ChevronDown className={cn("h-3 w-3 transition-transform", open ? "rotate-180" : "-rotate-90")} />
				{label}
				{!open ? (
					<span className="text-muted-foreground/70 ml-1 font-mono text-[10px] tracking-normal normal-case">{text.length} chars</span>
				) : null}
			</button>
			{open ? <pre className="font-mono text-[12.5px] leading-[1.6] break-all whitespace-pre-wrap">{text}</pre> : null}
		</div>
	);
}

export function CollapsibleCode({
	text,
	preview = 3,
	lang,
	mono = true,
}: {
	text: string;
	preview?: number;
	lang?: string;
	mono?: boolean;
}) {
	const [open, setOpen] = useState(false);
	// Trailing blank lines would otherwise count as hidden content and render a
	// "Show more" that expands to nothing visible.
	const lines = text.replace(/\s+$/, "").split("\n");
	const shown = open ? lines : lines.slice(0, preview);
	const hasMore = lines.length > preview;
	const moreCount = lines.length - preview;
	return (
		<>
			{mono ? (
				<pre className="font-mono text-[12.5px] leading-[1.6] break-words whitespace-pre-wrap">{shown.join("\n")}</pre>
			) : (
				<div className="text-[13px] leading-relaxed break-words whitespace-pre-wrap">{shown.join("\n")}</div>
			)}
			{hasMore && (
				<div className="mt-1.5 flex items-center justify-between">
					<button
						type="button"
						onClick={() => setOpen((o) => !o)}
						className="text-primary inline-flex items-center gap-1 text-[11.5px] font-medium hover:underline"
					>
						{open ? "Show less" : `Show ${moreCount} more lines`}
						<ChevronDown className={cn("h-3 w-3 transition-transform", open && "rotate-180")} />
					</button>
					<span className="text-muted-foreground font-mono text-[10.5px]">
						{lines.length} lines{lang ? ` · ${lang}` : ""}
					</span>
				</div>
			)}
		</>
	);
}

// Generated tool identifiers (e.g. Codex-style names with embedded signatures)
// can run to hundreds of characters; truncate the middle and keep the full name
// one hover away.
export const TOOL_NAME_MAX = 48;

export function ToolNameLabel({ name }: { name: string }) {
	if (name.length <= TOOL_NAME_MAX) return <>{name}</>;
	const truncated = `${name.slice(0, 32)}…${name.slice(-12)}`;
	return (
		<Tooltip>
			<TooltipTrigger asChild>
				<span className="cursor-default" data-testid="log-tool-name-truncated">
					{truncated}
				</span>
			</TooltipTrigger>
			<TooltipContent className="max-w-[480px] font-mono text-[11px] break-all">{name}</TooltipContent>
		</Tooltip>
	);
}

export function MessageRow({
	role,
	meta,
	children,
	last = false,
	label,
}: {
	role: MessageRole;
	meta?: ReactNode;
	children: ReactNode;
	last?: boolean;
	label?: string;
}) {
	return (
		<div className="flex gap-3">
			<div className="flex flex-col items-center pt-1.5">
				<span className={cn("h-2 w-2 rounded-sm", messageDotClass[role])} />
				{!last && <div className="bg-border my-1 w-px flex-1" />}
			</div>
			<div className="min-w-0 flex-1 pb-4">
				<div className="mb-1 flex items-center gap-2">
					<span className="text-foreground text-[11.5px] font-semibold">{label ?? messageRoleLabel[role]}</span>
					{meta ? <span className="text-muted-foreground text-[11px]">{meta}</span> : null}
				</div>
				<div className={cn("rounded-sm border p-3 text-[13px] leading-relaxed", messageToneClass[role])}>{children}</div>
			</div>
		</div>
	);
}

export function ResponsesItemRow({
	msg,
	mapping,
	last = false,
	children,
}: {
	msg: ResponsesMessage;
	mapping?: Record<string, string>;
	last?: boolean;
	children?: ReactNode;
}) {
	const role = getResponsesRole(msg);
	const isToolCall = isResponsesToolCallItem(msg.type);
	// `{}` arguments rendered as a one-line body read as an empty result.
	const noArguments = isToolCall && hasNoToolArguments(msg.arguments);
	const reasoningParts = role === "reasoning" ? extractReasoningParts(msg, mapping) : null;
	const reasoningHasAny =
		!!reasoningParts &&
		(reasoningParts.summaries.length > 0 ||
			!!reasoningParts.encrypted ||
			!!reasoningParts.contentText ||
			reasoningParts.signatures.length > 0);
	const text = role === "reasoning" || noArguments ? "" : extractResponsesText(msg, mapping);
	// Whatever the item carries outside the fields rendered below — a server tool's `action`,
	// a custom_tool_call's `input`, a compaction item's `encrypted_content`.
	const itemPayload = extractResponsesItemPayload(msg);
	const lineCount = text ? text.split("\n").length : 0;
	const approxTokens = text ? Math.max(1, Math.round(text.length / 4)) : 0;
	let meta: ReactNode | undefined;
	if (role === "reasoning" && reasoningParts) {
		const totalLen =
			reasoningParts.summaries.reduce((acc, s) => acc + s.length, 0) +
			(reasoningParts.contentText?.length ?? 0) +
			(reasoningParts.encrypted?.length ?? 0);
		const totalApprox = totalLen ? Math.max(1, Math.round(totalLen / 4)) : 0;
		const hasOpaqueOnly =
			(!!reasoningParts.encrypted || reasoningParts.signatures.length > 0) &&
			reasoningParts.summaries.length === 0 &&
			!reasoningParts.contentText;
		meta = totalApprox ? `~${totalApprox} tokens${hasOpaqueOnly ? " · encrypted" : ""}` : hasOpaqueOnly ? "encrypted" : undefined;
	} else {
		meta = text ? (
			role === "system" || role === "tool" ? (
				msg.name ? (
					<>
						<ToolNameLabel name={msg.name} />
						{` · ${lineCount} line${lineCount === 1 ? "" : "s"} · ~${approxTokens} tokens`}
					</>
				) : (
					`${lineCount} line${lineCount === 1 ? "" : "s"} · ~${approxTokens} tokens`
				)
			) : (
				`${lineCount} line${lineCount === 1 ? "" : "s"}`
			)
		) : noArguments ? (
			<>
				{msg.name ? <ToolNameLabel name={msg.name} /> : null}
				{msg.name ? " · no arguments" : "no arguments"}
			</>
		) : msg.name ? (
			<ToolNameLabel name={msg.name} />
		) : msg.type === "function_call_output" && msg.call_id ? (
			<ToolNameLabel name={msg.call_id} />
		) : Array.isArray(msg.tools) ? (
			(() => {
				const callable = flattenDeclaredTools(msg.tools).length;
				return callable !== msg.tools.length
					? `${msg.type} · ${msg.tools.length} declarations · ${callable} callable tools`
					: `${msg.type} · ${msg.tools.length} tool${msg.tools.length === 1 ? "" : "s"}`;
			})()
		) : (
			[msg.type, summarizeResponsesToolCall(msg, mapping)].filter(Boolean).join(" · ") || undefined
		);
	}
	const usePlainText = role === "user" || role === "assistant";
	return (
		<MessageRow role={role} meta={meta} last={last} label={isToolCall ? "Tool Call" : undefined}>
			{role === "reasoning" ? (
				reasoningHasAny && reasoningParts ? (
					<div className="space-y-3">
						{reasoningParts.contentText ? <CollapsibleCode text={reasoningParts.contentText} preview={3} mono={false} /> : null}
						{reasoningParts.summaries.map((s, i) => (
							<div key={`s-${i}`} className="space-y-1">
								{reasoningParts.summaries.length > 1 ? (
									<div className="text-muted-foreground text-[10.5px] font-semibold tracking-wider uppercase">Summary {i + 1}</div>
								) : null}
								<CollapsibleCode text={s} preview={3} mono={false} />
							</div>
						))}
						{reasoningParts.encrypted ? (
							// Ciphertext is noise even at two preview lines; fold it
							// entirely until the reader asks for it.
							<EncryptedReveal text={reasoningParts.encrypted} label="Encrypted" />
						) : null}
						{reasoningParts.signatures.length > 0 ? (
							<EncryptedReveal
								text={reasoningParts.signatures.join("\n\n")}
								label={reasoningParts.signatures.length > 1 ? "Encrypted signatures" : "Encrypted signature"}
							/>
						) : null}
					</div>
				) : (
					<div className="text-muted-foreground text-[12px] italic">No reasoning content available</div>
				)
			) : noArguments ? (
				<div className="text-muted-foreground text-[12px] italic">No arguments</div>
			) : text ? (
				usePlainText ? (
					<CollapsibleCode text={text} preview={3} mono={false} />
				) : (
					<CollapsibleCode text={text} preview={3} lang={role === "system" ? "xml" : undefined} />
				)
			) : msg.output !== undefined ? (
				<CollapsibleCode
					text={
						typeof msg.output === "string"
							? applyRedactionMapping(msg.output, mapping)
							: JSON.stringify(applyRedactionMappingToValue(msg.output, mapping), null, 2)
					}
					preview={3}
				/>
			) : Array.isArray(msg.tools) && msg.tools.length > 0 ? (
				<CollapsibleCode text={JSON.stringify(msg.tools, null, 2)} preview={3} />
			) : Array.isArray(msg.tools) ? (
				<div className="text-muted-foreground text-[12px] italic">No tools declared</div>
			) : itemPayload ? (
				<CollapsibleCode text={JSON.stringify(applyRedactionMappingToValue(itemPayload, mapping), null, 2)} preview={3} />
			) : (
				<div className="text-muted-foreground text-[12px] italic">No content</div>
			)}
			{Array.isArray(msg.content) &&
				msg.content
					.filter((b) => b?.type === "input_image" && b.image_url)
					.map((b, i) => (
						<img key={`${i}-${b.image_url}`} src={b.image_url} alt="Attached image" className="mt-2 max-w-full rounded border" />
					))}
			{children}
		</MessageRow>
	);
}