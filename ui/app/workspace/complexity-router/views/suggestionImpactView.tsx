import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from "@/components/ui/collapsible";
import { getLocale } from "@/lib/i18n";
import { getErrorMessage } from "@/lib/store";
import { useGetComplexitySuggestionImpactMutation } from "@/lib/store/apis/governanceApi";
import { EditableKeywordConfig, UNMATCHED_TIER } from "@/lib/types/complexityRouter";
import { cn } from "@/lib/utils";
import { ArrowLeft, ChevronDown, LoaderCircle } from "lucide-react";
import { useEffect, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { draftBank } from "./suggestions.utils";

interface Props {
	routerId: string;
	// The draft phrase lists, staged suggestions included.
	keywords: EditableKeywordConfig;
	// The similarity floor the draft would save; the draft side is replayed at
	// it, the live side at the serving one.
	minSimilarity?: number;
	// The suggestion each staged phrase came from, so a flagged phrase can lead
	// back to its group.
	groupOfPhrase: (phrase: string) => string | undefined;
	tierLabel: (tier: string) => string;
	onBack: () => void;
	onReviewGroup: (id: string) => void;
}

// SuggestionImpactView replays the router's recent captured requests against
// the draft phrase bank and the live one, before the draft is applied: how the
// tier mix moves, which requests change tier, which new phrases may be taking
// requests by mistake, and what the change would save.
export default function SuggestionImpactView({
	routerId,
	keywords,
	minSimilarity,
	groupOfPhrase,
	tierLabel,
	onBack,
	onReviewGroup,
}: Props) {
	const { t } = useTranslation("models", { keyPrefix: "routing.complexityUi.suggestions.impact" });
	const [replay, { data, isLoading, error }] = useGetComplexitySuggestionImpactMutation();
	const bank = useMemo(() => draftBank(keywords), [keywords]);
	const bankKey = useMemo(() => JSON.stringify(bank), [bank]);
	// Keyed by the bank's text, so a re-render with an equal draft does not
	// replay again. The hook's reset is left out on purpose: its identity
	// changes with every request, so depending on it would replay after each
	// answer, forever. A new request already starts with no data.
	useEffect(() => {
		const phrases = JSON.parse(bankKey) as ReturnType<typeof draftBank>;
		if (phrases.length > 0) void replay({ routerId, phrases, minSimilarity });
	}, [routerId, bankKey, minSimilarity, replay]);
	const number = (value: number) => new Intl.NumberFormat(getLocale()).format(value);
	const percent = (value: number) => new Intl.NumberFormat(getLocale(), { style: "percent", maximumFractionDigits: 1 }).format(value);
	const money = (value: number) =>
		new Intl.NumberFormat(getLocale(), { style: "currency", currency: "USD", maximumFractionDigits: Math.abs(value) < 1 ? 4 : 2 }).format(
			value,
		);
	const signed = (value: number) => (value > 0 ? `+${number(value)}` : number(value));
	const tierName = (tier: string) => (tier === UNMATCHED_TIER ? t("noMatchTier") : tierLabel(tier));
	const days = data ? Math.max(1, Math.round(data.window_seconds / 86400)) : 0;
	const changedShare = data && data.requests > 0 ? data.changed_count / data.requests : 0;

	return (
		<div className="space-y-4" data-testid="complexity-suggestions-impact">
			<button
				type="button"
				onClick={onBack}
				className="text-muted-foreground hover:text-foreground inline-flex items-center gap-1 text-sm"
				data-testid="complexity-suggestions-impact-back"
			>
				<ArrowLeft className="size-3.5" />
				{t("back")}
			</button>

			{bank.length === 0 ? (
				<section className="text-muted-foreground rounded-sm border p-6 text-sm" data-testid="complexity-suggestions-impact-empty">
					{t("emptyBank")}
				</section>
			) : isLoading || (!data && !error) ? (
				<section className="text-muted-foreground flex items-center gap-2 rounded-sm border p-6 text-sm">
					<LoaderCircle className="size-4 animate-spin" />
					{t("replaying")}
				</section>
			) : error ? (
				<section className="space-y-2 rounded-sm border p-6 text-sm" data-testid="complexity-suggestions-impact-error">
					<p>{t("failed", { error: getErrorMessage(error) })}</p>
					<Button type="button" variant="outline" size="sm" onClick={() => void replay({ routerId, phrases: bank, minSimilarity })}>
						{t("retry")}
					</Button>
				</section>
			) : data ? (
				<>
					<section className="space-y-2 rounded-sm border p-5" data-testid="complexity-suggestions-impact-summary">
						<p className="text-muted-foreground text-xs">
							{data.total > data.requests
								? t("sourceSampled", { count: days, requests: number(data.requests), total: number(data.total) })
								: t("source", { count: days, requests: number(data.requests) })}
						</p>
						<p className="text-lg leading-snug font-medium">
							{t("headline", { count: data.changed_count, n: number(data.changed_count), percent: percent(changedShare) })}{" "}
							{data.priced > 0 && (
								<span data-testid="complexity-suggestions-impact-savings">
									{data.savings >= 0
										? t("savings", { amount: money(data.savings), count: days })
										: t("extraCost", { amount: money(-data.savings), count: days })}
								</span>
							)}
						</p>
						<p className="text-muted-foreground text-xs leading-relaxed">{t("caveat")}</p>
					</section>

					<section className="rounded-sm border" data-testid="complexity-suggestions-impact-mix">
						<h3 className="border-b px-4 py-2.5 text-sm font-semibold">{t("mixTitle")}</h3>
						<div className="text-muted-foreground grid grid-cols-[minmax(0,1fr)_minmax(0,1fr)_minmax(0,1fr)_6rem] gap-3 border-b px-4 py-2 text-xs font-medium">
							<span>{t("columnTier")}</span>
							<span className="text-right">{t("columnLive")}</span>
							<span className="text-right">{t("columnDraft")}</span>
							<span className="text-right">{t("columnChange")}</span>
						</div>
						{data.tiers
							.filter((row) => row.tier !== UNMATCHED_TIER || row.live > 0 || row.draft > 0)
							.map((row) => (
								<div
									key={row.tier}
									className="grid grid-cols-[minmax(0,1fr)_minmax(0,1fr)_minmax(0,1fr)_6rem] gap-3 border-b px-4 py-2.5 text-sm last:border-b-0"
									data-testid={`complexity-suggestions-impact-mix-${row.tier.toLowerCase()}`}
								>
									<span>{tierName(row.tier)}</span>
									<span className="text-right font-mono tabular-nums">{number(row.live)}</span>
									<span className="text-right font-mono tabular-nums">{number(row.draft)}</span>
									<span className={cn("text-right font-mono tabular-nums", row.draft === row.live && "text-muted-foreground")}>
										{signed(row.draft - row.live)}
									</span>
								</div>
							))}
					</section>

					{data.attention.length > 0 && (
						<ImpactSection
							title={t("attentionTitle")}
							count={data.attention.length}
							defaultOpen
							testId="complexity-suggestions-impact-attention"
						>
							<ul className="divide-y">
								{data.attention.map((item) => {
									const group = groupOfPhrase(item.phrase);
									return (
										<li key={`${item.tier}-${item.phrase}`} className="flex items-start gap-3 py-2.5 text-sm">
											<div className="min-w-0 flex-1 space-y-0.5">
												<p>{t("attentionLine", { count: item.requests, n: number(item.requests), tier: tierLabel(item.tier) })}</p>
												<p className="text-muted-foreground text-xs">
													{item.text
														? t("attentionDetail", { phrase: item.phrase, from: tierLabel(item.from_tier), example: item.text })
														: t("attentionDetailNoExample", { phrase: item.phrase, from: tierLabel(item.from_tier) })}
												</p>
											</div>
											{group && (
												<Button
													type="button"
													variant="outline"
													size="sm"
													className="h-7 shrink-0"
													onClick={() => onReviewGroup(group)}
													data-testid="complexity-suggestions-impact-review-group"
												>
													{t("reviewGroup")}
												</Button>
											)}
										</li>
									);
								})}
							</ul>
						</ImpactSection>
					)}

					<ImpactSection title={t("changedTitle")} count={data.changed_count} testId="complexity-suggestions-impact-changed">
						{data.changed.length === 0 ? (
							<p className="text-muted-foreground text-sm">{t("noChanges")}</p>
						) : (
							<div className="rounded-sm border">
								<div className="text-muted-foreground grid grid-cols-[minmax(0,2fr)_7rem_7rem] gap-3 border-b px-3 py-2 text-xs font-medium">
									<span>{t("columnRequest")}</span>
									<span>{t("columnLive")}</span>
									<span>{t("columnDraft")}</span>
								</div>
								{data.changed.map((change) => (
									<div
										key={change.log_id}
										className="grid grid-cols-[minmax(0,2fr)_7rem_7rem] gap-3 border-b px-3 py-2 text-sm last:border-b-0"
									>
										<span className={cn("min-w-0 truncate", !change.text && "text-muted-foreground italic")}>
											{change.text || t("textHidden")}
										</span>
										<span>{tierName(change.from)}</span>
										<span className="font-medium">{tierName(change.to)}</span>
									</div>
								))}
								{data.changed_count > data.changed.length && (
									<p className="text-muted-foreground px-3 py-2 text-xs">
										{t("moreChanged", { n: number(data.changed_count - data.changed.length) })}
									</p>
								)}
							</div>
						)}
					</ImpactSection>

					<ImpactSection title={t("savingsTitle")} testId="complexity-suggestions-impact-pricing">
						<div className="space-y-3">
							<div className="rounded-sm border">
								<div className="text-muted-foreground grid grid-cols-[7rem_minmax(0,1.4fr)_minmax(0,1fr)] gap-3 border-b px-3 py-2 text-xs font-medium">
									<span>{t("columnTier")}</span>
									<span>{t("columnPricedAt")}</span>
									<span className="text-right">{t("columnRequests")}</span>
								</div>
								{data.tiers
									.filter((row) => row.tier !== UNMATCHED_TIER)
									.map((row) => (
										<div
											key={row.tier}
											className="grid grid-cols-[7rem_minmax(0,1.4fr)_minmax(0,1fr)] gap-3 border-b px-3 py-2 text-sm last:border-b-0"
										>
											<span>{tierLabel(row.tier)}</span>
											<span className={cn("min-w-0 truncate font-mono text-xs", !row.model && "text-muted-foreground font-sans italic")}>
												{row.model ? `${row.model.provider}/${row.model.model}` : t("noModel")}
											</span>
											<span className="text-right font-mono tabular-nums">
												{number(row.live)} → {number(row.draft)}
											</span>
										</div>
									))}
							</div>
							<p className="text-sm">
								{t("costLine", { live: money(data.live_cost), draft: money(data.draft_cost), count: data.priced, n: number(data.priced) })}
							</p>
							<ul className="text-muted-foreground list-disc space-y-1 pl-4 text-xs leading-relaxed">
								<li>{t("ruleServed")}</li>
								<li>{t("ruleListPrice")}</li>
								<li>{t("ruleSameLength")}</li>
								{data.unpriced > 0 && <li>{t("ruleUnpriced", { count: data.unpriced, n: number(data.unpriced) })}</li>}
							</ul>
						</div>
					</ImpactSection>
				</>
			) : null}
		</div>
	);
}

function ImpactSection({
	title,
	count,
	defaultOpen = false,
	testId,
	children,
}: {
	title: string;
	count?: number;
	defaultOpen?: boolean;
	testId: string;
	children: React.ReactNode;
}) {
	const [open, setOpen] = useState(defaultOpen);
	return (
		<Collapsible open={open} onOpenChange={setOpen} className="rounded-sm border" data-testid={testId}>
			<CollapsibleTrigger
				className="flex w-full items-center gap-2 px-4 py-3 text-left text-sm font-medium"
				data-testid={`${testId}-toggle`}
			>
				{title}
				{count !== undefined && (
					<Badge variant="secondary" className="h-5 px-1.5 text-[10px]">
						{new Intl.NumberFormat(getLocale()).format(count)}
					</Badge>
				)}
				<ChevronDown className={cn("text-muted-foreground ml-auto size-4 transition-transform", open && "rotate-180")} />
			</CollapsibleTrigger>
			<CollapsibleContent className="border-t px-4 py-3">{children}</CollapsibleContent>
		</Collapsible>
	);
}