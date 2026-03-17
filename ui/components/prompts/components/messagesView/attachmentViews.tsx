import { MessageContent } from "@/lib/message";
import { Mic, FileIcon, XIcon } from "lucide-react";

/**
 * Renders a compact badge for a single attachment with an inline remove control.
 *
 * Displays different visuals based on the attachment type:
 * - image_url with a valid URL: a thumbnail and the label "Image"
 * - input_audio: a microphone icon and the audio format in uppercase or "Audio"
 * - other: a file icon and the filename or "File"
 *
 * @param attachment - The attachment to display; its `type` determines the badge content.
 * @param onRemove - Callback invoked when the badge's remove button is clicked.
 * @returns The rendered attachment badge element.
 */
export function AttachmentBadge({ attachment, onRemove }: { attachment: MessageContent; onRemove: () => void }) {
	const isImage = attachment.type === "image_url";
	const isAudio = attachment.type === "input_audio";

	return (
		<div className="group/att bg-muted/50 relative flex items-center gap-1.5 rounded-sm border px-2 py-1 text-xs">
			{isImage && attachment.image_url?.url ? (
				<>
					<img src={attachment.image_url.url} alt="attachment" className="h-8 w-8 rounded object-cover" />
					<span className="text-muted-foreground max-w-[100px] truncate">Image</span>
				</>
			) : isAudio ? (
				<>
					<Mic className="text-muted-foreground size-3" />
					<span className="text-muted-foreground max-w-[100px] truncate">{attachment.input_audio?.format?.toUpperCase() || "Audio"}</span>
				</>
			) : (
				<>
					<FileIcon className="text-muted-foreground size-3" />
					<span className="text-muted-foreground max-w-[120px] truncate">{attachment.file?.filename || "File"}</span>
				</>
			)}
			<button
				onClick={onRemove}
				className="text-muted-foreground hover:bg-card hover:text-destructive ml-0.5 rounded-full p-0.5 cursor-pointer"
				type="button"
			>
				<XIcon className="size-3" />
			</button>
		</div>
	);
}

/**
 * Renders a compact list of attachment previews (images, audio controls, or file rows).
 *
 * Renders each attachment according to its type:
 * - image_url with a URL: an image thumbnail
 * - input_audio: an HTML audio control built from base64 data
 * - file: a row with a file icon, filename, and optional file type
 *
 * When `editable` is true and `onRemoveAttachment` is provided, a remove button is shown for each attachment and invokes the callback with the attachment's index when clicked.
 *
 * @param attachments - The attachments to render.
 * @param editable - If true, show per-attachment remove controls.
 * @param onRemoveAttachment - Callback invoked with the attachment index when a remove control is clicked.
 * @returns A JSX element containing the rendered attachments, or `null` when `attachments` is empty.
 */
export function AttachmentDisplay({
	attachments,
	editable,
	onRemoveAttachment,
}: {
	attachments: MessageContent[];
	editable?: boolean;
	onRemoveAttachment?: (index: number) => void;
}) {
	if (attachments.length === 0) return null;

	return (
		<div className="mt-2 flex flex-wrap gap-2">
			{attachments.map((att, i) => {
				if (att.type === "image_url" && att.image_url?.url) {
					return (
						<div key={i} className="group/att relative max-w-full">
							{/* eslint-disable-next-line @next/next/no-img-element */}
							<img src={att.image_url.url} alt="attached image" className="max-h-48 max-w-full rounded-sm border object-contain sm:max-w-xs" />
							{editable && onRemoveAttachment && (
								<button
									onClick={() => onRemoveAttachment(i)}
									className="bg-background/80 text-muted-foreground hover:bg-card hover:text-destructive absolute -top-1.5 -right-1.5 rounded-full border p-0.5 opacity-0 transition-opacity group-hover/att:opacity-100 cursor-pointer"
								>
									<XIcon className="size-3" />
								</button>
							)}
						</div>
					);
				}

				if (att.type === "input_audio") {
					const format = att.input_audio?.format || "wav";
					const dataUrl = `data:audio/${format};base64,${att.input_audio?.data || ""}`;
					return (
						<div key={i} className="group/att bg-muted/30 relative flex w-full items-center gap-2 rounded-sm border px-3 py-2">
							<audio controls className="h-8 w-full min-w-0 grow">
								<source src={dataUrl} type={`audio/${format}`} />
							</audio>
							{editable && onRemoveAttachment && (
								<button
									onClick={() => onRemoveAttachment(i)}
									className="bg-background/80 text-muted-foreground cursor-pointer hover:bg-card hover:text-destructive absolute -top-1.5 -right-1.5 rounded-full border p-0.5 opacity-0 transition-opacity group-hover/att:opacity-100"
								>
									<XIcon className="size-3" />
								</button>
							)}
						</div>
					);
				}

				if (att.type === "file") {
					return (
						<div
							key={i}
							className="group/att bg-muted/30 text-muted-foreground relative flex max-w-full items-center gap-2 rounded-sm border px-3 py-1.5 text-sm"
						>
							<FileIcon className="size-3 shrink-0" />
							<span className="min-w-0 truncate">{att.file?.filename || "File"}</span>
							{att.file?.file_type && <span className="shrink-0 text-xs opacity-60">{att.file.file_type}</span>}
							{editable && onRemoveAttachment && (
								<button
									onClick={() => onRemoveAttachment(i)}
									className="bg-background/80 text-muted-foreground hover:bg-card hover:text-destructive absolute -top-1.5 -right-1.5 rounded-full border p-0.5 opacity-0 transition-opacity group-hover/att:opacity-100"
								>
									<XIcon className="size-3" />
								</button>
							)}
						</div>
					);
				}

				return null;
			})}
		</div>
	);
}
