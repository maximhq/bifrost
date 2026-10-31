import { AgentCardPreview } from "@/components/agents/agentCardPreview";
import { mapAgentCard } from "@/lib/utils/agentCard";
import { InteractiveValue, type FilterTarget } from "./interactiveValue";
import BlockHeader from "@/app/workspace/logs/views/blockHeader";
import { Badge } from "@/components/ui/badge";
import { CodeEditor } from "@/components/ui/codeEditor";
import { DottedSeparator } from "@/components/ui/separator";
import { TruncatedLabel } from "@/components/ui/truncatedLabel";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { useCopyToClipboard } from "@/hooks/useCopyToClipboard";
import { StatusColors } from "@/lib/constants/logs";
import { cn } from "@/lib/utils";
import { format, isValid } from "date-fns";
import { AlertCircle, ChevronDown, Download, ExternalLink, FileText, Webhook } from "lucide-react";
import { createContext, useContext, useState, type ReactNode } from "react";

/**
 * Structured renderer for A2A payload bodies.
 *
 * The bodies are A2A protocol v1 ProtoJSON objects, but the shapes are only a
 * best-effort contract: agents can emit anything. Every renderer here therefore
 * recognizes what it can and routes the rest to a pretty-printed JSON block, so
 * nothing in a payload is ever silently dropped.
 */

const TaskFilterContext = createContext<((filters: FilterTarget) => void) | undefined>(undefined);

function TaskIdValue({ value }: { value: string }) {
	const onFilter = useContext(TaskFilterContext);
	return (
		<InteractiveValue
			value={value}
			label="Task"
			filter={{ task_id: value }}
			onFilter={onFilter}
			testId="agent-log-response-task-id"
			className="block font-mono text-[11px]"
		/>
	);
}

// ---------------------------------------------------------------------------
// Defensive JSON helpers
// ---------------------------------------------------------------------------

function isRecord(value: unknown): value is Record<string, unknown> {
	return typeof value === "object" && value !== null && !Array.isArray(value);
}

function asRecord(value: unknown): Record<string, unknown> | null {
	return isRecord(value) ? value : null;
}

function asArray(value: unknown): unknown[] | null {
	return Array.isArray(value) ? value : null;
}

function asString(value: unknown): string | undefined {
	return typeof value === "string" && value !== "" ? value : undefined;
}

function prettyPrint(value: unknown): string {
	try {
		return JSON.stringify(value, null, 2) ?? String(value);
	} catch {
		return String(value);
	}
}

/** Parse a body string, returning the raw string when it is not JSON. */
function parseBody(body: string): { value: unknown; parsed: boolean } {
	try {
		return { value: JSON.parse(body) as unknown, parsed: true };
	} catch {
		return { value: body, parsed: false };
	}
}

/**
 * Fields of `source` that no renderer claimed. Surfaced in a collapsed JSON
 * block so unrecognized protocol additions stay visible.
 */
function unclaimedFields(source: Record<string, unknown>, claimed: string[]): Record<string, unknown> | null {
	const rest: Record<string, unknown> = {};
	for (const [key, value] of Object.entries(source)) {
		if (claimed.includes(key)) continue;
		if (value === null || value === undefined) continue;
		if (Array.isArray(value) && value.length === 0) continue;
		if (isRecord(value) && Object.keys(value).length === 0) continue;
		rest[key] = value;
	}
	return Object.keys(rest).length > 0 ? rest : null;
}

function formatTimestamp(value: unknown): string | undefined {
	const raw = asString(value);
	if (!raw) return undefined;
	const date = new Date(raw);
	return isValid(date) ? format(date, "yyyy-MM-dd HH:mm:ss") : raw;
}

// ---------------------------------------------------------------------------
// Shared primitives
// ---------------------------------------------------------------------------

function SubLabel({ children }: { children: ReactNode }) {
	return <div className="text-muted-foreground text-xs font-medium uppercase">{children}</div>;
}

function JsonBlock({ code, maxHeight = 320 }: { code: string; maxHeight?: number }) {
	return (
		<CodeEditor
			className="z-0 w-full"
			shouldAdjustInitialHeight={true}
			maxHeight={maxHeight}
			wrap={true}
			code={code}
			lang="json"
			readonly={true}
			options={{
				scrollBeyondLastLine: false,
				collapsibleBlocks: true,
				lineNumbers: "off",
				alwaysConsumeMouseWheel: false,
			}}
		/>
	);
}

/** Collapsed-by-default disclosure used for every piece of secondary detail. */
function Disclosure({ summary, meta, children }: { summary: string; meta?: string; children: ReactNode }) {
	return (
		<details className="group bg-card rounded-sm border">
			<summary className="hover:bg-muted/30 flex cursor-pointer items-center justify-between gap-3 px-3 py-2 text-[12.5px] transition">
				<span className="text-foreground font-medium">{summary}</span>
				<span className="text-muted-foreground flex items-center gap-2 text-[11px]">
					{meta}
					<ChevronDown className="h-3.5 w-3.5 transition-transform group-open:rotate-180" />
				</span>
			</summary>
			<div className="space-y-3 border-t p-3">{children}</div>
		</details>
	);
}

function UnclaimedFields({ source, claimed }: { source: Record<string, unknown>; claimed: string[] }) {
	const rest = unclaimedFields(source, claimed);
	if (!rest) return null;
	return (
		<Disclosure summary="Other fields" meta={Object.keys(rest).join(", ")}>
			<JsonBlock code={prettyPrint(rest)} maxHeight={280} />
		</Disclosure>
	);
}

/** Plain text that wraps, clamped to a preview until expanded. */
function CollapsibleText({ text, preview = 8 }: { text: string; preview?: number }) {
	const [open, setOpen] = useState(false);
	const lines = text.split("\n");
	const hasMore = lines.length > preview;
	const shown = open || !hasMore ? lines : lines.slice(0, preview);
	return (
		<>
			<div className="text-[13px] leading-relaxed break-words whitespace-pre-wrap">{shown.join("\n")}</div>
			{hasMore && (
				<button
					type="button"
					onClick={() => setOpen((value) => !value)}
					className="text-primary mt-1.5 inline-flex items-center gap-1 text-[11.5px] font-medium hover:underline"
				>
					{open ? "Show less" : `Show ${lines.length - preview} more lines`}
					<ChevronDown className={cn("h-3 w-3 transition-transform", open && "rotate-180")} />
				</button>
			)}
		</>
	);
}

