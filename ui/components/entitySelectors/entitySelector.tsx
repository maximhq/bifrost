// Shared shell behind every async entity selector (virtual keys, users,
// teams, customers, business units).
//
// Owns everything that is identical across entities: the three selection
// modes, the label cache that keeps an already-selected id from rendering as
// a raw UUID, and the search wiring.
//
// Two presentations, picked by mode:
//   - single / add -> SearchSelect (a popover behind a button trigger)
//   - multi        -> Combobox with chips on the trigger, matching ModelSelector.
// Fetching, debouncing and label resolution are identical either way.
//
// Each entity still ships a thin wrapper, because the parts that genuinely
// differ — which endpoint to call, whether it pages by offset or page number,
// and which field is the label — aren't worth abstracting over. A wrapper
// owns its query and its debounce and hands the results down here.
//
// Lives in OSS so the enterprise-only selectors can build on it too.

import { CheckIcon, ChevronDownIcon, Loader2Icon, PlusIcon, SearchIcon, XIcon } from "lucide-react";
import type React from "react";
import { type ComponentType, type ReactNode, useCallback, useEffect, useMemo, useRef, useState } from "react";
import type { ParseKeys } from "i18next";
import { useTranslation } from "react-i18next";

const ENTITY_NOUN_KEYS: Partial<Record<string, ParseKeys<"common">>> = {
	team: "entities.team",
	teams: "entities.teams",
	customer: "entities.customer",
	customers: "entities.customers",
	"virtual key": "entities.virtualKey",
	"virtual keys": "entities.virtualKeys",
	"MCP server": "entities.mcpServer",
	"MCP servers": "entities.mcpServers",
	"Virtual MCP": "entities.virtualMcp",
	"Virtual MCPs": "entities.virtualMcps",
	agent: "entities.agent",
	agents: "entities.agents",
	relay: "entities.relay",
	relays: "entities.relays",
};

import { Button } from "@/components/ui/button";
import { Combobox, ComboboxContent, ComboboxGroup, ComboboxItem, ComboboxLabel, ComboboxList } from "@/components/ui/combobox";
import { PopoverTrigger } from "@/components/ui/popover";
import { SearchSelect } from "@/components/ui/searchSelect";
import { cn } from "@/lib/utils";

import {
	accumulatePages,
	chipsAfterBackspace,
	EMPTY_ENTITY_PAGES,
	hasMoreEntities,
	loadMoreNeedsRefetch,
	multiListState,
	shouldAutoLoadMore,
	showsLoadedRows,
} from "./entitySelectorPaging";

export const ENTITY_SEARCH_DEBOUNCE_MS = 300;
export const ENTITY_SELECTOR_PAGE_SIZE = 20;

export interface EntitySelectorOption {
	value: string;
	label: string;
}

/** One row in the dropdown. `description` renders as a dimmed sub-label. */
export interface EntitySelectorEntry extends EntitySelectorOption {
	description?: string;
}

/**
 * Fetches one entity by id and reports its label, then renders nothing.
 *
 * A selector loads its first page only when the popover opens, so ids handed
 * in by the caller — the row being edited, ids read off a URL — have no label
 * until then and would otherwise render as raw UUIDs. There is no batch
 * by-ids filter on any of these endpoints, so resolution is one request per
 * id; a component per id is what lets each one own a hook.
 */
export interface EntityLabelResolverProps {
	id: string;
	onResolved: (option: EntitySelectorOption) => void;
}

/**
 * Props every wrapper re-exposes verbatim, so callers see the same surface
 * whichever entity they are picking.
 */
