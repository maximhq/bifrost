import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
	Combobox,
	ComboboxContent,
	ComboboxEmpty,
	ComboboxGroup,
	ComboboxItem,
	ComboboxLabel,
	ComboboxList,
} from "@/components/ui/combobox";
import { PopoverTrigger } from "@/components/ui/popover";
import { RenderProviderIcon, resolveProviderIconKey, type ProviderIconType } from "@/lib/constants/icons";
import { getProviderLabel, VisibleProviderNames } from "@/lib/constants/logs";
import { useGetProvidersQuery } from "@/lib/store/apis/providersApi";
import type { ModelProvider } from "@/lib/types/config";
import { cn } from "@/lib/utils";
import { ChevronDownIcon, Loader2Icon, XIcon } from "lucide-react";
import type React from "react";
import { useCallback, useEffect, useMemo, useState } from "react";
import { normalizeValueOption, type ProviderSelectorOption, type ProviderSelectorValue } from "./providerSelector.utils";

export type { ProviderSelectorOption, ProviderSelectorValue } from "./providerSelector.utils";

interface ProviderSelectorBaseProps {
	/**
	 * Where the list comes from. "configured" is every provider the user has set up,
	 * "catalog" the static list of providers Bifrost knows how to talk to, and "values"
	 * a list that came from somewhere else entirely, such as analytics series labels.
	 */
	source?: "configured" | "catalog" | "values";
	/**
	 * The list itself, when `source` is "values". A bare name is given the standard provider
	 * label and mark; pass an option object instead to keep a row's own label, or to hand over
	 * a row that must not be selectable.
	 */
	values?: readonly ProviderSelectorValue[];
	/** Last word on which configured providers make the list, e.g. embedding-capable only. */
	filter?: (provider: ModelProvider) => boolean;
	/** Values to drop, for picking one that has not been configured yet. */
	excludeValues?: string[];
	/**
	 * Rows that are not in the source list: a provider a saved rule still points at after it
	 * was deleted, or an action row such as "Manage providers". They sit above the rest and
	 * keep that slot whatever the search turns up.
	 */
	extraOptions?: ProviderSelectorOption[];
	/** Like `extraOptions`, but pinned below the list: an action that follows the choices. */
	footerOptions?: ProviderSelectorOption[];
	/** Per-row test id, for suites that address one provider's row directly. */
	optionTestId?: (value: string) => string;
	/** Test id on the open dropdown, for suites that assert the list itself. */
	contentTestId?: string;
	/** The "all providers" row. The sentinel value is the call site's to choose. */
	allOption?: { value: string; label?: string };
	/** Renders base and custom providers under separate headings. */
	groupByCustom?: boolean;
	placeholder?: string;
	searchPlaceholder?: string;
	emptyMessage?: string;
	disabled?: boolean;
	/** "sm" matches the compact triggers used in filter bars and table headers. */
	size?: "sm" | "default";
	className?: string;
	contentClassName?: string;
	/**
	 * Widens the dropdown past the trigger, for a narrow trigger with long provider names.
	 * A number is px. Always clamped to the room radix reports, so it never leaves the
	 * viewport. Left out, the dropdown matches the trigger.
	 */
	contentWidth?: number | string;
	/** Renders the dropdown in place rather than in a portal, for use inside a sheet. */
	noPortal?: boolean;
	/** Custom rendering for a selected value on the trigger. Defaults to icon plus label. */
	renderValueLabel?: (value: string) => React.ReactNode;
	/** id for the trigger, so a form label and its error message can point at it. */
	inputId?: string;
	ariaLabelledBy?: string;
	ariaDescribedBy?: string;
	ariaInvalid?: boolean;
	"data-testid"?: string;
}

interface ProviderSelectorSingleProps extends ProviderSelectorBaseProps {
	mode?: "select";
	multiple?: false;
	value: string;
	onChange: (provider: string) => void;
}

interface ProviderSelectorMultiProps extends ProviderSelectorBaseProps {
	mode?: "select";
	multiple: true;
	value: string[];
	onChange: (providers: string[]) => void;
}

interface ProviderSelectorAddProps extends ProviderSelectorBaseProps {
	/** Fire-and-forget: nothing is held selected and the trigger keeps its own label. */
	mode: "add";
	onSelect: (provider: string) => void;
	/** Replaces the default trigger, for an "Add provider" button that opens the list. */
	trigger?: React.ReactNode;
}

export type ProviderSelectorProps = ProviderSelectorSingleProps | ProviderSelectorMultiProps | ProviderSelectorAddProps;

