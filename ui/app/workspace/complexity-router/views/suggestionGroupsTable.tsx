import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuLabel, DropdownMenuTrigger } from "@/components/ui/dropdownMenu";
import { Input } from "@/components/ui/input";
import { getLocale } from "@/lib/i18n";
import { getErrorMessage } from "@/lib/store";
import { useGetComplexitySuggestionQuery } from "@/lib/store/apis/governanceApi";
import { COMPLEXITY_TIER_VALUES, DecisionTier, Suggestion, UNMATCHED_TIER } from "@/lib/types/complexityRouter";
import { cn } from "@/lib/utils";
import { ChevronDown, History, LoaderCircle, X } from "lucide-react";
import { DragEvent, useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { proposedTier, routingSplit, StagedSuggestion } from "./suggestions.utils";

export type GroupList = "pending" | "dismissed";

interface Props {
	list: GroupList;
	onListChange: (list: GroupList) => void;
	pendingCount: number;
	dismissedCount: number;
	groups: Suggestion[];
	// While the selected list's first page loads: no rows, and nothing to act on.
	loading?: boolean;
	// A group to open, as when the impact view leads back to one.
	expandGroupId?: string;
	// How many groups the list holds; groups are the ones loaded so far.
	total: number;
	hasMore: boolean;
	loadingMore: boolean;
	onLoadMore: () => void;
	staged: StagedSuggestion[];
	// Phrases each tier's draft list holds now, staged ones included.
	tierCounts: Record<DecisionTier, number>;
	minSimilarity?: number;
	canUpdate: boolean;
	tierLabel: (tier: string) => string;
	onAccept: (groups: Suggestion[], tier: (group: Suggestion) => DecisionTier | null) => void;
	onUnstage: (id: string) => void;
	// Reports whether the edit was taken.
	onEditStaged: (id: string, from: string, to: string) => boolean;
	// While a save is in flight: nothing is staged, unstaged or edited.
	stagingLocked?: boolean;
	onDismiss: (groups: Suggestion[]) => void;
	onRestore: (group: Suggestion) => void;
}

const GRID =
	"grid grid-cols-[1.75rem_minmax(14rem,2.4fr)_5.5rem_minmax(12rem,1.5fr)_minmax(9rem,1fr)_minmax(13rem,auto)] items-start gap-x-4";

// SuggestionGroupsTable lists a run's groups of similar requests with the
// tier proposed for each. Accepting a group stages its phrases in the draft
// phrase lists, from a row, the bulk bar, or by dragging rows onto a tier;
// the draft is applied with the page's Save.
export default function SuggestionGroupsTable({
	list,
	onListChange,
	pendingCount,
	dismissedCount,
	groups,
	loading = false,
	expandGroupId,
	total,
	hasMore,
	loadingMore,
	onLoadMore,
	staged,
	tierCounts,
	minSimilarity,
	canUpdate,
	tierLabel,
	onAccept,
	onUnstage,
	onEditStaged,
	stagingLocked = false,
	onDismiss,
	onRestore,
}: Props) {
	const { t } = useTranslation("models", { keyPrefix: "routing.complexityUi.suggestions" });
	const [selected, setSelected] = useState<Set<string>>(() => new Set());
	const [open, setOpen] = useState<Set<string>>(() => new Set(expandGroupId ? [expandGroupId] : []));
	useEffect(() => {
		if (expandGroupId) setOpen((current) => (current.has(expandGroupId) ? current : new Set(current).add(expandGroupId)));
	}, [expandGroupId]);
	const [dragIds, setDragIds] = useState<string[] | null>(null);
	const [over, setOver] = useState<DecisionTier | null>(null);
	const stagedById = new Map(staged.map((entry) => [entry.id, entry]));
	const pending = list === "pending";
	// Only a pending group not yet accepted can be selected.
	const selectable = canUpdate && pending ? groups.filter((group) => !stagedById.has(group.id)) : [];
	const selectedGroups = selectable.filter((group) => selected.has(group.id));
	const withProposal = selectable.filter((group) => proposedTier(group) !== null);
	const bulkTargets = selectedGroups.length > 0 ? selectedGroups.filter((group) => proposedTier(group) !== null) : withProposal;
	useEffect(() => setSelected(new Set()), [list]);

	const accept = (rows: Suggestion[], tier: (group: Suggestion) => DecisionTier | null) => {
		if (stagingLocked) return;
		onAccept(rows, tier);
		setSelected(new Set());
	};
	const toggle = (set: Set<string>, id: string) => {
		const next = new Set(set);
		if (next.has(id)) next.delete(id);
		else next.add(id);
		return next;
	};
	// A drag carries the whole selection when it starts on a selected row.
	const startDrag = (event: DragEvent<HTMLDivElement>, group: Suggestion) => {
		const rows = selected.has(group.id) ? selectedGroups : [group];
		event.dataTransfer.effectAllowed = "move";
		event.dataTransfer.setData("text/plain", rows.map((row) => row.id).join(","));
		// Deferred so the browser takes its drag image before the rows dim.
		setTimeout(() => setDragIds(rows.map((row) => row.id)), 0);
	};
	const drop = (tier: DecisionTier) => {
		setOver(null);
		if (!dragIds) return;
		const ids = new Set(dragIds);
		setDragIds(null);
		accept(
			groups.filter((group) => ids.has(group.id)),
			() => tier,
		);
	};

	return (
		<div className="space-y-3">
			<div className="grid gap-2.5 sm:grid-cols-3" data-testid="complexity-suggestions-tier-zones">
				{COMPLEXITY_TIER_VALUES.map((tier) => {
					const added = staged.filter((entry) => entry.tier === tier).length;
					return (
						<div
							key={tier}
							onDragOver={(event) => {
								if (!dragIds) return;
								event.preventDefault();
								event.dataTransfer.dropEffect = "move";
								if (over !== tier) setOver(tier);
							}}
							onDragLeave={(event) => {
								if (!event.currentTarget.contains(event.relatedTarget as Node | null)) setOver(null);
							}}
							onDrop={(event) => {
								event.preventDefault();
								drop(tier);
							}}
							className={cn(
								"flex items-center gap-2 rounded-sm border px-3 py-2.5 text-sm transition-colors",
								dragIds && "border-dashed",
								over === tier && "border-foreground/60 bg-muted/60",
							)}
							data-testid={`complexity-suggestions-zone-${tier.toLowerCase()}`}
						>
							<span className="font-medium">{tierLabel(tier)}</span>
							<span className="text-muted-foreground font-mono text-xs tabular-nums">
								{t("tierPhraseCount", { count: tierCounts[tier] })}
							</span>
							{added > 0 && (
								<Badge
									variant="success"
									className="h-5 px-1.5 text-[10px]"
									data-testid={`complexity-suggestions-zone-new-${tier.toLowerCase()}`}
								>
									{t("newCount", { n: added })}
								</Badge>
							)}
							<span className="text-muted-foreground ml-auto text-xs">{over === tier ? t("releaseHere") : t("dropHere")}</span>
						</div>
					);
				})}
			</div>

			<section className="custom-scrollbar overflow-x-auto rounded-sm border" data-testid="complexity-suggestions-list-card">
				<div className="min-w-[64rem]">
					<div
						className="bg-muted/30 flex flex-wrap items-center gap-x-2 gap-y-1.5 border-b px-3 py-2"
						data-testid="complexity-suggestions-list-header"
					>
						{selectable.length > 0 && (
							<Checkbox
								checked={selectedGroups.length === 0 ? false : selectedGroups.length === selectable.length ? true : "indeterminate"}
								onCheckedChange={() =>
									setSelected(selectedGroups.length === selectable.length ? new Set() : new Set(selectable.map((group) => group.id)))
								}
								aria-label={t("selectAllAria")}
								data-testid="complexity-suggestions-select-all"
							/>
						)}
						<span className={cn("min-w-24 text-xs", selectedGroups.length > 0 ? "font-medium" : "text-muted-foreground")}>
							{selectedGroups.length > 0
								? t("selectedGroups", { count: selectedGroups.length })
								: t("groupCount", { count: groups.length })}
						</span>
						{selectable.length > 0 && (
							<>
								<Button
									type="button"
									size="sm"
									className="h-7"
									disabled={stagingLocked || bulkTargets.length === 0}
									onClick={() => accept(bulkTargets, proposedTier)}
									data-testid="complexity-suggestions-bulk-accept"
								>
									{selectedGroups.length > 0
										? t("acceptSelected", { n: bulkTargets.length })
										: t("acceptAllProposed", { n: bulkTargets.length })}
								</Button>
								<span className="text-muted-foreground ml-1 text-xs">{t("setSelectedTo")}</span>
								{COMPLEXITY_TIER_VALUES.map((tier) => (
									<Button
										key={tier}
										type="button"
										variant="outline"
										size="sm"
										className="h-7"
										disabled={stagingLocked || selectedGroups.length === 0}
										onClick={() => accept(selectedGroups, () => tier)}
										data-testid={`complexity-suggestions-bulk-tier-${tier.toLowerCase()}`}
									>
										{tierLabel(tier)}
									</Button>
								))}
								<Button
									type="button"
									variant="ghost"
									size="sm"
									className="h-7"
									disabled={selectedGroups.length === 0}
									onClick={() => {
										onDismiss(selectedGroups);
										setSelected(new Set());
									}}
									data-testid="complexity-suggestions-bulk-dismiss"
								>
									{t("dismissSelected", { n: selectedGroups.length })}
								</Button>
							</>
						)}
						<div className="bg-muted ml-auto flex h-7 items-center rounded-sm p-0.5" role="tablist" aria-label={t("listAria")}>
							{(["pending", "dismissed"] as const).map((value) => (
								<button
									key={value}
									type="button"
									role="tab"
									aria-selected={list === value}
									onClick={() => onListChange(value)}
									data-testid={`complexity-suggestions-list-${value === "pending" ? "open" : "dismissed"}`}
									className={cn(
										"flex h-full items-center rounded-sm px-2.5 text-xs",
										list === value ? "bg-background font-medium shadow-sm" : "text-muted-foreground hover:text-foreground",
									)}
								>
									{value === "pending" ? t("pendingTab") : t("dismissedTab")}
									<span className="text-muted-foreground ml-1.5 font-mono text-[11px] tabular-nums">
										{value === "pending" ? pendingCount : dismissedCount}
									</span>
								</button>
							))}
						</div>
					</div>

					<div className={cn(GRID, "text-muted-foreground border-b px-3 py-2 text-xs font-medium")} aria-hidden>
						<span />
						<span>{t("columnRequest")}</span>
						<span className="text-right">{t("columnRequests")}</span>
						<span>{t("whyHeading")}</span>
						<span>{t("columnProposed")}</span>
						<span />
					</div>

					{loading ? (
						<p
							className="text-muted-foreground flex items-center justify-center gap-2 px-3 py-10 text-sm"
							data-testid="complexity-suggestions-list-loading"
						>
							<LoaderCircle className="size-4 animate-spin" />
							{t("loading")}
						</p>
					) : groups.length === 0 ? (
						<p className="text-muted-foreground px-3 py-10 text-center text-sm" data-testid="complexity-suggestions-empty">
							{pending ? t("emptyPending") : t("emptyDismissed")}
						</p>
					) : (
						<div className="divide-y" data-testid="complexity-suggestions-list">
							{groups.map((group) => (
								<GroupRow
									key={group.id}
									group={group}
									staged={stagedById.get(group.id)}
									dismissed={!pending}
									canUpdate={canUpdate}
									stagingLocked={stagingLocked}
									selectable={selectable.includes(group)}
									selected={selected.has(group.id)}
									expanded={open.has(group.id)}
									dragging={dragIds?.includes(group.id) ?? false}
									minSimilarity={minSimilarity}
									tierLabel={tierLabel}
									onToggleSelected={() => setSelected((current) => toggle(current, group.id))}
									onToggleExpanded={() => setOpen((current) => toggle(current, group.id))}
									onDragStart={(event) => startDrag(event, group)}
									onDragEnd={() => {
										setDragIds(null);
										setOver(null);
									}}
									onAccept={(tier) => accept([group], () => tier)}
									onUnstage={() => onUnstage(group.id)}
									onEditStaged={(from, to) => onEditStaged(group.id, from, to)}
									onDismiss={() => onDismiss([group])}
									onRestore={() => onRestore(group)}
								/>
							))}
						</div>
					)}
					{groups.length > 0 && (hasMore || total > groups.length) && (
						<div
							className="text-muted-foreground flex items-center justify-between gap-2 border-t px-3 py-2 text-xs"
							data-testid="complexity-suggestions-list-footer"
						>
							<span data-testid="complexity-suggestions-shown">{t("shownOf", { shown: groups.length, total })}</span>
							{hasMore && (
								<Button
									type="button"
									variant="outline"
									size="sm"
									className="h-7"
									disabled={loadingMore}
									onClick={onLoadMore}
									data-testid="complexity-suggestions-load-more"
								>
									{loadingMore && <LoaderCircle className="size-3.5 animate-spin" />}
									{t("loadMore")}
								</Button>
							)}
						</div>
					)}
				</div>
			</section>
		</div>
	);
}

interface RowProps {
	group: Suggestion;
	staged?: StagedSuggestion;
	dismissed: boolean;
	canUpdate: boolean;
	stagingLocked: boolean;
	selectable: boolean;
	selected: boolean;
	expanded: boolean;
	dragging: boolean;
	minSimilarity?: number;
	tierLabel: (tier: string) => string;
	onToggleSelected: () => void;
	onToggleExpanded: () => void;
	onDragStart: (event: DragEvent<HTMLDivElement>) => void;
	onDragEnd: () => void;
	onAccept: (tier: DecisionTier) => void;
	onUnstage: () => void;
	onEditStaged: (from: string, to: string) => boolean;
	onDismiss: () => void;
	onRestore: () => void;
}

function GroupRow({
	group,
	staged,
	dismissed,
	canUpdate,
	stagingLocked,
	selectable,
	selected,
	expanded,
	dragging,
	minSimilarity,
	tierLabel,
	onToggleSelected,
	onToggleExpanded,
	onDragStart,
	onDragEnd,
	onAccept,
	onUnstage,
	onEditStaged,
	onDismiss,
	onRestore,
}: RowProps) {
	const { t } = useTranslation("models", { keyPrefix: "routing.complexityUi.suggestions" });
	const id = group.id;
	const number = (value: number) => new Intl.NumberFormat(getLocale()).format(value);
	const fraction = (value: number) =>
		new Intl.NumberFormat(getLocale(), { minimumFractionDigits: 2, maximumFractionDigits: 2 }).format(value);
	const proposal = proposedTier(group);
	// The tier model answers high, medium or low; low is no proposal at all.
	const confidence = (group.confidence ?? "high").toLowerCase();
	const confidenceLabel =
		confidence === "medium" ? t("confidence.medium") : confidence === "high" ? t("confidence.high") : (group.confidence ?? "");
	const accepted = staged !== undefined;
	const draggable = canUpdate && !stagingLocked && !dismissed && !accepted;
	const split = routingSplit(group.current_routing);
	const splitLine = split
		.map((share) =>
			share.tier === UNMATCHED_TIER ? t("splitNoMatch", { n: number(share.count) }) : `${number(share.count)} ${tierLabel(share.tier)}`,
		)
		.join(" · ");
	// The why column's second line: where the group routes today, or how close
	// its nearest phrase is when the split was not recorded.
	const whyLine =
		splitLine ||
		(group.nearest_phrase
			? t("nearestLine", { tier: tierLabel(group.nearest_tier ?? ""), similarity: fraction(group.nearest_similarity) })
			: "");

	return (
		<div
			draggable={draggable}
			onDragStart={onDragStart}
			onDragEnd={onDragEnd}
			className={cn(
				"transition-opacity",
				selected && "bg-muted/50",
				accepted && "bg-emerald-500/5",
				dragging && "opacity-40",
				draggable && "cursor-grab active:cursor-grabbing",
			)}
			data-testid={`complexity-suggestion-row-${id}`}
		>
			<div className={cn(GRID, "px-3 py-3")}>
				<div className="pt-0.5">
					{selectable && (
						<Checkbox
							checked={selected}
							onCheckedChange={onToggleSelected}
							aria-label={t("selectRowAria")}
							data-testid={`complexity-suggestion-select-${id}`}
						/>
					)}
				</div>
				<div className="min-w-0 space-y-1">
					<p className="text-sm break-words">
						{group.phrases.map((phrase, index) => (
							<span key={phrase}>
								{index > 0 && <span className="text-muted-foreground px-1.5">/</span>}“{phrase}”
							</span>
						))}
					</p>
					<div className="flex flex-wrap items-center gap-x-2 gap-y-1">
						<button
							type="button"
							onClick={onToggleExpanded}
							aria-expanded={expanded}
							className="text-muted-foreground hover:text-foreground inline-flex items-center gap-1 text-xs"
							data-testid={`complexity-suggestion-expand-${id}`}
						>
							{expanded ? t("hideDetails") : t("showDetails")}
							<ChevronDown className={cn("size-3 transition-transform", expanded && "rotate-180")} />
						</button>
						{group.stale && (
							<span
								className="text-muted-foreground inline-flex items-center gap-1 text-xs"
								title={t("staleTip")}
								data-testid={`complexity-suggestion-stale-${id}`}
							>
								<History className="size-3" aria-hidden />
								{t("stale")}
							</span>
						)}
					</div>
				</div>
				<span className="text-right font-mono text-sm tabular-nums" data-testid={`complexity-suggestion-count-${id}`}>
					{number(group.request_count)}
				</span>
				<div className="min-w-0 space-y-0.5">
					<p className="text-xs">{t(`category.${group.category}`)}</p>
					{whyLine && (
						<p className="text-muted-foreground text-xs" data-testid={`complexity-suggestion-split-${id}`}>
							{whyLine}
						</p>
					)}
				</div>
				<div className="min-w-0 space-y-0.5" data-testid={`complexity-suggestion-proposal-${id}`}>
					<p className="text-sm font-medium">{proposal ? tierLabel(proposal) : t("youDecide")}</p>
					<p className="text-muted-foreground text-xs">{proposal ? confidenceLabel : t("lowConfidence")}</p>
				</div>
				<div className="flex flex-nowrap items-center justify-end gap-1.5">
					{accepted && staged ? (
						<>
							<Badge variant="success" className="h-6 px-2 text-xs" data-testid={`complexity-suggestion-added-${id}`}>
								{t("addedTo", { tier: tierLabel(staged.tier) })}
							</Badge>
							{canUpdate && (
								<Button
									type="button"
									variant="outline"
									size="sm"
									className="h-7"
									disabled={stagingLocked}
									onClick={onUnstage}
									data-testid={`complexity-suggestion-undo-${id}`}
								>
									{t("undo")}
								</Button>
							)}
						</>
					) : dismissed ? (
						canUpdate && (
							<Button
								type="button"
								variant="outline"
								size="sm"
								className="h-7"
								onClick={onRestore}
								data-testid={`complexity-suggestion-restore-${id}`}
							>
								{t("restore")}
							</Button>
						)
					) : (
						canUpdate && (
							<>
								{proposal && (
									<Button
										type="button"
										size="sm"
										className="h-7"
										disabled={stagingLocked}
										onClick={() => onAccept(proposal)}
										data-testid={`complexity-suggestion-accept-${id}`}
									>
										{t("accept")}
									</Button>
								)}
								<DropdownMenu>
									<DropdownMenuTrigger asChild>
										<Button
											type="button"
											variant="outline"
											size="sm"
											className="h-7"
											disabled={stagingLocked}
											data-testid={`complexity-suggestion-set-tier-${id}`}
										>
											{t("setTier")}
											<ChevronDown className="size-3" />
										</Button>
									</DropdownMenuTrigger>
									<DropdownMenuContent align="end">
										<DropdownMenuLabel className="text-muted-foreground text-xs font-normal">{t("addGroupTo")}</DropdownMenuLabel>
										{COMPLEXITY_TIER_VALUES.map((tier) => (
											<DropdownMenuItem
												key={tier}
												onSelect={() => onAccept(tier)}
												data-testid={`complexity-suggestion-set-tier-${id}-${tier.toLowerCase()}`}
											>
												{tierLabel(tier)}
												{tier === proposal && <span className="text-muted-foreground ml-auto pl-4 text-xs">{t("proposed")}</span>}
											</DropdownMenuItem>
										))}
									</DropdownMenuContent>
								</DropdownMenu>
								<Button
									type="button"
									variant="ghost"
									size="sm"
									className="size-7 p-0"
									onClick={onDismiss}
									aria-label={t("dismissGroupAria")}
									title={t("dismiss")}
									data-testid={`complexity-suggestion-dismiss-${id}`}
								>
									<X className="size-3.5" />
								</Button>
							</>
						)
					)}
				</div>
			</div>

			{accepted && staged && (
				<AcceptedDetails group={group} staged={staged} canUpdate={canUpdate && !stagingLocked} onEdit={onEditStaged} />
			)}

			{expanded && (
				<div className="grid gap-6 px-3 pb-4 pl-[3.25rem] text-xs md:grid-cols-2" data-testid={`complexity-suggestion-details-${id}`}>
					<div className="space-y-3">
						<div className="space-y-1">
							<h4 className="font-medium">{t("whyHeading")}</h4>
							<p className="text-muted-foreground leading-relaxed">{t(`categoryDescription.${group.category}`)}</p>
							{splitLine && <p className="text-muted-foreground">{t("splitToday", { split: splitLine })}</p>}
							{group.nearest_phrase && (
								<p className="text-muted-foreground">
									{t("nearestShort", { phrase: group.nearest_phrase, tier: tierLabel(group.nearest_tier ?? "") })}{" "}
									{minSimilarity && minSimilarity > 0
										? group.nearest_similarity >= minSimilarity
											? t("similarityAbove", { similarity: fraction(group.nearest_similarity), cutoff: fraction(minSimilarity) })
											: t("similarityBelow", { similarity: fraction(group.nearest_similarity), cutoff: fraction(minSimilarity) })
										: t("similarityOnly", { similarity: fraction(group.nearest_similarity) })}
								</p>
							)}
						</div>
						<div className="space-y-1">
							<h4 className="font-medium">{t("proposalHeading")}</h4>
							<p className="text-muted-foreground leading-relaxed">
								{proposal ? t("proposalFrom", { tier: tierLabel(proposal) }) : t("proposalNone", { tier: tierLabel(group.tier) })}
							</p>
							{group.reason && <p className="text-muted-foreground leading-relaxed italic">“{group.reason}”</p>}
						</div>
					</div>
					<div className="space-y-2">
						<h4 className="font-medium">{t("howOftenHeading")}</h4>
						<GroupExamples id={id} routerId={group.router_id} count={group.request_count} />
					</div>
				</div>
			)}
		</div>
	);
}

// AcceptedDetails shows what an accepted group adds: its phrases, editable
// until Save.
function AcceptedDetails({
	group,
	staged,
	canUpdate,
	onEdit,
}: {
	group: Suggestion;
	staged: StagedSuggestion;
	canUpdate: boolean;
	onEdit: (from: string, to: string) => boolean;
}) {
	return (
		<div className="space-y-2 px-3 pb-3 pl-[3.25rem]" data-testid={`complexity-suggestion-accepted-${group.id}`}>
			{staged.phrases.map((phrase) => (
				<StagedPhraseInput
					key={phrase}
					phrase={phrase}
					disabled={!canUpdate}
					onCommit={(next) => onEdit(phrase, next)}
					testId={`complexity-suggestion-phrase-input-${group.id}`}
				/>
			))}
		</div>
	);
}

// StagedPhraseInput edits one added phrase, committing on Enter or blur and
// putting the text back on Escape.
function StagedPhraseInput({
	phrase,
	disabled,
	onCommit,
	testId,
}: {
	phrase: string;
	disabled: boolean;
	// Reports whether the edit was taken; a refused one puts the phrase back.
	onCommit: (next: string) => boolean;
	testId: string;
}) {
	const { t } = useTranslation("models", { keyPrefix: "routing.complexityUi.suggestions" });
	const [draft, setDraft] = useState(phrase);
	useEffect(() => setDraft(phrase), [phrase]);
	const commit = () => {
		const next = draft.trim();
		if (!next) {
			setDraft(phrase);
			return;
		}
		if (next !== phrase && !onCommit(next)) setDraft(phrase);
	};
	return (
		<label className="flex items-center gap-2 text-xs">
			<span className="shrink-0 rounded-[3px] bg-emerald-500/15 px-1 font-mono text-[9px] font-semibold tracking-wide text-emerald-700 uppercase dark:text-emerald-300">
				{t("newTag")}
			</span>
			<span className="text-muted-foreground shrink-0">{t("phraseAdded")}</span>
			<Input
				value={draft}
				disabled={disabled}
				onChange={(event) => setDraft(event.target.value)}
				onBlur={commit}
				onKeyDown={(event) => {
					if (event.key === "Enter") {
						event.preventDefault();
						commit();
					}
					if (event.key === "Escape") {
						event.preventDefault();
						setDraft(phrase);
					}
				}}
				aria-label={t("stagedInputAria")}
				className="h-7 max-w-md text-xs"
				data-testid={testId}
			/>
		</label>
	);
}

// GroupExamples reads the requests a group came from, only once its row is
// opened.
function GroupExamples({ id, routerId, count }: { id: string; routerId: string; count: number }) {
	const { t } = useTranslation("models", { keyPrefix: "routing.complexityUi.suggestions" });
	const { t: tCommon } = useTranslation("common");
	const { data, isLoading, isError, error, refetch } = useGetComplexitySuggestionQuery({ id, routerId });
	const readable = (data?.examples ?? []).filter((example) => example.text);
	const hidden = (data?.examples.length ?? 0) - readable.length;
	return (
		<div className="min-w-0 space-y-1.5" data-testid={`complexity-suggestion-examples-${id}`}>
			<p className="text-muted-foreground leading-relaxed">
				{t("howOftenLine", { count, formatted: new Intl.NumberFormat(getLocale()).format(count) })}
			</p>
			{isLoading ? (
				<LoaderCircle className="text-muted-foreground size-3.5 animate-spin" />
			) : isError ? (
				<div className="flex flex-wrap items-center gap-2" data-testid={`complexity-suggestion-examples-error-${id}`}>
					<p className="text-destructive">{t("examplesFailed", { error: getErrorMessage(error) })}</p>
					<Button
						type="button"
						variant="outline"
						size="sm"
						className="h-7"
						onClick={() => void refetch()}
						data-testid={`complexity-suggestion-examples-retry-${id}`}
					>
						{tCommon("retry")}
					</Button>
				</div>
			) : (
				<ul className="text-muted-foreground space-y-1">
					{readable.slice(0, 5).map((example) => (
						<li key={example.log_id} className="line-clamp-2 break-words">
							“{example.text}”
						</li>
					))}
				</ul>
			)}
			{hidden > 0 && <p className="text-muted-foreground italic">{t("hiddenExamples", { count: hidden })}</p>}
		</div>
	);
}