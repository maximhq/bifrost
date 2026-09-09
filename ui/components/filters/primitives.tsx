import { Checkbox } from "@/components/ui/checkbox";
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from "@/components/ui/collapsible";
import { Input } from "@/components/ui/input";
import { Skeleton } from "@/components/ui/skeleton";
import { TruncatedLabel } from "@/components/ui/truncatedLabel";
import { cn } from "@/lib/utils";
import { ChevronDown, LoaderCircle, Plus, Search } from "lucide-react";
import { Ref, useEffect, useRef, useState } from "react";

// Building blocks shared by the filter sidebars (logs, MCP logs, webhook
// deliveries). Lifted verbatim out of mcpFilterSidebar.tsx, where they had been
// hand-copied from logsFilterSidebar.tsx; new sidebars should build on these
// rather than make a fourth copy.

// ---------------------------------------------------------------------------
// FilterSection – collapsible wrapper
// ---------------------------------------------------------------------------

export function FilterSectionSkeleton({ rows = 3 }: { rows?: number }) {
	return (
		<>
			{Array.from({ length: rows }).map((_, i) => (
				<div key={i} className="flex items-center gap-2.5 px-3 py-2">
					<Skeleton className="size-4 shrink-0 rounded-[4px]" />
					<Skeleton className="h-3.5 w-full rounded" />
				</div>
			))}
		</>
	);
}

export function FilterSection({
	title,
	children,
	defaultOpen = false,
	loading = false,
	onOpenChange,
	testId,
}: {
	title: string;
	children: React.ReactNode;
	defaultOpen?: boolean;
	loading?: boolean;
	onOpenChange?: (open: boolean) => void;
	testId?: string;
}) {
	const [open, setOpen] = useState(defaultOpen);

	useEffect(() => {
		if (defaultOpen) setOpen(true);
	}, [defaultOpen]);

	const handleOpenChange = (next: boolean) => {
		setOpen(next);
		onOpenChange?.(next);
	};

	return (
		<Collapsible open={open} onOpenChange={handleOpenChange} className="last:pb-2">
			<CollapsibleTrigger
				className="flex h-8 w-full cursor-pointer items-center gap-1.5 px-2 py-2 text-sm font-medium hover:opacity-80"
				data-testid={testId}
			>
				<ChevronDown className={cn("size-3.5 transition-transform", open ? "rotate-0" : "-rotate-90")} />
				<span>{title}</span>
			</CollapsibleTrigger>
			<CollapsibleContent className="pt-1">
				<div className="divide-border divide-y overflow-hidden rounded-sm border">{loading ? <FilterSectionSkeleton /> : children}</div>
			</CollapsibleContent>
		</Collapsible>
	);
}

// ---------------------------------------------------------------------------
// CheckboxFilterItem
// ---------------------------------------------------------------------------

export function CheckboxFilterItem({
	label,
	checked,
	onCheckedChange,
	labelClassName,
	testId,
}: {
	label: string;
	checked: boolean;
	onCheckedChange: (checked: boolean) => void;
	labelClassName?: string;
	testId?: string;
}) {
	return (
		<label className="hover:bg-muted/50 flex cursor-pointer items-center gap-2.5 px-3 py-2 text-sm" data-testid={testId}>
			<Checkbox checked={checked} onCheckedChange={onCheckedChange} />
			<TruncatedLabel className={labelClassName}>{label}</TruncatedLabel>
		</label>
	);
}

// ---------------------------------------------------------------------------
// SearchableCheckboxList – list of checkbox rows with a search input.
// Caller passes `inputRef` to control focus (see `useAutoFocusOnOpen`).
// ---------------------------------------------------------------------------

export function useAutoFocusOnOpen(isOpen: boolean) {
	const ref = useRef<HTMLInputElement>(null);
	useEffect(() => {
		if (isOpen) ref.current?.focus({ preventScroll: true });
	}, [isOpen]);
	return ref;
}

export function SearchableCheckboxList({
	items,
	isSelected,
	onToggle,
	placeholder = "Search...",
	inputRef,
	testIdPrefix,
	normalizeTestIdKey = false,
	allowCustom = false,
	onSearch,
	fetching,
}: {
	items: { key: string; label: string }[];
	isSelected: (key: string) => boolean;
	onToggle: (key: string) => void;
	placeholder?: string;
	inputRef?: Ref<HTMLInputElement>;
	testIdPrefix?: string;
	// When true, item keys are slugified before composing the per-row data-testid
	// (e.g. "Claude Desktop" -> "claude-desktop"). Use for free-form keys like app
	// names so E2E selectors stay space/case-stable; leave off for already-safe keys.
	normalizeTestIdKey?: boolean;
	allowCustom?: boolean;
	onSearch?: (query: string) => void;
	fetching?: boolean;
}) {
	const [query, setQuery] = useState("");
	const normalized = query.trim().toLowerCase();
	const filtered = normalized ? items.filter((item) => item.label.toLowerCase().includes(normalized)) : items;
	const trimmed = query.trim();
	const hasExactMatch = trimmed !== "" && items.some((item) => item.label.toLowerCase() === trimmed.toLowerCase());
	const showAddCustom = allowCustom && trimmed !== "" && !hasExactMatch;

	useEffect(() => {
		if (!onSearch) return;
		const timer = setTimeout(() => {
			onSearch(query.trim());
		}, 300);
		return () => clearTimeout(timer);
	}, [query, onSearch]);

	const commitCustom = () => {
		if (!showAddCustom) return;
		onToggle(trimmed);
		setQuery("");
	};

	return (
		<>
			<div className="relative border-b">
				{fetching ? (
					<LoaderCircle className="text-muted-foreground pointer-events-none absolute top-1/2 left-2.5 size-3.5 -translate-y-1/2 animate-spin" />
				) : (
					<Search className="text-muted-foreground pointer-events-none absolute top-1/2 left-2.5 size-3.5 -translate-y-1/2" />
				)}
				<Input
					ref={inputRef}
					value={query}
					onChange={(e) => setQuery(e.target.value)}
					onKeyDown={(e) => {
						if (e.key === "Enter") {
							e.preventDefault();
							commitCustom();
						}
					}}
					placeholder={placeholder}
					className="h-8 border-0 pl-8 text-xs"
					data-testid={testIdPrefix ? `${testIdPrefix}-search` : undefined}
				/>
			</div>
			{filtered.map((item) => (
				<CheckboxFilterItem
					key={item.key}
					label={item.label}
					checked={isSelected(item.key)}
					onCheckedChange={() => onToggle(item.key)}
					testId={
						testIdPrefix
							? `${testIdPrefix}-checkbox-${
									normalizeTestIdKey
										? item.key
												.toLowerCase()
												.replace(/[^a-z0-9]+/g, "-")
												.replace(/^-+|-+$/g, "")
										: item.key
								}`
							: undefined
					}
				/>
			))}
			{filtered.length === 0 && !showAddCustom && (
				<div className="text-muted-foreground flex h-9 items-center px-3 text-xs">No results</div>
			)}
			{showAddCustom && (
				<button
					type="button"
					onClick={commitCustom}
					className="hover:bg-muted/50 flex w-full cursor-pointer items-center gap-2.5 px-3 py-2 text-left text-sm"
					data-testid={testIdPrefix ? `${testIdPrefix}-add-custom` : undefined}
				>
					<Plus className="text-muted-foreground size-3.5 shrink-0" />
					<span className="truncate">
						Use <span className="font-medium">&quot;{trimmed}&quot;</span>
					</span>
				</button>
			)}
		</>
	);
}