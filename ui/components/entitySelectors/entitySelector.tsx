// Shared shell behind every async entity selector (virtual keys, users,
// teams, customers, business units).
//
// Owns everything that is identical across entities: the three selection
// modes, the label cache that keeps an already-selected id from rendering as
// a raw UUID, and the search wiring.
//
// Two presentations, picked by mode:
//   - single / add -> SearchSelect (a popover behind a button trigger)
//   - multi        -> AsyncMultiSelect, the react-select control already used
//                     for association fields, so chips render inline in the
//                     control rather than in a separate row below it.
// Fetching, debouncing and label resolution are identical either way.
//
// Each entity still ships a thin wrapper, because the parts that genuinely
// differ — which endpoint to call, whether it pages by offset or page number,
// and which field is the label — aren't worth abstracting over. A wrapper
// owns its query and its debounce and hands the results down here.
//
// Lives in OSS so the enterprise-only selectors can build on it too.

import { CheckIcon, ChevronDownIcon, PlusIcon } from "lucide-react";
import { type ComponentType, type ReactNode, useCallback, useEffect, useMemo, useRef, useState } from "react";

import { AsyncMultiSelect } from "@/components/ui/asyncMultiselect";
import { Button } from "@/components/ui/button";
import type { Option } from "@/components/ui/multiselectUtils";
import { SearchSelect } from "@/components/ui/searchSelect";
import { cn } from "@/lib/utils";

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

/** Carried on each react-select option so the custom option view can read it. */
interface EntityOptionMeta {
	description?: string;
}

/**
 * Props every wrapper re-exposes verbatim, so callers see the same surface
 * whichever entity they are picking.
 */
export interface EntitySelectorCommonProps {
	disabled?: boolean;
	placeholder?: string;
	className?: string;
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
	 * Single mode only.
	 */
	triggerClassName?: string;
	/** Ids to omit from the results — e.g. entities already added to a list. */
	excludeIds?: string[];
	/**
	 * Replaces the default full-width combobox trigger. Surfaces that already
	 * show the selection elsewhere (a table, a chip row) pass a compact button
	 * instead; the trigger then renders no selected label of its own.
	 * Single/add only — multi mode renders its chips inside the control.
	 */
	trigger?: ReactNode;
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
	onSelect: (option: EntitySelectorOption) => void;
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
	/**
	 * Must be referentially stable while the underlying data is unchanged —
	 * memoize it in the wrapper. Multi mode feeds it to react-select as
	 * `defaultOptions`, which re-syncs on identity change and would otherwise
	 * loop.
	 */
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
		searchPlaceholder,
		LabelResolver,
		disabled = false,
		placeholder,
		className,
		fallbackOption,
		fallbackOptions,
		noPortal = true,
		triggerClassName,
		excludeIds,
		trigger,
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

	// Only reached by the single/add presentation; multi selection is handled
	// by react-select's own onChange below.
	const handleSelect = (option: EntitySelectorEntry) => {
		cacheLabel(option);

		if (props.mode === "add") {
			props.onSelect({ value: option.value, label: option.label });
		} else if (props.multiple !== true) {
			props.onChange(option.value);
		}
		onOpenChange(false);
	};

	const resolvedPlaceholder = placeholder ?? (isMulti ? `Select ${entityLabelPlural}...` : `Select a ${entityLabel}...`);
	const resolvedSearchPlaceholder = searchPlaceholder ?? `Search ${entityLabelPlural}...`;
	const emptyMessage = debouncedSearch ? `No matching ${entityLabelPlural}` : `No ${entityLabelPlural} found`;
	const errorMessage = `Failed to load ${entityLabelPlural}`;

	// ---------------------------------------------------------------------
	// Multi mode — react-select, so chips live inside the control.
	// ---------------------------------------------------------------------

	const multiOptions = useMemo<Option<EntityOptionMeta>[]>(
		() => visibleOptions.map((option) => ({ label: option.label, value: option.value, meta: { description: option.description } })),
		[visibleOptions],
	);

	const selectedValues = useMemo<Option<EntityOptionMeta>[]>(
		() => selectedIds.map((id) => ({ label: labelCache[id] ?? fallbackLabels[id] ?? id, value: id })),
		[selectedIds, labelCache, fallbackLabels],
	);

	// react-select asks for options through a callback, but the results arrive
	// through RTK Query on a later render. Park the callback and settle it once
	// the debounce has caught up and the request is done.
	const pendingLoad = useRef<{ query: string; callback: (options: Option<EntityOptionMeta>[]) => void } | null>(null);

	const handleReload = useCallback((query: string, callback: (loaded: Option<EntityOptionMeta>[]) => void) => {
		pendingLoad.current = { query, callback };
	}, []);

	useEffect(() => {
		const pending = pendingLoad.current;
		if (!pending) return;
		// `options` only answers the pending query once the debounce has settled
		// on it and nothing is in flight.
		if (pending.query !== debouncedSearch || isFetching) return;
		pendingLoad.current = null;
		pending.callback(multiOptions);
	}, [multiOptions, isFetching, debouncedSearch]);