// ---------------------------------------------------------------------------
// Task state
// ---------------------------------------------------------------------------

type StateTone = keyof typeof StatusColors;

function taskStateTone(state: string): StateTone | null {
	const normalized = state.replace(/^TASK_STATE_/, "").toLowerCase();
	if (normalized === "completed") return "success";
	if (normalized === "failed" || normalized === "rejected") return "error";
	if (normalized === "canceled" || normalized === "cancelled") return "cancelled";
	if (normalized === "working" || normalized === "submitted" || normalized.endsWith("required")) return "processing";
	return null;
}

function taskStateLabel(state: string): string {
	return state
		.replace(/^TASK_STATE_/, "")
		.replace(/_/g, " ")
		.toLowerCase();
}

export function TaskStateBadge({ state }: { state: string }) {
	const tone = taskStateTone(state);
	return (
		<span
			className={cn("rounded-sm px-2 py-0.5 text-xs font-medium capitalize", tone ? StatusColors[tone] : "bg-muted text-muted-foreground")}
		>
			{taskStateLabel(state)}
		</span>
	);
}

// ---------------------------------------------------------------------------
// Message parts
// ---------------------------------------------------------------------------

function safeMediaSource(source: string): string | null {
	if (source.startsWith("data:")) return source;
	try {
		const url = new URL(source);
		return ["http:", "https:", "blob:"].includes(url.protocol) ? source : null;
	} catch {
		return null;
	}
}

// rendersMediaPreview matches the media types and sources supported by the inline renderer.
function rendersMediaPreview(source: string, mediaType?: string) {
	const type = (mediaType ?? "application/octet-stream").toLowerCase();
	return Boolean(safeMediaSource(source)) && type !== "image/svg+xml" && /^(image|audio|video)\//.test(type);
}

function FilePartView({ source, mediaType, filename }: { source: string; mediaType?: string; filename?: string }) {
	const safeSource = safeMediaSource(source);
	const type = (mediaType ?? "application/octet-stream").toLowerCase();
	const label = filename ?? "File";
	const inlineSafe = rendersMediaPreview(source, mediaType);

	if (safeSource && inlineSafe && type.startsWith("image/")) {
		return <img src={safeSource} alt={label} loading="lazy" className="max-h-80 max-w-full rounded-sm border object-contain" />;
	}
	if (safeSource && inlineSafe && type.startsWith("audio/")) {
		return <audio src={safeSource} controls preload="metadata" className="w-full" />;
	}
	if (safeSource && inlineSafe && type.startsWith("video/")) {
		return <video src={safeSource} controls preload="metadata" className="max-h-80 max-w-full rounded-sm border" />;
	}

	return (
		<div className="bg-muted/30 flex items-center justify-between gap-3 rounded-sm border p-2.5 text-xs">
			<div className="min-w-0">
				<div className="truncate font-medium">{label}</div>
				<div className="text-muted-foreground mt-1 truncate">{type}</div>
			</div>
			{safeSource ? (
				<a
					href={safeSource}
					download={filename}
					target="_blank"
					rel="noreferrer"
					className="text-primary inline-flex shrink-0 items-center gap-1 hover:underline"
				>
					{source.startsWith("data:") ? <Download className="size-3.5" /> : <ExternalLink className="size-3.5" />}
					{source.startsWith("data:") ? "Download" : "View"}
				</a>
			) : (
				<FileText className="text-muted-foreground size-4 shrink-0" />
			)}
		</div>
	);
}

function PartView({ part }: { part: unknown }) {
	const record = asRecord(part);
	if (!record) return <JsonBlock code={prettyPrint(part)} maxHeight={200} />;

	const text = asString(record.text);
	if (text !== undefined) {
		return <CollapsibleText text={text} />;
	}

	const mediaType = asString(record.mediaType) ?? asString(record.media_type) ?? asString(record.mimeType);
	const filename = asString(record.filename);
	const url = asString(record.url);
	const raw = asString(record.raw);
	if (url) return <FilePartView source={url} mediaType={mediaType} filename={filename} />;
	if (raw)
		return (
			<FilePartView source={`data:${mediaType ?? "application/octet-stream"};base64,${raw}`} mediaType={mediaType} filename={filename} />
		);

	const file = asRecord(record.file);
	if (file) {
		const nestedMediaType = asString(file.mimeType) ?? asString(file.mediaType) ?? mediaType;
		const nestedFilename = asString(file.name) ?? filename;
		const nestedURL = asString(file.uri) ?? asString(file.fileWithUri);
		const nestedRaw = asString(file.bytes) ?? asString(file.fileWithBytes);
		if (nestedURL) return <FilePartView source={nestedURL} mediaType={nestedMediaType} filename={nestedFilename} />;
		if (nestedRaw) {
			return (
				<FilePartView
					source={`data:${nestedMediaType ?? "application/octet-stream"};base64,${nestedRaw}`}
					mediaType={nestedMediaType}
					filename={nestedFilename}
				/>
			);
		}
		return <JsonBlock code={prettyPrint(file)} maxHeight={200} />;
	}

	if (record.data !== undefined) {
		return <JsonBlock code={prettyPrint(record.data)} maxHeight={200} />;
	}

	return <JsonBlock code={prettyPrint(record)} maxHeight={200} />;
}

export function A2APartsView({ parts, showMetadata = true }: { parts: unknown[]; showMetadata?: boolean }) {
	if (parts.length === 0) return <div className="text-muted-foreground text-[12.5px]">No content.</div>;
	return (
		<div className="space-y-2">
			{parts.map((part, index) => {
				const record = asRecord(part);
				const details = record ? unclaimedFields(record, ["text", "url", "raw", "data", "file"]) : null;
				return (
					<div key={index}>
						<PartView part={part} />
						{showMetadata && details && (
							<details className="group/part mt-2">
								<summary className="text-muted-foreground hover:text-foreground inline-flex cursor-pointer list-none items-center gap-1.5 text-[11px] [&::-webkit-details-marker]:hidden">
									<ChevronDown className="size-3 transition-transform group-open/part:rotate-180" />
									Part details
								</summary>
								<div className="mt-2">
									<OperationParametersView params={details} />
								</div>
							</details>
						)}
					</div>
				);
			})}
		</div>
	);
}

