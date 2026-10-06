import BlockHeader from "@/app/workspace/logs/views/blockHeader";
import { Badge } from "@/components/ui/badge";
import { CodeEditor } from "@/components/ui/codeEditor";
import { DottedSeparator } from "@/components/ui/separator";
import { StatusColors } from "@/lib/constants/logs";
import { cn } from "@/lib/utils";
import { format, isValid } from "date-fns";
import { AlertCircle, ChevronDown, Download, ExternalLink, FileText, Webhook } from "lucide-react";
import { useState, type ReactNode } from "react";

/**
 * Structured renderer for A2A payload bodies.
 *
 * The bodies are A2A protocol v1 ProtoJSON objects, but the shapes are only a
 * best-effort contract: agents can emit anything. Every renderer here therefore
 * recognizes what it can and routes the rest to a pretty-printed JSON block, so
 * nothing in a payload is ever silently dropped.
 */

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

function FilePartView({ source, mediaType, filename }: { source: string; mediaType?: string; filename?: string }) {
	const safeSource = safeMediaSource(source);
	const type = (mediaType ?? "application/octet-stream").toLowerCase();
	const label = filename ?? "File";
	const inlineSafe = type !== "image/svg+xml" && type !== "text/html" && type !== "application/xhtml+xml";

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

export function A2APartsView({ parts }: { parts: unknown[] }) {
	if (parts.length === 0) return <div className="text-muted-foreground text-[12.5px]">No content.</div>;
	return (
		<div className="space-y-2">
			{parts.map((part, index) => (
				<div key={index}>
					<PartView part={part} />
				</div>
			))}
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
			<div className="min-w-0 flex-1 pb-4">
				<div className="mb-1 flex items-center gap-2">
					<span className="text-foreground text-[11.5px] font-semibold capitalize">{messageRoleLabel(role)}</span>
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

function MessageList({ messages }: { messages: unknown[] }) {
	return (
		<div>
			{messages.map((entry, index) => {
				const record = asRecord(entry);
				const last = index === messages.length - 1;
				if (!record) {
					return <JsonBlock key={index} code={prettyPrint(entry)} maxHeight={200} />;
				}
				return <MessageRow key={index} message={record} last={last} />;
			})}
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
				<A2APartsView parts={asArray(record.parts) ?? []} />
				<UnclaimedFields source={record} claimed={ARTIFACT_CLAIMED} />
			</div>
		</div>
	);
}

// ---------------------------------------------------------------------------
// Push notification config
// ---------------------------------------------------------------------------

function PushConfigView({ config }: { config: Record<string, unknown> }) {
	const url = asString(config.url);
	const id = asString(config.id);
	const taskId = asString(config.taskId);
	const token = asString(config.token);
	const authentication = asRecord(config.authentication);
	const schemes = asArray(authentication?.schemes)?.filter((value): value is string => typeof value === "string") ?? [];
	const credentials = asString(authentication?.credentials);
	return (
		<div className="space-y-4">
			<div className="flex items-start gap-3">
				<div className="bg-muted flex size-8 shrink-0 items-center justify-center rounded-sm">
					<Webhook className="text-muted-foreground size-4" />
				</div>
				<div className="min-w-0 flex-1">
					<div className="text-sm font-medium">Notification destination</div>
					<div className="text-muted-foreground mt-1 font-mono text-xs break-all">{url ?? "No callback URL provided"}</div>
				</div>
			</div>
			{schemes.length > 0 && (
				<div>
					<SubLabel>Authentication</SubLabel>
					<div className="mt-2 flex flex-wrap items-center gap-1.5">
						{schemes.map((scheme) => (
							<Badge key={scheme} variant="outline" className="rounded-sm font-normal">
								{scheme}
							</Badge>
						))}
						{credentials && <span className="text-muted-foreground text-xs">Credentials provided</span>}
					</div>
				</div>
			)}
			{(id || taskId || token || authentication) && (
				<Disclosure summary="Configuration details">
					<OperationParametersView
						params={{
							...(id ? { configurationId: id } : {}),
							...(taskId ? { taskId } : {}),
							...(token ? { token } : {}),
						}}
					/>
					{authentication && <UnclaimedFields source={authentication} claimed={["schemes", "credentials"]} />}
					<UnclaimedFields source={config} claimed={["url", "id", "taskId", "token", "authentication"]} />
				</Disclosure>
			)}
		</div>
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

function TaskArtifactView({ artifact }: { artifact: unknown }) {
	const record = asRecord(artifact);
	if (!record) return <JsonBlock code={prettyPrint(artifact)} maxHeight={200} />;

	const name = asString(record.name);
	const artifactId = asString(record.artifactId);
	const description = asString(record.description);

	return (
		<div className="space-y-2 py-2 first:pt-0 last:pb-0">
			<div className="flex flex-wrap items-baseline justify-between gap-x-3 gap-y-1">
				<span className="text-[12.5px] font-medium">{name ?? "Output"}</span>
				{artifactId && <span className="text-muted-foreground font-mono text-[10.5px] break-all">{artifactId}</span>}
			</div>
			{description && <div className="text-muted-foreground text-[12.5px] leading-relaxed">{description}</div>}
			<A2APartsView parts={asArray(record.parts) ?? []} />
			<UnclaimedFields source={record} claimed={ARTIFACT_CLAIMED} />
		</div>
	);
}

function TaskView({ task, showIdentity = true }: { task: Record<string, unknown>; showIdentity?: boolean }) {
	const status = asRecord(task.status);
	const state = asString(status?.state);
	const timestamp = formatTimestamp(status?.timestamp);
	const statusMessage = asRecord(status?.message);
	const history = asArray(task.history) ?? [];
	const artifacts = asArray(task.artifacts) ?? [];
	const taskId = asString(task.id);
	const contextId = asString(task.contextId);

	return (
		<div className="space-y-4">
			{showIdentity && (taskId || contextId || state || timestamp) && (
				<div className="space-y-2">
					<div className="flex flex-wrap items-center gap-2">
						{state ? <TaskStateBadge state={state} /> : null}
						{timestamp && <span className="text-muted-foreground text-[11px] tabular-nums">{timestamp}</span>}
					</div>
					{(taskId || contextId) && (
						<dl className="text-muted-foreground grid gap-x-4 gap-y-1 text-[11px] sm:grid-cols-2">
							{taskId && (
								<div className="min-w-0">
									<dt className="inline font-medium">Task </dt>
									<dd className="inline font-mono break-all">{taskId}</dd>
								</div>
							)}
							{contextId && (
								<div className="min-w-0">
									<dt className="inline font-medium">Context </dt>
									<dd className="inline font-mono break-all">{contextId}</dd>
								</div>
							)}
						</dl>
					)}
				</div>
			)}

			{statusMessage && (
				<section className="space-y-2">
					<SubLabel>Status message</SubLabel>
					<A2APartsView parts={asArray(statusMessage.parts) ?? []} />
					<UnclaimedFields source={statusMessage} claimed={MESSAGE_CLAIMED} />
				</section>
			)}

			{artifacts.length > 0 && (
				<section className="space-y-2">
					<SubLabel>Outputs ({artifacts.length})</SubLabel>
					<div className="divide-y">
						{artifacts.map((artifact, index) => (
							<TaskArtifactView key={asString(asRecord(artifact)?.artifactId) ?? index} artifact={artifact} />
						))}
					</div>
				</section>
			)}

			{history.length > 0 && (
				<Disclosure summary="Conversation history" meta={`${history.length} ${history.length === 1 ? "message" : "messages"}`}>
					<MessageList messages={history} />
				</Disclosure>
			)}

			{status && <UnclaimedFields source={status} claimed={["state", "timestamp", "message"]} />}
			<UnclaimedFields source={task} claimed={TASK_CLAIMED} />
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
	const name = asString(card.name) ?? "Agent card";
	const description = asString(card.description);
	const version = asString(card.version);
	const provider = asRecord(card.provider);
	const providerName = asString(provider?.organization);
	const providerURL = asString(provider?.url);
	const interfaces = asArray(card.supportedInterfaces) ?? [];
	const skills = asArray(card.skills) ?? [];
	const inputModes = asArray(card.defaultInputModes)?.filter((value): value is string => typeof value === "string") ?? [];
	const outputModes = asArray(card.defaultOutputModes)?.filter((value): value is string => typeof value === "string") ?? [];
	const capabilities = asRecord(card.capabilities);
	const enabledCapabilities = capabilities
		? Object.entries(capabilities)
				.filter(([, value]) => value === true)
				.map(([key]) => labelFromKey(key))
		: [];

	return (
		<div className="bg-card overflow-hidden rounded-sm border shadow-sm">
			<div className="border-b px-4 py-4">
				<div className="space-y-1">
					<div className="flex flex-wrap items-center gap-2">
						<span className="text-sm font-semibold">{name}</span>
						{version && (
							<Badge variant="outline" className="rounded-sm font-mono text-[10.5px]">
								v{version}
							</Badge>
						)}
					</div>
					{description && <div className="text-muted-foreground text-[12.5px] leading-relaxed">{description}</div>}
					{(providerName || providerURL) && (
						<div className="text-muted-foreground flex flex-wrap items-center gap-1.5 text-[11.5px]">
							<span>{providerName ?? "Provider"}</span>
							{providerURL && (
								<a
									href={providerURL}
									target="_blank"
									rel="noreferrer"
									className="hover:text-foreground inline-flex items-center gap-1 break-all hover:underline"
								>
									{providerURL}
									<ExternalLink className="h-3 w-3 shrink-0" />
								</a>
							)}
						</div>
					)}
				</div>
			</div>

			<div className="space-y-4 p-4">
				{(enabledCapabilities.length > 0 || inputModes.length > 0 || outputModes.length > 0) && (
					<div className="flex flex-wrap gap-1.5">
						{enabledCapabilities.map((capability) => (
							<Badge key={capability} variant="secondary" className="rounded-sm">
								{capability}
							</Badge>
						))}
						{inputModes.map((mode) => (
							<Badge key={`in-${mode}`} variant="outline" className="rounded-sm">
								in: {mode}
							</Badge>
						))}
						{outputModes.map((mode) => (
							<Badge key={`out-${mode}`} variant="outline" className="rounded-sm">
								out: {mode}
							</Badge>
						))}
					</div>
				)}

				{interfaces.length > 0 && (
					<Disclosure summary="Interfaces" meta={`${interfaces.length}`}>
						<div className="space-y-2">
							{interfaces.map((entry, index) => {
								const iface = asRecord(entry);
								if (!iface) return <JsonBlock key={index} code={prettyPrint(entry)} maxHeight={160} />;
								return (
									<div key={index} className="rounded-sm border px-3 py-2">
										<div className="flex flex-wrap items-center gap-2 text-[12px]">
											<span className="font-medium">{asString(iface.protocolBinding) ?? "Interface"}</span>
											{asString(iface.protocolVersion) && <span className="text-muted-foreground">v{asString(iface.protocolVersion)}</span>}
										</div>
										{asString(iface.url) && (
											<div className="text-muted-foreground mt-1 font-mono text-[11px] break-all">{asString(iface.url)}</div>
										)}
									</div>
								);
							})}
						</div>
					</Disclosure>
				)}

				{skills.length > 0 && (
					<Disclosure summary="Skills" meta={`${skills.length}`}>
						<div className="space-y-2">
							{skills.map((entry, index) => {
								const skill = asRecord(entry);
								if (!skill) return <JsonBlock key={index} code={prettyPrint(entry)} maxHeight={160} />;
								const tags = asArray(skill.tags)?.filter((value): value is string => typeof value === "string") ?? [];
								return (
									<div key={index} className="space-y-1.5 rounded-sm border px-3 py-2">
										<div className="text-[12.5px] font-medium">{asString(skill.name) ?? asString(skill.id) ?? `Skill ${index + 1}`}</div>
										{asString(skill.description) && (
											<div className="text-muted-foreground text-[11.5px] leading-relaxed">{asString(skill.description)}</div>
										)}
										{tags.length > 0 && <div className="text-muted-foreground text-[10.5px] break-words">{tags.join(" · ")}</div>}
									</div>
								);
							})}
						</div>
					</Disclosure>
				)}

				<UnclaimedFields
					source={card}
					claimed={[
						"name",
						"description",
						"version",
						"provider",
						"supportedInterfaces",
						"skills",
						"defaultInputModes",
						"defaultOutputModes",
						"capabilities",
					]}
				/>
			</div>
		</div>
	);
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
				const label = key === "id" || key === "taskId" ? "Task ID" : key === "contextId" ? "Context ID" : labelFromKey(key);
				return (
					<div key={key} className="grid gap-1 py-2.5 first:pt-0 last:pb-0 sm:grid-cols-[140px_minmax(0,1fr)] sm:gap-4">
						<dt className="text-muted-foreground text-[11px] font-medium uppercase">{label}</dt>
						<dd className="text-[12px] break-all">
							<ParameterValue value={value} />
						</dd>
					</div>
				);
			})}
		</dl>
	);
}

function TaskCollectionView({ response }: { response: Record<string, unknown> }) {
	const tasks = asArray(response.tasks) ?? [];
	const totalSize = typeof response.totalSize === "number" ? response.totalSize : tasks.length;
	const pageSize = typeof response.pageSize === "number" ? response.pageSize : undefined;
	const nextPageToken = asString(response.nextPageToken);

	return (
		<div className="space-y-4">
			<div>
				<div className="text-sm font-medium">{totalSize} tasks returned</div>
				{(pageSize !== undefined || nextPageToken) && (
					<div className="text-muted-foreground mt-1 text-xs">
						{pageSize !== undefined ? `${pageSize} on this page` : null}
						{nextPageToken ? `${pageSize !== undefined ? " · " : ""}More results available` : null}
					</div>
				)}
			</div>
			{tasks.length > 0 ? (
				<div className="divide-y border-y">
					{tasks.map((entry, index) => {
						const task = asRecord(entry);
						if (!task) return <JsonBlock key={index} code={prettyPrint(entry)} maxHeight={240} />;
						const state = asString(asRecord(task.status)?.state);
						const taskId = asString(task.id) ?? `Task ${index + 1}`;
						const artifacts = asArray(task.artifacts)?.length ?? 0;
						const history = asArray(task.history)?.length ?? 0;
						const title = taskTitle(task, index);
						return (
							<details key={taskId} className="group">
								<summary className="hover:bg-muted/30 flex cursor-pointer list-none items-start gap-3 px-1 py-3">
									{state && <TaskStateBadge state={state} />}
									<div className="min-w-0 flex-1">
										<div className="truncate text-sm font-medium">{title}</div>
										<div className="text-muted-foreground mt-1 flex flex-wrap items-center gap-x-2 gap-y-0.5 text-[11px]">
											<span className="font-mono">{taskId}</span>
											<span>
												{artifacts} {artifacts === 1 ? "output" : "outputs"} · {history} {history === 1 ? "message" : "messages"}
											</span>
										</div>
									</div>
									<ChevronDown className="text-muted-foreground size-4 transition-transform group-open:rotate-180" />
								</summary>
								<div className="pb-4 pl-1">
									<TaskView task={task} showIdentity={false} />
								</div>
							</details>
						);
					})}
				</div>
			) : (
				<div className="text-muted-foreground py-6 text-center text-sm">No tasks returned.</div>
			)}
			{nextPageToken && (
				<Disclosure summary="Pagination details">
					<OperationParametersView params={{ nextPageToken }} />
				</Disclosure>
			)}
			<UnclaimedFields source={response} claimed={["tasks", "totalSize", "pageSize", "nextPageToken"]} />
		</div>
	);
}

function PushConfigCollectionView({ response }: { response: Record<string, unknown> }) {
	const configs = asArray(response.configs) ?? [];
	return (
		<div className="space-y-4">
			<div>
				<div className="text-sm font-medium">
					{configs.length} notification {configs.length === 1 ? "destination" : "destinations"}
				</div>
				<div className="text-muted-foreground mt-1 text-xs">Callbacks configured for this task.</div>
			</div>
			{configs.length > 0 ? (
				<div className="divide-y border-y">
					{configs.map((config, index) => {
						const record = asRecord(config);
						return record ? (
							<div key={asString(record.id) ?? index} className="py-4">
								<PushConfigView config={record} />
							</div>
						) : (
							<JsonBlock key={index} code={prettyPrint(config)} />
						);
					})}
				</div>
			) : (
				<div className="text-muted-foreground py-6 text-center text-sm">No notification destinations configured.</div>
			)}
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

	if (requestBody) {
		const { value, parsed } = parseBody(requestBody);
		const isPushRelay = operation === "push_notification" || operation === "push_delivery";
		sections.push(
			<div key="request">
				{showSectionTitles && <BlockHeader title={payloadSectionTitle(operation, "request")} />}
				<div className={showSectionTitles ? "mt-3" : undefined}>
					{parsed ? (
						isPushRelay ? (
							<PushRelaySummary operation={operation} status={status} value={value} />
						) : (
							<PayloadBody value={value} />
						)
					) : (
						<JsonBlock code={requestBody} />
					)}
				</div>
			</div>,
		);
	}
	if (responseBody) {
		const { value, parsed } = parseBody(responseBody);
		sections.push(
			<div key="response">
				{showSectionTitles && <BlockHeader title={payloadSectionTitle(operation, "response")} />}
				<div className={showSectionTitles ? "mt-3" : undefined}>
					{parsed ? <PayloadBody value={value} /> : <JsonBlock code={responseBody} />}
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
		<div className="space-y-5">
			{sections.map((section, index) => (
				<div key={index} className="space-y-5">
					{index > 0 && <DottedSeparator />}
					{section}
				</div>
			))}
		</div>
	);
}