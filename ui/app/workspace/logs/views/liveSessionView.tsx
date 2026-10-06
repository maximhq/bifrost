import { Clipboard, Mic } from "lucide-react";

import { formatCost } from "@/app/workspace/dashboard/utils/chartUtils";
import { Badge } from "@/components/ui/badge";
import { useCopyToClipboard } from "@/hooks/useCopyToClipboard";
import { LiveDelegationLog, LiveSessionLog, LiveTranscriptLine, ResponsesMessage } from "@/lib/types/logs";
import { cn } from "@/lib/utils";
import { formatCompactNumber } from "@/lib/utils/numbers";

import { ResponsesItemRow, extractReasoningParts } from "./responsesItemRow";

interface LiveSessionViewProps {
	session: LiveSessionLog;
}

// formatClock renders a point on the session timeline as m:ss.
function formatClock(ms?: number): string {
	if (ms === undefined) return "";
	const total = Math.floor(ms / 1000);
	return `${Math.floor(total / 60)}:${String(total % 60).padStart(2, "0")}`;
}

function formatVoiceTime(seconds: number): string {
	if (seconds >= 60) return `${Math.floor(seconds / 60)}m ${Math.round(seconds % 60)}s`;
	return `${Math.round(seconds)}s`;
}

function Stat({ label, value }: { label: string; value: string }) {
	return (
		<div className="min-w-0 px-6 py-3">
			<div className="text-muted-foreground text-[10.5px] font-semibold tracking-wider uppercase">{label}</div>
			<div className="mt-0.5 text-[16px] font-semibold tabular-nums">{value}</div>
		</div>
	);
}

function TranscriptLine({ line, last }: { line: LiveTranscriptLine; last: boolean }) {
	const assistant = line.role === "assistant";
	const range = line.start_ms !== undefined ? `${formatClock(line.start_ms)}–${formatClock(line.end_ms)}` : "";
	return (
		<div className="flex gap-3">
			<div className="flex flex-col items-center pt-1.5">
				<span className={cn("h-2 w-2 rounded-sm", assistant ? "bg-zinc-900 dark:bg-zinc-100" : "bg-blue-500")} />
				{!last && <div className="bg-border my-1 w-px flex-1" />}
			</div>
			<div className="min-w-0 flex-1 pb-3">
				<div className="mb-1 flex items-center gap-2">
					<span className="text-foreground text-[11.5px] font-semibold">{assistant ? "Assistant" : "User"}</span>
					{range ? <span className="text-muted-foreground font-mono text-[11px]">{range}</span> : null}
				</div>
				<div
					className={cn(
						"rounded-sm border p-3 text-[13px] leading-relaxed whitespace-pre-wrap",
						assistant
							? "bg-white border-zinc-200 dark:bg-zinc-900 dark:border-zinc-800"
							: "bg-blue-50/60 border-blue-200 dark:bg-blue-950/30 dark:border-blue-900",
					)}
				>
					{line.text}
				</div>
			</div>
		</div>
	);
}

function CopyChip({ text }: { text: string }) {
	const { copy } = useCopyToClipboard({ successMessage: "Copied" });
	return (
		<button
			type="button"
			onClick={() => copy(text)}
			className="text-muted-foreground hover:bg-muted hover:text-foreground inline-flex items-center gap-1 rounded-sm px-1.5 py-0.5 font-mono text-[11px] transition"
			aria-label="Copy response id"
			data-testid="logdetails-live-delegation-copy-response-id"
		>
			{text}
			<Clipboard className="h-3 w-3" />
		</button>
	);
}

// A reasoning item without a summary or text has nothing to show; on a delegation it is noise.
function hasVisibleContent(item: ResponsesMessage): boolean {
	if (item.type !== "reasoning") return true;
	const parts = extractReasoningParts(item);
	return parts.summaries.length > 0 || !!parts.contentText;
}