// ---------------------------------------------------------------------------
// Messages
// ---------------------------------------------------------------------------

const MESSAGE_CLAIMED = ["role", "parts", "messageId", "taskId", "contextId"];

function messageRoleLabel(role: string | undefined): string {
	if (!role) return "Message";
	const normalized = role.replace(/^ROLE_/, "").toLowerCase();
	if (normalized === "user") return "User";
	if (normalized === "agent" || normalized === "assistant") return "Agent";
	return normalized.replace(/_/g, " ");
}

function isUserRole(role: string | undefined): boolean {
	return (role ?? "").replace(/^ROLE_/, "").toLowerCase() === "user";
}

function looksLikeMessage(value: Record<string, unknown>): boolean {
	return Array.isArray(value.parts) || typeof value.role === "string";
}

/** One message rendered on a timeline rail, mirroring the LLM logs conversation. */
function MessageRow({ message, last = false }: { message: Record<string, unknown>; last?: boolean }) {
	const { copy } = useCopyToClipboard({ successMessage: "Copied" });
	const role = asString(message.role);
	const user = isUserRole(role);
	const parts = asArray(message.parts) ?? [];
	const messageId = asString(message.messageId);

	return (
		<div className="flex gap-3">
			<div className="flex flex-col items-center pt-1.5">
				<span className={cn("h-2 w-2 rounded-sm", user ? "bg-blue-500" : "bg-zinc-900 dark:bg-zinc-100")} />
				{!last && <div className="bg-border my-1 w-px flex-1" />}
			</div>
			<div className={cn("min-w-0 flex-1", !last && "pb-4")}>
				<div className="mb-1 flex items-center gap-2">
					{messageId ? (
						<Tooltip>
							<TooltipTrigger asChild>
								<button
									type="button"
									aria-label="Copy message ID"
									className="text-foreground hover:text-primary cursor-pointer text-[11.5px] font-semibold capitalize"
									onClick={() => copy(messageId)}
								>
									{messageRoleLabel(role)}
								</button>
							</TooltipTrigger>
							<TooltipContent>Copy message ID</TooltipContent>
						</Tooltip>
					) : (
						<span className="text-foreground text-[11.5px] font-semibold capitalize">{messageRoleLabel(role)}</span>
					)}
					{messageId && <span className="text-muted-foreground truncate font-mono text-[10.5px]">{messageId}</span>}
				</div>
				<div
					className={cn(
						"space-y-2 rounded-sm border p-3 text-[13px] leading-relaxed",
						user
							? "border-blue-200 bg-blue-50/60 dark:border-blue-900 dark:bg-blue-950/30"
							: "border-zinc-200 bg-white dark:border-zinc-800 dark:bg-zinc-900",
					)}
				>
					<A2APartsView parts={parts} />
					<UnclaimedFields source={message} claimed={MESSAGE_CLAIMED} />
				</div>
			</div>
		</div>
	);
}

// ---------------------------------------------------------------------------
// Artifacts
// ---------------------------------------------------------------------------

const ARTIFACT_CLAIMED = ["artifactId", "name", "description", "parts"];

function ArtifactView({ artifact }: { artifact: unknown }) {
	const record = asRecord(artifact);
	if (!record) return <JsonBlock code={prettyPrint(artifact)} maxHeight={200} />;

	const name = asString(record.name);
	const artifactId = asString(record.artifactId);
	const description = asString(record.description);

	return (
		<div className="rounded-sm border">
			<div className="flex items-center justify-between gap-3 border-b px-3 py-2">
				<span className="text-foreground truncate text-[12.5px] font-medium">{name ?? artifactId ?? "Artifact"}</span>
				{name && artifactId && <span className="text-muted-foreground shrink-0 font-mono text-[10.5px]">{artifactId}</span>}
			</div>
			<div className="space-y-2 p-3">
				{description && <div className="text-muted-foreground text-[12.5px] leading-relaxed">{description}</div>}
				<A2APartsView parts={asArray(record.parts) ?? []} showMetadata={false} />
				<ArtifactMetadata artifact={record} parts={asArray(record.parts) ?? []} />
			</div>
		</div>
	);
}

// ---------------------------------------------------------------------------
// Push notification config
// ---------------------------------------------------------------------------

function PushConfigView({ config, inList = false }: { config: Record<string, unknown>; inList?: boolean }) {
	const url = asString(config.url);
	const id = asString(config.id);
	const authentication = asRecord(config.authentication);
	const schemes = asArray(authentication?.schemes)?.filter((value): value is string => typeof value === "string") ?? [];
	const credentials = asString(authentication?.credentials);
	return (
		<details className={inList ? "group/push" : "group/push overflow-hidden rounded-sm border"}>
			<summary
				data-testid="agent-log-push-config-summary"
				className="hover:bg-muted/30 flex cursor-pointer list-none items-center gap-3 p-3 [&::-webkit-details-marker]:hidden"
			>
				<Webhook className="text-muted-foreground size-4 shrink-0" />
				<div className="min-w-0 flex-1 space-y-1">
					<TruncatedLabel className="block font-mono text-xs">{url ?? "No callback URL provided"}</TruncatedLabel>
					{id && <TruncatedLabel className="text-muted-foreground block font-mono text-[11px]">{id}</TruncatedLabel>}
				</div>
				<ChevronDown className="text-muted-foreground size-4 shrink-0 transition-transform group-open/push:rotate-180" />
			</summary>
			<div className="space-y-3 border-t p-3">
				<OperationParametersView params={{ configurationId: id, taskId: config.taskId, token: config.token }} />
				{(schemes.length > 0 || credentials) && (
					<div className="space-y-2">
						<SubLabel>Authentication</SubLabel>
						<div className="flex flex-wrap items-center gap-1.5">
							{schemes.map((scheme) => (
								<Badge key={scheme} variant="outline" className="rounded-sm font-normal">
									{scheme}
								</Badge>
							))}
							{credentials && <span className="text-muted-foreground text-xs">Credentials provided</span>}
						</div>
					</div>
				)}
				{authentication && <UnclaimedFields source={authentication} claimed={["schemes", "credentials"]} />}
				<UnclaimedFields source={config} claimed={["url", "id", "taskId", "token", "authentication"]} />
			</div>
		</details>
	);
}

