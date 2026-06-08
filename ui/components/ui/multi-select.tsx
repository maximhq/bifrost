import { CheckIcon, ChevronDown, X } from "lucide-react";
import * as React from "react";

import { Badge } from "@/components/ui/badge";
import { Command, CommandGroup, CommandItem, CommandList } from "@/components/ui/command";
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover";
import { cn } from "@/lib/utils";

interface MultiSelectContextValue {
	value: string[];
	onValueChange: (value: string[]) => void;
	close: () => void;
}

const MultiSelectContext = React.createContext<MultiSelectContextValue | null>(null);

export function useMultiSelect() {
	const ctx = React.useContext(MultiSelectContext);
	if (!ctx) {
		throw new Error("useMultiSelect must be used within MultiSelect");
	}
	return ctx;
}

interface MultiSelectProps extends React.HTMLAttributes<HTMLDivElement> {
	value: string[];
	onValueChange: (value: string[]) => void;
	placeholder?: string;
	children?: React.ReactNode;
	getBadgeLabel?: (value: string) => string;
}

function MultiSelect({ value, onValueChange, placeholder = "Select...", className, children, getBadgeLabel, ...props }: MultiSelectProps) {
	const [open, setOpen] = React.useState(false);
	const popupId = React.useId();

	const handleUnselect = (item: string) => {
		onValueChange(value.filter((i) => i !== item));
	};

	return (
		<MultiSelectContext.Provider value={{ value, onValueChange, close: () => setOpen(false) }}>
			<Popover open={open} onOpenChange={setOpen}>
				<PopoverTrigger asChild>
					<div
						role="combobox"
						aria-expanded={open}
						aria-controls={popupId}
						tabIndex={0}
						data-testid="multi-select-trigger"
						onKeyDown={(e) => {
							if (e.key === "Enter" || e.key === " ") {
								e.preventDefault();
								setOpen(true);
							}
						}}
						className={cn(
							"border-input ring-offset-background focus:ring-ring flex min-h-9 w-full flex-wrap items-center gap-1 rounded-sm border bg-transparent px-3 py-1 text-sm shadow-xs focus:outline-hidden focus:ring-2 focus:ring-offset-2",
							className,
						)}
						{...props}
					>
						{value.length > 0 ? (
							value.map((item) => (
								<Badge key={item} variant="secondary" className="gap-1" data-testid={`multi-select-badge-${item}`}>
									{getBadgeLabel ? getBadgeLabel(item) : item}
									<button
										type="button"
										aria-label={`Remove ${getBadgeLabel ? getBadgeLabel(item) : item}`}
										data-testid={`multi-select-remove-${item}`}
										className="ring-offset-background focus:ring-ring ml-1 rounded-full outline-hidden focus:ring-2 focus:ring-offset-2"
										onMouseDown={(e) => {
											e.preventDefault();
											e.stopPropagation();
										}}
										onClick={(e) => {
											e.preventDefault();
											e.stopPropagation();
											handleUnselect(item);
										}}
									>
										<X className="h-3 w-3" />
									</button>
								</Badge>
							))
						) : (
							<span className="text-muted-foreground">{placeholder}</span>
						)}
						<ChevronDown className="ml-auto h-4 w-4 shrink-0 opacity-50" />
					</div>
				</PopoverTrigger>
				<PopoverContent id={popupId} className="w-full p-0" align="start">
					<Command>
						<CommandList>
							<CommandGroup>{children}</CommandGroup>
						</CommandList>
					</Command>
				</PopoverContent>
			</Popover>
		</MultiSelectContext.Provider>
	);
}

function MultiSelectContent({ children, className, ...props }: React.HTMLAttributes<HTMLDivElement>) {
	return (
		<div className={cn(className)} {...props}>
			{children}
		</div>
	);
}

function MultiSelectGroup({ children, className, ...props }: React.HTMLAttributes<HTMLDivElement>) {
	return (
		<div className={cn(className)} {...props}>
			{children}
		</div>
	);
}

function MultiSelectItem({ children, className, value, ...props }: { children: React.ReactNode; className?: string; value: string }) {
	const ctx = useMultiSelect();
	const isSelected = ctx.value.includes(value);
	return (
		<CommandItem
			data-testid={`multi-select-item-${value}`}
			className={cn("cursor-pointer", className)}
			onSelect={() => {
				const next = isSelected ? ctx.value.filter((v: string) => v !== value) : [...ctx.value, value];
				ctx.onValueChange(next);
			}}
			value={value}
			{...props}
		>
			{isSelected && <CheckIcon className="mr-2 h-4 w-4" />}
			{children}
		</CommandItem>
	);
}

export { MultiSelect, MultiSelectContent, MultiSelectGroup, MultiSelectItem };