	if (isMulti) {
		return (
			<>
				{labelResolvers}
				<AsyncMultiSelect<EntityOptionMeta>
					className={className}
					disabled={disabled}
					placeholder={resolvedPlaceholder}
					defaultOptions={multiOptions}
					reload={handleReload}
					isLoading={isSearching || (isFetching && multiOptions.length === 0)}
					value={selectedValues}
					onChange={(selected) => {
						if (props.multiple !== true) return;
						props.onChange(selected.map((option) => option.value));
					}}
					// Clearing the input never reaches `reload`, so mirror every
					// keystroke into the search state instead.
					onInputChange={(inputValue) => onSearchChange(inputValue)}
					onMenuOpen={() => onOpenChange(true)}
					onMenuClose={() => onOpenChange(false)}
					isClearable
					closeMenuOnSelect={false}
					hideSelectedOptions={false}
					noOptionsMessage={() => (isError ? errorMessage : emptyMessage)}
					views={{
						option: (optionProps) => {
							const data = optionProps.data as Option<EntityOptionMeta>;
							return (
								<div
									className={cn(
										"flex w-full cursor-pointer flex-col gap-0.5 rounded-sm p-2 text-sm",
										optionProps.isFocused && "bg-background-highlight-primary/60",
										optionProps.isSelected && "bg-background-highlight-primary/40",
									)}
									onClick={() => optionProps.selectOption(optionProps.data)}
								>
									<div className="flex items-center justify-between">
										<span className="text-content-primary font-medium">{data.label}</span>
										{optionProps.isSelected && <span className="text-primary text-xs">Selected</span>}
									</div>
									{data.meta?.description && data.meta.description !== data.label && (
										<span className="text-content-tertiary line-clamp-1 text-xs">{data.meta.description}</span>
									)}
								</div>
							);
						},
					}}
				/>
			</>
		);
	}

	// ---------------------------------------------------------------------
	// Single / add mode — popover behind a button trigger.
	// ---------------------------------------------------------------------

	const triggerLabel = selectedIds.length > 0 ? labelFor(selectedIds[0]) : "";

	// Add mode has no value to display, so it defaults to a compact action
	// button rather than the full-width combobox.
	const defaultTrigger = isAdd ? (
		<Button type="button" variant="outline" size="sm" disabled={disabled} className="h-7.5 gap-1.5 px-2 py-1 text-sm font-medium">
			<PlusIcon className="size-4" />
			{placeholder ?? `Add ${entityLabel}`}
		</Button>
	) : (
		<Button
			type="button"
			variant="outline"
			role="combobox"
			disabled={disabled}
			className={cn(
				"h-8 w-full justify-between !bg-transparent font-normal active:scale-none",
				!triggerLabel && "text-muted-foreground",
				triggerClassName,
			)}
		>
			<span className="truncate">{triggerLabel || resolvedPlaceholder}</span>
			<ChevronDownIcon className="ml-2 size-4 shrink-0 opacity-50" />
		</Button>
	);

	return (
		// Single wrapping element, so no vertical spacing utility here — it only
		// ever holds the trigger and the (invisible) label resolvers.
		<div className={cn(isAdd ? "inline-flex" : "w-full", className)}>
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
				open={open}
				onOpenChange={onOpenChange}
				onSearchChange={onSearchChange}
				isSearching={isSearching}
				// Skeletons only when there's nothing to show yet; refetching on
				// each keystroke shouldn't blank out the current results.
				isLoading={isFetching && visibleOptions.length === 0}
				isError={isError}
				errorMessage={errorMessage}
				searchPlaceholder={resolvedSearchPlaceholder}
				emptyMessage={emptyMessage}
				disabled={disabled}
				// A compact trigger shouldn't dictate the popover width, so add
				// mode keeps SearchSelect's own fixed width.
				align={isAdd ? "end" : "start"}
				className={isAdd ? undefined : "w-full"}
				contentClassName={isAdd ? undefined : "w-(--radix-popover-trigger-width)"}
				noPortal={noPortal}
			/>
		</div>
	);
}

/**
 * The open/search/debounce state every wrapper needs, so none of them
 * hand-roll it. `skip` feeds RTK Query — nothing is fetched until the picker
 * opens.
 */
export function useEntitySelectorSearch() {
	const [open, setOpen] = useState(false);
	const [search, setSearch] = useState("");
	const [debouncedSearch, setDebouncedSearch] = useState("");

	// Deployments can hold far more rows than one page, so pickers search
	// server-side instead of filtering a capped pre-fetched list.
	useEffect(() => {
		const timer = setTimeout(() => setDebouncedSearch(search), ENTITY_SEARCH_DEBOUNCE_MS);
		return () => clearTimeout(timer);
	}, [search]);

	return {
		open,
		setOpen,
		setSearch,
		debouncedSearch,
		skip: !open,
		// Debounce is part of the wait, so the spinner covers it too.
		isDebouncing: search !== debouncedSearch,
	};
}