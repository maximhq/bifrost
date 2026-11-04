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
import { useEffect, useState } from "react";
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

	useEffect(() => {
		if (!suggestion) return;
		setPhrases(suggestion.phrases);
		setTier(suggestion.tier);
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
	const percent = (value: number) => new Intl.NumberFormat(getLocale(), { style: "percent", maximumFractionDigits: 0 }).format(value);
	const similarity = (value: number) =>
		new Intl.NumberFormat(getLocale(), { maximumFractionDigits: 2, minimumFractionDigits: 2 }).format(value);

	const run = () => {
		if (!suggestion || cleaned.length === 0) return;
		setError(null);
		runTest({ phrases: cleaned.map((text) => ({ tier, text })), suggestion_id: suggestion.id })
			.unwrap()
			.then(setResult)
			.catch((err) => {
				setResult(null);
				setError(getErrorMessage(err));
			});
	};

	const routing = (value: PhraseRouting, testid: string) => (
		<div className="min-w-0 space-y-0.5" data-testid={testid}>
			<div className="flex items-center gap-1.5">
				<Badge variant={value.candidate ? "success" : "secondary"}>{tierLabel(value.tier)}</Badge>
				{!value.accepted && <span className="text-muted-foreground text-[11px]">{t("belowFloor")}</span>}
			</div>
			<p className="text-muted-foreground truncate text-[11px]" title={value.phrase}>
				{t("via", { phrase: value.phrase, similarity: similarity(value.similarity) })}
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
									onChange={(event) => setPhrases((current) => current.map((p, i) => (i === index ? event.target.value : p)))}
								/>
							))}
						</div>
						<Select value={tier} onValueChange={(value) => setTier(value as DecisionTier)}>
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

					{result && (
						<div className="space-y-3" data-testid="complexity-phrase-test-result">
							<p className="text-sm font-medium" data-testid="complexity-phrase-test-taken-over">
								{result.examples.length > 0
									? t("takenOver", { percent: percent(result.taken_over), count: result.examples.length })
									: t("noReadableExamples")}
							</p>
							{result.phrases
								.filter((phrase) => phrase.nearest_existing.similarity >= DUPLICATE_SIMILARITY)
								.map((phrase) => (
									<Alert key={phrase.text} variant="warning" data-testid="complexity-phrase-test-duplicate">
										<TriangleAlert className="h-4 w-4" />
										<AlertDescription>
											{t("nearDuplicate", { phrase: phrase.text, existing: phrase.nearest_existing.phrase })}
										</AlertDescription>
									</Alert>
								))}
							<div className="divide-y rounded-sm border">
								{result.examples.map((example) => (
									<div
										key={example.log_id}
										className={cn(
											"grid gap-3 p-3 text-xs sm:grid-cols-[1fr_9rem_auto_9rem] sm:items-center",
											example.changed && "bg-muted/40",
										)}
										data-testid={`complexity-phrase-test-example-${example.log_id}`}
									>
										<p className="line-clamp-3 break-words">{example.text}</p>
										{routing(example.current, `complexity-phrase-test-example-current-${example.log_id}`)}
										<ArrowRight className="text-muted-foreground hidden size-3.5 sm:block" />
										{routing(example.with_phrases, `complexity-phrase-test-example-with-${example.log_id}`)}
									</div>
								))}
							</div>
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