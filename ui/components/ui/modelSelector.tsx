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
import { useDebouncedValue } from "@/hooks/useDebounce";
import { getProviderLabel } from "@/lib/constants/logs";
import { useGetBaseModelsQuery, useGetModelsQuery } from "@/lib/store/apis/providersApi";
import { cn } from "@/lib/utils";
import { ChevronDownIcon, Loader2Icon, PlusIcon, SearchIcon, XIcon } from "lucide-react";
import type React from "react";
import { useCallback, useEffect, useMemo, useState } from "react";
import { Trans, useTranslation } from "react-i18next";

/** One row in the dropdown, after the consumer has had a say in it. */
export interface ModelSelectorOption {
	value: string;
	label: string;
	provider?: string;
	isDeprecated?: boolean;
	/** Non-selectable. The row still renders, greyed, with `disabledReason` beside it. */
	disabled?: boolean;
	/** Short phrase shown on the row explaining why it cannot be picked, e.g. "Deprecated". */
	disabledReason?: string;
}

interface ModelSelectorBaseProps {
	/** Scopes the search to one provider. Omitted, the search spans every configured provider. */
	provider?: string;
	/** Provider key IDs to narrow the listing by. */
	keys?: string[];
	/** Virtual key IDs to narrow the listing by. */
	vks?: string[];
	/** Bypasses the provider-level model pool, matching the /api/models `unfiltered` flag. */
	unfiltered?: boolean;
	/**
	 * Search on the server (default). Set false to pull one page up front and filter it in
	 * the browser — worth it only for small, fixed pools.
	 */
	serverSearch?: boolean;
	/** Rows per fetch. Also the size of each "Load more" step. */
	pageSize?: number;
	placeholder?: string;
	searchPlaceholder?: string;
	disabled?: boolean;
	className?: string;
	contentClassName?: string;
	/**
	 * Widens the dropdown past the trigger, for a narrow trigger with long model names.
	 * A number is px. Always clamped to the room radix reports, so it never leaves the
	 * viewport. Left out, the dropdown matches the trigger.
	 */
	contentWidth?: number | string;
	/**
	 * With no provider selected, list distinct base model names instead of every provider's
	 * catalog. For cross-provider matching, where "gpt-4o" should appear once.
	 */
	baseModelsWithoutProvider?: boolean;
	/** Lets the user commit whatever they typed as a model name. */
	allowCustomModel?: boolean;
	/** Renders deprecated models as non-selectable. On by default. */
	disableDeprecated?: boolean;
	/**
	 * Last word on whether a row is selectable. Return `{ disabled, disabledReason }` to
	 * override the default (deprecated) rule, or nothing to leave the row alone.
	 */
	getOptionState?: (option: ModelSelectorOption) => { disabled?: boolean; disabledReason?: string } | undefined | void;
	emptyMessage?: string;
	/**
	 * Rows that do not come from the catalog, such as a wildcard. They sit above the fetched
	 * ones, keep that slot whatever the server returns, and are matched against the search
	 * term here rather than on the server.
	 */
	extraOptions?: ModelSelectorOption[];
	/** Custom rendering for a selected value on the trigger. Defaults to the plain value. */
	renderValueLabel?: (value: string) => React.ReactNode;
	/** Drops the magnifier from the trigger. The one in the search field stays either way. */
	hideSearchIcon?: boolean;
	/** id for the trigger, so a form label and its error message can point at it. */
	inputId?: string;
	ariaLabelledBy?: string;
	ariaDescribedBy?: string;
	ariaInvalid?: boolean;
	"data-testid"?: string;
}

interface ModelSelectorSingleProps extends ModelSelectorBaseProps {
	multiple?: false;
	value: string;
	onChange: (model: string) => void;
}

interface ModelSelectorMultiProps extends ModelSelectorBaseProps {
	multiple: true;
	value: string[];
	onChange: (models: string[]) => void;
}

export type ModelSelectorProps = ModelSelectorSingleProps | ModelSelectorMultiProps;

