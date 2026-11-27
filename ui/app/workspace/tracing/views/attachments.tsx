import { useGetTraceUploadUrlQuery } from "@/lib/store";
import { Download, ExternalLink, FileText, Loader2 } from "lucide-react";
import { cn } from "@/lib/utils";
import { useTranslation } from "react-i18next";
import type { SpanAttachment } from "../traceTree";

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
			className="flex min-w-0 flex-col overflow-hidden rounded-sm border"
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
						className="text-muted-foreground hover:text-foreground hover:bg-muted flex size-7 shrink-0 items-center justify-center rounded-sm"
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
			<div className="grid grid-cols-1 gap-3 sm:grid-cols-2 xl:grid-cols-3">
				{attachments.map((attachment) => (
					<AttachmentCard key={attachment.uploadId ?? attachment.src ?? attachment.name} attachment={attachment} />
				))}
			</div>
		</section>
	);
}