/** Pulls the push config out of a SendMessage `configuration` object. */
function ConfigurationView({ configuration }: { configuration: Record<string, unknown> }) {
	const pushConfig = asRecord(configuration.taskPushNotificationConfig) ?? asRecord(configuration.pushNotificationConfig);
	const nested = asRecord(pushConfig?.pushNotificationConfig) ?? pushConfig;
	const settings = unclaimedFields(configuration, nested ? ["taskPushNotificationConfig", "pushNotificationConfig"] : []);
	return (
		<div className="space-y-3">
			{settings && <OperationParametersView params={settings} />}
			{nested && <PushConfigView config={nested} />}
		</div>
	);
}

// ---------------------------------------------------------------------------
// Errors
// ---------------------------------------------------------------------------

function ErrorView({ error }: { error: Record<string, unknown> }) {
	// Both JSON-RPC (`{code, message, data}`) and BifrostError (`{error: {...}}`)
	// shapes reach this view.
	const inner = asRecord(error.error) ?? error;
	const message = asString(inner.message) ?? asString(error.message) ?? asString(inner.error);
	const code = inner.code ?? error.code ?? error.status_code;
	// BifrostError repeats the message under `error.error`; drop the duplicate so
	// it does not show up as an unrecognized field.
	const innerClaimed = asString(inner.error) === message ? ["message", "code", "error"] : ["message", "code"];
	// A single merged disclosure: the wrapper and the inner error would otherwise
	// each contribute their own "Other fields" block.
	const rest = {
		...(unclaimedFields(inner, innerClaimed) ?? {}),
		...(inner === error ? {} : (unclaimedFields(error, ["error", "message", "code", "status_code"]) ?? {})),
	};

	return (
		<div className="border-destructive/50 rounded-sm border">
			<div className="border-destructive/50 text-destructive flex items-center gap-2 border-b px-3 py-2">
				<AlertCircle className="h-3.5 w-3.5 shrink-0" />
				<span className="text-[12.5px] font-semibold">Error</span>
				{code !== undefined && code !== null && code !== "" && (
					<Badge variant="outline" className="border-destructive/40 text-destructive rounded-sm px-1.5 py-0 font-mono text-[11px]">
						{String(code)}
					</Badge>
				)}
			</div>
			<div className="space-y-3 p-3">
				<div className="text-[13px] leading-relaxed break-words whitespace-pre-wrap">{message ?? "No error message recorded."}</div>
				<UnclaimedFields source={rest} claimed={[]} />
			</div>
		</div>
	);
}

// ---------------------------------------------------------------------------
// Task
// ---------------------------------------------------------------------------

const TASK_CLAIMED = ["id", "contextId", "status", "history", "artifacts"];

function looksLikeTask(value: Record<string, unknown>): boolean {
	return isRecord(value.status) || Array.isArray(value.history) || Array.isArray(value.artifacts);
}

function taskTitle(task: Record<string, unknown>, index?: number): string {
	const history = asArray(task.history) ?? [];
	for (const entry of history) {
		const message = asRecord(entry);
		if (!message || !isUserRole(asString(message.role))) continue;
		for (const part of asArray(message.parts) ?? []) {
			const text = asString(asRecord(part)?.text)?.trim();
			if (text) return text;
		}
	}

	const firstArtifact = asRecord((asArray(task.artifacts) ?? [])[0]);
	return asString(firstArtifact?.name) ?? (index === undefined ? "Task" : `Task ${index + 1}`);
}

function TaskView({ task }: { task: Record<string, unknown> }) {
	const state = asString(asRecord(task.status)?.state);
	const taskId = asString(task.id);

	return (
		<div className="overflow-hidden rounded-sm border">
			<div className="bg-muted/20 flex items-center gap-3 border-b px-3 py-3">
				<div className="min-w-0 flex-1 space-y-1">
					<TruncatedLabel className="block text-sm font-medium">{taskTitle(task)}</TruncatedLabel>
					{taskId && <TaskIdValue value={taskId} />}
				</div>
				{state && <TaskStateBadge state={state} />}
			</div>
			<div className="p-3">
				<TaskConversation task={task} />
			</div>
		</div>
	);
}

// ---------------------------------------------------------------------------
// Streamed events
// ---------------------------------------------------------------------------

function StatusUpdateView({ update }: { update: Record<string, unknown> }) {
	const status = asRecord(update.status);
	const state = asString(status?.state);
	const timestamp = formatTimestamp(status?.timestamp);
	const statusMessage = asRecord(status?.message);
	const final = update.final === true;

	return (
		<div className="space-y-3">
			<div className="flex flex-wrap items-center gap-2">
				{state ? <TaskStateBadge state={state} /> : null}
				{final && (
					<Badge variant="outline" className="text-muted-foreground rounded-sm px-1.5 py-0 text-[11px] font-normal">
						final
					</Badge>
				)}
				{timestamp && <span className="text-muted-foreground font-mono text-[11px] tabular-nums">{timestamp}</span>}
			</div>
			{statusMessage && (
				<section className="space-y-2">
					<SubLabel>Status message</SubLabel>
					<A2APartsView parts={asArray(statusMessage.parts) ?? []} />
					<UnclaimedFields source={statusMessage} claimed={MESSAGE_CLAIMED} />
				</section>
			)}
			{status && <UnclaimedFields source={status} claimed={["state", "timestamp", "message"]} />}
			<UnclaimedFields source={update} claimed={["taskId", "contextId", "status", "final"]} />
		</div>
	);
}