export interface EntitySelectorCommonProps {
	disabled?: boolean;
	placeholder?: string;
	className?: string;
	/** Id for the default single/add trigger, so a `<label htmlFor>` names it. A caller-supplied `trigger` keeps its own. */
	id?: string;
	/**
	 * Guarantees an already-selected entity renders a real label even when it
	 * falls outside the fetched search results — callers pass the edited row's
	 * own id/name. Accepts one option (single mode) or several (multi).
	 */
	fallbackOption?: EntitySelectorOption | null;
	fallbackOptions?: EntitySelectorOption[] | null;
	/** Rendered inside a Sheet/Dialog by default, so portalling is off. Single/add only. */
	noPortal?: boolean;
	/**
	 * Extra classes for the default combobox trigger — e.g. `h-9` to line up
	 * with a taller control beside it. Ignored when `trigger` is supplied.
	 * In multi mode the trigger grows with its chips, so pass a min-height
	 * (e.g. `!min-h-9`) rather than a fixed height.
	 */
	triggerClassName?: string;
	/** Icon shown before the label in the default combobox trigger. Single mode only. */
	triggerIcon?: ReactNode;
	/**
	 * Overrides the popover width, which otherwise matches the trigger. For a
	 * narrow trigger whose options need more room than it has. Single/add only.
	 */
	contentClassName?: string;
	/** Ids to omit from the results — e.g. entities already added to a list. */
	excludeIds?: string[];
	/**
	 * Replaces the default full-width combobox trigger. Surfaces that already
	 * show the selection elsewhere (a table, a chip row) pass a compact button
	 * instead; the trigger then renders no selected label of its own.
	 * Single/add only — multi mode renders its chips inside the control.
	 */
	trigger?: ReactNode;
	/** Add mode only: render the trigger full-width instead of the compact inline default. */
	fullWidth?: boolean;
	/**
	 * Pinned below the option list — for the action a search that found nothing
	 * leads to, typically "create a new one". Single/add only.
	 */
	footer?: ReactNode;
	/** Multi mode: data-testid for the trigger; chip X gets `<testid>-chip-remove-<id>`, clear-all `<testid>-clear`, Retry `<testid>-retry`. */
	"data-testid"?: string;
}

interface EntitySelectorSingleProps {
	mode?: undefined;
	multiple?: false;
	value: string;
	onChange: (value: string) => void;
}

interface EntitySelectorMultiProps {
	mode?: undefined;
	multiple: true;
	value: string[];
	onChange: (value: string[]) => void;
}

/**
 * Fire-and-forget "add" mode: the selector holds no value, and each pick is
 * reported once. For surfaces that own their own collection — e.g. the MCP
 * client sheet, where picking a key appends a row with its own tool config
 * and removal happens in the table, not here.
 */
interface EntitySelectorAddProps {
	mode: "add";
	/** The picked row, description included, so the caller can show more than its label. */
	onSelect: (option: EntitySelectorEntry) => void;
	multiple?: never;
	value?: never;
	onChange?: never;
}

/**
 * The selection contract, shared so wrappers can intersect it with their own
 * options rather than restating all three variants.
 */
export type EntitySelectorModeProps = EntitySelectorSingleProps | EntitySelectorMultiProps | EntitySelectorAddProps;

interface EntitySelectorChromeProps {
	/** Lowercase noun used to build placeholders and empty/error copy. */
	entityLabel: string;
	entityLabelPlural: string;
	/** Memoize in the wrapper; effects key off its identity. */
	options: EntitySelectorEntry[];
	isFetching: boolean;
	isError: boolean;
	open: boolean;
	onOpenChange: (open: boolean) => void;
	onSearchChange: (search: string) => void;
	/** The settled search the current `options` correspond to. */
	debouncedSearch: string;
	/** True while the wrapper's debounce is still settling, or a fetch is in flight for a search. */
	isSearching: boolean;
	/** More rows exist on the server than `options` holds. */
	hasMore?: boolean;
	/** Fetches the next page. Called on scroll, and while `excludeIds` leaves the list short. */
	onLoadMore?: () => void;
	isLoadingMore?: boolean;
	/** Retries the last failed request. */
	onRetry?: () => void;
	/** The search `options` were fetched for. Differs from `debouncedSearch` until the new search's first page lands. */
	optionsSearch?: string;
	searchPlaceholder?: string;
	/**
	 * Supplied by the wrapper that knows the by-id endpoint. Rendered once per
	 * selected id whose label is still unknown — in every mode, so multi-select
	 * chips resolve the same way a single trigger does.
	 */
	LabelResolver?: ComponentType<EntityLabelResolverProps>;
}

