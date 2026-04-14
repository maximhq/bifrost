import { Command as CommandPrimitive } from "cmdk";
import { CheckIcon, ChevronDownIcon, XIcon } from "lucide-react";
import * as React from "react";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover";
import { cn } from "@/lib/utils";

interface ComboboxContextValue {
	open: boolean;
	setOpen: (open: boolean) => void;
	value: string | string[] | null;
	onValueChange: (value: any) => void;
	inputValue: string;
	setInputValue: (value: string) => void;
	multiple?: boolean;
	filter: ((value: string, search: string) => number) | null;
	itemToStringLabel?: (value: string | null) => string;
}

const ComboboxContext = React.createContext<ComboboxContextValue | null>(null);

function useComboboxContext() {
	const ctx = React.useContext(ComboboxContext);
	if (!ctx) throw new Error("Combobox compound components must be used within <Combobox>");
	return ctx;
}

interface ComboboxRootProps {
	children: React.ReactNode;
	value?: string | string[] | null;
	onValueChange?: (value: any) => void;
	onOpenChange?: (open: boolean) => void;
	onInputValueChange?: (value: string) => void;
	filter?: ((value: string, search: string) => number) | null;
	multiple?: boolean;
	itemToStringLabel?: (value: string | null) => string;
	open?: boolean;
	defaultOpen?: boolean;
}

function Combobox({
	children,
	value: controlledValue,
	onValueChange,
	onOpenChange,
	onInputValueChange,
	filter = null,
	multiple,
	itemToStringLabel,
	open: controlledOpen,
	defaultOpen = false,
}: ComboboxRootProps) {
	const [internalOpen, setInternalOpen] = React.useState(defaultOpen);
	const [inputValue, setInputValueState] = React.useState("");

	const open = controlledOpen !== undefined ? controlledOpen : internalOpen;

	const setOpen = React.useCallback(
		(v: boolean) => {
			setInternalOpen(v);
			onOpenChange?.(v);
		},
		[onOpenChange],
	);

	const setInputValue = React.useCallback(
		(v: string) => {
			setInputValueState(v);
			onInputValueChange?.(v);
		},
		[onInputValueChange],
	);

	const handleValueChange = React.useCallback(
		(v: any) => {
			onValueChange?.(v);
		},
		[onValueChange],
	);

	const ctx = React.useMemo<ComboboxContextValue>(
		() => ({
			open,
			setOpen,
			value: controlledValue ?? null,
			onValueChange: handleValueChange,
			inputValue,
			setInputValue,
			multiple,
			filter,
			itemToStringLabel,
		}),
		[open, setOpen, controlledValue, handleValueChange, inputValue, setInputValue, multiple, filter, itemToStringLabel],
	);

	return (
		<ComboboxContext.Provider value={ctx}>
			<Popover open={open} onOpenChange={setOpen}>
				{children}
			</Popover>
		</ComboboxContext.Provider>
	);
}

function ComboboxInput({
	className,
	disabled = false,
	showTrigger = true,
	showClear = false,
	placeholder,
}: {
	className?: string;
	children?: React.ReactNode;
	disabled?: boolean;
	showTrigger?: boolean;
	showClear?: boolean;
	placeholder?: string;
	readOnly?: boolean;
	autoFocus?: boolean;
}) {
	const { value, itemToStringLabel, onValueChange } = useComboboxContext();

	const displayValue = React.useMemo(() => {
		if (Array.isArray(value)) return "";
		if (value && itemToStringLabel) return itemToStringLabel(value);
		return value ?? "";
	}, [value, itemToStringLabel]);

	return (
		<PopoverTrigger asChild disabled={disabled}>
			<Button
				variant="outline"
				role="combobox"
				disabled={disabled}
				data-testid="combobox-trigger-button"
				className={cn(
					"h-8 w-full justify-between !bg-transparent font-normal active:scale-none",
					!value && "text-muted-foreground",
					className,
				)}
			>
				<span className="truncate">{displayValue || placeholder || "Select..."}</span>
				<div className="ml-2 flex shrink-0 items-center gap-1">
					{showClear && value && (
						<button
							type="button"
							aria-label="Clear selection"
							data-testid="combobox-clear-button"
							className="rounded-sm opacity-50 hover:opacity-100"
							onClick={(e) => {
								e.stopPropagation();
								onValueChange(null);
							}}
						>
							<XIcon className="size-3.5" />
						</button>
					)}
					{showTrigger && <ChevronDownIcon className="size-4 opacity-50" />}
				</div>
			</Button>
		</PopoverTrigger>
	);
}

