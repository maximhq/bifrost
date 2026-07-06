import { Command as CommandPrimitive } from "cmdk";
import { Loader2, Plus, SearchIcon } from "lucide-react";
import * as React from "react";

import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover";
import { Skeleton } from "@/components/ui/skeleton";
import { cn } from "@/lib/utils";

interface SearchSelectOption {
	value: string;
	label: string;
	description?: string;
	disabled?: boolean;
	[key: string]: unknown;
}

interface SearchSelectBaseProps<T extends SearchSelectOption = SearchSelectOption> {
	options: T[];
	onValueSelect: (option: T) => void;
	label: React.ReactNode;
	entryView?: (option: T) => React.ReactNode;
	footer?: React.ReactNode;
	searchPlaceholder?: string;
	emptyMessage?: string;
	disabled?: boolean;
	open?: boolean;
	onOpenChange?: (open: boolean) => void;
	align?: "start" | "center" | "end";
	className?: string;
	triggerClassName?: string;
	contentClassName?: string;
	noPortal?: boolean;
}

interface SearchSelectSyncProps<T extends SearchSelectOption = SearchSelectOption> extends SearchSelectBaseProps<T> {
	async?: false;
	onSearchChange?: never;
	isSearching?: never;
	isLoading?: never;
	isError?: never;
	errorMessage?: never;
}

interface SearchSelectAsyncProps<T extends SearchSelectOption = SearchSelectOption> extends SearchSelectBaseProps<T> {
	async: true;
	onSearchChange: (search: string) => void;
	isSearching?: boolean;
	isLoading?: boolean;
	isError?: boolean;
	errorMessage?: string;
}

type SearchSelectProps<T extends SearchSelectOption = SearchSelectOption> = SearchSelectSyncProps<T> | SearchSelectAsyncProps<T>;

function DefaultEntryView({ option }: { option: SearchSelectOption }) {
	return (
		<>
			<div className="flex flex-col">
				<span className="font-medium">{option.label}</span>
				{option.description && <span className="text-muted-foreground text-xs">{option.description}</span>}
			</div>
			<Plus className="ml-auto h-3.5 w-3.5" />
		</>
	);
}

function SearchSelect<T extends SearchSelectOption = SearchSelectOption>(props: SearchSelectProps<T>) {
	const {
		options,
		onValueSelect,
		label,
		entryView,
		footer,
		searchPlaceholder = "Search...",
		emptyMessage = "No results found.",
		disabled = false,
		open: controlledOpen,
		onOpenChange,
		align = "end",
		className,
		triggerClassName,
		contentClassName,
		noPortal,
	} = props;

	const isAsync = props.async === true;
	const onSearchChange = isAsync ? props.onSearchChange : undefined;
	const isSearching = isAsync ? (props.isSearching ?? false) : false;
	const isLoading = isAsync ? (props.isLoading ?? false) : false;
	const isError = isAsync ? (props.isError ?? false) : false;
	const errorMessage = isAsync ? (props.errorMessage ?? "Failed to load.") : "";

	const [internalOpen, setInternalOpen] = React.useState(false);
	const [search, setSearch] = React.useState("");

	const open = controlledOpen !== undefined ? controlledOpen : internalOpen;

	const setOpen = React.useCallback(
		(v: boolean) => {
			setInternalOpen(v);
			onOpenChange?.(v);
			if (!v) {
				setSearch("");
				onSearchChange?.("");
			}
		},
		[onOpenChange, onSearchChange],
	);

	const handleSearchChange = React.useCallback(
		(v: string) => {
			setSearch(v);
			onSearchChange?.(v);
		},
		[onSearchChange],
	);

	const inputRef = React.useRef<HTMLInputElement>(null);

	React.useEffect(() => {
		if (open) {
			requestAnimationFrame(() => inputRef.current?.focus());
		} else {
			setSearch("");
			onSearchChange?.("");
		}
	}, [open, onSearchChange]);

	return (
		<Popover open={open} onOpenChange={setOpen}>
			<PopoverTrigger asChild disabled={disabled}>
				<span data-slot="search-select" className={cn("inline-flex", className)}>
					{typeof label === "string" ? (
						<button
							type="button"
							className={cn(
								"inline-flex items-center gap-2 rounded-sm border px-2 py-1 text-sm font-medium transition-colors hover:bg-accent hover:text-accent-foreground disabled:pointer-events-none disabled:opacity-50",
								triggerClassName,
							)}
							disabled={disabled}
						>
							{label}
						</button>
					) : (
						label
					)}
				</span>
			</PopoverTrigger>
			<PopoverContent
				data-slot="search-select-content"
				className={cn("w-72 p-0", contentClassName)}
				align={align}
				sideOffset={4}
				noPortal={noPortal}
				onOpenAutoFocus={(e) => e.preventDefault()}
			>
				<CommandPrimitive filter={isAsync ? () => 1 : undefined}>
					<div data-slot="search-select-input" className="flex items-center gap-2 border-b px-3">
						{isSearching ? (
							<Loader2 className="size-4 shrink-0 animate-spin opacity-50" />
						) : (
							<SearchIcon className="size-4 shrink-0 opacity-50" />
						)}
						<CommandPrimitive.Input
							ref={inputRef}
							placeholder={searchPlaceholder}
							className="placeholder:text-muted-foreground flex h-9 w-full bg-transparent py-3 text-sm outline-none disabled:cursor-not-allowed disabled:opacity-50"
							value={search}
							onValueChange={handleSearchChange}
						/>
					</div>
					<CommandPrimitive.List data-slot="search-select-list" className="max-h-[300px] overflow-x-hidden overflow-y-auto p-1">
						{isLoading ? (
							<div className="space-y-1 p-1">
								{Array.from({ length: 3 }).map((_, i) => (
									<div key={i} className="flex items-center gap-2 rounded-sm px-2 py-1.5">
										<div className="flex flex-1 flex-col gap-1.5">
											<Skeleton className="h-3.5 w-28" />
											<Skeleton className="h-3 w-40" />
										</div>
										<Skeleton className="size-3.5 rounded-full" />
									</div>
								))}
							</div>
						) : isError ? (
							<div className="text-destructive py-6 text-center text-sm">{errorMessage}</div>
						) : (
							<>
								<CommandPrimitive.Empty className="text-muted-foreground py-6 text-center text-sm">{emptyMessage}</CommandPrimitive.Empty>
								{options.map((option) => (
									<CommandPrimitive.Item
										key={option.value}
										value={option.label}
										disabled={option.disabled}
										onSelect={() => onValueSelect(option)}
										className="data-[selected=true]:bg-accent data-[selected=true]:text-accent-foreground relative flex w-full cursor-default items-center gap-2 rounded-sm px-2 py-1.5 text-sm outline-hidden select-none data-[disabled=true]:pointer-events-none data-[disabled=true]:opacity-50"
									>
										{entryView ? entryView(option) : <DefaultEntryView option={option} />}
									</CommandPrimitive.Item>
								))}
							</>
						)}
					</CommandPrimitive.List>
				</CommandPrimitive>
				{footer && (
					<div data-slot="search-select-footer" className="border-t">
						{footer}
					</div>
				)}
			</PopoverContent>
		</Popover>
	);
}

export { SearchSelect };
export type { SearchSelectOption, SearchSelectProps };