import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { getLocale } from "@/lib/i18n";
import { getErrorMessage } from "@/lib/store";
import { useTestComplexitySuggestionExamplesMutation } from "@/lib/store/apis/governanceApi";
import { cn } from "@/lib/utils";
import { ArrowLeft, ArrowRight, LoaderCircle } from "lucide-react";
import { useTranslation } from "react-i18next";
import { examplesTestSummary, StagedSuggestion } from "./suggestions.utils";

interface Props {
	routerId: string;
	// The staged suggestions whose examples are tried, each on its own group's
	// requests.
	staged: StagedSuggestion[];
	tierLabel: (tier: string) => string;
	onBack: () => void;
}

// SuggestionExamplesTestView is a decision-model router's counterpart of the
// impact review. A decision model is asked about every request, so there is no
// replay of the whole window: each staged group's examples are tried on that
// group's own requests, asked of the router's decision model with its
// guidance as it is and with the staged examples added; a staged signal is
// tried the same way, on the requests of the groups it was written from. Each
// test costs two decision-model calls per request, so it runs only when asked
// to.
export default function SuggestionExamplesTestView({ routerId, staged, tierLabel, onBack }: Props) {
	const { t } = useTranslation("models", { keyPrefix: "routing.complexityUi.suggestions.examplesTest" });
	return (
		<div className="space-y-4" data-testid="complexity-suggestions-examples-test">
			<button
				type="button"
				onClick={onBack}
				className="text-muted-foreground hover:text-foreground inline-flex items-center gap-1 text-sm"
				data-testid="complexity-suggestions-examples-test-back"
			>
				<ArrowLeft className="size-3.5" />
				{t("back")}
			</button>
			<section className="space-y-1 rounded-sm border p-5">
				<h3 className="text-sm font-semibold">{t("title")}</h3>
				<p className="text-muted-foreground text-xs leading-relaxed">{t("description")}</p>
			</section>
			{staged.map((entry) => (
				<StagedGroupTest key={entry.id} routerId={routerId} entry={entry} tierLabel={tierLabel} />
			))}
		</div>
	);
}

function StagedGroupTest({
	routerId,
	entry,
	tierLabel,
}: {
	routerId: string;
	entry: StagedSuggestion;
	tierLabel: (tier: string) => string;
}) {
	const { t } = useTranslation("models", { keyPrefix: "routing.complexityUi.suggestions.examplesTest" });
	const [runTest, { data, isLoading, error, reset }] = useTestComplexitySuggestionExamplesMutation();
	const run = () => {
		reset();
		const draft = entry.phrases.map((text) => ({ tier: entry.tier, text }));
		void runTest({ routerId, suggestionId: entry.id, ...(entry.list === "signals" ? { signals: draft } : { examples: draft }) });
	};
	const money = (value: number) =>
		new Intl.NumberFormat(getLocale(), { style: "currency", currency: "USD", maximumFractionDigits: value < 1 ? 4 : 2 }).format(value);
	const summary = data ? examplesTestSummary(data, entry.tier) : undefined;
	const tierName = (tier: string) => (tier ? tierLabel(tier) : t("noTier"));

	return (
		<section className="rounded-sm border" data-testid={`complexity-suggestions-examples-test-group-${entry.id}`}>
			<div className="flex flex-wrap items-start justify-between gap-3 border-b px-4 py-3">
				<div className="min-w-0 space-y-1">
					<p className="text-sm font-medium">
						{entry.list === "signals"
							? t("signalHeading", { tier: tierLabel(entry.tier) })
							: t("groupHeading", { tier: tierLabel(entry.tier) })}
					</p>
					<ul className="text-muted-foreground list-disc space-y-0.5 pl-4 text-xs">
						{entry.phrases.map((example) => (
							<li key={example}>{example}</li>
						))}
					</ul>
				</div>
				<Button
					type="button"
					size="sm"
					variant={data ? "outline" : "default"}
					onClick={run}
					disabled={isLoading}
					data-testid={`complexity-suggestions-examples-test-run-${entry.id}`}
				>
					{isLoading && <LoaderCircle className="size-3.5 animate-spin" />}
					{data ? t("rerun") : t("run")}
				</Button>
			</div>
			{isLoading ? (
				<p className="text-muted-foreground flex items-center gap-2 px-4 py-3 text-sm">
					<LoaderCircle className="size-4 animate-spin" />
					{t("running")}
				</p>
			) : error ? (
				<p className="text-destructive px-4 py-3 text-sm" data-testid={`complexity-suggestions-examples-test-error-${entry.id}`}>
					{t("failed", { error: getErrorMessage(error) })}
				</p>
			) : data && summary ? (
				summary.asked === 0 ? (
					<p className="text-muted-foreground px-4 py-3 text-sm">{t("noRequests")}</p>
				) : (
					<>
						<p className="px-4 py-3 text-sm" data-testid={`complexity-suggestions-examples-test-summary-${entry.id}`}>
							{t("summary", { count: summary.asked, changed: summary.changed, toTier: summary.toTier, tier: tierLabel(entry.tier) })}{" "}
							<span className="text-muted-foreground">{t("cost", { cost: money(data.cost) })}</span>
						</p>
						<ul className="divide-y border-t">
							{data.requests.map((request) => (
								<li
									key={request.log_id}
									className="grid grid-cols-[minmax(0,1fr)_auto] items-center gap-3 px-4 py-2 text-sm"
									data-testid={`complexity-suggestions-examples-test-request-${request.log_id}`}
								>
									<span className="truncate" title={request.text}>
										{request.text}
									</span>
									<span className={cn("flex items-center gap-1.5 text-xs whitespace-nowrap", !request.changed && "text-muted-foreground")}>
										{tierName(request.current.tier)}
										<ArrowRight className="size-3" />
										{request.changed ? (
											<Badge variant="secondary">{tierName(request.with_examples.tier)}</Badge>
										) : (
											tierName(request.with_examples.tier)
										)}
									</span>
								</li>
							))}
						</ul>
					</>
				)
			) : (
				<p className="text-muted-foreground px-4 py-3 text-xs">{t("notRun")}</p>
			)}
		</section>
	);
}