/**
 * The wildcard row, for the surfaces whose list accepts "*" as "every model". Ready to hand
 * to `extraOptions`; module level so its identity is stable across renders.
 */
export const ALL_MODELS_OPTION: ModelSelectorOption[] = [{ value: "*", label: "All Models" }];

/** A catalog row. Base models carry only a name, so everything past it is optional. */
interface ListedModel {
	name: string;
	provider?: string;
	is_deprecated?: boolean;
}

const DEFAULT_PAGE_SIZE = 20;
const CUSTOM_VALUE_PREFIX = "__custom__";

/** Substring match, used only when serverSearch is off. */
function clientFilter(value: string, search: string): number {
	if (!search) return 1;
	return value.toLowerCase().includes(search.toLowerCase()) ? 1 : 0;
}

export function ModelSelector(props: ModelSelectorProps) {
	const { t } = useTranslation("common");
	const {
		provider,
		keys,
		vks,
		unfiltered = false,
		serverSearch = true,
		pageSize = DEFAULT_PAGE_SIZE,
		placeholder: placeholderProp,
		searchPlaceholder: searchPlaceholderProp,
		disabled = false,
		className,
		contentClassName,
		contentWidth,
		baseModelsWithoutProvider = false,
		allowCustomModel = false,
		disableDeprecated = true,
		getOptionState,
		emptyMessage,
		extraOptions,
		renderValueLabel,
		hideSearchIcon = false,
		inputId,
		ariaLabelledBy,
		ariaDescribedBy,
		ariaInvalid,
	} = props;
	const placeholder = placeholderProp ?? t("modelAccess.selectModel");
	const searchPlaceholder = searchPlaceholderProp ?? t("modelAccess.searchModels");

	const isMulti = props.multiple === true;
	const onChange = props.onChange;
	const selected = useMemo<string[]>(
		() => (isMulti ? ((props.value as string[]) ?? []) : props.value ? [props.value as string] : []),
		[isMulti, props.value],
	);

	const [open, setOpen] = useState(false);
	const [hasOpened, setHasOpened] = useState(false);
	const [search, setSearch] = useState("");
	const debouncedSearch = useDebouncedValue(search, 300);
	const [pages, setPages] = useState(1);
	/*
	 * What the field was opened with, when single select seeds it with the current selection.
	 * Held so the seed can skip the debounce: it is not something the user is in the middle of
	 * typing, so waiting 300ms would only show one page of the whole catalog and then replace it.
	 */
	const [seededSearch, setSeededSearch] = useState("");

	// Server-side search resets paging; client-side search does not, because the whole
	// page is already in the browser.
	const effectiveSearch = serverSearch ? (search === seededSearch ? search : (debouncedSearch as string)) : "";
	useEffect(() => {
		setPages(1);
	}, [effectiveSearch, provider, unfiltered]);

	// Base models are the same catalog with the provider dimension collapsed, so only one of
	// the two ever runs; the rest of the component works off whichever answered.
	const useBaseModels = baseModelsWithoutProvider && !provider;
	const idle = disabled || !hasOpened;

	const catalog = useGetModelsQuery(
		{
			query: effectiveSearch || undefined,
			provider: provider || undefined,
			keys: keys && keys.length > 0 ? keys : undefined,
			vks: vks && vks.length > 0 ? vks : undefined,
			limit: pageSize * pages,
			unfiltered,
		},
		{ skip: idle || useBaseModels },
	);
	const baseCatalog = useGetBaseModelsQuery(
		{ query: effectiveSearch || undefined, limit: pageSize * pages },
		{ skip: idle || !useBaseModels },
	);

	const { isFetching, isError } = useBaseModels ? baseCatalog : catalog;
	const rows = useMemo<ListedModel[]>(
		() => (useBaseModels ? (baseCatalog.data?.models ?? []).map((name) => ({ name })) : (catalog.data?.models ?? [])),
		[useBaseModels, baseCatalog.data, catalog.data],
	);
	const total = (useBaseModels ? baseCatalog.data?.total : catalog.data?.total) ?? 0;

	const isSearching = isFetching || (serverSearch && search !== debouncedSearch && search !== seededSearch);

	const options = useMemo<ModelSelectorOption[]>(() => {
		const mapped = rows.map((model) => {
			const base: ModelSelectorOption = {
				value: model.name,
				label: model.name,
				provider: model.provider,
				isDeprecated: model.is_deprecated,
			};
			if (disableDeprecated && model.is_deprecated) {
				base.disabled = true;
				base.disabledReason = t("modelAccess.deprecated");
			}
			const override = getOptionState?.(base);
			return override ? { ...base, ...override } : base;
		});
		// The server already sinks deprecated models in a search, but a row can also be
		// disabled by the consumer, and the two have to end up in one order. A row nobody
		// can pick never sits above one they can.
		return mapped.sort((a, b) => Number(Boolean(a.disabled)) - Number(Boolean(b.disabled)));
	}, [rows, disableDeprecated, getOptionState, t]);

	const extraValues = useMemo(() => new Set((extraOptions ?? []).map((o) => o.value)), [extraOptions]);

	// Nothing on the server knows about these rows, so the search term is applied here, over
	// the label as well as the value: "*" is found by typing "all".
	const visibleExtras = useMemo<ModelSelectorOption[]>(() => {
		const term = search.trim().toLowerCase();
		if (!term) return extraOptions ?? [];
		return (extraOptions ?? []).filter((o) => o.label.toLowerCase().includes(term) || o.value.toLowerCase().includes(term));
	}, [extraOptions, search]);

	// A selected model can sit outside the loaded page — it was picked under a different
	// search, or saved before the catalog changed. Pin it so it stays visible and
	// deselectable instead of silently vanishing from the list.
	const pinned = useMemo<ModelSelectorOption[]>(() => {
		const loaded = new Set(options.map((o) => o.value));
		return selected.filter((v) => !loaded.has(v) && !extraValues.has(v)).map((v) => ({ value: v, label: v }));
	}, [options, selected, extraValues]);

	const loadedCount = rows.length + pinned.length + visibleExtras.length;
	const hasMore = rows.length < total;

	const demoted = useMemo(() => new Set(options.filter((o) => o.disabled).map((o) => o.value)), [options]);

	// Only consulted when cmdk is doing the filtering, which is the client-search mode.
	// Ranks what cannot be picked below what can, and the create option below both.
	const filter = useCallback(
		(value: string, term: string) => {
			if (value.startsWith(CUSTOM_VALUE_PREFIX)) return 0.5;
			// Extra rows were already matched against the term above, by label as well as value.
			if (extraValues.has(value)) return 1;
			const score = clientFilter(value, term);
			if (score === 0) return 0;
			return demoted.has(value) ? 0.75 : score;
		},
		[demoted, extraValues],
	);

	const trimmedSearch = search.trim();
	const exactMatch = options.some((o) => o.value === trimmedSearch) || extraValues.has(trimmedSearch) || selected.includes(trimmedSearch);
	const showCustomEntry = allowCustomModel && trimmedSearch.length > 0 && !exactMatch;

	// Every row the arrow keys can land on, in the order they are rendered.
	const navigableValues = useMemo(() => {
		const values = [...visibleExtras, ...pinned, ...options].filter((o) => !o.disabled).map((o) => o.value);
		if (showCustomEntry) values.push(`${CUSTOM_VALUE_PREFIX}${trimmedSearch}`);
		return values;
	}, [visibleExtras, pinned, options, showCustomEntry, trimmedSearch]);

	/*
	 * cmdk normally tracks the highlight itself, but it re-points it only when the row that
	 * was highlighted is the one unmounting. Under server search a response swaps out every
	 * row at once, and cmdk's scheduler keeps just the last unmount callback, so the highlight
	 * is left on a row that no longer exists: arrow keys have nothing to step from and Enter
	 * has nothing to fire. So the highlight is held here and re-pointed at the first row
	 * whenever the one it names goes away.
	 */
	const [highlighted, setHighlighted] = useState("");
	useEffect(() => {
		setHighlighted((current) => (current && navigableValues.includes(current) ? current : (navigableValues[0] ?? "")));
	}, [navigableValues]);

	// Multi-select keeps the search between picks, so several matches can be taken in one
	// pass; it only resets once the list is closed.
	const closeAndResetSearch = useCallback(() => {
		setOpen(false);
		setSearch("");
		setSeededSearch("");
	}, []);

	const commit = useCallback(
		(next: string[]) => {
			if (isMulti) {
				(onChange as (models: string[]) => void)(next);
			} else {
				(onChange as (model: string) => void)(next[0] ?? "");
				closeAndResetSearch();
			}
		},
		[isMulti, onChange, closeAndResetSearch],
	);

	const toggle = useCallback(
		(value: string) => {
			if (!isMulti) {
				commit(selected.includes(value) ? [] : [value]);
				return;
			}
			commit(selected.includes(value) ? selected.filter((v) => v !== value) : [...selected, value]);
		},
		[commit, isMulti, selected],
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
			/*
			 * Single select opens with the current model already in the search field, caret at the
			 * end. Picking its sibling is then a matter of typing the bit that differs
			 * ("claude-fable-5" → ".1") or trimming a few characters, instead of retyping a name the
			 * component already knows — which is the whole job when the models on offer share a
			 * long prefix, or when the value is a custom name being corrected. The X in the field
			 * gets back to the full list in one click. Multi keeps its empty field: its search is
			 * for the next pick, not the ones already made.
			 */
			const seed = !isMulti && selected[0] && !extraValues.has(selected[0]) ? selected[0] : "";
			setSeededSearch(seed);
			setSearch(seed);
			setOpen(true);
			setHasOpened(true);
		},
		[closeAndResetSearch, isMulti, selected, extraValues],
	);

	// Paging happens on scroll rather than through a button, so the list stays a list.
	const handleListScroll = useCallback(
		(e: React.UIEvent<HTMLDivElement>) => {
			if (!hasMore || isFetching) return;
			const el = e.currentTarget;
			if (el.scrollHeight - el.scrollTop - el.clientHeight < 48) {
				setPages((p) => p + 1);
			}
		},
		[hasMore, isFetching],
	);

	const renderOption = (option: ModelSelectorOption) => (
		<ComboboxItem
			key={option.value}
			value={option.value}
			disabled={option.disabled}
			onSelect={() => {
				if (option.disabled) return;
				toggle(option.value);
			}}
			className={cn(option.disabled && "cursor-not-allowed")}
		>
			<span className={cn("min-w-0 grow truncate", option.disabled && "text-muted-foreground")}>{option.label}</span>
			{!provider && option.provider && <span className="text-muted-foreground shrink-0 text-xs">{getProviderLabel(option.provider)}</span>}
			{option.disabled && option.disabledReason && (
				<Badge variant="outline" className="shrink-0 text-[10px] font-medium tracking-wide uppercase">
					{option.disabledReason}
				</Badge>
			)}
		</ComboboxItem>
	);

	// An extra row carries a label the value does not, "*" reading as "All Models", and the
	// trigger has to say the same thing the list did.
	const labelFor = (value: string) => extraOptions?.find((o) => o.value === value)?.label ?? value;

	return (
		<Combobox
			multiple={isMulti}
			value={isMulti ? selected : (selected[0] ?? null)}
			open={open}
			onOpenChange={handleOpenChange}
			inputValue={search}
			filter={filter}
			onInputValueChange={setSearch}
		>
			<PopoverTrigger asChild disabled={disabled}>
				<Button
					variant="outline"
					role="combobox"
					disabled={disabled}
					id={inputId}
					aria-labelledby={ariaLabelledBy}
					aria-describedby={ariaDescribedBy}
					aria-invalid={ariaInvalid}
					data-testid={props["data-testid"] ?? "model-selector-trigger"}
					className={cn(
						"h-auto min-h-9 w-full justify-between !bg-transparent py-1.5 font-normal active:scale-none",
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
									{renderValueLabel ? renderValueLabel(value) : <span className="min-w-0 truncate">{labelFor(value)}</span>}
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
										aria-label={`Remove ${labelFor(value)}`}
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
					) : renderValueLabel ? (
						renderValueLabel(selected[0])
					) : (
						<span className="min-w-0 truncate">{labelFor(selected[0])}</span>
					)}
					<span className="ml-2 flex shrink-0 items-center gap-1">
						{/* The magnifier on the closed trigger is what tells the user this opens into a search. */}
						{!hideSearchIcon && <SearchIcon className="size-3.5 opacity-50" />}
						<ChevronDownIcon className="size-4 opacity-50" />
					</span>
				</Button>
			</PopoverTrigger>

			{/*
			 * cmdk scores an item once, when it registers, and never revisits it. Under server
			 * search the rows register while the response for the new term is still in flight,
			 * so they are all scored against the old list and then sorted by those stale scores
			 * — which is what floated deprecated models back to the top. The server has already
			 * decided both membership and order here, so cmdk is told not to rank at all.
			 */}
			<ComboboxContent
				className={cn("min-w-[var(--radix-popover-trigger-width)]", contentClassName)}
				// Radix measures the room it has each time it positions, so the clamp holds wherever the trigger sits.
				style={contentWidth === undefined ? undefined : { width: contentWidth, maxWidth: "var(--radix-popper-available-width)" }}
				shouldFilter={!serverSearch}
				highlightedValue={highlighted}
				onHighlightedValueChange={setHighlighted}
			>
				<ComboboxList
					searchPlaceholder={searchPlaceholder}
					showSearchIcon
					isSearching={isSearching}
					className="max-h-[300px]"
					onScroll={handleListScroll}
					onInputKeyDown={handleInputKeyDown}
					clearableSearch
				>
					{visibleExtras.map(renderOption)}

					{pinned.length > 0 && (
						<ComboboxGroup>
							<ComboboxLabel>Selected</ComboboxLabel>
							{pinned.map(renderOption)}
						</ComboboxGroup>
					)}

					{options.map(renderOption)}

					{/* Last, so it never pushes a real match down the list. */}
					{showCustomEntry && (
						<ComboboxItem value={`${CUSTOM_VALUE_PREFIX}${trimmedSearch}`} onSelect={() => toggle(trimmedSearch)}>
							<PlusIcon className="size-3.5 shrink-0 opacity-60" />
							<span className="min-w-0 grow truncate">
								<Trans
									t={t}
									i18nKey="modelAccess.useModel"
									values={{ name: trimmedSearch }}
									components={{ name: <span className="font-medium" /> }}
								/>
							</span>
						</ComboboxItem>
					)}

					{/* cmdk decides when this shows, which also covers client-side filtering to zero. */}
					{!isSearching && (
						<ComboboxEmpty className="text-muted-foreground">
							{isError
								? t("modelAccess.couldntLoad")
								: (emptyMessage ?? (search ? t("modelAccess.noMatching") : t("modelAccess.noneAvailable")))}
						</ComboboxEmpty>
					)}

					{isSearching && loadedCount === 0 && (
						<div className="text-muted-foreground flex items-center justify-center gap-2 py-6 text-sm">
							<Loader2Icon className="size-3.5 animate-spin" />
							{t("modelAccess.searching")}
						</div>
					)}

					{hasMore && isFetching && (
						<div className="text-muted-foreground flex items-center justify-center gap-2 py-2 text-xs">
							<Loader2Icon className="size-3 animate-spin" />
							{t("modelAccess.loadingMore")}
						</div>
					)}
				</ComboboxList>
			</ComboboxContent>
		</Combobox>
	);
}