// ---------------------------------------------------------------------------
// ComboboxContent — popover dropdown with Command
// ---------------------------------------------------------------------------

function ComboboxContent({
	className,
	children,
	...props
}: {
	className?: string;
	children?: React.ReactNode;
	anchor?: React.RefObject<HTMLElement | null>;
	[key: string]: any;
}) {
	const { filter } = useComboboxContext();

	return (
		<PopoverContent
			className={cn("w-[var(--radix-popover-trigger-width)] p-0", className)}
			align="start"
			sideOffset={4}
			onOpenAutoFocus={(e) => e.preventDefault()}
			{...props}
		>
			<CommandPrimitive
				filter={
					filter === null
						? () => 1 // disable internal filtering — consumer controls it
						: (filter ?? undefined)
				}
			>
				{children}
			</CommandPrimitive>
		</PopoverContent>
	);
}

function ComboboxList({ className, ...props }: React.ComponentProps<typeof CommandPrimitive.List>) {
	const { inputValue, setInputValue } = useComboboxContext();

	return (
		<>
			<div className="flex items-center border-b px-3">
				<CommandPrimitive.Input
					placeholder="Search..."
					className="placeholder:text-muted-foreground flex h-8 w-full bg-transparent py-3 text-sm outline-none disabled:cursor-not-allowed disabled:opacity-50"
					value={inputValue}
					autoFocus
					onValueChange={setInputValue}
				/>
			</div>
			<CommandPrimitive.List data-slot="combobox-list" className={cn("max-h-[300px] overflow-y-auto p-1", className)} {...props} />
		</>
	);
}

function ComboboxItem({
	className,
	children,
	value: itemValue,
	...props
}: {
	className?: string;
	children?: React.ReactNode;
	value: string;
	[key: string]: any;
}) {
	const { value: selectedValue, onValueChange, setOpen, multiple } = useComboboxContext();

	const isSelected = Array.isArray(selectedValue) ? selectedValue.includes(itemValue) : selectedValue === itemValue;

	return (
		<CommandPrimitive.Item
			data-slot="combobox-item"
			className={cn(
				"data-[selected=true]:bg-accent data-[selected=true]:text-accent-foreground relative flex w-full cursor-default items-center gap-2 rounded-sm py-1.5 pr-8 pl-2 text-sm outline-hidden select-none data-[disabled=true]:pointer-events-none data-[disabled=true]:opacity-50",
				className,
			)}
			value={itemValue}
			onSelect={() => {
				if (multiple) {
					const current = Array.isArray(selectedValue) ? selectedValue : [];
					const next = isSelected ? current.filter((v) => v !== itemValue) : [...current, itemValue];
					onValueChange(next);
					return;
				}
				onValueChange(isSelected ? null : itemValue);
				setOpen(false);
			}}
			{...props}
		>
			{children}
			<span className="pointer-events-none absolute right-2 flex size-4 items-center justify-center">
				{isSelected && <CheckIcon className="size-4" />}
			</span>
		</CommandPrimitive.Item>
	);
}

function ComboboxGroup({ className, ...props }: React.ComponentProps<typeof CommandPrimitive.Group>) {
	return <CommandPrimitive.Group data-slot="combobox-group" className={cn(className)} {...props} />;
}

function ComboboxLabel({ className, ...props }: React.ComponentProps<"div">) {
	return <div data-slot="combobox-label" className={cn("text-muted-foreground px-2 py-1.5 text-xs font-medium", className)} {...props} />;
}