function ArtifactUpdateView({ update }: { update: Record<string, unknown> }) {
	const flags = [update.append === true ? "append" : null, update.lastChunk === true ? "last chunk" : null].filter(Boolean) as string[];
	return (
		<div className="space-y-3">
			{flags.length > 0 && (
				<div className="flex flex-wrap items-center gap-2">
					{flags.map((flag) => (
						<Badge key={flag} variant="outline" className="text-muted-foreground rounded-sm px-1.5 py-0 text-[11px] font-normal">
							{flag}
						</Badge>
					))}
				</div>
			)}
			<ArtifactView artifact={update.artifact} />
			<UnclaimedFields source={update} claimed={["taskId", "contextId", "artifact", "append", "lastChunk"]} />
		</div>
	);
}

// ---------------------------------------------------------------------------
// Agent cards and operation parameters
// ---------------------------------------------------------------------------

function labelFromKey(value: string): string {
	return value
		.replace(/([a-z0-9])([A-Z])/g, "$1 $2")
		.replace(/[_-]+/g, " ")
		.replace(/^./, (letter) => letter.toUpperCase());
}

function AgentCardView({ card }: { card: Record<string, unknown> }) {
	return <AgentCardPreview card={mapAgentCard(card)} showFetched={false} gatewayCard />;
}

function ParameterValue({ value }: { value: unknown }) {
	if (typeof value === "boolean") return <span>{value ? "Yes" : "No"}</span>;
	if (typeof value === "string" || typeof value === "number") return <span className="font-mono break-all">{String(value)}</span>;
	if (Array.isArray(value) && value.every((item) => typeof item === "string" || typeof item === "number")) {
		return (
			<div className="flex flex-wrap gap-1.5">
				{value.map((item, index) => (
					<Badge key={`${String(item)}-${index}`} variant="outline" className="rounded-sm font-normal">
						{String(item)}
					</Badge>
				))}
			</div>
		);
	}
	return <JsonBlock code={prettyPrint(value)} maxHeight={160} />;
}

function OperationParametersView({ params }: { params: Record<string, unknown> }) {
	const fields = Object.entries(params).filter(([, value]) => value !== null && value !== undefined && value !== "");
	return (
		<dl className="divide-y">
			{fields.map(([key, value]) => {
				const label =
					key === "configurationId"
						? "Configuration ID"
						: key === "id" || key === "taskId"
							? "Task ID"
							: key === "contextId"
								? "Context ID"
								: labelFromKey(key);
				return (
					<div key={key} className="grid gap-1 py-2.5 first:pt-0 last:pb-0 sm:grid-cols-[140px_minmax(0,1fr)] sm:gap-4">
						<dt className="text-muted-foreground text-[11px] font-medium uppercase">{label}</dt>
						<dd className="text-[12px] break-all">
							{(key === "id" || key === "taskId") && typeof value === "string" ? (
								<TaskIdValue value={value} />
							) : (
								<ParameterValue value={value} />
							)}
						</dd>
					</div>
				);
			})}
		</dl>
	);
}

// ArtifactMetadata collects fields not already shown in the artifact body.
// File cards show names and media types, while media previews need them here.
function ArtifactMetadata({ artifact, parts }: { artifact: Record<string, unknown>; parts: unknown[] }) {
	const fields = unclaimedFields(artifact, ["artifactId", "name", "description", "parts"]);
	if (fields) {
		for (const key of Object.keys(fields)) if (fields[key] === "") delete fields[key];
	}
	const partFields = parts.flatMap((part, index) => {
		const record = asRecord(part);
		if (!record) return [];
		const displayed = ["text", "url", "raw", "data", "file"];
		const file = asRecord(record.file);
		const mediaType = asString(record.mediaType) ?? asString(record.media_type) ?? asString(record.mimeType);
		const effectiveType = asString(file?.mimeType) ?? asString(file?.mediaType) ?? mediaType;
		const source = asString(record.url) ?? asString(file?.uri) ?? asString(file?.fileWithUri);
		const raw = asString(record.raw) ?? asString(file?.bytes) ?? asString(file?.fileWithBytes);
		const preview =
			!asString(record.text) && rendersMediaPreview(source ?? (raw ? `data:${effectiveType};base64,${raw}` : ""), effectiveType);
		if (!preview && (record.url || record.raw || record.file)) displayed.push("filename", "mediaType", "mimeType", "media_type");
		const fields = unclaimedFields(record, displayed);
		if (preview && file) {
			const fileDetails = unclaimedFields(file, ["uri", "fileWithUri", "bytes", "fileWithBytes"]);
			if (fileDetails) return [{ index, fields: { ...fields, file: fileDetails } }];
		}
		if (fields) {
			for (const key of Object.keys(fields)) if (fields[key] === "") delete fields[key];
		}
		return fields && Object.keys(fields).length ? [{ index, fields }] : [];
	});
	if ((!fields || Object.keys(fields).length === 0) && partFields.length === 0) return null;
	return (
		<details className="group/metadata mt-2" data-testid="agent-artifact-metadata">
			<summary className="text-muted-foreground hover:text-foreground inline-flex cursor-pointer list-none items-center gap-1.5 text-[11px] [&::-webkit-details-marker]:hidden">
				<ChevronDown className="size-3 transition-transform group-open/metadata:rotate-180" />
				Metadata
			</summary>
			<div className="mt-2 space-y-3">
				{fields && Object.keys(fields).length > 0 && <OperationParametersView params={fields} />}
				{partFields.map(({ index, fields }) => (
					<div key={index} className="overflow-hidden rounded-sm border">
						<div className="bg-muted/30 text-muted-foreground border-b px-3 py-2 text-[11px] font-medium">Part {index + 1}</div>
						<div className="py-2.5 [&>dl>div]:px-3">
							<OperationParametersView params={fields} />
						</div>
					</div>
				))}
			</div>
		</details>
	);
}