export type EntitySelectorProps = EntitySelectorChromeProps & EntitySelectorCommonProps & EntitySelectorModeProps;

export function EntitySelector(props: EntitySelectorProps) {
	const { t } = useTranslation("common");
	const {
		entityLabel,
		entityLabelPlural,
		options,
		isFetching,
		isError,
		open,
		onOpenChange,
		onSearchChange,
		debouncedSearch,
		isSearching,
		hasMore = false,
		onLoadMore,
		isLoadingMore = false,
		onRetry,
		optionsSearch,
		searchPlaceholder,
		LabelResolver,
		disabled = false,
		id,
		placeholder,
		className,
		fallbackOption,
		fallbackOptions,
		noPortal = true,
		triggerClassName,
		triggerIcon,
		contentClassName,
		excludeIds,
		trigger,
		fullWidth = false,
		footer,
		"data-testid": testId,
	} = props;

	const isAdd = props.mode === "add";
	const isMulti = props.multiple === true;
	const selectedIds = useMemo(() => {
		if (isAdd) return [];
		return props.multiple === true ? props.value : props.value ? [props.value] : [];
	}, [isAdd, props.multiple, props.value]);

	// Labels of entities seen so far. Results change as the user types and are
	// dropped entirely once the popover closes, so selected ids can't rely on
	// being present in `options` — the cache is what keeps their labels stable.
	const [labelCache, setLabelCache] = useState<Record<string, string>>({});

	useEffect(() => {
		if (options.length === 0) return;
		setLabelCache((prev) => {
			let changed = false;
			const next = { ...prev };
			for (const option of options) {
				if (next[option.value] !== option.label) {
					next[option.value] = option.label;
					changed = true;
				}
			}
			return changed ? next : prev;
		});
	}, [options]);

	const fallbackLabels = useMemo(() => {
		const entries = [...(fallbackOption ? [fallbackOption] : []), ...(fallbackOptions ?? [])];
		// A fallback whose label is just the id names nothing — the common shape
		// when a surface holds only ids. Drop it so the resolver still runs.
		return Object.fromEntries(entries.filter((o) => o.label && o.label !== o.value).map((o) => [o.value, o.label]));
	}, [fallbackOption, fallbackOptions]);

	// Falls back to the raw id only when no label is known from any source.
	const labelFor = (id: string) => labelCache[id] ?? fallbackLabels[id] ?? id;

	const cacheLabel = useCallback((option: EntitySelectorOption) => {
		setLabelCache((prev) => (prev[option.value] === option.label ? prev : { ...prev, [option.value]: option.label }));
	}, []);

	// Each resolver unmounts as soon as it reports, since its id leaves this
	// list; the label it cached is what keeps it from being fetched again.
	const unresolvedIds = useMemo(
		() => (LabelResolver ? selectedIds.filter((id) => !labelCache[id] && !fallbackLabels[id]) : []),
		[LabelResolver, selectedIds, labelCache, fallbackLabels],
	);

	const labelResolvers = LabelResolver ? unresolvedIds.map((id) => <LabelResolver key={id} id={id} onResolved={cacheLabel} />) : null;

	// Array identity has to survive renders, so key off the contents.
	const excludeKey = excludeIds?.join(" ") ?? "";
	const visibleOptions = useMemo(
		() => (excludeKey ? options.filter((option) => !excludeKey.split(" ").includes(option.value)) : options),
		[options, excludeKey],
	);

	// Excluded rows still take up slots in each fetched page, so a picker whose
	// first page is all already-added rows would look empty with more left on
	// the server. Keep fetching until there is a page's worth to show. A short
	// list can't be scrolled either, so this is also what reaches the rest.
	const loadMore = hasMore ? onLoadMore : undefined;
	useEffect(() => {
		const visibleCount = visibleOptions.length;
		if (shouldAutoLoadMore({ open, hasMore: !!loadMore, isFetching, isError, visibleCount, pageSize: ENTITY_SELECTOR_PAGE_SIZE }))
			loadMore?.();
	}, [open, loadMore, isFetching, isError, visibleOptions.length]);

	// Only reached by the single/add presentation; multi mode toggles through toggleMulti.
	const handleSelect = (option: EntitySelectorEntry) => {
		cacheLabel(option);

		if (props.mode === "add") {
			props.onSelect(option);
		} else if (props.multiple !== true) {
			props.onChange(option.value);
		}
		onOpenChange(false);
	};

	const entityKey = ENTITY_NOUN_KEYS[entityLabel];
	const entitiesKey = ENTITY_NOUN_KEYS[entityLabelPlural];
	const entity = entityKey ? t(entityKey) : entityLabel;
	const entities = entitiesKey ? t(entitiesKey) : entityLabelPlural;
	const resolvedPlaceholder = placeholder ?? (isMulti ? t("selectEntities", { entity: entities }) : t("selectEntity", { entity }));
	const resolvedSearchPlaceholder = searchPlaceholder ?? t("searchEntities", { entity: entities });
	const emptyMessage = debouncedSearch ? t("noMatchingEntities", { entity: entities }) : t("noEntitiesFound", { entity: entities });
	const errorMessage = t("failedToLoadEntities", { entity: entities });
	// A failed later page keeps the rows already shown and offers a retry under them.
	const hasRows = visibleOptions.length > 0;

	// ---------------------------------------------------------------------
	// Multi mode — chips on an outline trigger, a searchable list in a popover (same pattern as ModelSelector).
	// ---------------------------------------------------------------------

	const [multiSearch, setMultiSearch] = useState("");
	const handleMultiSearch = useCallback(
		(value: string) => {
			setMultiSearch(value);
			onSearchChange(value);
		},
		[onSearchChange],
	);
	const handleMultiOpenChange = useCallback(
		(next: boolean) => {
			if (!next) handleMultiSearch("");
			onOpenChange(next);
		},
		[handleMultiSearch, onOpenChange],
	);

	const rowsAreCurrent = optionsSearch === undefined || optionsSearch === debouncedSearch;
	const isBusy = isSearching || isFetching;
	const multiRows = useMemo(
		() => (showsLoadedRows({ rowsAreCurrent, isBusy, isError }) ? visibleOptions : []),
		[rowsAreCurrent, isBusy, isError, visibleOptions],
	);

	// Selected ids missing from the loaded page stay reachable (to untick) while the search is empty.
	const pinnedIds = useMemo(() => {
		if (!isMulti || multiSearch) return [];
		const loaded = new Set(multiRows.map((o) => o.value));
		return selectedIds.filter((id) => !loaded.has(id));
	}, [isMulti, multiSearch, multiRows, selectedIds]);

	const listState = multiListState({
		isError,
		isBusy,
		rowCount: visibleOptions.length,
		pinnedCount: pinnedIds.length,
		rowsAreCurrent,
	});
	const rowsSelectable = listState.rowsSelectable;

	// cmdk loses its highlight when a server response swaps every row, so it is held here.
	// Rows still showing an earlier search are not navigable, so Enter cannot pick one.
	const navigableIds = useMemo(
		() => [...pinnedIds, ...(rowsSelectable ? multiRows.map((o) => o.value) : [])],
		[pinnedIds, multiRows, rowsSelectable],
	);
	// Disabling while open, e.g. during a save, closes through the normal path so the search clears and the picker stays shut afterwards.
	useEffect(() => {
		if (isMulti && disabled && open) handleMultiOpenChange(false);
	}, [isMulti, disabled, open, handleMultiOpenChange]);

	const [highlighted, setHighlighted] = useState("");
	useEffect(() => {
		setHighlighted((current) => (current && navigableIds.includes(current) ? current : (navigableIds[0] ?? "")));
	}, [navigableIds]);

	if (isMulti) {
		const commitMulti = (next: string[]) => {
			// The parent can disable the picker while its list is open, e.g. during a save.
			if (disabled) return;
			if (props.multiple === true) props.onChange(next);
		};
		const toggleMulti = (value: string) => {
			const option = visibleOptions.find((o) => o.value === value);
			if (option) cacheLabel(option);
			commitMulti(selectedIds.includes(value) ? selectedIds.filter((v) => v !== value) : [...selectedIds, value]);
		};
		const removeLabel = (value: string) => t("combobox.removeAria", { label: labelFor(value) });
		const retryButton = onRetry && (
			<Button
				type="button"
				variant="ghost"
				size="sm"
				className="h-6 px-2 text-xs"
				onClick={onRetry}
				data-testid={testId ? `${testId}-retry` : undefined}
				onKeyDown={(e) => {
					// cmdk's root cancels Enter and selects the highlighted row, so keep the key on this button.
					if (e.key === "Enter" || e.key === " ") e.stopPropagation();
				}}
			>
				{t("retry")}
			</Button>
		);

		return (
			<div className={cn("w-full", className)}>
				{labelResolvers}
				<Combobox
					multiple
					value={selectedIds}
					open={open && !disabled}
					onOpenChange={handleMultiOpenChange}
					inputValue={multiSearch}
					onInputValueChange={handleMultiSearch}
				>
					<PopoverTrigger asChild disabled={disabled}>
						<Button
							type="button"
							variant="outline"
							role="combobox"
							id={id}
							disabled={disabled}
							data-testid={testId}
							className={cn(
								"h-auto min-h-8 w-full justify-between !bg-transparent py-1 font-normal active:scale-none",
								selectedIds.length === 0 && "text-muted-foreground",
								triggerClassName,
							)}
						>
							{selectedIds.length === 0 ? (
								<span className="min-w-0 truncate">{resolvedPlaceholder}</span>
							) : (
								<span className="flex min-w-0 flex-1 flex-wrap items-center gap-1">
									{selectedIds.map((value) => (
										<span
											key={value}
											className="bg-accent dark:bg-card flex max-w-[180px] items-center gap-1 rounded-sm px-1 py-0.5 text-xs"
										>
											<span className="min-w-0 truncate">{labelFor(value)}</span>
											{/* A span, not a button: the trigger is a Button and its svgs ignore pointer events. */}
											<span
												role="button"
												tabIndex={-1}
												aria-label={removeLabel(value)}
												data-testid={testId ? `${testId}-chip-remove-${value}` : undefined}
												className="text-muted-foreground hover:text-foreground flex shrink-0 cursor-pointer items-center"
												onClick={(e) => {
													e.preventDefault();
													e.stopPropagation();
													toggleMulti(value);
												}}
												onKeyDown={(e) => {
													if (e.key === "Enter" || e.key === " ") {
														e.preventDefault();
														e.stopPropagation();
														toggleMulti(value);
													}
												}}
											>
												<XIcon className="size-3" />
											</span>
										</span>
									))}
								</span>
							)}
							<span className="ml-2 flex shrink-0 items-center gap-1">
								{selectedIds.length > 0 && !disabled && (
									// Clears every chip at once, as the react-select control did. A span for the same reason as the chip X.
									<span
										role="button"
										tabIndex={-1}
										aria-label={t("combobox.clearSelection")}
										data-testid={testId ? `${testId}-clear` : undefined}
										className="text-muted-foreground hover:text-foreground flex cursor-pointer items-center"
										onClick={(e) => {
											e.preventDefault();
											e.stopPropagation();
											commitMulti([]);
										}}
									>
										<XIcon className="size-3.5" />
									</span>
								)}
								<SearchIcon className="size-3.5 opacity-50" />
								<ChevronDownIcon className="size-4 opacity-50" />
							</span>
						</Button>
					</PopoverTrigger>

					<ComboboxContent
						className={cn("min-w-[var(--radix-popover-trigger-width)]", contentClassName)}
						shouldFilter={false}
						highlightedValue={highlighted}
						onHighlightedValueChange={setHighlighted}
					>
						<ComboboxList
							searchPlaceholder={resolvedSearchPlaceholder}
							showSearchIcon
							isSearching={isSearching}
							className="max-h-[300px]"
							clearableSearch
							onInputKeyDown={(e) => {
								if (e.key !== "Backspace") return;
								const next = chipsAfterBackspace(selectedIds, multiSearch);
								if (!next) return;
								e.preventDefault();
								commitMulti(next);
							}}
							onScroll={(e: React.UIEvent<HTMLDivElement>) => {
								if (!loadMore || isFetching) return;
								const el = e.currentTarget;
								if (el.scrollHeight - el.scrollTop - el.clientHeight < 48) loadMore();
							}}
						>
							{pinnedIds.length > 0 && (
								<ComboboxGroup>
									<ComboboxLabel>{t("entityAssociation.selected")}</ComboboxLabel>
									{pinnedIds.map((value) => (
										<ComboboxItem key={value} value={value} onSelect={() => toggleMulti(value)}>
											<span className="min-w-0 grow truncate">{labelFor(value)}</span>
										</ComboboxItem>
									))}
								</ComboboxGroup>
							)}

							{multiRows.map((option) => (
								<ComboboxItem key={option.value} value={option.value} disabled={!rowsSelectable} onSelect={() => toggleMulti(option.value)}>
									<div className="flex min-w-0 grow flex-col">
										<span className="truncate">{option.label}</span>
										{option.description && option.description !== option.label && (
											<span className="text-muted-foreground truncate text-xs">{option.description}</span>
										)}
									</div>
								</ComboboxItem>
							))}

							{listState.body === "empty" && <div className="text-muted-foreground py-6 text-center text-sm">{emptyMessage}</div>}

							{listState.body === "firstPageError" && (
								<div className="text-muted-foreground flex flex-col items-center gap-2 py-6 text-center text-sm">
									<span>{errorMessage}</span>
									{retryButton}
								</div>
							)}

							{listState.body === "loading" && (
								<div className="text-muted-foreground flex items-center justify-center gap-2 py-6 text-sm">
									<Loader2Icon className="size-3.5 animate-spin" />
									{t("modelAccess.searching")}
								</div>
							)}

							{(isLoadingMore || (hasMore && isFetching)) && hasRows && (
								<div className="text-muted-foreground flex items-center justify-center gap-2 py-2 text-xs">
									<Loader2Icon className="size-3 animate-spin" />
									{t("modelAccess.loadingMore")}
								</div>
							)}

							{listState.laterPageRetry && (
								<div className="text-muted-foreground flex items-center justify-between gap-2 px-2 py-2 text-xs">
									<span>{t("failedToLoadMoreEntities", { entity: entities })}</span>
									{retryButton}
								</div>
							)}
						</ComboboxList>
					</ComboboxContent>
				</Combobox>
			</div>
		);
	}

	// ---------------------------------------------------------------------
	// Single / add mode — popover behind a button trigger.
	// ---------------------------------------------------------------------

	const triggerLabel = selectedIds.length > 0 ? labelFor(selectedIds[0]) : "";

	// Add mode has no value to display, so it defaults to a compact action button rather than the
	// full-width combobox — unless fullWidth is set, which uses the combobox trigger (showing the
	// placeholder) for surfaces that want the picker to span the row.
	const defaultTrigger =
		isAdd && !fullWidth ? (
			<Button type="button" variant="outline" size="sm" disabled={disabled} className="h-7.5 gap-1.5 px-2 py-1 text-sm font-medium">
				<PlusIcon className="size-4" />
				{placeholder ?? t("addEntity", { entity })}
			</Button>
		) : (
			<Button
				type="button"
				variant="outline"
				role="combobox"
				id={id}
				disabled={disabled}
				className={cn(
					"h-8 w-full justify-between !bg-transparent font-normal active:scale-none",
					!triggerLabel && "text-muted-foreground",
					triggerClassName,
				)}
			>
				{triggerIcon ? (
					<span className="flex min-w-0 items-center gap-2">
						{triggerIcon}
						<span data-slot="entity-selector-label" className="truncate">
							{triggerLabel || resolvedPlaceholder}
						</span>
					</span>
				) : (
					<span data-slot="entity-selector-label" className="truncate">
						{triggerLabel || resolvedPlaceholder}
					</span>
				)}
				<ChevronDownIcon data-slot="entity-selector-chevron" className="ml-2 size-4 shrink-0 opacity-50" />
			</Button>
		);

	return (
		// Single wrapping element, so no vertical spacing utility here — it only
		// ever holds the trigger and the (invisible) label resolvers.
		<div className={cn(isAdd && !fullWidth ? "inline-flex" : "w-full", className)}>
			{labelResolvers}
			<SearchSelect
				async
				options={visibleOptions.map((option) => ({ ...option, selected: selectedIds.includes(option.value) }))}
				onValueSelect={handleSelect}
				entryView={(option) => (
					<>
						<div className="flex min-w-0 flex-col">
							<span className="truncate font-medium">{option.label}</span>
							{option.description && option.description !== option.label && (
								<span className="text-muted-foreground truncate text-xs">{option.description}</span>
							)}
						</div>
						{/* Add mode never marks rows as selected — picked entities leave the list. */}
						{!isAdd && <CheckIcon className={cn("ml-auto size-3.5 shrink-0", option.selected ? "opacity-100" : "opacity-0")} />}
					</>
				)}
				label={trigger ?? defaultTrigger}
				// A footer action always leads somewhere else — a create sheet, another
				// surface — so dismiss the picker on the way out rather than leaving a
				// popover open behind whatever it opened.
				//
				// Capture, not bubble: footer is a public prop, and a footer whose own
				// handler calls stopPropagation would otherwise never let this run,
				// leaving the popover open behind whatever it opened. Capture fires on
				// the way down, before the descendant can stop anything.
				footer={footer && <div onClickCapture={() => onOpenChange(false)}>{footer}</div>}
				open={open}
				onOpenChange={onOpenChange}
				onSearchChange={onSearchChange}
				isSearching={isSearching}
				// Skeletons only when there's nothing to show yet; refetching on
				// each keystroke shouldn't blank out the current results.
				isLoading={isFetching && visibleOptions.length === 0}
				onLoadMore={loadMore}
				isLoadingMore={isLoadingMore}
				isError={isError && !hasRows}
				errorMessage={errorMessage}
				loadMoreErrorMessage={isError && hasRows ? t("failedToLoadMoreEntities", { entity: entities }) : undefined}
				onRetry={onRetry}
				searchPlaceholder={resolvedSearchPlaceholder}
				emptyMessage={emptyMessage}
				disabled={disabled}
				// A compact trigger shouldn't dictate the popover width, so add
				// mode keeps SearchSelect's own fixed width. Either default gives
				// way to an explicit contentClassName.
				align={isAdd && !fullWidth ? "end" : "start"}
				className={isAdd && !fullWidth ? undefined : "w-full"}
				contentClassName={contentClassName ?? (isAdd && !fullWidth ? undefined : "w-(--radix-popover-trigger-width)")}
				noPortal={noPortal}
			/>
		</div>
	);
}

