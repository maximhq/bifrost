import { isRouterConfigured } from "@/app/workspace/complexity-router/formSchema";
import { toneClass } from "@/app/workspace/logs/views/metricStrip";
import { formatPctChange, type MetricsState } from "@/app/workspace/logs/views/metricStrip.utils";
import { Fact, FactStrip } from "@/components/factStrip";
import { MetricSegment, MetricStripCard, MetricTrailing } from "@/components/metricStrip";
import { SetupChecklist, type SetupChecklistItem } from "@/components/setupChecklist";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { getLocale } from "@/lib/i18n";
import { COMPLEXITY_TIER_VALUES, DEFAULT_DECISION_CONFIG, ModelRouter, TIER_PHRASE_LIST_DEFINITIONS } from "@/lib/types/complexityRouter";
import { getScopeLabel } from "@/lib/utils/labels";
import { COMPACT_NUMBER_FORMAT, formatCompactNumber, formatCurrencyNumber } from "@/lib/utils/numbers";
import { TIME_PERIODS } from "@/lib/utils/timeRange";
import NumberFlow from "@number-flow/react";
import { Link } from "@tanstack/react-router";
import type { ParseKeys } from "i18next";
import { Trans, useTranslation } from "react-i18next";
import type { RouterTab } from "./routerDetailView";
import {
	type ComplexityTierValue,
	customRangeLabel,
	type RouterTraffic,
	type TrafficFilter,
	trafficLogsSearch,
	type TrafficSelection,
	useRouterTierSplit,
} from "./useRouterTraffic";

// The classifier's name, and the model behind it.
export function classifierSummary(
	router: ModelRouter,
	t: (key: "classifierNameSemantic" | "classifierNameDecision" | "classifierNameLlm") => string,
) {
	switch (router.classifier) {
		case "decision":
			// Read as the Complexity Router form reads it: a block saved without
			// both a provider and a model uses the gateway's default model.
			return {
				name: t("classifierNameDecision"),
				model: router.decision?.provider && router.decision?.model ? router.decision.model : DEFAULT_DECISION_CONFIG.model,
			};
		case "llm":
			return { name: t("classifierNameLlm"), model: router.llm?.model };
		default:
			return { name: t("classifierNameSemantic"), model: router.semantic?.embedding_model };
	}
}

// tiersWithModels counts the tiers that have at least one target setting a
// model; a tier without one keeps the model the request asked for, even when
// a target moves it to another provider.
export function tiersWithModels(router: ModelRouter): number {
	return COMPLEXITY_TIER_VALUES.filter((tier) =>
		(router.targets ?? []).some((target) => target.tier === tier && Boolean(target.model?.trim())),
	).length;
}

// modelCount counts the models set across the tiers: every target that names
// a model, so two models sharing a tier are two. A target that only moves the
// request to another provider names no model and is not counted, as
// tiersWithModels does not count its tier.
export function modelCount(router: ModelRouter): number {
	return (router.targets ?? []).filter((target) => Boolean(target.model?.trim())).length;
}

// OverviewStatus is which line the overview's status card shows.
export type OverviewStatus = "off" | "on" | "noTraffic" | "serving";

// overviewStatus picks the status line from whether the router is on and what
// it took over the window. Until the traffic has been read (or when the read
// failed) it says only that the router is on: a zero it has not read is not
// "no traffic", as the logs metric strip will not show one either.
export function overviewStatus(enabled: boolean, trafficState: MetricsState, requests: number): OverviewStatus {
	if (!enabled) return "off";
	if (trafficState !== "ready") return "on";
	return requests === 0 ? "noTraffic" : "serving";
}

// trafficPeriodLabel names the period the figures cover: a preset by the name
// the picker gives it, a custom range by its two dates in the picker's timezone.
export function trafficPeriodLabel(selection: TrafficSelection, t: (key: ParseKeys<"common">) => string, timezone: string): string {
	if (selection.period === undefined) return customRangeLabel(selection.start, selection.end, getLocale(), timezone);
	const preset = TIME_PERIODS.find((period) => period.value === selection.period);
	return preset ? t(preset.labelKey) : selection.period;
}

const TIER_LABEL_KEYS = { SIMPLE: "tierSimple", MEDIUM: "tierMedium", COMPLEX: "tierComplex" } as const satisfies Record<
	ComplexityTierValue,
	string
