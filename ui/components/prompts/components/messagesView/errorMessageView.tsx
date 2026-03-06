import { Message } from "@/lib/message";
import { AlertCircle, XIcon } from "lucide-react";

export default function ErrorMessageView({ message, disabled, onRemove }: { message: Message; disabled?: boolean; onRemove?: () => void }) {
	return (
		<div className="group hover:border-destructive/30 rounded-lg border border-transparent px-3 py-2 transition-colors">
			<div className="mb-1 flex items-center">
				<span className="text-destructive flex items-center gap-1 py-0.5 text-xs font-medium uppercase">
					<AlertCircle className="h-3 w-3" />
					Error
				</span>
				<div className="ml-auto">
					{!disabled && onRemove && (
						<XIcon
							className="text-muted-foreground hover:text-foreground h-4 w-4 shrink-0 cursor-pointer opacity-0 transition-opacity group-hover:opacity-100"
							onClick={onRemove}
						/>
					)}
				</div>
			</div>
			<div className="bg-destructive/10 rounded-md px-2.5 py-1.5">
				<p className="text-muted-foreground text-sm whitespace-pre-wrap">{message.content}</p>
			</div>
		</div>
	);
}