/**
 * The open/search/debounce/offset state every wrapper needs, so none of them
 * hand-roll it. `skip` feeds RTK Query — nothing is fetched until the picker
 * opens. Pass `offset` to the list query and hand the result to
 * `useEntitySelectorPages`.
 */
export function useEntitySelectorSearch() {
	const [open, setOpenState] = useState(false);
	const [search, setSearch] = useState("");
	const [debouncedSearch, setDebouncedSearch] = useState("");
	// The offset belongs to the search it was scrolled under, so a new search
	// starts from the first page without an effect to reset it.
	const [page, setPage] = useState({ search: "", offset: 0 });

	// Deployments can hold far more rows than one page, so pickers search
	// server-side instead of filtering a capped pre-fetched list.
	useEffect(() => {
		const timer = setTimeout(() => setDebouncedSearch(search), ENTITY_SEARCH_DEBOUNCE_MS);
		return () => clearTimeout(timer);
	}, [search]);

	const setOpen = useCallback((next: boolean) => {
		setOpenState(next);
		// Reopening starts from the first page again.
		if (!next) setPage((prev) => (prev.offset === 0 ? prev : { search: "", offset: 0 }));
	}, []);

	const setOffset = useCallback(
		(offset: number) =>
			setPage((prev) => (prev.search === debouncedSearch && prev.offset === offset ? prev : { search: debouncedSearch, offset })),
		[debouncedSearch],
	);

	return {
		open,
		setOpen,
		setSearch,
		debouncedSearch,
		offset: page.search === debouncedSearch ? page.offset : 0,
		setOffset,
		skip: !open,
		// Debounce is part of the wait, so the spinner covers it too.
		isDebouncing: search !== debouncedSearch,
	};
}

