import { Alert, AlertDescription } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from "@/components/ui/collapsible";
import { Input } from "@/components/ui/input";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { getLocale } from "@/lib/i18n";
import { getErrorMessage } from "@/lib/store";
import {
	useGetComplexitySuggestionQuery,
	useGetComplexitySuggestionsQuery,
	useStartComplexitySuggestionRunMutation,
	useUpdateComplexitySuggestionStatusMutation,
} from "@/lib/store/apis/governanceApi";
import {
	COMPLEXITY_TIER_VALUES,
	DecisionTier,
	SUGGESTION_CATEGORIES,
	Suggestion,
	SuggestionCategory,
	SuggestionRun,
} from "@/lib/types/complexityRouter";
import { cn } from "@/lib/utils";
import { ChevronDown, FlaskConical, LoaderCircle, Play, Plus, Settings2, Sparkles, Undo2, X } from "lucide-react";
import { useEffect, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { toast } from "sonner";
import PhraseTestDialog from "./phraseTestDialog";
import { categoryCounts, filterSuggestions, StagedSuggestion } from "./suggestions.utils";

interface Props {
	canUpdate: boolean;
	// Whether the saved config has suggestions on; the form may differ until
	// it is saved.
	enabled: boolean;
	lookbackDays: number;
	staged: StagedSuggestion[];
	// Phrases the bank can still take before the cap.
	remainingPhrases: number;
	onStage: (suggestion: Suggestion, tier: DecisionTier, phrases: string[]) => number;
	onUnstage: (id: string) => void;
	onEditStaged: (id: string, from: string, to: string) => void;
	onOpenSettings: () => void;
}

type ListTab = "open" | "dismissed";

// SuggestionsTab lists the phrases suggested from routed traffic. Adding one
// stages its phrases into the draft phrase lists, where they can be edited or
// taken back; the page's Save applies them and marks the suggestion accepted.
export default function SuggestionsTab({
	canUpdate,
	enabled,
	lookbackDays,
	staged,
	remainingPhrases,
	onStage,
	onUnstage,
	onEditStaged,
	onOpenSettings,
}: Props) {
	const { t } = useTranslation("models", { keyPrefix: "routing.complexityUi.suggestions" });
	const [listTab, setListTab] = useState<ListTab>("open");
	const [query, setQuery] = useState("");
	const [category, setCategory] = useState<SuggestionCategory | "all">("all");
	const [testing, setTesting] = useState<Suggestion | null>(null);

	// Polled only while a run is going, so its outcome replaces the spinner.
	const [pollInterval, setPollInterval] = useState(0);
	const { data, isLoading, error, refetch } = useGetComplexitySuggestionsQuery({ statuses: [listTab] }, { pollingInterval: pollInterval });
	const running = data?.last_run?.status === "running";
	useEffect(() => setPollInterval(running ? 5000 : 0), [running]);
	const [startRun, { isLoading: isStarting }] = useStartComplexitySuggestionRunMutation();
	const [updateStatus] = useUpdateComplexitySuggestionStatusMutation();

	const stagedIds = useMemo(() => new Set(staged.map((entry) => entry.id)), [staged]);
	const suggestions = data?.suggestions ?? [];
	const counts = useMemo(() => categoryCounts(suggestions, stagedIds), [suggestions, stagedIds]);
	const visible = useMemo(() => filterSuggestions(suggestions, { query, category }, stagedIds), [suggestions, query, category, stagedIds]);
	const openCount = Math.max(0, (data?.counts.open ?? 0) - (listTab === "open" ? staged.length : 0));

	const { t: tTier } = useTranslation("models", { keyPrefix: "routing.complexityUi" });
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

	const stage = (suggestion: Suggestion, tier: DecisionTier, phrases: string[]) => {
		const added = onStage(suggestion, tier, phrases);
		if (added === 0) toast.info(t("alreadyInBank"), { position: "top-right" });
	};

	const setStatus = (suggestion: Suggestion, status: "dismissed" | "open") => {
		updateStatus({ id: suggestion.id, status })
			.unwrap()
			.then(() => {
				toast.success(status === "dismissed" ? t("dismissed") : t("restored"), {
					position: "top-right",
					action:
						status === "dismissed"
							? { label: t("undo"), onClick: () => void updateStatus({ id: suggestion.id, status: "open" }) }
							: undefined,
				});
			})
			.catch((err) => toast.error(t("statusFailed", { error: getErrorMessage(err) }), { position: "top-right" }));
	};

	const runNow = () => {
		startRun()
			.unwrap()
			.then(() => toast.success(t("runStarted"), { position: "top-right" }))
			.catch((err) => toast.error(t("runFailedToStart", { error: getErrorMessage(err) }), { position: "top-right" }));
	};

	// 503 means this deployment cannot keep suggestions (no log store); that is
	// a fact to state, not an error to retry.
	const unavailable = typeof error === "object" && error !== null && "status" in error && error.status === 503;

	return (
		<div className="space-y-4" data-testid="complexity-suggestions-tab">
			<div className="flex flex-col gap-3 md:flex-row md:items-start md:justify-between">
				<div className="min-w-0 space-y-1">
					<h2 className="text-sm font-semibold">{t("title")}</h2>
					<p className="text-muted-foreground max-w-2xl text-xs leading-relaxed">{t("description")}</p>
				</div>
				<div className="flex shrink-0 flex-wrap items-center gap-2">
					<RunStatus run={data?.last_run} nextRunAt={data?.next_run_at} enabled={data?.enabled ?? enabled} />
					<Button type="button" variant="outline" size="sm" onClick={onOpenSettings} data-testid="complexity-suggestions-settings-button">
						<Settings2 className="size-3.5" />
						{t("settings")}
					</Button>
					{canUpdate && (data?.enabled ?? enabled) && (
						<Button
							type="button"
							variant="outline"
							size="sm"
							onClick={runNow}
							disabled={isStarting || running}
							data-testid="complexity-suggestions-run-now-button"
						>
							{isStarting || running ? <LoaderCircle className="size-3.5 animate-spin" /> : <Play className="size-3.5" />}
							{running ? t("running") : t("runNow")}
						</Button>
					)}
				</div>
			</div>

			{!enabled && !(data?.enabled ?? false) && (
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

			{data?.last_run?.status === "failed" && data.last_run.error && (
				<Alert variant="warning" data-testid="complexity-suggestions-last-run-error">
					<AlertDescription>{t("lastRunFailed", { error: data.last_run.error })}</AlertDescription>
				</Alert>
			)}

			<div className="grid gap-4 lg:grid-cols-[minmax(0,1fr)_20rem]">
				<div className="min-w-0 space-y-3">
					<div className="flex flex-wrap items-center gap-2">
						<div className="bg-muted flex rounded-sm p-0.5" role="tablist" aria-label={t("listAria")}>
							{(["open", "dismissed"] as const).map((tab) => (
								<button
									key={tab}
									type="button"
									role="tab"
									aria-selected={listTab === tab}
									onClick={() => setListTab(tab)}
									data-testid={`complexity-suggestions-list-${tab}`}
									className={cn(
										"rounded-sm px-2.5 py-1 text-xs",
										listTab === tab ? "bg-background font-medium shadow-sm" : "text-muted-foreground",
									)}
								>
									{tab === "open" ? t("pendingTab") : t("dismissedTab")}
									<span className="text-muted-foreground ml-1.5 font-mono tabular-nums">
										{tab === "open" ? openCount : (data?.counts.dismissed ?? 0)}
									</span>
								</button>
							))}
						</div>
						<Input
							value={query}
							onChange={(event) => setQuery(event.target.value)}
							placeholder={t("searchPlaceholder")}
							aria-label={t("searchPlaceholder")}
							className="h-8 max-w-xs"
							data-testid="complexity-suggestions-search-input"
						/>
						<Select value={category} onValueChange={(value) => setCategory(value as SuggestionCategory | "all")}>
							<SelectTrigger className="h-8 w-48" data-testid="complexity-suggestions-category-select" aria-label={t("reasonAria")}>
								<SelectValue />
							</SelectTrigger>
							<SelectContent>
								<SelectItem value="all">{t("allReasons", { n: counts.all })}</SelectItem>
								{SUGGESTION_CATEGORIES.map((value) => (
									<SelectItem key={value} value={value} data-testid={`complexity-suggestions-category-option-${value}`}>
										{t(`categoryOption.${value}`, { n: counts[value] })}
									</SelectItem>
								))}
							</SelectContent>
						</Select>
						<span className="text-muted-foreground ml-auto text-xs">{t("lastDays", { count: lookbackDays })}</span>
					</div>

					{isLoading ? (
						<div className="text-muted-foreground flex items-center gap-2 py-8 text-sm">
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
					) : visible.length === 0 ? (
						<p className="text-muted-foreground py-8 text-sm" data-testid="complexity-suggestions-empty">
							{suggestions.length - stagedIds.size > 0 && (query || category !== "all")
								? t("noMatches")
								: listTab === "open"
									? t("emptyPending")
									: t("emptyDismissed")}
						</p>
					) : (
						<div className="divide-y rounded-sm border" data-testid="complexity-suggestions-list">
							{visible.map((suggestion) => (
								<SuggestionRow
									key={suggestion.id}
									suggestion={suggestion}
									dismissed={listTab === "dismissed"}
									canUpdate={canUpdate}
									atCap={remainingPhrases < suggestion.phrases.length}
									tierLabel={tierLabel}
									onStage={stage}
									onTest={() => setTesting(suggestion)}
									onDismiss={() => setStatus(suggestion, "dismissed")}
									onRestore={() => setStatus(suggestion, "open")}
								/>
							))}
						</div>
					)}
				</div>

				<StagedPanel staged={staged} tierLabel={tierLabel} canUpdate={canUpdate} onUnstage={onUnstage} onEdit={onEditStaged} />
			</div>

			<PhraseTestDialog
				suggestion={testing}
				onOpenChange={(open) => !open && setTesting(null)}
				canStage={canUpdate && listTab === "open"}
				onStage={stage}
			/>
		</div>
	);
}

function RunStatus({ run, nextRunAt, enabled }: { run?: SuggestionRun; nextRunAt?: string; enabled: boolean }) {
	const { t } = useTranslation("models", { keyPrefix: "routing.complexityUi.suggestions" });
	const format = (value: string) => new Date(value).toLocaleString(getLocale(), { dateStyle: "medium", timeStyle: "short" });
	return (
		<div className="text-muted-foreground space-y-0.5 text-right text-[11px] leading-tight">
			{run && (
				<p data-testid="complexity-suggestions-last-run">
					{run.status === "running"
						? t("lastRunRunning", { time: format(run.started_at) })
						: run.status === "failed"
							? t("lastRunFailedShort", { time: format(run.finished_at ?? run.started_at) })
							: t("lastRunSummary", { time: format(run.finished_at ?? run.started_at), count: run.created })}
				</p>
			)}
			{enabled && nextRunAt && <p data-testid="complexity-suggestions-next-run">{t("nextRun", { time: format(nextRunAt) })}</p>}
		</div>
	);
}

interface RowProps {
	suggestion: Suggestion;
	dismissed: boolean;
	canUpdate: boolean;
	atCap: boolean;
	tierLabel: (tier: string) => string;
	onStage: (suggestion: Suggestion, tier: DecisionTier, phrases: string[]) => void;
	onTest: () => void;
	onDismiss: () => void;
	onRestore: () => void;
}

function SuggestionRow({ suggestion, dismissed, canUpdate, atCap, tierLabel, onStage, onTest, onDismiss, onRestore }: RowProps) {
	const { t } = useTranslation("models", { keyPrefix: "routing.complexityUi.suggestions" });
	const [open, setOpen] = useState(false);
	const [tier, setTier] = useState<DecisionTier>(suggestion.tier);
	const id = suggestion.id;
	const number = (value: number) => new Intl.NumberFormat(getLocale()).format(value);
	const fraction = (value: number) =>
		new Intl.NumberFormat(getLocale(), { maximumFractionDigits: 2, minimumFractionDigits: 2 }).format(value);
	const percent = (value: number) => new Intl.NumberFormat(getLocale(), { style: "percent", maximumFractionDigits: 0 }).format(value);

	return (
		<Collapsible open={open} onOpenChange={setOpen} data-testid={`complexity-suggestion-row-${id}`}>
			<div className="flex flex-col gap-2 p-3 sm:flex-row sm:items-start">
				<CollapsibleTrigger asChild>
					<button
						type="button"
						className="min-w-0 flex-1 space-y-1 text-left"
						aria-label={t("expandAria")}
						data-testid={`complexity-suggestion-expand-${id}`}
					>
						<div className="flex flex-wrap items-center gap-2">
							{suggestion.phrases.map((phrase) => (
								<span key={phrase} className="text-sm font-medium break-words">
									“{phrase}”
								</span>
							))}
							<Badge variant="secondary">{tierLabel(suggestion.tier)}</Badge>
						</div>
						<p className="text-muted-foreground flex items-center gap-1 text-xs">
							<ChevronDown className={cn("size-3 transition-transform", open && "rotate-180")} />
							{t("reasonLine", {
								reason: t(`category.${suggestion.category}`),
								count: suggestion.request_count,
								formatted: number(suggestion.request_count),
							})}
						</p>
					</button>
				</CollapsibleTrigger>
				{canUpdate && (
					<div className="flex shrink-0 flex-wrap items-center gap-1.5">
						{dismissed ? (
							<Button type="button" variant="outline" size="sm" onClick={onRestore} data-testid={`complexity-suggestion-restore-${id}`}>
								<Undo2 className="size-3.5" />
								{t("restore")}
							</Button>
						) : (
							<>
								<Select value={tier} onValueChange={(value) => setTier(value as DecisionTier)}>
									<SelectTrigger className="h-8 w-28" data-testid={`complexity-suggestion-tier-select-${id}`} aria-label={t("tierAria")}>
										<SelectValue />
									</SelectTrigger>
									<SelectContent>
										{COMPLEXITY_TIER_VALUES.map((value) => (
											<SelectItem key={value} value={value}>
												{tierLabel(value)}
											</SelectItem>
										))}
									</SelectContent>
								</Select>
								<Button
									type="button"
									size="sm"
									onClick={() => onStage(suggestion, tier, suggestion.phrases)}
									disabled={atCap}
									title={atCap ? t("atCap") : undefined}
									data-testid={`complexity-suggestion-add-${id}`}
								>
									<Plus className="size-3.5" />
									{t("add")}
								</Button>
								<Button type="button" variant="ghost" size="sm" onClick={onTest} data-testid={`complexity-suggestion-test-${id}`}>
									<FlaskConical className="size-3.5" />
									{t("test")}
								</Button>
								<Button
									type="button"
									variant="ghost"
									size="sm"
									onClick={onDismiss}
									aria-label={t("dismiss")}
									data-testid={`complexity-suggestion-dismiss-${id}`}
								>
									<X className="size-3.5" />
								</Button>
							</>
						)}
					</div>
				)}
			</div>
			<CollapsibleContent>
				<div className="grid gap-4 px-3 pb-3 text-xs sm:grid-cols-2" data-testid={`complexity-suggestion-details-${id}`}>
					<div className="space-y-1.5">
						<p className="font-medium">{t("whyHeading")}</p>
						<p className="text-muted-foreground leading-relaxed">{t(`categoryDescription.${suggestion.category}`)}</p>
						{suggestion.nearest_phrase && (
							<p className="text-muted-foreground">
								{t("nearestLine", {
									phrase: suggestion.nearest_phrase,
									tier: tierLabel(suggestion.nearest_tier ?? ""),
									similarity: fraction(suggestion.nearest_similarity),
								})}
							</p>
						)}
						{suggestion.reason && <p className="text-muted-foreground italic">{suggestion.reason}</p>}
						<p className="text-muted-foreground">{t("coverageLine", { percent: percent(suggestion.coverage) })}</p>
					</div>
					{open && <SuggestionExamples id={id} count={suggestion.request_count} />}
				</div>
			</CollapsibleContent>
		</Collapsible>
	);
}

// SuggestionExamples reads the requests a suggestion came from, only once its
// row is opened.
function SuggestionExamples({ id, count }: { id: string; count: number }) {
	const { t } = useTranslation("models", { keyPrefix: "routing.complexityUi.suggestions" });
	const { data, isLoading } = useGetComplexitySuggestionQuery(id);
	const readable = (data?.examples ?? []).filter((example) => example.text);
	const hidden = (data?.examples.length ?? 0) - readable.length;
	return (
		<div className="min-w-0 space-y-1.5" data-testid={`complexity-suggestion-examples-${id}`}>
			<p className="font-medium">{t("howOftenHeading")}</p>
			<p className="text-muted-foreground">{t("howOftenLine", { count, formatted: new Intl.NumberFormat(getLocale()).format(count) })}</p>
			{isLoading ? (
				<LoaderCircle className="text-muted-foreground size-3.5 animate-spin" />
			) : (
				<ul className="space-y-1">
					{readable.slice(0, 5).map((example) => (
						<li key={example.log_id} className="bg-muted/50 line-clamp-2 rounded-sm px-2 py-1 break-words">
							{example.text}
						</li>
					))}
				</ul>
			)}
			{hidden > 0 && <p className="text-muted-foreground">{t("hiddenExamples", { count: hidden })}</p>}
		</div>
	);
}

interface StagedPanelProps {
	staged: StagedSuggestion[];
	tierLabel: (tier: string) => string;
	canUpdate: boolean;
	onUnstage: (id: string) => void;
	onEdit: (id: string, from: string, to: string) => void;
}

// StagedPanel shows what Save will add, per tier, editable until then.
function StagedPanel({ staged, tierLabel, canUpdate, onUnstage, onEdit }: StagedPanelProps) {
	const { t } = useTranslation("models", { keyPrefix: "routing.complexityUi.suggestions" });
	return (
		<div className="bg-card h-fit space-y-3 rounded-sm border p-3" data-testid="complexity-suggestions-staged-panel">
			<div className="space-y-1">
				<p className="text-sm font-semibold">{t("stagedTitle")}</p>
				<p className="text-muted-foreground text-xs leading-relaxed">{t("stagedDescription")}</p>
			</div>
			{COMPLEXITY_TIER_VALUES.map((tier) => {
				const entries = staged.filter((entry) => entry.tier === tier);
				return (
					<div key={tier} className="space-y-1.5" data-testid={`complexity-suggestions-staged-${tier.toLowerCase()}`}>
						<div className="flex items-center justify-between">
							<span className="text-xs font-medium">{tierLabel(tier)}</span>
							{entries.length > 0 && (
								<Badge variant="success">{t("newCount", { n: entries.reduce((sum, e) => sum + e.phrases.length, 0) })}</Badge>
							)}
						</div>
						{entries.length === 0 ? (
							<p className="text-muted-foreground text-[11px]">{t("stagedEmpty")}</p>
						) : (
							entries.map((entry) => (
								<div key={entry.id} className="flex items-start gap-1">
									<div className="min-w-0 flex-1 space-y-1">
										{entry.phrases.map((phrase, index) => (
											<StagedPhraseInput
												key={`${entry.id}-${phrase}`}
												value={phrase}
												disabled={!canUpdate}
												testid={`complexity-suggestions-staged-input-${entry.id}-${index}`}
												onCommit={(next) => onEdit(entry.id, phrase, next)}
											/>
										))}
									</div>
									<Button
										type="button"
										variant="ghost"
										size="sm"
										className="h-8 px-2"
										onClick={() => onUnstage(entry.id)}
										aria-label={t("unstage")}
										disabled={!canUpdate}
										data-testid={`complexity-suggestions-staged-undo-${entry.id}`}
									>
										<Undo2 className="size-3.5" />
									</Button>
								</div>
							))
						)}
					</div>
				);
			})}
		</div>
	);
}

// StagedPhraseInput edits a staged phrase and commits on Enter or blur; Esc
// puts the text back.
function StagedPhraseInput({
	value,
	disabled,
	testid,
	onCommit,
}: {
	value: string;
	disabled: boolean;
	testid: string;
	onCommit: (next: string) => void;
}) {
	const { t } = useTranslation("models", { keyPrefix: "routing.complexityUi.suggestions" });
	const [draft, setDraft] = useState(value);
	const commit = () => {
		const next = draft.trim();
		if (!next) {
			setDraft(value);
			return;
		}
		if (next !== value) onCommit(next);
	};
	return (
		<Input
			value={draft}
			disabled={disabled}
			aria-label={t("stagedInputAria")}
			className="h-8 text-xs"
			data-testid={testid}
			onChange={(event) => setDraft(event.target.value)}
			onBlur={commit}
			onKeyDown={(event) => {
				if (event.key === "Enter") {
					event.preventDefault();
					commit();
				}
				if (event.key === "Escape") setDraft(value);
			}}
		/>
	);
}