export function A2AConversationMessage({
	item,
	timestamp,
	context,
	last = false,
}: {
	item: {
		id?: string;
		role: "user" | "agent";
		kind: "message" | "status" | "artifact";
		parts: unknown[];
		state?: string;
		artifactName?: string;
		artifactDescription?: string;
		artifactFields?: Record<string, unknown>;
	};
	timestamp?: string;
	context?: string;
	last?: boolean;
}) {
	const { copy } = useCopyToClipboard({ successMessage: "Copied" });
	const idLabel = item.kind === "artifact" ? "artifact" : "message";
	const user = item.role === "user";
	const label = user ? "You" : item.kind === "artifact" ? "Artifact" : item.kind === "status" ? "Agent update" : "Agent response";
	return (
		<div className="flex gap-3">
			<div className="flex flex-col items-center pt-1.5">
				<span className={cn("size-2 rounded-full", user ? "bg-blue-500" : item.kind === "artifact" ? "bg-violet-500" : "bg-foreground")} />
				<div className="bg-border mt-1 w-px flex-1" />
			</div>
			<div className={cn("min-w-0 flex-1", !last && "pb-5")}>
				<div className="mb-1.5 flex min-w-0 items-center gap-2">
					{item.id ? (
						<Tooltip>
							<TooltipTrigger asChild>
								<button
									type="button"
									data-testid="agent-conversation-copy-id"
									aria-label={`Copy ${idLabel} ID`}
									className="hover:text-primary cursor-pointer text-xs font-semibold transition-colors"
									onClick={() => {
										if (item.id) copy(item.id);
									}}
								>
									{label}
								</button>
							</TooltipTrigger>
							<TooltipContent>Copy {idLabel} ID</TooltipContent>
						</Tooltip>
					) : (
						<span className="text-xs font-semibold">{label}</span>
					)}
					{item.state ? <TaskStateBadge state={item.state} /> : null}
					{item.kind === "artifact" && item.artifactName ? (
						<span className="text-muted-foreground truncate text-xs">{item.artifactName}</span>
					) : null}
					{context ? <span className="text-muted-foreground truncate text-xs">{context}</span> : null}
					{timestamp ? <span className="text-muted-foreground ml-auto font-mono text-[10px]">{timestamp}</span> : null}
				</div>
				<div
					className={cn(
						"overflow-hidden rounded-sm border text-sm leading-5",
						user && "border-blue-200 bg-blue-50/60 dark:border-blue-900 dark:bg-blue-950/30",
						item.kind === "artifact" && "border-violet-200 bg-violet-50/60 dark:border-violet-900 dark:bg-violet-950/25",
						!user && item.kind !== "artifact" && "bg-card",
					)}
				>
					{item.kind === "artifact" && item.artifactDescription && (
						<div className="text-muted-foreground border-b border-violet-200 px-3 py-2 text-xs dark:border-violet-900">
							{item.artifactDescription}
						</div>
					)}
					<div className="p-3">
						<A2APartsView parts={item.parts} showMetadata={item.kind !== "artifact"} />
						{item.kind === "artifact" && <ArtifactMetadata artifact={{ artifactId: item.id, ...item.artifactFields }} parts={item.parts} />}
					</div>
				</div>
			</div>
		</div>
	);
}

// TaskConversation renders the returned task's history, final update and artifacts
// using the same timeline as the operation conversation view.
function TaskConversation({ task }: { task: Record<string, unknown> }) {
	const history = asArray(task.history) ?? [];
	const status = asRecord(task.status);
	const statusMessage = asRecord(status?.message);
	const statusId = asString(statusMessage?.messageId);
	const hasStatusInHistory = Boolean(statusId && history.some((entry) => asString(asRecord(entry)?.messageId) === statusId));
	const artifacts = asArray(task.artifacts) ?? [];
	return (
		<div>
			<div className="mb-3">
				<Disclosure summary="Task details" meta={formatTimestamp(status?.timestamp)}>
					<OperationParametersView
						params={{
							id: task.id,
							contextId: task.contextId,
							state: status?.state,
							timestamp: status?.timestamp,
							...unclaimedFields(task, TASK_CLAIMED),
						}}
					/>
					{status && <UnclaimedFields source={status} claimed={["state", "timestamp", "message"]} />}
				</Disclosure>
			</div>
			{history.map((entry, index) => {
				const message = asRecord(entry);
				if (!message) return <JsonBlock key={index} code={prettyPrint(entry)} maxHeight={200} />;
				return (
					<div key={asString(message.messageId) ?? index}>
						<A2AConversationMessage
							last={index === history.length - 1 && (!statusMessage || hasStatusInHistory) && artifacts.length === 0}
							item={{
								id: asString(message.messageId),
								role: isUserRole(asString(message.role)) ? "user" : "agent",
								kind: "message",
								parts: asArray(message.parts) ?? [],
							}}
						/>
						<UnclaimedFields source={message} claimed={MESSAGE_CLAIMED} />
					</div>
				);
			})}
			{statusMessage && !hasStatusInHistory && (
				<>
					<A2AConversationMessage
						last={artifacts.length === 0}
						item={{
							id: asString(statusMessage.messageId),
							role: "agent",
							kind: "status",
							state: asString(status?.state) ?? undefined,
							parts: asArray(statusMessage.parts) ?? [],
						}}
					/>
					<UnclaimedFields source={statusMessage} claimed={MESSAGE_CLAIMED} />
				</>
			)}
			{artifacts.map((entry, index) => {
				const artifact = asRecord(entry);
				if (!artifact) return <JsonBlock key={index} code={prettyPrint(entry)} maxHeight={200} />;
				const description = asString(artifact.description);
				return (
					<div key={asString(artifact.artifactId) ?? index}>
						<A2AConversationMessage
							last={index === artifacts.length - 1}
							item={{
								id: asString(artifact.artifactId),
								role: "agent",
								kind: "artifact",
								artifactName: asString(artifact.name) ?? undefined,
								artifactDescription: description,
								artifactFields: unclaimedFields(artifact, ARTIFACT_CLAIMED) ?? undefined,
								parts: asArray(artifact.parts) ?? [],
							}}
						/>
					</div>
				);
			})}
			{history.length === 0 && !statusMessage && artifacts.length === 0 && (
				<div className="text-muted-foreground py-3 text-sm">No messages or artifacts in this response.</div>
			)}
		</div>
	);
}

