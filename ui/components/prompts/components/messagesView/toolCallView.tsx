import { Message, SerializedMessage } from "@/lib/message";
import { Wrench, XIcon } from "lucide-react";
import MessageRoleSwitcher from "./messageRoleSwitcher";

export default function ToolCallMessageView({
	message,
	disabled,
	onChange,
	onRemove,
}: {
	message: Message;
	disabled?: boolean;
	onChange: (serialized: SerializedMessage) => void;
	onRemove?: () => void;
}) {
	const toolCalls = message.toolCalls ?? [];

	const handleRoleChange = (role: string) => {
		const clone = message.clone();
		clone.role = role as any;
		onChange(clone.serialized);
	};

	return (
		<div className="group hover:border-border rounded-lg border border-transparent px-3 py-2 transition-colors">
			<div className="mb-1 flex items-center">
				<MessageRoleSwitcher role={message.role ?? ""} disabled={disabled} onRoleChange={handleRoleChange} />
				<div className="ml-auto">
					{!disabled && onRemove && (
						<XIcon
							className="text-muted-foreground hover:text-foreground h-4 w-4 shrink-0 cursor-pointer opacity-0 transition-opacity group-hover:opacity-100"
							onClick={onRemove}
						/>
					)}
				</div>
			</div>
			<div className="space-y-2">
				{toolCalls.map((tc) => (
					<div key={tc.id} className="text-muted-foreground text-sm">
						<div className="flex items-center gap-2">
							<Wrench className="text-muted-foreground h-3 w-3" />
							<span className="font-mono text-xs font-medium">{tc.function.name}</span>
						</div>
						<pre className="text-muted-foreground mt-1 overflow-x-auto text-xs whitespace-pre-wrap">{tc.function.arguments}</pre>
					</div>
				))}
			</div>
		</div>
	);
}
