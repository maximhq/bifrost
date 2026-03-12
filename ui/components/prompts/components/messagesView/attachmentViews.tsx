import { MessageContent } from "@/lib/message";
import { Mic, FileIcon, XIcon } from "lucide-react";

export function AttachmentBadge({ attachment, onRemove }: { attachment: MessageContent; onRemove: () => void }) {
	const isImage = attachment.type === "image_url";
	const isAudio = attachment.type === "input_audio";

	return (
		<div className="group/att bg-muted/50 relative flex items-center gap-1.5 rounded-md border px-2 py-1 text-xs">
			{isImage && attachment.image_url?.url ? (
				<>
					<img src={attachment.image_url.url} alt="attachment" className="h-8 w-8 rounded object-cover" />
					<span className="text-muted-foreground max-w-[100px] truncate">Image</span>
				</>
			) : isAudio ? (
				<>
					<Mic className="text-muted-foreground h-3.5 w-3.5" />
					<span className="text-muted-foreground max-w-[100px] truncate">{attachment.input_audio?.format?.toUpperCase() || "Audio"}</span>
				</>
			) : (
				<>
					<FileIcon className="text-muted-foreground h-3.5 w-3.5" />
					<span className="text-muted-foreground max-w-[120px] truncate">{attachment.file?.filename || "File"}</span>
				</>
			)}
			<button
				onClick={onRemove}
				className="text-muted-foreground hover:bg-destructive/20 hover:text-destructive ml-0.5 rounded-full p-0.5"
				type="button"
			>
				<XIcon className="h-3 w-3" />
			</button>
		</div>
	);
}