function ComboboxSeparator({ className, ...props }: React.ComponentProps<typeof CommandPrimitive.Separator>) {
	return <CommandPrimitive.Separator data-slot="combobox-separator" className={cn("bg-border -mx-1 my-1 h-px", className)} {...props} />;
}

function ComboboxEmpty({ className, ...props }: React.ComponentProps<typeof CommandPrimitive.Empty>) {
	return <CommandPrimitive.Empty data-slot="combobox-empty" className={cn("py-6 text-center text-sm", className)} {...props} />;
}

interface ComboboxSelectOption {
	label: string;
	value: string;
}

interface ComboboxSelectBaseProps {
	options: ComboboxSelectOption[];
	placeholder?: string;
	disabled?: boolean;
	disableSearch?: boolean;
	hideClear?: boolean;
	className?: string;
	emptyMessage?: string;
}

interface ComboboxSelectSingleProps extends ComboboxSelectBaseProps {
	multiple?: false;
	value?: string | null;
	onValueChange?: (value: string | null) => void;
}

interface ComboboxSelectMultiProps extends ComboboxSelectBaseProps {
	multiple: true;
	value?: string[];
	onValueChange?: (value: string[]) => void;
}

type ComboboxSelectProps = ComboboxSelectSingleProps | ComboboxSelectMultiProps;

