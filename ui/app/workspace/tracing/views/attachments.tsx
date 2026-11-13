import { useGetTraceUploadUrlQuery } from "@/lib/store";
import { Download, ExternalLink, FileText, Loader2, Pause, Play, SkipBack, SkipForward } from "lucide-react";
import { useEffect, useRef, useState } from "react";
import { cn } from "@/lib/utils";
import { useTranslation } from "react-i18next";
import type { AudioTrack, SpanAttachment } from "../traceTree";

/** Shows one attachment where it can be shown (image, audio, video) and offers it as a download. */
function AttachmentCard({ attachment, compact = false }: { attachment: SpanAttachment; compact?: boolean }) {
	const { t } = useTranslation("observability");
	const upload = useGetTraceUploadUrlQuery(attachment.uploadId ?? "", { skip: attachment.kind !== "upload" });
	const src = attachment.kind === "upload" ? upload.data : attachment.src;
	const media = attachment.contentType.split("/")[0];
	const missing = attachment.kind === "upload" && (upload.isError || (!upload.isFetching && !upload.data?.startsWith("blob:")));

	let preview: React.ReactNode;
	if (attachment.kind === "upload" && upload.isFetching) {
		preview = <Loader2 className="text-muted-foreground size-4 animate-spin" />;
	} else if (missing || !src) {
		preview = <span className="text-muted-foreground px-3 text-center text-xs">{t("tracing.attachments.missing")}</span>;
	} else if (media === "image") {
		preview = <img src={src} alt={attachment.name} className="max-h-full max-w-full object-contain" />;
	} else if (media === "audio") {
		preview = <audio controls src={src} className="w-full px-2" />;
	} else if (media === "video") {
		preview = <video controls src={src} className="max-h-full max-w-full" />;
	} else {
		preview = <FileText className="text-muted-foreground size-8" />;
	}

	return (
		<div
			className="flex min-w-0 flex-col overflow-hidden rounded-md border"
			data-testid={`trace-attachment-${attachment.uploadId ?? attachment.name}`}
		>
			<div className={cn("bg-muted/40 flex items-center justify-center overflow-hidden", compact ? "h-24" : "h-36")}>{preview}</div>
			<div className="flex items-center gap-2 border-t px-2.5 py-2">
				<div className="min-w-0 flex-1">
					<div className="truncate text-xs font-medium" title={attachment.name}>
						{attachment.name}
					</div>
					<div className="text-muted-foreground truncate text-[11px]">
						{attachment.contentType || t("tracing.attachments.unknownType")} · {t(`tracing.attachments.kind.${attachment.kind}`)}
					</div>
				</div>
				{src && !missing && (
					<a
						href={src}
						{...(attachment.kind === "url" ? { target: "_blank", rel: "noreferrer" } : { download: attachment.name })}
						className="text-muted-foreground hover:text-foreground hover:bg-muted flex size-7 shrink-0 items-center justify-center rounded-md"
						aria-label={attachment.kind === "url" ? t("tracing.attachments.open") : t("tracing.attachments.download")}
						data-testid={`trace-attachment-action-${attachment.uploadId ?? attachment.name}`}
					>
						{attachment.kind === "url" ? <ExternalLink className="size-3.5" /> : <Download className="size-3.5" />}
					</a>
				)}
			</div>
		</div>
	);
}

/** The files a span refers to, as a grid of previews. */
export function AttachmentList({ attachments, compact = false }: { attachments: SpanAttachment[]; compact?: boolean }) {
	const { t } = useTranslation("observability");
	if (attachments.length === 0) return null;
	if (compact) {
		// Under a thread message: small cards, no heading.
		return (
			<div className="mt-1.5 grid max-w-xl grid-cols-2 gap-2 sm:grid-cols-3" data-testid="trace-thread-attachments">
				{attachments.map((attachment) => (
					<AttachmentCard key={attachment.uploadId ?? attachment.src ?? attachment.name} attachment={attachment} compact />
				))}
			</div>
		);
	}
	return (
		<section className="space-y-2" data-testid="trace-span-attachments">
			<h4 className="text-muted-foreground text-xs font-medium tracking-wide uppercase">
				{t("tracing.attachments.heading", { count: attachments.length })}
			</h4>
			<ConversationPlayer
				tracks={attachments
					.filter((attachment) => attachment.contentType.startsWith("audio/"))
					.map((attachment, i) => ({ attachment, messageIndex: i, role: attachment.field === "output" ? "assistant" : "user" }))}
			/>
			<div className="grid grid-cols-1 gap-3 sm:grid-cols-2 xl:grid-cols-3">
				{attachments.map((attachment) => (
					<AttachmentCard key={attachment.uploadId ?? attachment.src ?? attachment.name} attachment={attachment} />
				))}
			</div>
		</section>
	);
}
/** The playable URL of one attachment: its data or outside URL, or the uploaded file fetched with auth. */
function useAttachmentSrc(attachment: SpanAttachment | undefined): string | undefined {
	const upload = useGetTraceUploadUrlQuery(attachment?.uploadId ?? "", { skip: attachment?.kind !== "upload" });
	if (!attachment) return undefined;
	if (attachment.kind !== "upload") return attachment.src;
	return upload.data?.startsWith("blob:") ? upload.data : undefined;
}

