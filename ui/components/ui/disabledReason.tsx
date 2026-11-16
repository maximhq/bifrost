import { DropdownMenuItem } from "@/components/ui/dropdownMenu";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { cn } from "@/lib/utils";
import * as React from "react";

interface DisabledReasonProps {
	/** Why the wrapped control is disabled. Nothing is wrapped when this is empty. */
	reason?: string;
	children: React.ReactNode;
	className?: string;
	"data-testid"?: string;
}

/**
 * Explains a disabled button on hover and focus. A disabled button receives no
 * pointer events, so the tooltip hangs off a wrapper instead, and the wrapper
 * takes focus so the reason is reachable by keyboard. Menu items use
 * DisabledReasonMenuItem instead, since a menu only moves focus between its items.
 */
export function DisabledReason({ reason, children, className, "data-testid": dataTestId }: DisabledReasonProps) {
	if (!reason) return <>{children}</>;

	return (
		<Tooltip>
			<TooltipTrigger asChild>
				<span tabIndex={0} className={cn("inline-flex", className)} data-testid={dataTestId}>
					{children}
				</span>
			</TooltipTrigger>
			<TooltipContent className="max-w-xs">{reason}</TooltipContent>
		</Tooltip>
	);
}

/**
 * Props that deny a menu item without taking it out of keyboard navigation.
 * Radix skips `disabled` items on arrow keys, so a denied item stays enabled for
 * Radix, is marked aria-disabled, and swallows selection without running the action.
 */
export function disabledMenuItemProps(
	reason: string | undefined,
	onSelect?: (event: Event) => void,
): { "aria-disabled"?: true; onSelect?: (event: Event) => void } {
	if (!reason) return { onSelect };
	return { "aria-disabled": true, onSelect: (event) => event.preventDefault() };
}

type DisabledReasonMenuItemProps = Omit<React.ComponentProps<typeof DropdownMenuItem>, "disabled"> & {
	/** Why the item is denied. The item is enabled when this is empty. */
	reason?: string;
};

/** A dropdown menu item that explains, on hover and keyboard focus, why it is denied. */
export function DisabledReasonMenuItem({ reason, onSelect, className, ...props }: DisabledReasonMenuItemProps) {
	const item = (
		<DropdownMenuItem
			{...props}
			{...disabledMenuItemProps(reason, onSelect)}
			className={cn(className, reason && "cursor-not-allowed opacity-50")}
		/>
	);
	if (!reason) return item;

	return (
		<Tooltip>
			<TooltipTrigger asChild>{item}</TooltipTrigger>
			<TooltipContent className="max-w-xs">{reason}</TooltipContent>
		</Tooltip>
	);
}