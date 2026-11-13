import { isRouterConfigured } from "@/app/workspace/complexity-router/formSchema";
import { toneClass } from "@/app/workspace/logs/views/metricStrip";
import { formatPctChange, type MetricsState } from "@/app/workspace/logs/views/metricStrip.utils";
import { Fact, FactStrip } from "@/components/factStrip";
import { MetricSegment, MetricStripCard, MetricTrailing } from "@/components/metricStrip";
import { SetupChecklist, type SetupChecklistItem } from "@/components/setupChecklist";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { COMPLEXITY_TIER_VALUES, DEFAULT_DECISION_CONFIG, ModelRouter, TIER_PHRASE_LIST_DEFINITIONS } from "@/lib/types/complexityRouter";
import { getScopeLabel } from "@/lib/utils/labels";
import { COMPACT_NUMBER_FORMAT, formatCompactNumber, formatCurrencyNumber } from "@/lib/utils/numbers";
import NumberFlow from "@number-flow/react";
import { Link } from "@tanstack/react-router";
import { Trans, useTranslation } from "react-i18next";
import type { RouterTab } from "./routerDetailView";
import { type ComplexityTierValue, type RouterTraffic, type TrafficWindow, useRouterTierSplit } from "./useRouterTraffic";

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

const TIER_LABEL_KEYS = { SIMPLE: "tierSimple", MEDIUM: "tierMedium", COMPLEX: "tierComplex" } as const satisfies Record<
	ComplexityTierValue,
	string
>;

interface OverviewTabProps {
	router: ModelRouter;
	// What the router took over the last seven days; undefined until the read answers.
	traffic: RouterTraffic | undefined;
	// The state of that read: pending, unavailable when it failed, or ready.
	trafficState: MetricsState;
	// The window those figures cover; the tier split reads the same one.
	trafficWindow: TrafficWindow;
	onNavigate: (tab: RouterTab) => void;
}

// OverviewTab answers, at a glance, what the router has taken lately, what it
// still needs, and how it is set up. It follows the project page: the two
// figures that matter most on top (requests over the last week split by tier,
// and their cost against the week before), a setup checklist while something
// is missing, a strip of facts that each open their tab, and one line on
// whether the router is serving, with the way into its requests.
export function OverviewTab({ router, traffic, trafficState, trafficWindow, onNavigate }: OverviewTabProps) {
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
	const tiers = useRouterTierSplit(router.id, trafficWindow);
	const tierLine = tiers
		?.map((entry) => t("metrics.tierCount", { tier: tcpx(TIER_LABEL_KEYS[entry.tier]), requests: formatCompactNumber(entry.requests) }))
		.join(" · ");
	// The gateway sends the cost change as a percentage; against a base of 100
	// the logs strip's formatter gives the same arrow, figure and tone.
	const costChange = traffic?.costTrend != null ? formatPctChange(100 + traffic.costTrend, 100, "lower-is-better") : undefined;

	// What the router still needs configured, as a project's checklist lists
	// only configuration; it is turned on from the routers list, which the
	// "off" status line below points to.
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

	return (
		<div className="flex min-h-0 grow flex-col gap-4 overflow-y-auto" data-testid="model-router-overview">
			<MetricStripCard className="grid-cols-1 sm:grid-cols-2" testId="model-router-metrics">
				<MetricSegment
					title={t("metrics.requests")}
					aside={t("metrics.window")}
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
					aside={t("metrics.window")}
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
					value={t("facts.modelsSet", { count: modelsSet })}
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
						<Link to="/workspace/logs" search={{ model_router_ids: [router.id] }}>
							{t("status.viewRequests")}
						</Link>
					</Button>
				</div>
			</Card>
		</div>
	);
}