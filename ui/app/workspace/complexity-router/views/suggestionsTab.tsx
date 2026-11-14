import { Alert, AlertDescription } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { getErrorMessage } from "@/lib/store";
import { useAppDispatch } from "@/lib/store/hooks";
import {
	governanceApi,
	useGetComplexitySuggestionPagesInfiniteQuery,
	useUpdateComplexitySuggestionStatusMutation,
} from "@/lib/store/apis/governanceApi";
import { DecisionTier, EditableKeywordConfig, Suggestion } from "@/lib/types/complexityRouter";
import { mergeSuggestionPages } from "@/lib/utils/suggestionPages";
import { LoaderCircle, Sparkles, TriangleAlert } from "lucide-react";
import { parseAsStringLiteral, useQueryState } from "nuqs";
import { useEffect, useMemo, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { toast } from "sonner";
import RunNowDialog from "./runNowDialog";
import SuggestionGroupsTable, { GroupList } from "./suggestionGroupsTable";
import SuggestionImpactView from "./suggestionImpactView";
import SuggestionRunCard from "./suggestionRunCard";
import { runInProgress, StagedSuggestion } from "./suggestions.utils";

interface Props {
	// The router these suggestions belong to.
	routerId: string;
	canUpdate: boolean;
	// Whether the saved config has suggestions on; the form may differ until
	// it is saved.
	enabled: boolean;
	lookbackDays: number;
	staged: StagedSuggestion[];
	// The draft phrase lists, staged suggestions included.
	keywords: EditableKeywordConfig;
	// Phrases each tier's draft list holds now, staged ones included.
	tierCounts: Record<DecisionTier, number>;
	// The similarity floor a nearest phrase must clear; 0 or absent means none.
	minSimilarity?: number;
	// The floor the draft would save, for replaying the draft's impact.
	draftMinSimilarity?: number;
	// Phrases the bank can still take before the cap.
	remainingPhrases: number;
	onStage: (suggestion: Suggestion, tier: DecisionTier, phrases: string[]) => number;
	onUnstage: (id: string) => void;
	// Reports whether the edit was taken.
	onEditStaged: (id: string, from: string, to: string) => boolean;
	// While a save is in flight: nothing is staged, unstaged or edited.
	stagingLocked?: boolean;
	onOpenSettings: () => void;
	// Why requests are not captured for suggestions, when they are not.
	captureUnavailableReason?: "no_vector_store" | "embedded_vector_store";
}

const TOAST_POSITION = { position: "top-right" } as const;

// How long the tab waits for a run it started to record itself and finish.
const AWAIT_RUN_LIMIT_MS = 10 * 60 * 1000;

// SUGGESTIONS_VIEWS are the views the suggestions tab has besides its list,
// kept in the URL so the draft bar can open one from any tab.
export const SUGGESTIONS_VIEWS = ["impact"] as const;
export const suggestionsViewParser = parseAsStringLiteral(SUGGESTIONS_VIEWS);

// SuggestionsTab shows what the router's suggestion runs found: where the
// last run stands, the groups of similar requests it proposed a tier for,
// and, once groups are accepted into the draft, what applying them would do.
export default function SuggestionsTab({
	routerId,
	canUpdate,
	enabled,
	lookbackDays,
	staged,
	keywords,
	tierCounts,
	minSimilarity,
	draftMinSimilarity,
	remainingPhrases,
	onStage,
	onUnstage,
	onEditStaged,
	stagingLocked = false,
	onOpenSettings,
	captureUnavailableReason,
}: Props) {
	const { t } = useTranslation("models", { keyPrefix: "routing.complexityUi.suggestions" });
	const { t: tTier } = useTranslation("models", { keyPrefix: "routing.complexityUi" });
	const [list, setList] = useState<GroupList>("pending");
	const [view, setView] = useQueryState("view", suggestionsViewParser);
	const [runDialogOpen, setRunDialogOpen] = useState(false);
	// The group a flagged phrase in the impact view leads back to, opened when
	// the list shows again.
	const [focusGroup, setFocusGroup] = useState<string | undefined>(undefined);

	// Polled only while a run is going, so its outcome replaces the progress.
	// A run started here is queued before it records itself, so the list is
	// also polled until that run shows up as the latest one and has ended.
	const [awaitedRun, setAwaitedRun] = useState<string | null>(null);
	const [pollInterval, setPollInterval] = useState(0);
	// The list loads a page at a time; the first page carries the counts, the
	// last run and the schedule, which are the same for either list.
	const {
		data: pages,
		currentData: listPages,
		isLoading,
		error,
		refetch,
		hasNextPage,
		fetchNextPage,
		isFetchingNextPage,
	} = useGetComplexitySuggestionPagesInfiniteQuery(
		{ statuses: [list === "pending" ? "open" : "dismissed"], routerId },
		{ pollingInterval: pollInterval },
	);
	const data = pages?.pages[0];
	// The rows come from the selected list only (currentData): data keeps the
	// last list's rows while a switch loads, and they would show, and be acted
	// on, as the other list's.
	const listLoading = listPages === undefined && !error;
	const groups = useMemo(() => mergeSuggestionPages(listPages?.pages), [listPages]);
	const dispatch = useAppDispatch();
	const latestRun = data?.last_run;
	const awaiting = awaitedRun !== null && (latestRun?.id !== awaitedRun || runInProgress(latestRun));
	const running = runInProgress(latestRun) || awaiting;
	useEffect(() => setPollInterval(running ? 3000 : 0), [running]);
	// Any run ending refreshes every view of the suggestions, the tab's count
	// too: a scheduled run ends while this tab polls just as one started here
	// does, and the count is a query of its own that nothing else refetches.
	const wasRunning = useRef(running);
	useEffect(() => {
		const ended = wasRunning.current && !running;
		wasRunning.current = running;
		if (ended) dispatch(governanceApi.util.invalidateTags(["ComplexitySuggestions"]));
	}, [running, dispatch]);
	useEffect(() => {
		if (awaitedRun === null || awaiting) return;
		setAwaitedRun(null);
	}, [awaitedRun, awaiting]);
	useEffect(() => {
		if (awaitedRun === null) return;
		// A run that never records itself (the node restarted, say) must not
		// keep the tab polling forever.
		const timer = setTimeout(() => setAwaitedRun(null), AWAIT_RUN_LIMIT_MS);
		return () => clearTimeout(timer);
	}, [awaitedRun]);
	const [updateStatus] = useUpdateComplexitySuggestionStatusMutation();

	// A toast's Undo runs after later renders, so it must reach the parent's
	// current staging rather than the one captured when the toast was shown.
	const unstageRef = useRef(onUnstage);
	unstageRef.current = onUnstage;

	const tierLabel = (tier: string) => {
		switch (tier) {
			case "SIMPLE":
				return tTier("tierSimple");
			case "MEDIUM":
				return tTier("tierMedium");
			case "COMPLEX":
				return tTier("tierComplex");
			default:
				return tier;
		}
	};

	// accept stages each group's phrases under the tier chosen for it; a group
	// with no tier (no proposal) is left pending.
	const accept = (groups: Suggestion[], tierOf: (group: Suggestion) => DecisionTier | null) => {
		if (stagingLocked) return;
		const added: string[] = [];
		// Groups with a tier that staged nothing: every phrase is already in
		// the lists or the draft, or the lists are full. Said, not dropped
		// silently, since they stay pending.
		let skipped = 0;
		for (const group of groups) {
			const tier = tierOf(group);
			if (!tier) continue;
			if (onStage(group, tier, group.phrases) > 0) added.push(group.id);
			else skipped++;
		}
		if (added.length === 0) {
			toast.info(remainingPhrases === 0 ? t("atCap") : t("alreadyInBank"), TOAST_POSITION);
			return;
		}
		toast.success(t("acceptedGroups", { count: added.length }), {
			...TOAST_POSITION,
			...(skipped > 0 ? { description: t("acceptedSkipped", { count: skipped }) } : {}),
			action: { label: t("undo"), onClick: () => added.forEach((id) => unstageRef.current(id)) },
		});
	};

	const setStatus = (rows: Suggestion[], status: "dismissed" | "open") => {
		const update = (row: Suggestion, next: "dismissed" | "open") =>
			updateStatus({ id: row.id, status: next, routerId: row.router_id }).unwrap();
		Promise.all(rows.map((row) => update(row, status)))
			.then(() => {
				if (status === "open") {
					toast.success(t("restored"), TOAST_POSITION);
					return;
				}
				toast.success(rows.length === 1 ? t("dismissed") : t("dismissedMany", { count: rows.length }), {
					...TOAST_POSITION,
					action: {
						label: t("undo"),
						onClick: () =>
							rows.forEach((row) =>
								update(row, "open").catch((err) => toast.error(t("statusFailed", { error: getErrorMessage(err) }), TOAST_POSITION)),
							),
					},
				});
			})
			.catch((err) => toast.error(t("statusFailed", { error: getErrorMessage(err) }), TOAST_POSITION));
	};

	const suggestionsOn = data?.enabled ?? enabled;
	const canRun = canUpdate && suggestionsOn && !captureUnavailableReason && !running;
	// 503 means this deployment cannot keep suggestions (no log store); that is
	// a fact to state, not an error to retry.
	const unavailable = typeof error === "object" && error !== null && "status" in error && error.status === 503;
	const counts = data?.counts ?? {};
	const storedCount = (counts.open ?? 0) + (counts.accepted ?? 0) + (counts.dismissed ?? 0) + (counts.superseded ?? 0);

	// The impact view replays what is staged, so with nothing staged the list
	// shows instead; the view stays in the URL and opens once a group is staged.
	if (view === "impact" && staged.length > 0) {
		const groupOfPhrase = (phrase: string) => staged.find((entry) => entry.phrases.includes(phrase))?.id;
		return (
			<SuggestionImpactView
				routerId={routerId}
				staged={staged}
				minSimilarity={draftMinSimilarity}
				groupOfPhrase={groupOfPhrase}
				tierLabel={tierLabel}
				onBack={() => void setView(null)}
				onReviewGroup={(id) => {
					setList("pending");
					setFocusGroup(id);
					void setView(null);
				}}
			/>
		);
	}

	return (
		<div className="space-y-4" data-testid="complexity-suggestions-tab">
			{!suggestionsOn && (
				<Alert variant="info" data-testid="complexity-suggestions-off-callout">
					<Sparkles className="h-4 w-4" />
					<AlertDescription className="flex flex-wrap items-center justify-between gap-2">
						<span>{t("offCallout")}</span>
						{canUpdate && (
							<Button
								type="button"
								size="sm"
								variant="outline"
								onClick={onOpenSettings}
								data-testid="complexity-suggestions-turn-on-button"
							>
								{t("turnOn")}
							</Button>
						)}
					</AlertDescription>
				</Alert>
			)}

			{captureUnavailableReason && (
				<Alert variant="warning" data-testid="complexity-suggestions-capture-unavailable">
					<TriangleAlert className="h-4 w-4" />
					<AlertDescription>
						{captureUnavailableReason === "embedded_vector_store" ? t("captureUnavailableEmbedded") : t("captureUnavailableNone")}
					</AlertDescription>
				</Alert>
			)}

			{latestRun?.status === "failed" && latestRun.error && (
				<Alert variant="warning" data-testid="complexity-suggestions-last-run-error">
					<AlertDescription>{t("lastRunFailed", { error: latestRun.error })}</AlertDescription>
				</Alert>
			)}

			{isLoading ? (
				<div className="text-muted-foreground flex items-center gap-2 px-1 py-8 text-sm">
					<LoaderCircle className="size-4 animate-spin" />
					{t("loading")}
				</div>
			) : unavailable ? (
				<p className="text-muted-foreground py-8 text-sm" data-testid="complexity-suggestions-unavailable">
					{getErrorMessage(error)}
				</p>
			) : error ? (
				<div className="space-y-2 py-8">
					<p className="text-muted-foreground text-sm">{t("loadFailed", { error: getErrorMessage(error) })}</p>
					<Button type="button" size="sm" variant="outline" onClick={() => refetch()} data-testid="complexity-suggestions-retry-button">
						{t("retry")}
					</Button>
				</div>
			) : (
				<>
					<SuggestionRunCard
						routerId={routerId}
						run={latestRun}
						hasSuggestions={storedCount > 0}
						nextRunAt={data?.next_run_at}
						enabled={suggestionsOn}
						canRun={canRun}
						canUpdate={canUpdate}
						awaiting={awaiting}
						onRun={() => setRunDialogOpen(true)}
						onOpenSettings={onOpenSettings}
					/>
					{(storedCount > 0 || staged.length > 0) && (
						<SuggestionGroupsTable
							list={list}
							onListChange={setList}
							pendingCount={counts.open ?? 0}
							dismissedCount={counts.dismissed ?? 0}
							groups={groups}
							loading={listLoading}
							expandGroupId={focusGroup}
							total={listPages?.pages[0]?.total ?? groups.length}
							hasMore={!listLoading && hasNextPage}
							loadingMore={isFetchingNextPage}
							onLoadMore={() => void fetchNextPage()}
							staged={staged}
							tierCounts={tierCounts}
							minSimilarity={minSimilarity}
							canUpdate={canUpdate}
							tierLabel={tierLabel}
							onAccept={accept}
							onUnstage={onUnstage}
							onEditStaged={onEditStaged}
							stagingLocked={stagingLocked}
							onDismiss={(rows) => setStatus(rows, "dismissed")}
							onRestore={(row) => setStatus([row], "open")}
						/>
					)}
				</>
			)}

			<RunNowDialog
				open={runDialogOpen}
				onOpenChange={setRunDialogOpen}
				routerId={routerId}
				lookbackDays={lookbackDays}
				onStarted={setAwaitedRun}
			/>
		</div>
	);
}