/** Custom providers come after the base ones, then alphabetical by what the user reads. */
function byLabel(a: ProviderSelectorOption, b: ProviderSelectorOption): number {
	if (Boolean(a.isCustom) !== Boolean(b.isCustom)) return a.isCustom ? 1 : -1;
	return a.label.localeCompare(b.label, undefined, { sensitivity: "base" });
}

function matches(option: ProviderSelectorOption, term: string): boolean {
	if (!term) return true;
	// Both, so "Amazon Bedrock" is found by typing either half of it or the raw key.
	return option.label.toLowerCase().includes(term) || option.value.toLowerCase().includes(term);
}

export function ProviderSelector(props: ProviderSelectorProps) {
	const {
		source = "configured",
		values,
		filter,
		excludeValues,
		extraOptions,
		footerOptions,
		optionTestId,
		contentTestId,
		allOption,
		groupByCustom = false,
		placeholder = "Select provider",
		searchPlaceholder = "Search providers...",
		emptyMessage,
		disabled = false,
		size = "default",
		className,
		contentClassName,
		contentWidth,
		noPortal,
		renderValueLabel,
		inputId,
		ariaLabelledBy,
		ariaDescribedBy,
		ariaInvalid,
	} = props;

	const isAdd = props.mode === "add";
	const isMulti = !isAdd && props.multiple === true;

	// Pulled out of the union before the hooks, so the memo and callback deps below are the
	// values themselves rather than the props object, whose identity changes every render.
	const singleValue = isAdd || isMulti ? undefined : (props as ProviderSelectorSingleProps).value;
	const multiValue = isMulti ? (props as ProviderSelectorMultiProps).value : undefined;
	const onChange = isAdd ? undefined : (props as ProviderSelectorSingleProps | ProviderSelectorMultiProps).onChange;
	const onSelect = isAdd ? (props as ProviderSelectorAddProps).onSelect : undefined;
	const addTrigger = isAdd ? (props as ProviderSelectorAddProps).trigger : undefined;

	const selected = useMemo<string[]>(() => {
		if (isAdd) return [];
		if (isMulti) return multiValue ?? [];
		return singleValue ? [singleValue] : [];
	}, [isAdd, isMulti, multiValue, singleValue]);

	const [open, setOpen] = useState(false);
	const [search, setSearch] = useState("");

	// Void-arg and cached: fifteen other components already subscribe to it, so gating it
	// behind the popover would buy nothing and leave the trigger showing a bare key.
	const { data: providers, isLoading, isError } = useGetProvidersQuery(undefined, { skip: source !== "configured" });

	const sourceOptions = useMemo<ProviderSelectorOption[]>(() => {
		if (source === "configured") {
			return (providers ?? [])
				.filter((provider) => (filter ? filter(provider) : true))
				.map((provider) => ({
					value: provider.name,
					label: getProviderLabel(provider.name),
					iconKey: resolveProviderIconKey(provider.name, provider.custom_provider_config?.base_provider_type),
					isCustom: Boolean(provider.custom_provider_config),
				}));
		}
		const names: readonly ProviderSelectorValue[] = source === "catalog" ? VisibleProviderNames : (values ?? []);
		return names.filter(Boolean).map(normalizeValueOption);
	}, [source, providers, filter, values]);

	const options = useMemo<ProviderSelectorOption[]>(() => {
		const excluded = new Set(excludeValues ?? []);
		return sourceOptions.filter((option) => !excluded.has(option.value)).sort(byLabel);
	}, [sourceOptions, excludeValues]);

	// Filtering is local: the whole list is already in the browser, so there is nothing to
	// debounce and nothing to page. cmdk is told to leave membership and order alone.
	const term = search.trim().toLowerCase();
	const visibleExtras = useMemo(() => (extraOptions ?? []).filter((o) => matches(o, term)), [extraOptions, term]);
	const visibleFooters = useMemo(() => (footerOptions ?? []).filter((o) => matches(o, term)), [footerOptions, term]);
	const visibleAll = useMemo<ProviderSelectorOption[]>(() => {
		if (!allOption) return [];
		const row = { value: allOption.value, label: allOption.label ?? "All Providers" };
		return matches(row, term) ? [row] : [];
	}, [allOption, term]);
	const visible = useMemo(() => options.filter((o) => matches(o, term)), [options, term]);

	const base = useMemo(() => visible.filter((o) => !o.isCustom), [visible]);
	const custom = useMemo(() => visible.filter((o) => o.isCustom), [visible]);

	// A saved value can sit outside the list: the provider was deleted, or the caller narrowed
	// the list after it was picked. Pin it so the trigger says something and it stays
	// deselectable, instead of the row silently vanishing.
	const pinned = useMemo<ProviderSelectorOption[]>(() => {
		if (isAdd) return [];
		const known = new Set([...options, ...(extraOptions ?? []), ...visibleAll].map((o) => o.value));
		return selected
			.filter((value) => value && !known.has(value))
			.map((value) => ({ value, label: getProviderLabel(value), iconKey: resolveProviderIconKey(value) }))
			.filter((o) => matches(o, term));
	}, [isAdd, options, extraOptions, visibleAll, selected, term]);

	// Every row the arrow keys can land on, in the order they are rendered.
	const navigableValues = useMemo(() => {
		const rows = groupByCustom
			? [...visibleExtras, ...visibleAll, ...pinned, ...base, ...custom, ...visibleFooters]
			: [...visibleExtras, ...visibleAll, ...pinned, ...visible, ...visibleFooters];
		return rows.filter((o) => !o.disabled).map((o) => o.value);
	}, [groupByCustom, visibleExtras, visibleAll, pinned, base, custom, visible, visibleFooters]);

	/*
	 * cmdk re-points the highlight only when the row holding it is the one unmounting. Typing
	 * swaps out every row at once, and cmdk's scheduler keeps just the last unmount callback,
	 * so the highlight is left on a row that no longer exists: arrow keys have nothing to step
	 * from and Enter has nothing to fire. So it is held here and re-pointed at the first row
	 * whenever the one it names goes away.
	 */
	const [highlighted, setHighlighted] = useState("");
	useEffect(() => {
		setHighlighted((current) => (current && navigableValues.includes(current) ? current : (navigableValues[0] ?? "")));
	}, [navigableValues]);

	const closeAndResetSearch = useCallback(() => {
		setOpen(false);
		setSearch("");
	}, []);

	const commit = useCallback(
		(next: string[]) => {
			if (isMulti) {
				(onChange as (providers: string[]) => void)(next);
				return;
			}
			(onChange as (provider: string) => void)(next[0] ?? "");
			closeAndResetSearch();
		},
		[isMulti, onChange, closeAndResetSearch],
	);

	const toggle = useCallback(
		(value: string) => {
			if (isAdd) {
				onSelect?.(value);
				closeAndResetSearch();
				return;
			}
			if (!isMulti) {
				commit(selected.includes(value) ? [] : [value]);
				return;
			}
			commit(selected.includes(value) ? selected.filter((v) => v !== value) : [...selected, value]);
		},
		[isAdd, isMulti, onSelect, commit, selected, closeAndResetSearch],
	);

	// Backspace on an empty search field drops the last chip, the way a tag input behaves.
	// Only with the field empty, so it never eats a character the user meant to erase.
	const handleInputKeyDown = useCallback(
		(e: React.KeyboardEvent<HTMLInputElement>) => {
			if (e.key !== "Backspace" || search.length > 0 || !isMulti || selected.length === 0) return;
			e.preventDefault();
			commit(selected.slice(0, -1));
		},
		[commit, isMulti, search, selected],
	);

	const handleOpenChange = useCallback(
		(next: boolean) => {
			if (!next) {
				closeAndResetSearch();
				return;
			}
			setOpen(true);
		},
		[closeAndResetSearch],
	);

	const optionFor = useCallback(
		(value: string): ProviderSelectorOption =>
			[...(extraOptions ?? []), ...(footerOptions ?? []), ...visibleAll, ...options, ...pinned].find((o) => o.value === value) ?? {
				value,
				label: getProviderLabel(value),
				iconKey: resolveProviderIconKey(value),
			},
		[extraOptions, footerOptions, visibleAll, options, pinned],
	);

	const renderOption = (option: ProviderSelectorOption) => (
		<ComboboxItem
			key={option.value}
			value={option.value}
			disabled={option.disabled}
			data-testid={optionTestId?.(option.value)}
			onSelect={() => {
				if (option.disabled) return;
				toggle(option.value);
			}}
			className={cn(option.disabled && "cursor-not-allowed")}
		>
			{option.icon ?? (option.iconKey && <RenderProviderIcon provider={option.iconKey} size="sm" className="h-4 w-4" />)}
			<span className={cn("min-w-0 grow truncate", option.disabled && "text-muted-foreground")}>{option.label}</span>
			{option.disabled && option.disabledReason && (
				<Badge variant="outline" className="shrink-0 text-[10px] font-medium tracking-wide uppercase">
					{option.disabledReason}
				</Badge>
			)}
		</ComboboxItem>
	);

	const renderTriggerValue = (value: string) => {
		if (renderValueLabel) return renderValueLabel(value);
		const option = optionFor(value);
		return (
			<>
				{option.icon ?? (option.iconKey && <RenderProviderIcon provider={option.iconKey} size="sm" className="h-4 w-4" />)}
				<span className="min-w-0 truncate">{option.label}</span>
			</>
		);
	};

	return (
		<Combobox
			multiple={isMulti}
			value={isMulti ? selected : (selected[0] ?? null)}
			open={open}
			onOpenChange={handleOpenChange}
			inputValue={search}
			filter={null}
			onInputValueChange={setSearch}
		>
			<PopoverTrigger asChild disabled={disabled}>
				{addTrigger ?? (
					<Button
						variant="outline"
						role="combobox"
						disabled={disabled}
						id={inputId}
						aria-labelledby={ariaLabelledBy}
						aria-describedby={ariaDescribedBy}
						aria-invalid={ariaInvalid}
						data-testid={props["data-testid"] ?? "provider-selector-trigger"}
						className={cn(
							"w-full justify-between !bg-transparent font-normal active:scale-none",
							size === "sm" ? "h-auto min-h-8 py-1 text-xs" : "h-auto min-h-9 py-1.5",
							selected.length === 0 && "text-muted-foreground",
							className,
						)}
					>
						{/* Multi keeps every chip on the trigger: the row wraps and the trigger grows to fit. */}
						{selected.length === 0 ? (
							<span className="min-w-0 truncate">{placeholder}</span>
						) : isMulti ? (
							<span className="flex min-w-0 flex-1 flex-wrap items-center gap-1">
								{selected.map((value) => (
									<span key={value} className="bg-accent dark:bg-card flex max-w-[180px] items-center gap-1 rounded-sm px-1 py-0.5 text-xs">
										{renderTriggerValue(value)}
										{/*
										 * The handler sits on the span, not the icon: the trigger is a Button, and
										 * its variants set [&_svg]:pointer-events-none, so an icon inside it never
										 * sees a click. Being transparent to hit testing, the icon passes the click
										 * to this span instead. A nested <button> would be invalid inside the
										 * trigger, hence role and tabIndex here.
										 */}
										<span
											role="button"
											tabIndex={-1}
											aria-label={`Remove ${optionFor(value).label}`}
											className="text-muted-foreground hover:text-foreground flex shrink-0 cursor-pointer items-center"
											onClick={(e) => {
												e.preventDefault();
												e.stopPropagation();
												toggle(value);
											}}
										>
											<XIcon className="size-3" />
										</span>
									</span>
								))}
							</span>
						) : (
							<span className="flex min-w-0 items-center gap-2">{renderTriggerValue(selected[0])}</span>
						)}
						<ChevronDownIcon className="ml-2 size-4 shrink-0 opacity-50" />
					</Button>
				)}
			</PopoverTrigger>

			{/* The list is complete and already ordered here, so cmdk is told not to rank it. */}
			<ComboboxContent
				data-testid={contentTestId}
				noPortalForContent={noPortal}
				className={cn("min-w-[var(--radix-popover-trigger-width)]", contentClassName)}
				// Radix measures the room it has each time it positions, so the clamp holds wherever the trigger sits.
				style={contentWidth === undefined ? undefined : { width: contentWidth, maxWidth: "var(--radix-popper-available-width)" }}
				shouldFilter={false}
				highlightedValue={highlighted}
				onHighlightedValueChange={setHighlighted}
			>
				<ComboboxList searchPlaceholder={searchPlaceholder} showSearchIcon className="max-h-[300px]" onInputKeyDown={handleInputKeyDown}>
					{visibleExtras.map(renderOption)}
					{visibleAll.map(renderOption)}

					{pinned.length > 0 && (
						<ComboboxGroup>
							<ComboboxLabel>Selected</ComboboxLabel>
							{pinned.map(renderOption)}
						</ComboboxGroup>
					)}

					{groupByCustom ? (
						<>
							{/* Only the custom ones are called out; the rest need no heading to be read as providers. */}
							{base.map(renderOption)}
							{custom.length > 0 && (
								<ComboboxGroup>
									<ComboboxLabel>Custom Providers</ComboboxLabel>
									{custom.map(renderOption)}
								</ComboboxGroup>
							)}
						</>
					) : (
						visible.map(renderOption)
					)}

					{visibleFooters.map(renderOption)}

					{isLoading ? (
						<div className="text-muted-foreground flex items-center justify-center gap-2 py-6 text-sm">
							<Loader2Icon className="size-3.5 animate-spin" />
							Loading providers…
						</div>
					) : (
						<ComboboxEmpty className="text-muted-foreground">
							{isError ? "Couldn't load providers." : (emptyMessage ?? (search ? "No matching providers." : "No providers available."))}
						</ComboboxEmpty>
					)}
				</ComboboxList>
			</ComboboxContent>
		</Combobox>
	);
}