/**
 * Plays every audio part of a conversation one after another, as the call sounded: user, assistant,
 * user, and so on. Only the current track is fetched. onTrackChange reports which track is playing,
 * so the view can mark its message; -1 when playback stops.
 */
export function ConversationPlayer({ tracks, onTrackChange }: { tracks: AudioTrack[]; onTrackChange?: (index: number) => void }) {
	const { t } = useTranslation("observability");
	const audio = useRef<HTMLAudioElement>(null);
	const [index, setIndex] = useState(0);
	const [playing, setPlaying] = useState(false);
	const [progress, setProgress] = useState(0);
	const current = tracks[Math.min(index, tracks.length - 1)];
	const src = useAttachmentSrc(current?.attachment);

	// Each track starts from the beginning while the conversation is playing. Keyed on the track, not
	// only the source: two turns can carry the same clip, and an unchanged src would not restart.
	useEffect(() => {
		const el = audio.current;
		if (!playing || !src || !el) return;
		el.currentTime = 0;
		void el.play().catch(() => setPlaying(false));
	}, [src, playing, index]);
	useEffect(() => {
		onTrackChange?.(playing ? index : -1);
	}, [index, playing, onTrackChange]);

	if (tracks.length < 2) return null;
	const go = (next: number) => {
		setProgress(0);
		setIndex(Math.max(0, Math.min(next, tracks.length - 1)));
	};
	const toggle = () => {
		if (playing) {
			audio.current?.pause();
			setPlaying(false);
			return;
		}
		setPlaying(true);
		void audio.current?.play().catch(() => undefined);
	};
	return (
		<div className="bg-muted/40 flex items-center gap-2 rounded-md border px-2 py-1.5" data-testid="trace-conversation-player">
			<audio
				ref={audio}
				src={src}
				preload="auto"
				onTimeUpdate={(event) => {
					const el = event.currentTarget;
					setProgress(el.duration > 0 ? el.currentTime / el.duration : 0);
				}}
				onEnded={() => {
					if (index + 1 < tracks.length) go(index + 1);
					else {
						setPlaying(false);
						go(0);
					}
				}}
			/>
			<button
				type="button"
				onClick={() => go(index - 1)}
				disabled={index === 0}
				className="text-muted-foreground hover:text-foreground flex size-7 items-center justify-center rounded-md disabled:opacity-40"
				aria-label={t("tracing.player.previous")}
				data-testid="trace-conversation-player-previous"
			>
				<SkipBack className="size-3.5" />
			</button>
			<button
				type="button"
				onClick={toggle}
				className="bg-primary text-primary-foreground flex h-7 items-center gap-1.5 rounded-md px-2.5 text-xs font-medium"
				data-testid="trace-conversation-player-toggle"
			>
				{playing ? <Pause className="size-3.5" /> : <Play className="size-3.5" />}
				{playing ? t("tracing.player.pause") : t("tracing.player.play")}
			</button>
			<button
				type="button"
				onClick={() => go(index + 1)}
				disabled={index >= tracks.length - 1}
				className="text-muted-foreground hover:text-foreground flex size-7 items-center justify-center rounded-md disabled:opacity-40"
				aria-label={t("tracing.player.next")}
				data-testid="trace-conversation-player-next"
			>
				<SkipForward className="size-3.5" />
			</button>
			<span className="text-muted-foreground shrink-0 text-xs tabular-nums">
				{t("tracing.player.position", { current: index + 1, total: tracks.length })} · <span className="capitalize">{current?.role}</span>
			</span>
			{/* One segment per track: played ones full, the current one by its progress. */}
			<div className="flex min-w-0 flex-1 gap-0.5" aria-hidden="true">
				{tracks.map((track, i) => (
					<button
						key={`${track.messageIndex}-${i}`}
						type="button"
						tabIndex={-1}
						onClick={() => go(i)}
						className="bg-border h-1.5 flex-1 overflow-hidden rounded-md"
					>
						<span className="bg-primary block h-full" style={{ width: `${(i < index ? 1 : i === index ? progress : 0) * 100}%` }} />
					</button>
				))}
			</div>
		</div>
	);
}