function TaskCollectionView({ response }: { response: Record<string, unknown> }) {
	const tasks = asArray(response.tasks) ?? [];
	const nextPageToken = asString(response.nextPageToken);

	return (
		<div className="space-y-4">
			<div className="overflow-hidden rounded-sm border">
				<div className="bg-muted/20 flex items-center justify-between gap-3 border-b px-3 py-2.5">
					<div className="flex items-center gap-2">
						<span className="text-sm font-medium">Tasks</span>
						<Badge variant="secondary" className="rounded-sm px-1.5 text-[11px] font-normal tabular-nums">
							{tasks.length}
						</Badge>
					</div>
					{nextPageToken && <span className="text-muted-foreground text-xs">More tasks available</span>}
				</div>
				{tasks.length > 0 ? (
					<div className="divide-y">
						{tasks.map((entry, index) => {
							const task = asRecord(entry);
							if (!task) return <JsonBlock key={index} code={prettyPrint(entry)} maxHeight={240} />;
							const state = asString(asRecord(task.status)?.state);
							const taskId = asString(task.id) ?? `Task ${index + 1}`;
							const title = taskTitle(task, index);
							return (
								<details key={taskId} className="group/task">
									<summary
										data-testid="agent-log-task-summary"
										className="hover:bg-muted/30 flex cursor-pointer list-none items-center gap-3 px-3 py-3 [&::-webkit-details-marker]:hidden"
									>
										<div className="min-w-0 flex-1 space-y-1">
											<TruncatedLabel className="block text-sm font-medium">{title}</TruncatedLabel>
											<TaskIdValue value={taskId} />
										</div>
										<div className="flex shrink-0 items-center gap-3">
											{state && <TaskStateBadge state={state} />}
											<ChevronDown className="text-muted-foreground size-4 shrink-0 transition-transform group-open/task:rotate-180" />
										</div>
									</summary>
									<div className="border-t px-3 pt-3 pb-4">
										<TaskConversation task={task} />
									</div>
								</details>
							);
						})}
					</div>
				) : (
					<div className="text-muted-foreground py-6 text-center text-sm">No tasks returned.</div>
				)}
			</div>
			<Disclosure summary="Response details">
				<OperationParametersView params={{ totalSize: response.totalSize, pageSize: response.pageSize, nextPageToken }} />
			</Disclosure>
			<UnclaimedFields source={response} claimed={["tasks", "totalSize", "pageSize", "nextPageToken"]} />
		</div>
	);
}

function PushConfigCollectionView({ response }: { response: Record<string, unknown> }) {
	const configs = asArray(response.configs) ?? [];
	return (
		<div className="space-y-4">
			<div className="overflow-hidden rounded-sm border">
				<div className="bg-muted/20 flex items-center gap-2 border-b px-3 py-2.5">
					<span className="text-sm font-medium">Push configurations</span>
					<Badge variant="secondary" className="rounded-sm px-1.5 text-[11px] font-normal tabular-nums">
						{configs.length}
					</Badge>
				</div>
				{configs.length > 0 ? (
					<div className="divide-y">
						{configs.map((config, index) => {
							const record = asRecord(config);
							return record ? (
								<div key={asString(record.id) ?? index}>
									<PushConfigView config={record} inList />
								</div>
							) : (
								<JsonBlock key={index} code={prettyPrint(config)} />
							);
						})}
					</div>
				) : (
					<div className="text-muted-foreground py-6 text-center text-sm">No notification destinations configured.</div>
				)}
			</div>
			<UnclaimedFields source={response} claimed={["configs"]} />
		</div>
	);
}

function EmptyResultView() {
	return (
		<div className="text-muted-foreground rounded-sm border border-dashed p-5 text-center text-sm">
			The operation completed without a response body.
		</div>
	);
}

// ---------------------------------------------------------------------------
// Dispatcher
// ---------------------------------------------------------------------------

function PayloadBody({ value }: { value: unknown }) {
	const root = asRecord(value);
	if (!root) return <JsonBlock code={prettyPrint(value)} />;

	const result = root.result;
	if (result !== undefined) {
		if (result === null) return <EmptyResultView />;
		return <PayloadBody value={result} />;
	}

	const params = asRecord(root.params);
	if (params) return <PayloadBody value={params} />;

	const error = asRecord(root.error);
	if (error) return <ErrorView error={root} />;

	const statusUpdate = asRecord(root.statusUpdate);
	if (statusUpdate) {
		return (
			<div className="space-y-3">
				<StatusUpdateView update={statusUpdate} />
				<UnclaimedFields source={root} claimed={["statusUpdate"]} />
			</div>
		);
	}

	if (isRecord(root.status) && (typeof root.taskId === "string" || root.final !== undefined)) {
		return <StatusUpdateView update={root} />;
	}

	const artifactUpdate = asRecord(root.artifactUpdate);
	if (artifactUpdate) {
		return (
			<div className="space-y-3">
				<ArtifactUpdateView update={artifactUpdate} />
				<UnclaimedFields source={root} claimed={["artifactUpdate"]} />
			</div>
		);
	}

	if (isRecord(root.artifact) && typeof root.taskId === "string") {
		return <ArtifactUpdateView update={root} />;
	}

	if (typeof root.name === "string" && (Array.isArray(root.supportedInterfaces) || Array.isArray(root.skills))) {
		return <AgentCardView card={root} />;
	}

	if (Array.isArray(root.tasks)) return <TaskCollectionView response={root} />;
	if (Array.isArray(root.configs)) return <PushConfigCollectionView response={root} />;

	if (typeof root.url === "string" && (typeof root.id === "string" || typeof root.taskId === "string" || isRecord(root.authentication))) {
		return <PushConfigView config={root} />;
	}

	const pushConfig = asRecord(root.pushNotificationConfig);
	if (pushConfig) {
		return (
			<div className="space-y-3">
				<PushConfigView config={pushConfig} />
				<UnclaimedFields source={root} claimed={["pushNotificationConfig"]} />
			</div>
		);
	}

	const wrappedTask = asRecord(root.task);
	if (wrappedTask) {
		return (
			<div className="space-y-3">
				<TaskView task={wrappedTask} />
				<UnclaimedFields source={root} claimed={["task"]} />
			</div>
		);
	}

	const wrappedMessage = asRecord(root.message);
	if (wrappedMessage && looksLikeMessage(wrappedMessage)) {
		const configuration = asRecord(root.configuration);
		return (
			<div className="space-y-3">
				<MessageRow message={wrappedMessage} last />
				{configuration && <ConfigurationView configuration={configuration} />}
				<UnclaimedFields source={root} claimed={["message", "configuration"]} />
			</div>
		);
	}

	if (looksLikeTask(root)) {
		return <TaskView task={root} />;
	}

	if (looksLikeMessage(root)) {
		return <MessageRow message={root} last />;
	}

	if (Object.keys(root).length === 0) return <EmptyResultView />;

	if (Object.keys(root).length > 0 && Object.values(root).every((value) => !isRecord(value))) {
		return <OperationParametersView params={root} />;
	}

	return <JsonBlock code={prettyPrint(root)} />;
}

