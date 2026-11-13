import { Alert, AlertDescription } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Textarea } from "@/components/ui/textarea";
import { getLocale } from "@/lib/i18n";
import { getErrorMessage } from "@/lib/store";
import { useTestComplexitySuggestionPhrasesMutation } from "@/lib/store/apis/governanceApi";
import { COMPLEXITY_TIER_VALUES, DecisionTier, PhraseRouting, PhraseTestResult, Suggestion } from "@/lib/types/complexityRouter";
import { cn } from "@/lib/utils";
import { ArrowRight, FlaskConical, LoaderCircle, Plus, TriangleAlert } from "lucide-react";
import { useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";

// A tested phrase this close to an existing one adds nothing the bank does not
// already say; the gateway drops suggested phrases above the same bar.
const DUPLICATE_SIMILARITY = 0.97;

interface Props {
	suggestion: Suggestion | null;
	onOpenChange: (open: boolean) => void;
	canStage: boolean;
	// Stages the phrases as edited in the dialog, under the chosen tier.
	onStage: (suggestion: Suggestion, tier: DecisionTier, phrases: string[]) => void;
}

// PhraseTestDialog routes a suggestion's examples against the live phrase bank
// with and without its phrases, edited if wanted, before they are staged.
// Nothing is saved.
export default function PhraseTestDialog({ suggestion, onOpenChange, canStage, onStage }: Props) {
	const { t } = useTranslation("models", { keyPrefix: "routing.complexityUi.suggestions" });
	const { t: tTier } = useTranslation("models", { keyPrefix: "routing.complexityUi" });
	const { t: tCommon } = useTranslation("common");
	const [phrases, setPhrases] = useState<string[]>([]);
	const [tier, setTier] = useState<DecisionTier>("SIMPLE");
	const [result, setResult] = useState<PhraseTestResult | null>(null);
	const [error, setError] = useState<string | null>(null);
	const [runTest, { isLoading }] = useTestComplexitySuggestionPhrasesMutation();
	// testRef counts the inputs a result may belong to: it moves on with every
	// edit and every run, and an answer is shown only if it still matches, so a
	// result is never displayed against phrases or a tier it was not run on.
	const testRef = useRef(0);
	const clearResult = () => {
		testRef.current += 1;
		setResult(null);
		setError(null);
	};

	useEffect(() => {
		if (!suggestion) return;
		setPhrases(suggestion.phrases);
		setTier(suggestion.tier);
		testRef.current += 1;
		setResult(null);
		setError(null);
	}, [suggestion]);

	const cleaned = phrases.map((phrase) => phrase.trim()).filter(Boolean);
	const tierLabel = (value: string) => {
		switch (value) {
			case "SIMPLE":
				return tTier("tierSimple");
			case "MEDIUM":
				return tTier("tierMedium");
			case "COMPLEX":
				return tTier("tierComplex");
			default:
				return value;
		}
	};
	const similarity = (value: number) =>
		new Intl.NumberFormat(getLocale(), { maximumFractionDigits: 2, minimumFractionDigits: 2 }).format(value);

	const run = () => {
		if (!suggestion || cleaned.length === 0) return;
		setError(null);
		const test = ++testRef.current;
		runTest({ phrases: cleaned.map((text) => ({ tier, text })), suggestion_id: suggestion.id, routerId: suggestion.router_id })
			.unwrap()
			.then((answer) => {
				if (testRef.current === test) setResult(answer);
			})
			.catch((err) => {
				if (testRef.current !== test) return;
				setResult(null);
				setError(getErrorMessage(err));
			});
	};

	// routing shows where a request goes: the tier of its nearest phrase when
	// that clears the router's similarity floor, and "no match" when it does
	// not, since phrases then give no tier and the fallback decides. The
	// similarity always shows; the phrase it was measured against is
	// truncated after it.
	const routing = (value: PhraseRouting, testid: string) => (
		<div className="min-w-0 space-y-0.5" data-testid={testid}>
			{value.accepted ? (
				<Badge variant={value.candidate ? "success" : "secondary"} data-testid={`${testid}-tier`}>
					{tierLabel(value.tier)}
				</Badge>
			) : (
				<Badge variant="outline" title={t("belowFloorTip")} data-testid={`${testid}-unmatched`}>
					{t("noMatch")}
				</Badge>
			)}
			<p className="text-muted-foreground flex min-w-0 items-baseline gap-1 text-[11px]">
				<span className="shrink-0 font-mono tabular-nums" title={t("similarityTip")}>
					{similarity(value.similarity)}
				</span>
				{!value.accepted && <span className="shrink-0">{t("nearestTier", { tier: tierLabel(value.tier) })}</span>}
				<span className="truncate" title={value.phrase}>
					{t("quoted", { phrase: value.phrase })}
				</span>
			</p>
		</div>
	);

	return (
		<Dialog open={suggestion !== null} onOpenChange={onOpenChange}>
			<DialogContent className="max-w-3xl" data-testid="complexity-phrase-test-dialog">
				<DialogHeader>
					<DialogTitle>{t("testTitle")}</DialogTitle>
					<DialogDescription>{t("testDescription")}</DialogDescription>
				</DialogHeader>

				<div className="custom-scrollbar max-h-[60vh] space-y-4 overflow-y-auto pr-1">
					<div className="flex flex-wrap items-end gap-3">
						<div className="min-w-0 flex-1 space-y-2">
							{phrases.map((phrase, index) => (
								<Textarea
									// Position is the identity here: phrases are edited in place.
									key={index}
									rows={2}
									value={phrase}
									aria-label={t("phraseInputAria", { index: index + 1 })}
									data-testid={`complexity-phrase-test-phrase-input-${index}`}
									onChange={(event) => {
										setPhrases((current) => current.map((p, i) => (i === index ? event.target.value : p)));
										clearResult();
									}}
								/>
							))}
						</div>
						<Select
							value={tier}
							onValueChange={(value) => {
								setTier(value as DecisionTier);
								clearResult();
							}}
						>
							<SelectTrigger className="w-36" data-testid="complexity-phrase-test-tier-select" aria-label={t("tierAria")}>
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
					</div>

					{error && (
						<Alert variant="destructive" data-testid="complexity-phrase-test-error">
							<TriangleAlert className="h-4 w-4" />
							<AlertDescription>{error}</AlertDescription>
						</Alert>
					)}

					{result && <PhraseTestSummary result={result} tier={tier} tierLabel={tierLabel} />}
					{result && result.examples.length > 0 && (
						<div className="divide-y rounded-sm border" data-testid="complexity-phrase-test-result">
							<div className="text-muted-foreground hidden gap-3 px-3 py-2 text-[11px] font-medium sm:grid sm:grid-cols-[1fr_10rem_auto_10rem]">
								<span>{t("testColumnExample")}</span>
								<span>{t("testColumnNow")}</span>
								<span aria-hidden className="w-3.5" />
								<span>{t("testColumnWith")}</span>
							</div>
							{result.examples.map((example) => (
								<div
									key={example.log_id}
									className={cn(
										"grid gap-3 border-l-2 p-3 text-xs sm:grid-cols-[1fr_10rem_auto_10rem] sm:items-center",
										example.changed ? "border-l-primary" : "border-l-transparent opacity-60",
									)}
									data-testid={`complexity-phrase-test-example-${example.log_id}`}
									data-changed={example.changed}
								>
									<div className="min-w-0 space-y-1">
										<p className="line-clamp-3 break-words">{example.text}</p>
										{example.changed && (
											<Badge variant="outline" className="text-[10px]">
												{t("changedBadge")}
											</Badge>
										)}
									</div>
									{routing(example.current, `complexity-phrase-test-example-current-${example.log_id}`)}
									<ArrowRight className="text-muted-foreground hidden size-3.5 sm:block" />
									{routing(example.with_phrases, `complexity-phrase-test-example-with-${example.log_id}`)}
								</div>
							))}
						</div>
					)}
				</div>

				<DialogFooter className="gap-2">
					<Button
						type="button"
						variant="outline"
						size="sm"
						onClick={() => onOpenChange(false)}
						data-testid="complexity-phrase-test-close-button"
					>
						{tCommon("close")}
					</Button>
					<Button
						type="button"
						variant="outline"
						size="sm"
						onClick={run}
						disabled={isLoading || cleaned.length === 0}
						data-testid="complexity-phrase-test-run-button"
					>
						{isLoading ? <LoaderCircle className="size-3.5 animate-spin" /> : <FlaskConical className="size-3.5" />}
						{t("runTest")}
					</Button>
					{canStage && suggestion && (
						<Button
							type="button"
							size="sm"
							disabled={cleaned.length === 0}
							onClick={() => {
								onStage(suggestion, tier, cleaned);
								onOpenChange(false);
							}}
							data-testid="complexity-phrase-test-add-button"
						>
							<Plus className="size-3.5" />
							{t("addToTier", { tier: tierLabel(tier) })}
						</Button>
					)}
				</DialogFooter>
			</DialogContent>
		</Dialog>
	);
}
interface SummaryProps {
	result: PhraseTestResult;
	tier: DecisionTier;
	tierLabel: (tier: string) => string;
}

// PhraseTestSummary says what adding the phrases changes: how many examples
// would route differently, how many land on the chosen tier, and how many
// clear the similarity floor that did not before. It warns when a phrase
// nearly repeats the bank, or nearly repeats one of the examples, which makes
// it match that one request rather than requests like it.
function PhraseTestSummary({ result, tier, tierLabel }: SummaryProps) {
	const { t } = useTranslation("models", { keyPrefix: "routing.complexityUi.suggestions" });
	const examples = result.examples;
	const changed = examples.filter((example) => example.changed).length;
	const toTier = examples.filter((example) => example.with_phrases.accepted && example.with_phrases.tier === tier).length;
	const newlyMatched = examples.filter((example) => !example.current.accepted && example.with_phrases.accepted).length;
	const copies = examples.filter((example) => example.with_phrases.candidate && example.with_phrases.similarity >= DUPLICATE_SIMILARITY);
	// With no example to route, what the phrases change is unknown, but a
	// phrase that repeats the bank is still worth saying.
	return (
		<div className="space-y-3" data-testid="complexity-phrase-test-summary">
			{examples.length === 0 ? (
				<p className="text-sm font-medium" data-testid="complexity-phrase-test-no-examples">
					{t("noReadableExamples")}
				</p>
			) : (
				<>
					<p className="text-sm font-medium" data-testid="complexity-phrase-test-changed">
						{t("testChanged", { changed, count: examples.length })}
					</p>
					<div className="flex flex-wrap gap-2 text-xs">
						<span className="bg-muted rounded-sm px-2 py-1" data-testid="complexity-phrase-test-to-tier">
							{t("testToTier", { tier: tierLabel(tier), routed: toTier, count: examples.length })}
						</span>
						<span className="bg-muted rounded-sm px-2 py-1" data-testid="complexity-phrase-test-newly-matched">
							{t("testNewlyMatched", { count: newlyMatched })}
						</span>
					</div>
				</>
			)}
			{result.phrases
				.filter((phrase) => phrase.nearest_existing.similarity >= DUPLICATE_SIMILARITY)
				.map((phrase) => (
					<Alert key={phrase.text} variant="warning" data-testid="complexity-phrase-test-duplicate">
						<TriangleAlert className="h-4 w-4" />
						<AlertDescription>{t("nearDuplicate", { phrase: phrase.text, existing: phrase.nearest_existing.phrase })}</AlertDescription>
					</Alert>
				))}
			{copies.map((example) => (
				<Alert key={example.log_id} variant="warning" data-testid="complexity-phrase-test-copies-example">
					<TriangleAlert className="h-4 w-4" />
					<AlertDescription>{t("copiesExample", { phrase: example.with_phrases.phrase, example: example.text })}</AlertDescription>
				</Alert>
			))}
		</div>
	);
}