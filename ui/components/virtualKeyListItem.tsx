import { Button } from "@/components/ui/button";
import { useCopyToClipboard } from "@/hooks/useCopyToClipboard";
import { cn } from "@/lib/utils";
import { Copy, X } from "lucide-react";

// Show the leading chars, mask the rest. Matches the virtual keys table.
function maskKey(key: string) {
	return key.substring(0, 8) + "•".repeat(Math.max(0, key.length - 8));
}

interface VirtualKeyListItemProps {
	name?: string;
	// The key's secret value: masked in the row and copied on click. Omit when unavailable.
	secret?: string;
	// Shown (as code) when there is no secret, e.g. the key id.
	fallbackLabel: string;
	// When set, the row is clickable (e.g. to open the key's detail) and the name reads as a link.
	onClick?: () => void;
	onRemove?: () => void;
	removeAriaLabel?: string;
	removeDisabled?: boolean;
	// Extra classes on the row, e.g. to tune padding to the surrounding list.
	className?: string;
	"data-testid"?: string;
}

// One virtual-key row: name, masked secret with a copy button, and an optional remove action.
// Shared by the Virtual MCP access tab and the user detail sheet so the row stays identical.
export function VirtualKeyListItem({
	name,
	secret,
	fallbackLabel,
	onClick,
	onRemove,
	removeAriaLabel = "Remove virtual key",
	removeDisabled,
	className,
	"data-testid": testId,
}: VirtualKeyListItemProps) {
	const { copy } = useCopyToClipboard({ successMessage: "Virtual key copied" });
	return (
		<div
			className={cn("group flex items-center justify-between gap-2 p-3", onClick && "cursor-pointer", className)}
			role={onClick ? "button" : undefined}
			tabIndex={onClick ? 0 : undefined}
			onClick={onClick}
			onKeyDown={
				onClick
					? (event) => {
							// Nested buttons (copy/remove) handle their own keys; keep them from bubbling into row nav.
							if (event.target !== event.currentTarget) return;
							if (event.key === "Enter" || event.key === " ") {
								event.preventDefault();
								onClick();
							}
						}
					: undefined
			}
		>
			<div className="flex min-w-0 flex-col gap-0.5">
				{name && <span className={cn("truncate text-sm font-medium", onClick && "text-primary hover:underline")}>{name}</span>}
				<div className="flex items-center gap-1.5">
					<code className="text-muted-foreground truncate font-mono text-xs">{secret ? maskKey(secret) : fallbackLabel}</code>
					{secret && (
						<Button
							variant="ghost"
							size="icon"
							className="text-muted-foreground h-5 w-5 shrink-0"
							aria-label="Copy virtual key"
							onClick={(event) => {
								event.stopPropagation();
								copy(secret);
							}}
						>
							<Copy className="h-3 w-3" />
						</Button>
					)}
				</div>
			</div>
			{onRemove && (
				<Button
					variant="ghost"
					size="icon"
					className="text-muted-foreground hover:text-destructive h-7 w-7 shrink-0 opacity-0 transition-opacity group-focus-within:opacity-100 group-hover:opacity-100"
					aria-label={removeAriaLabel}
					disabled={removeDisabled}
					data-testid={testId}
					onClick={(event) => {
						event.stopPropagation();
						onRemove();
					}}
				>
					<X className="h-4 w-4" />
				</Button>
			)}
		</div>
	);
}