interface A2APayloadViewProps {
	onFilter?: (filters: FilterTarget) => void;
	operation?: string;
	status?: string;
	requestBody?: string;
	responseBody?: string;
	eventBody?: string;
	errorDetails?: unknown;
	showSectionTitles?: boolean;
}

function payloadSectionTitle(operation: string | undefined, kind: "request" | "response"): string {
	if (operation === "push_notification") return kind === "request" ? "Received update" : "Acknowledgement";
	if (operation === "push_delivery") return kind === "request" ? "Forwarded update" : "Delivery response";
	return kind === "request" ? "Request" : "Response";
}

function PushRelaySummary({
	operation,
	status,
	value,
}: {
	operation: "push_notification" | "push_delivery";
	status?: string;
	value: unknown;
}) {
	const root = asRecord(value);
	const update = asRecord(root?.statusUpdate) ?? asRecord(root?.artifactUpdate) ?? root;
	const isDelivery = operation === "push_delivery";
	const succeeded = status === "success";
	const failed = status === "error";

	return (
		<div className="space-y-4">
			<div className="flex flex-wrap items-center gap-2">
				<span className="text-sm font-medium">
					{isDelivery ? "Update forwarded to the client callback" : "Update received from the agent"}
				</span>
				{(succeeded || failed) && (
					<Badge
						variant="outline"
						className={cn(
							"rounded-sm",
							succeeded ? "border-emerald-500/30 text-emerald-700 dark:text-emerald-400" : "border-destructive/40 text-destructive",
						)}
					>
						{succeeded ? (isDelivery ? "Delivered" : "Accepted") : "Failed"}
					</Badge>
				)}
			</div>
			{update ? <PayloadBody value={update} /> : <JsonBlock code={prettyPrint(value)} />}
		</div>
	);
}

export function A2APayloadView({
	onFilter,
	operation,
	status,
	requestBody,
	responseBody,
	eventBody,
	errorDetails,
	showSectionTitles = true,
}: A2APayloadViewProps) {
	const errorRecord = asRecord(errorDetails);
	const sections: ReactNode[] = [];
	const isPushConfigOperation = [
		"CreateTaskPushNotificationConfig",
		"GetTaskPushNotificationConfig",
		"ListTaskPushNotificationConfigs",
		"DeleteTaskPushNotificationConfig",
	].includes(operation ?? "");
	const compactSections = isPushConfigOperation || ["ListTasks", "GetTask", "CancelTask", "SubscribeToTask"].includes(operation ?? "");

	if (requestBody) {
		const { value, parsed } = parseBody(requestBody);
		const isPushRelay = operation === "push_notification" || operation === "push_delivery";
		const requestParams = asRecord(asRecord(value)?.params) ?? asRecord(value);
		const requestContent = parsed ? (
			isPushConfigOperation && requestParams ? (
				<OperationParametersView params={{ ...requestParams, id: undefined, configurationId: requestParams.id }} />
			) : isPushRelay ? (
				<PushRelaySummary operation={operation} status={status} value={value} />
			) : (
				<PayloadBody value={value} />
			)
		) : (
			<JsonBlock code={requestBody} />
		);
		sections.push(
			compactSections ? (
				<Disclosure key="request" summary="Request parameters">
					{requestContent}
				</Disclosure>
			) : (
				<div key="request">
					{showSectionTitles && <BlockHeader title={payloadSectionTitle(operation, "request")} />}
					<div className={showSectionTitles ? "mt-3" : undefined}>{requestContent}</div>
				</div>
			),
		);
	}
	if (responseBody) {
		const { value, parsed } = parseBody(responseBody);
		const response = asRecord(value);
		const hasOwnHeader =
			isPushConfigOperation ||
			(parsed && response !== null && (asArray(response.tasks) !== null || looksLikeTask(response) || asRecord(response.task) !== null));
		sections.push(
			<div key="response">
				{showSectionTitles && !hasOwnHeader && <BlockHeader title={payloadSectionTitle(operation, "response")} />}
				<div className={showSectionTitles && !hasOwnHeader ? "mt-3" : undefined}>
					{operation === "DeleteTaskPushNotificationConfig" && parsed && response !== null && Object.keys(response).length === 0 ? (
						<div className="bg-muted/20 rounded-sm border px-3 py-3 text-sm">Push configuration deleted</div>
					) : parsed ? (
						<PayloadBody value={value} />
					) : (
						<JsonBlock code={responseBody} />
					)}
				</div>
			</div>,
		);
	}
	if (eventBody) {
		const { value, parsed } = parseBody(eventBody);
		sections.push(<div key="event">{parsed ? <PayloadBody value={value} /> : <JsonBlock code={eventBody} />}</div>);
	}
	if (errorDetails !== undefined && errorDetails !== null) {
		sections.push(
			<div key="error" className="space-y-3">
				{showSectionTitles && <BlockHeader title="Error Details" />}
				{errorRecord ? <ErrorView error={errorRecord} /> : <JsonBlock code={prettyPrint(errorDetails)} />}
			</div>,
		);
	}

	if (sections.length === 0) {
		return (
			<div className="text-muted-foreground rounded-sm border border-dashed p-5 text-center text-sm">No protocol body was recorded.</div>
		);
	}

	return (
		<TaskFilterContext.Provider value={onFilter}>
			<div className={compactSections ? "space-y-3" : "space-y-5"}>
				{sections.map((section, index) => (
					<div key={index} className="space-y-5">
						{index > 0 && !compactSections && <DottedSeparator />}
						{section}
					</div>
				))}
			</div>
		</TaskFilterContext.Provider>
	);
}