>;

interface OverviewTabProps {
	router: ModelRouter;
	// What the router took over the chosen period; undefined until the read answers.
	traffic: RouterTraffic | undefined;
	// The state of that read: pending, unavailable when it failed, or ready.
	trafficState: MetricsState;
	// The period those figures cover; the tier split reads the same one.
	trafficFilter: TrafficFilter;
	onNavigate: (tab: RouterTab) => void;
	canUpdate: boolean;
	// Opens the router sheet with its switch on, from the status line of a router that is off.
	onTurnOn: () => void;
}

// OverviewTab answers, at a glance, what the router has taken lately, what it
// still needs, and how it is set up. It follows the project page: the two
// figures that matter most on top (requests over the chosen period split by
// tier, and their cost against the period before), a setup checklist while something
// is missing, a strip of facts that each open their tab, and one line on
// whether the router is serving, with the way into its requests and, while it
// is off, the way to turn it on.
export function OverviewTab({ router, traffic, trafficState, trafficFilter, onNavigate, canUpdate, onTurnOn }: OverviewTabProps) {
	const { t } = useTranslation("models", { keyPrefix: "routing.modelRouters.overview" });
	const { t: tcpx } = useTranslation("models", { keyPrefix: "routing.complexityUi" });
	const { t: tmr } = useTranslation("models", { keyPrefix: "routing.modelRouters" });
	const { t: tc } = useTranslation("common");
	const classifier = classifierSummary(router, tcpx);
	const configured = isRouterConfigured(router);
	const modelsSet = tiersWithModels(router);
	const tierNames = TIER_PHRASE_LIST_DEFINITIONS.map((definition) => definition.label).join(" · ");
	const { routing } = router;
	const requests = traffic?.requests ?? 0;
	const cost = traffic?.cost ?? 0;
	const metricState = trafficState;
	const tiers = useRouterTierSplit(router.id, trafficFilter.window);
	const periodLabel = trafficPeriodLabel(trafficFilter.selection, tc, trafficFilter.timezone);
	const tierLine = tiers
		?.map((entry) => t("metrics.tierCount", { tier: tcpx(TIER_LABEL_KEYS[entry.tier]), requests: formatCompactNumber(entry.requests) }))
		.join(" · ");
	// The gateway sends the cost change as a percentage; against a base of 100
	// the logs strip's formatter gives the same arrow, figure and tone.
	const costChange = traffic?.costTrend != null ? formatPctChange(100 + traffic.costTrend, 100, "lower-is-better") : undefined;

	// What the router still needs configured, as a project's checklist lists
	// only configuration; turning it on is the "off" status line's button below.
	const steps: SetupChecklistItem[] = [
		{
			key: "classifier",
			title: t("setup.classifier"),
			detail: configured ? `${classifier.name} · ${tierNames}` : t("setup.classifierPending"),
			done: configured,
			actionLabel: tc("edit"),
			onAction: () => onNavigate("configuration"),
		},
		{
			key: "models",
			title: t("facts.pickModels"),
			detail: modelsSet === COMPLEXITY_TIER_VALUES.length ? t("setup.modelsDone") : t("setup.modelsPending"),
			done: modelsSet === COMPLEXITY_TIER_VALUES.length,
			actionLabel: t("setup.pickModels"),
			onAction: () => onNavigate("models"),
		},
	];
	const done = steps.filter((step) => step.done).length;
	const setupComplete = steps.every((step) => step.done);

	// The status line says whether the router is serving; the figures above
	// already carry the numbers.
	const status = {
		off: { title: t("status.offTitle"), detail: t("status.offDetail") },
		on: { title: t("status.onTitle"), detail: t("status.servingDetail") },
		noTraffic: { title: t("status.noTrafficTitle"), detail: t("status.noTrafficDetail") },
		serving: { title: t("status.servingTitle"), detail: t("status.servingDetail") },
	}[overviewStatus(routing.enabled, trafficState, requests)];
	// Turning on opens the router sheet rather than flipping the router at
	// once: a new router takes every request in its scope, so its routing is
	// shown before the save that turns it on.
	const showTurnOn = canUpdate && !routing.enabled;

	return (
		<div className="flex min-h-0 grow flex-col gap-4 overflow-y-auto" data-testid="model-router-overview">
			<MetricStripCard className="grid-cols-1 sm:grid-cols-2" testId="model-router-metrics">
				<MetricSegment
					title={t("metrics.requests")}
					aside={periodLabel}
					state={metricState}
					footer={
						requests === 0 ? (
							<span className="text-muted-foreground truncate text-xs">{t("metrics.none")}</span>
						) : (
							tierLine && (
								<span className="text-muted-foreground truncate text-xs" title={tierLine} data-testid="model-router-metric-tiers">
									{tierLine}
								</span>
							)
						)
					}
					testId="model-router-metric-requests"
				>
					<NumberFlow value={requests} format={COMPACT_NUMBER_FORMAT} />
				</MetricSegment>
				<MetricSegment
					title={t("metrics.cost")}
					aside={periodLabel}
					state={metricState}
					footer={
						requests === 0 ? (
							<span className="text-muted-foreground truncate text-xs">{t("metrics.none")}</span>
						) : costChange ? (
							<span className="text-muted-foreground truncate text-xs" data-testid="model-router-metric-cost-trend">
								<Trans
									t={t}
									i18nKey="metrics.costTrend"
									values={{ change: costChange.text }}
									components={{ change: <MetricTrailing className={toneClass[costChange.tone]}>{""}</MetricTrailing> }}
								/>
							</span>
						) : (
							<span className="text-muted-foreground truncate text-xs">{t("metrics.noComparison")}</span>
						)
					}
					testId="model-router-metric-cost"
				>
					{formatCurrencyNumber(cost)}
				</MetricSegment>
			</MetricStripCard>

			{!setupComplete && (
				<SetupChecklist
					title={t("setup.title")}
					summary={t("setup.summary", { done, total: steps.length })}
					items={steps}
					testId="model-router-setup"
				/>
			)}

			<FactStrip className="lg:grid-cols-4" testId="model-router-facts">
				<Fact
					label={tcpx("stepClassifier")}
					value={classifier.name}
					sub={classifier.model || t("facts.notConfigured")}
					dim={!configured}
					onClick={() => onNavigate("configuration")}
					testId="model-router-fact-classifier"
				/>
				<Fact
					label={t("facts.tiers")}
					value={tierNames}
					sub={router.session?.enabled ? t("facts.sessionOn") : t("facts.sessionOff")}
					onClick={() => onNavigate("configuration")}
					testId="model-router-fact-tiers"
				/>
				<Fact
					label={tmr("tabs.models")}
					value={t("facts.modelsSet", { count: modelCount(router), tiers: modelsSet })}
					sub={
						modelsSet === 0
							? t("facts.pickModels")
							: Array.from(new Set((router.targets ?? []).map((target) => target.model || target.provider))).join(", ")
					}
					dim={modelsSet === 0}
					onClick={() => onNavigate("models")}
					testId="model-router-fact-models"
				/>
				<Fact
					label={tmr("tabs.routing")}
					value={t("facts.priority", { priority: routing.priority })}
					sub={`${getScopeLabel(routing.scope)} · ${routing.cel_expression ? t("facts.withCondition") : t("facts.everyRequest")}`}
					dim={!routing.enabled}
					onClick={() => onNavigate("routing")}
					testId="model-router-fact-routing"
				/>
			</FactStrip>

			<Card className="gap-0 rounded-sm p-0 shadow-none" data-testid="model-router-status">
				<div className="flex items-center gap-3 px-4 py-3">
					<div className="flex min-w-0 flex-1 flex-col gap-0.5">
						<span className="text-sm font-semibold">{status.title}</span>
						<span className="text-muted-foreground text-xs">{status.detail}</span>
					</div>
					<Button asChild variant="outline" size="sm" className="h-7 shrink-0 text-xs" data-testid="model-router-view-requests-button">
						<Link to="/workspace/logs" search={{ model_router_ids: [router.id], ...trafficLogsSearch(trafficFilter.selection) }}>
							{t("status.viewRequests")}
						</Link>
					</Button>
					{showTurnOn && (
						<Button
							type="button"
							size="sm"
							className="h-7 shrink-0 text-xs"
							onClick={onTurnOn}
							data-testid="model-router-status-turn-on-button"
						>
							{t("status.turnOn")}
						</Button>
					)}
				</div>
			</Card>
		</div>
	);
}