function ComboboxSelect(props: ComboboxSelectProps) {
	const {
		options,
		placeholder = "Select…",
		disabled = false,
		disableSearch = false,
		className,
		emptyMessage = "No results found.",
	} = props;

	const [open, setOpen] = React.useState(false);
	const [query, setQuery] = React.useState("");

	const filtered = React.useMemo(() => {
		if (disableSearch || !query) return options;
		const q = query.toLowerCase();
		return options.filter((o) => o.label.toLowerCase().includes(q));
	}, [options, query, disableSearch]);

	const getLabel = React.useCallback((val: string | null) => options.find((o) => o.value === val)?.label ?? val ?? "", [options]);

	// Multi-select variant
	if (props.multiple) {
		const selectedValues = props.value ?? [];

		return (
			<Popover
				open={open}
				onOpenChange={(v) => {
					setOpen(v);
					if (v) setQuery("");
				}}
			>
				<PopoverTrigger asChild disabled={disabled}>
					<Button
						variant="outline"
						role="combobox"
						aria-expanded={open}
						disabled={disabled}
						className={cn(
							"h-8 w-full justify-between !bg-transparent font-normal active:scale-none",
							selectedValues.length === 0 && "text-muted-foreground",
							className,
						)}
					>
						<div className="flex flex-1 flex-wrap gap-1 overflow-hidden">
							{selectedValues.length === 0 ? (
								<span>{placeholder}</span>
							) : (
								selectedValues.map((val) => (
									<Badge key={val} variant="secondary" className="text-xs">
										{getLabel(val)}
										<button
											type="button"
											aria-label={`Remove ${getLabel(val)}`}
											data-testid={`combobox-remove-${val}`}
											className="ml-1 rounded-full opacity-50 outline-none hover:opacity-100"
											onClick={(e) => {
												e.stopPropagation();
												props.onValueChange?.(selectedValues.filter((v) => v !== val));
											}}
										>
											<XIcon className="size-3" />
										</button>
									</Badge>
								))
							)}
						</div>
						<ChevronDownIcon className="ml-2 size-4 shrink-0 opacity-50" />
					</Button>
				</PopoverTrigger>
				<PopoverContent className="w-[var(--radix-popover-trigger-width)] p-0" align="start" sideOffset={1}>
					<CommandPrimitive filter={() => 1}>
						{!disableSearch && (
							<div className="flex items-center border-b px-3">
								<CommandPrimitive.Input
									placeholder="Search..."
									className="placeholder:text-muted-foreground flex h-8 w-full bg-transparent py-3 text-sm outline-none"
									value={query}
									onValueChange={setQuery}
								/>
							</div>
						)}
						<CommandPrimitive.List className="max-h-[300px] overflow-y-auto p-1">
							{filtered.map((option) => {
								const isSelected = selectedValues.includes(option.value);
								return (
									<CommandPrimitive.Item
										key={option.value}
										value={option.value}
										className="data-[selected=true]:bg-accent data-[selected=true]:text-accent-foreground relative flex w-full cursor-default items-center gap-2 rounded-sm py-1.5 pr-8 pl-2 text-sm outline-hidden select-none"
										onSelect={() => {
											const next = isSelected ? selectedValues.filter((v) => v !== option.value) : [...selectedValues, option.value];
											props.onValueChange?.(next);
										}}
									>
										{option.label}
										<span className="pointer-events-none absolute right-2 flex size-4 items-center justify-center">
											{isSelected && <CheckIcon className="size-4" />}
										</span>
									</CommandPrimitive.Item>
								);
							})}
							{!disableSearch && filtered.length === 0 && (
								<div className="text-muted-foreground py-6 text-center text-sm">{emptyMessage}</div>
							)}
						</CommandPrimitive.List>
					</CommandPrimitive>
				</PopoverContent>
			</Popover>
		);
	}

	// Single-select variant
	const selectedLabel = props.value ? getLabel(props.value) : null;

	return (
		<Popover
			open={open}
			onOpenChange={(v) => {
				setOpen(v);
				if (v) setQuery("");
			}}
		>
			<PopoverTrigger asChild disabled={disabled}>
				<Button
					variant="outline"
					role="combobox"
					aria-expanded={open}
					disabled={disabled}
					className={cn(
						"h-8 w-full justify-between !bg-transparent font-normal active:scale-none",
						!selectedLabel && "text-muted-foreground",
						className,
					)}
				>
					<span className="truncate">{selectedLabel || placeholder}</span>
					<div className="ml-2 flex shrink-0 items-center gap-1">
						{!props.hideClear && props.value && (
							<button
								type="button"
								aria-label="Clear selection"
								data-testid="combobox-select-clear-button"
								className="rounded-sm opacity-50 hover:opacity-100"
								onClick={(e) => {
									e.stopPropagation();
									props.onValueChange?.(null);
									setOpen(false);
								}}
							>
								<XIcon className="size-3.5" />
							</button>
						)}
						<ChevronDownIcon className="size-4 opacity-50" />
					</div>
				</Button>
			</PopoverTrigger>
			<PopoverContent className="w-[var(--radix-popover-trigger-width)] p-0" align="start" sideOffset={4}>
				<CommandPrimitive filter={() => 1}>
					{!disableSearch && (
						<div className="flex items-center border-b px-3">
							<CommandPrimitive.Input
								placeholder="Search..."
								className="placeholder:text-muted-foreground flex h-8 w-full bg-transparent py-3 text-sm outline-none"
								value={query}
								onValueChange={setQuery}
							/>
						</div>
					)}
					<CommandPrimitive.List className="max-h-[300px] overflow-y-auto p-1">
						{filtered.map((option) => (
							<CommandPrimitive.Item
								key={option.value}
								value={option.value}
								className="data-[selected=true]:bg-accent data-[selected=true]:text-accent-foreground relative flex w-full cursor-default items-center gap-2 rounded-sm py-1.5 pr-8 pl-2 text-sm outline-hidden select-none"
								onSelect={() => {
									props.onValueChange?.(option.value);
									setOpen(false);
								}}
							>
								{option.label}
								<span className="pointer-events-none absolute right-2 flex size-4 items-center justify-center">
									{props.value === option.value && <CheckIcon className="size-4" />}
								</span>
							</CommandPrimitive.Item>
						))}
						{!disableSearch && filtered.length === 0 && (
							<div className="text-muted-foreground py-6 text-center text-sm">{emptyMessage}</div>
						)}
					</CommandPrimitive.List>
				</CommandPrimitive>
			</PopoverContent>
		</Popover>
	);
}

export {
	Combobox,
	ComboboxContent,
	ComboboxEmpty,
	ComboboxGroup,
	ComboboxInput,
	ComboboxItem,
	ComboboxLabel,
	ComboboxList,
	ComboboxSelect,
	ComboboxSeparator,
};

export type { ComboboxSelectOption, ComboboxSelectProps };