interface EntitySelectorPageResult {
	/** One page of rows for the current query args; undefined while it is in flight. */
	entries: EntitySelectorEntry[] | undefined;
	/** The list endpoint's `total_count` for the current search. */
	totalCount: number | undefined;
	isFetching: boolean;
	isError: boolean;
	/** RTK Query's `refetch`, used to retry a page that failed. */
	refetch?: () => void;
}

/**
 * Accumulates the pages a wrapper fetches into one option list and returns
 * the list props to spread onto `EntitySelector`. Wrappers pass RTK Query's
 * `currentData`, not `data`: `data` still holds the previous args' page while
 * the next is in flight, which would be merged at the wrong offset.
 */
export function useEntitySelectorPages(
	search: ReturnType<typeof useEntitySelectorSearch>,
	{ entries, totalCount, isFetching, isError, refetch }: EntitySelectorPageResult,
) {
	const { open, setOpen, setSearch, debouncedSearch, offset, setOffset, isDebouncing } = search;

	// A ref so the merged list is ready in the same render the page lands; the merge is idempotent under StrictMode.
	const pagesRef = useRef(EMPTY_ENTITY_PAGES);
	const pages = useMemo(() => {
		pagesRef.current = accumulatePages(pagesRef.current, { open, search: debouncedSearch, offset, entries, total: totalCount });
		return pagesRef.current;
	}, [open, entries, totalCount, debouncedSearch, offset]);

	const hasMore = pages.search === debouncedSearch && hasMoreEntities(pages);
	const nextOffset = pages.nextOffset;
	const needsRefetch = loadMoreNeedsRefetch(pages, offset, isError);
	const onLoadMore = useCallback(
		() => (needsRefetch ? refetch?.() : setOffset(nextOffset)),
		[needsRefetch, refetch, setOffset, nextOffset],
	);
	const isLoadingMore = isFetching && offset > 0;

	return {
		options: pages.entries,
		isFetching,
		isError,
		open,
		onOpenChange: setOpen,
		onSearchChange: setSearch,
		debouncedSearch,
		isSearching: isDebouncing || (isFetching && !isLoadingMore && !!debouncedSearch),
		hasMore,
		onLoadMore,
		isLoadingMore,
		onRetry: refetch,
		optionsSearch: pages.search,
	};
}