// DelegationCard is one task the backend ran for the session: when, on which model, what it
// cost, and its items on the same timeline the Responses view draws.
function DelegationCard({ delegation }: { delegation: LiveDelegationLog }) {
	const usage = delegation.usage;
	const tokens = usage ? `${formatCompactNumber(usage.prompt_tokens)} in / ${formatCompactNumber(usage.completion_tokens)} out` : "";
	const cost = delegation.cost !== undefined ? formatCost(delegation.cost) : "";
	const items = (delegation.output ?? []).filter(hasVisibleContent);
	return (
		<div className="space-y-3 px-6 py-4" data-testid="logdetails-live-delegation">
			<div className="flex flex-wrap items-center gap-2">
				{delegation.started_ms !== undefined ? (
					<span className="text-muted-foreground font-mono text-[11px] tabular-nums">{formatClock(delegation.started_ms)}</span>
				) : null}
				<span className="font-mono text-xs font-medium">{delegation.model}</span>
				<span className="text-muted-foreground ml-auto font-mono text-[11px] tabular-nums">
					{[tokens, cost].filter(Boolean).join(" · ")}
				</span>
			</div>
			{delegation.error ? (
				<div className="rounded-sm border border-red-200 bg-red-50/60 p-3 text-xs text-red-700 dark:border-red-900 dark:bg-red-950/30 dark:text-red-400">
					{delegation.error}
				</div>
			) : null}
			{items.length > 0 ? (
				<div className="bg-card rounded-sm border p-5">
					{items.map((item, index) => (
						<ResponsesItemRow key={item.id ?? index} msg={item} last={index === items.length - 1} />
					))}
				</div>
			) : !delegation.error ? (
				<div className="text-muted-foreground text-[12px] italic">No answer</div>
			) : null}
			{delegation.response_ids?.length ? (
				<div className="flex flex-wrap gap-1">
					{delegation.response_ids.map((id) => (
						<CopyChip key={id} text={id} />
					))}
				</div>
			) : null}
		</div>
	);
}

// LiveSessionView is the detail panel of a GPT Live session: how it ran, the conversation on
// its timeline, and every call the backend made on its behalf.
export default function LiveSessionView({ session }: LiveSessionViewProps) {
	const transcript = session.transcript ?? [];
	const delegations = session.delegations ?? [];
	const voiceCost = session.voice_cost ?? 0;
	const backendCost = session.backend_cost ?? 0;

	return (
		<div className="space-y-4" data-testid="logdetails-live-session">
			<div className="w-full rounded-sm border">
				<div className="flex items-center gap-2 border-b px-6 py-2 text-sm font-medium">
					<Mic className="h-4 w-4" />
					Live Session
					{session.transport ? (
						<Badge variant="outline" className="ml-1 font-mono text-[10px] uppercase">
							{session.transport}
						</Badge>
					) : null}
				</div>
				<div className="divide-border/70 grid grid-cols-2 md:grid-cols-4 md:divide-x">
					<Stat label="Voice time" value={formatVoiceTime(session.voice_seconds)} />
					<Stat label="Voice cost" value={formatCost(voiceCost)} />
					<Stat label="Backend cost" value={formatCost(backendCost)} />
					<Stat label="Delegations" value={String(delegations.length)} />
				</div>
			</div>

			{transcript.length > 0 && (
				<div className="w-full rounded-sm border">
					<div className="border-b px-6 py-2 text-sm font-medium">Transcript</div>
					<div className="p-6">
						{transcript.map((line, index) => (
							<TranscriptLine key={`${line.start_ms ?? index}-${index}`} line={line} last={index === transcript.length - 1} />
						))}
					</div>
				</div>
			)}

			{delegations.length > 0 && (
				<div className="w-full rounded-sm border">
					<div className="border-b px-6 py-2 text-sm font-medium">Delegations</div>
					<div className="divide-y">
						{delegations.map((delegation) => (
							<DelegationCard key={delegation.request_id} delegation={delegation} />
						))}
					</div>
				</div>
			)}
		</div>
	);
}