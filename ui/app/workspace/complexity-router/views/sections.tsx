import { Alert, AlertDescription } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from "@/components/ui/collapsible";
import { Input } from "@/components/ui/input";
import { ModelSelector } from "@/components/ui/modelSelector";
import { ProviderSelector } from "@/components/ui/providerSelector";
import { Select, SelectContent, SelectGroup, SelectItem, SelectLabel, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Switch } from "@/components/ui/switch";
import { TagInput } from "@/components/ui/tagInput";
import { Textarea } from "@/components/ui/textarea";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import {
	COMPLEXITY_TIER_VALUES,
	DecisionGuidanceDefaults,
	DecisionTier,
	KeywordListKey,
	MAX_DECISION_CRITERIA_ITEMS,
	MAX_DECISION_DEFINITION_CHARACTERS,
	MAX_DECISION_PREVIOUS_MESSAGE_COUNT,
	MAX_LLM_PROMPT_CHARACTERS,
	fixedDecisionModels,
	SELF_HOSTED_DECISION_MODELS,
	TIER_PHRASE_LIST_DEFINITIONS,
} from "@/lib/types/complexityRouter";
import { useGetModelsQuery } from "@/lib/store/apis/providersApi";
import type { ModelProvider } from "@/lib/types/config";
import { cn } from "@/lib/utils";
import { decisionProviderIconKey } from "@/lib/utils/decisionModelProviders";
import { Link } from "@tanstack/react-router";
import { ArrowRight, Check, ChevronRight, Info, LoaderCircle, Pencil, RotateCcw, Scale, TriangleAlert, Waypoints } from "lucide-react";
import { useEffect, useMemo, useState } from "react";
import {
	Controller,
	useWatch,
	type Control,
	type FieldError,
	type FieldErrors,
	type UseFormRegister,
	type UseFormSetValue,
} from "react-hook-form";
import {
	clefModelFromProvider,
	decisionTimeoutFieldValue,
	defaultDecisionModel,
	isDecisionProvider,
	selfHostedModelGroups,
	type AnalyzerFormValues,
	type DecisionProviderState,
} from "../formSchema";
import { FieldLabel, InfoTip, SectionHeading } from "./formPrimitives";

// The Complexity Router page's sections. Each binds to the page's single form.

// The three tier lists sit side by side, so each is a fixed-height scroll
// container rather than a fixed number of phrases: phrases wrap to different
// numbers of lines, and equal counts would leave the columns visibly uneven.
//
// This only evens out the lists themselves. The header above them varies too --
// a tier description that wraps to two lines pushes its list down by a line
// while its neighbours stay put -- so the cards stretch to the grid row and each
// is a flex column whose description grows to absorb the difference,
// bottom-aligning all three lists at any column width.
const PHRASE_LIST_HEIGHT = 300;

// Each decision-model tier card stacks two lists, so each gets under half the phrase
// list's height and a card stays about as tall as a phrase card.
const DECISION_LIST_HEIGHT = 140;

const DECISION_TIER_LABELS: Record<DecisionTier, string> = {
	SIMPLE: "Simple",
	MEDIUM: "Medium",
	COMPLEX: "Complex",
};

function testIdPart(value: string) {
	return value.replace(/_/g, "-");
}

interface PhraseTierGridProps {
	control: Control<AnalyzerFormValues>;
	errors: FieldErrors<AnalyzerFormValues>["keywords"];
}

// PhraseTierGrid is the semantic classifier's work surface: one editable list
// of reference phrases per tier.
export function PhraseTierGrid({ control, errors }: PhraseTierGridProps) {
	return (
		<div className="space-y-3">
			<Alert variant="info" data-testid="complexity-router-phrase-defaults-callout">
				<Info className="h-4 w-4" />
				<AlertDescription>
					The added reference phrases are examples to help you get started. We recommend auditing, refining and adding your own reference
					phrases.
				</AlertDescription>
			</Alert>

			{/* Root-level phrase issues such as cross-tier duplicates have no single
			    field to attach to, so they render above the lists. */}
			{errors?.message && (
				<p className="text-destructive text-xs" data-testid="complexity-router-keywords-error">
					{errors.message}
				</p>
			)}

			{/* One column per tier, side by side: the three lists are read against
			    each other, and equal-width columns keep a phrase's tier obvious
			    from its position. */}
			<div className="grid items-stretch gap-3 md:grid-cols-3">
				{TIER_PHRASE_LIST_DEFINITIONS.map(({ key, label, description }) => {
					const fieldError = errors?.[key as KeywordListKey];
					const errorId = `keywords-${key}-error`;
					return (
						<div key={key} className="bg-card relative flex flex-col overflow-hidden rounded-sm border">
							<Controller
								control={control}
								name={`keywords.${key}` as const}
								rules={{
									validate: (value) => (value.length > 0 ? true : `${label} phrases cannot be empty`),
								}}
								render={({ field }) => (
									<div className="flex flex-1 flex-col space-y-2 p-4 pl-5">
										<div className="flex items-center justify-between">
											<span className="text-xs font-medium">{label}</span>
											<span className="text-muted-foreground font-mono text-[11px] tabular-nums">
												{field.value.length} {field.value.length === 1 ? "phrase" : "phrases"}
											</span>
										</div>
										<p className="text-muted-foreground grow text-xs leading-relaxed">{description}</p>
										<TagInput
											data-testid={`complexity-router-keywords-${testIdPart(key)}-input`}
											value={field.value}
											onValueChange={field.onChange}
											listHeight={PHRASE_LIST_HEIGHT}
											submitOnComma={false}
											placeholder="Type a reference phrase and press Enter"
											aria-invalid={fieldError ? true : undefined}
											aria-describedby={fieldError ? errorId : undefined}
											className={cn(fieldError && "border-destructive")}
										/>
										{fieldError && (
											<p id={errorId} className="text-destructive text-xs">
												{fieldError.message}
											</p>
										)}
									</div>
								)}
							/>
						</div>
					);
				})}
			</div>
		</div>
	);
}

interface LLMPromptSectionProps {
	control: Control<AnalyzerFormValues>;
	error: FieldError | undefined;
	canUpdate: boolean;
	defaultPrompt: string;
	livePrompt: string;
	// Called on any operator edit, so a later status refresh never refills a
	// prompt the operator cleared on purpose.
	onEdit: () => void;
	onReset: () => void;
}

// LLMPromptSection edits the fallback classifier's system prompt. It needs
// width and room to iterate, so it sits on the page below the phrase lists
// rather than in the sheet with the fallback's other settings, and only shows
// while the llm fallback is on, because that is the only time it runs.
export function LLMPromptSection({ control, error, canUpdate, defaultPrompt, livePrompt, onEdit, onReset }: LLMPromptSectionProps) {
	return (
		<div className="space-y-3">
			<SectionHeading
				title="Fallback Classification Prompt"
				description="Customize the classification model's system prompt; a default is provided when no phrase matches."
				aside={
					<Button
						type="button"
						variant="ghost"
						size="sm"
						onClick={onReset}
						disabled={!canUpdate || !defaultPrompt || livePrompt === defaultPrompt}
						data-testid="complexity-router-llm-prompt-reset-button"
					>
						<RotateCcw className="h-3.5 w-3.5" />
						Reset to default
					</Button>
				}
			/>
			<Controller
				control={control}
				name="llm.prompt"
				render={({ field }) => (
					<Textarea
						data-testid="complexity-router-llm-prompt-input"
						rows={8}
						maxLength={MAX_LLM_PROMPT_CHARACTERS}
						value={field.value}
						onChange={(event) => {
							onEdit();
							field.onChange(event);
						}}
						onBlur={field.onBlur}
						ref={field.ref}
						disabled={!canUpdate}
						aria-invalid={error ? true : undefined}
						className={cn("font-mono text-xs leading-relaxed", error && "border-destructive focus-visible:ring-destructive")}
					/>
				)}
			/>
			{error ? (
				<p className="text-destructive text-xs">{error.message}</p>
			) : (
				<p className="text-muted-foreground text-xs leading-relaxed">
					Leave blank to use default guidance. Bifrost always appends a fixed response-format section (the tier names and the JSON answer
					contract), so edits here refine what the tiers mean but cannot break routing.{" "}
					<span className="font-mono tabular-nums">
						{livePrompt.length}/{MAX_LLM_PROMPT_CHARACTERS}
					</span>
				</p>
			)}
		</div>
	);
}

interface SessionRoutingCardProps {
	control: Control<AnalyzerFormValues>;
	errors: FieldErrors<AnalyzerFormValues>["session"];
	canUpdate: boolean;
}

// SessionRoutingCard toggles session-aware routing, which works the same on
// top of either classifier.
export function SessionRoutingCard({ control, errors, canUpdate }: SessionRoutingCardProps) {
	return (
		<div className="bg-card flex items-center justify-between gap-6 rounded-sm border p-4">
			<div className="space-y-1">
				<FieldLabel htmlFor="complexity-router-session-enabled">Session-aware routing</FieldLabel>
				<p className="text-muted-foreground max-w-3xl text-xs leading-relaxed">
					Keep each session at its highest complexity tier for 24 hours of inactivity. Harder turns can move up; easier turns stay put to
					reduce model changes. Requests without a session ID route independently.
				</p>
				{errors?.enabled && (
					<p id="complexity-router-session-enabled-error" className="text-destructive text-xs">
						{errors.enabled.message}
					</p>
				)}
			</div>
			<Controller
				control={control}
				name="session.enabled"
				render={({ field }) => (
					<Switch
						id="complexity-router-session-enabled"
						data-testid="complexity-router-session-enabled-switch"
						checked={field.value}
						onCheckedChange={field.onChange}
						disabled={!canUpdate}
						aria-invalid={errors?.enabled ? true : undefined}
						aria-describedby={errors?.enabled ? "complexity-router-session-enabled-error" : undefined}
					/>
				)}
			/>
		</div>
	);
}

const LAYA_MODELS: readonly string[] = SELF_HOSTED_DECISION_MODELS[0].models;

interface DecisionFieldsProps {
	control: Control<AnalyzerFormValues>;
	register: UseFormRegister<AnalyzerFormValues>;
	setValue: UseFormSetValue<AnalyzerFormValues>;
	errors: FieldErrors<AnalyzerFormValues>["decision"];
	canUpdate: boolean;
	// Every configured provider; the provider picker narrows it to decision providers.
	providers: ModelProvider[];
	// Ids of the selected provider's enabled keys, narrowing its model list to
	// what those keys may serve.
	providerKeyIds: string[];
}

// DecisionFields holds the decision model's settings: which model answers, and
// how much conversation it sees and for how long. They apply wherever the
// decision model runs, as the primary classifier or as the semantic fallback, so
// both places render this. The model control follows the provider: Typesafe lists
// Jev releases, OpenRouter and OpenAI offer their fixed decision-model lists, a
// Cloudflare provider's URL fixes its Clef model, and a self-hosted provider offers
// the models it lists, or the known Laya and Nimble checkpoints when it lists none.
export function DecisionFields({ control, register, setValue, errors, canUpdate, providers, providerKeyIds }: DecisionFieldsProps) {
	const providerName = useWatch({ control, name: "decision.provider" });
	const model = useWatch({ control, name: "decision.model" });
	const provider = providers.find((candidate) => candidate.name === providerName);
	const fixedModels = fixedDecisionModels(providerName);
	const servesJev = providerName === "typesafe";
	const servesClef = clefModelFromProvider(provider) !== undefined;
	const selfHosted = provider !== undefined && !servesJev && !servesClef && !fixedModels;

	const { data: listed } = useGetModelsQuery(
		{ provider: providerName, keys: providerKeyIds.length > 0 ? providerKeyIds : undefined, limit: 50 },
		{ skip: !selfHosted },
	);
	const listedModels = useMemo(() => (listed?.models ?? []).map((entry) => entry.name), [listed]);
	// A self-hosted provider that lists its models starts on the first one.
	useEffect(() => {
		if (selfHosted && !model && listedModels.length > 0) setValue("decision.model", listedModels[0], { shouldDirty: true });
	}, [selfHosted, model, listedModels, setValue]);

	return (
		<div className="grid gap-4 sm:grid-cols-2" data-testid="complexity-router-decision-fields">
			<div className="space-y-2">
				<FieldLabel
					htmlFor="decision-provider"
					tooltip="Typesafe or OpenRouter for Jev, OpenAI for its Decisions API models (gpt-6-luna), or a custom provider with base format Typesafe serving Laya, Nimble, or Clef."
				>
					Provider
				</FieldLabel>
				<Controller
					control={control}
					name="decision.provider"
					render={({ field }) => (
						<ProviderSelector
							inputId="decision-provider"
							data-testid="complexity-router-decision-provider-select"
							filter={isDecisionProvider}
							resolveIconKey={decisionProviderIconKey}
							value={field.value || ""}
							onChange={(value: string) => {
								if (value === field.value) return;
								field.onChange(value);
								// A model name only means something on its own provider.
								const next = providers.find((candidate) => candidate.name === value);
								setValue("decision.model", defaultDecisionModel(next), { shouldDirty: true });
							}}
							disabled={!canUpdate}
						/>
					)}
				/>
				{!providers.some(isDecisionProvider) && (
					<p className="text-muted-foreground text-xs" data-testid="complexity-router-decision-no-provider">
						No provider serves decision models yet.{" "}
						<Link to="/workspace/providers" className="text-primary underline-offset-2 hover:underline">
							Add one
						</Link>
						.
					</p>
				)}
				{errors?.provider && <p className="text-destructive text-xs">{errors.provider.message}</p>}
			</div>
			<div className="space-y-2">
				<FieldLabel htmlFor="decision-model" tooltip="The model the provider runs for each classification.">
					Model
				</FieldLabel>
				<Controller
					control={control}
					name="decision.model"
					render={({ field }) => {
						if (fixedModels) {
							// OpenRouter lists chat models under Typesafe's namespace too, and OpenAI
							// lists its chat models; only their decision models are offered.
							return (
								<Select value={field.value || undefined} onValueChange={field.onChange} disabled={!canUpdate}>
									<SelectTrigger className="w-full" id="decision-model" data-testid="complexity-router-decision-model-select">
										<SelectValue placeholder="Select a model" />
									</SelectTrigger>
									<SelectContent>
										{fixedModels.map((name) => (
											<SelectItem key={name} value={name}>
												{name}
											</SelectItem>
										))}
									</SelectContent>
								</Select>
							);
						}
						if (servesJev || !provider) {
							return (
								<ModelSelector
									inputId="decision-model"
									data-testid="complexity-router-decision-model-select"
									provider={providerName || undefined}
									keys={providerKeyIds}
									value={field.value ?? ""}
									onChange={(next) => field.onChange(next)}
									allowCustomModel
									placeholder={providerName ? "Search or type a Jev model…" : "Select a provider first"}
									disabled={!canUpdate || !providerName}
								/>
							);
						}
						if (servesClef) {
							// Cloudflare binds the model to the provider's URL, so it is shown, not
							// chosen: selecting the provider fills it in from that URL.
							return (
								<Input
									id="decision-model"
									data-testid="complexity-router-decision-model-input"
									value={field.value ?? ""}
									readOnly
									disabled
									className="font-mono"
								/>
							);
						}
						if (listedModels.length > 0) {
							// A listing is one page of a possibly longer catalog, so search and typed
							// names stay available instead of a fixed list that hides later models.
							return (
								<ModelSelector
									inputId="decision-model"
									data-testid="complexity-router-decision-model-select"
									provider={providerName || undefined}
									keys={providerKeyIds}
									value={field.value ?? ""}
									onChange={(next) => field.onChange(next)}
									allowCustomModel
									placeholder="Search or type a model…"
									disabled={!canUpdate}
								/>
							);
						}
						return (
							<Select value={field.value || undefined} onValueChange={field.onChange} disabled={!canUpdate}>
								<SelectTrigger className="w-full" id="decision-model" data-testid="complexity-router-decision-model-select">
									<SelectValue placeholder="Select a model" />
								</SelectTrigger>
								<SelectContent>
									{selfHostedModelGroups(providerName).map((group) => (
										<SelectGroup key={group.label}>
											<SelectLabel>{group.label}</SelectLabel>
											{group.models.map((name) => (
												<SelectItem key={name} value={name}>
													{name}
												</SelectItem>
											))}
										</SelectGroup>
									))}
								</SelectContent>
							</Select>
						);
					}}
				/>
				{servesClef && <p className="text-muted-foreground text-xs">Set by this provider&rsquo;s Cloudflare URL.</p>}
				{LAYA_MODELS.includes(model) && (
					<p className="text-muted-foreground text-xs">
						Laya reads 512 tokens per request ({model === "english" ? "use multilingual for 1,024" : "keep tier guidance short"}).
					</p>
				)}
				{errors?.model && <p className="text-destructive text-xs">{errors.model.message}</p>}
			</div>
			<div className="space-y-2">
				<FieldLabel
					htmlFor="decision-previous-message-count"
					tooltip={
						<>
							User messages sent to the decision model in addition to the current request, oldest to newest. Widening this lets a short
							follow-up like &ldquo;and make it faster&rdquo; inherit earlier intent, but sends more input tokens per request. Assistant
							replies are never sent. Defaults to 1.
						</>
					}
				>
					Max messages to send
				</FieldLabel>
				<Input
					id="decision-previous-message-count"
					data-testid="complexity-router-decision-previous-message-count-input"
					type="number"
					min={0}
					max={MAX_DECISION_PREVIOUS_MESSAGE_COUNT}
					step={1}
					{...register("decision.previous_message_count", { valueAsNumber: true })}
					disabled={!canUpdate}
					aria-invalid={errors?.previous_message_count ? true : undefined}
					className={cn("font-mono", errors?.previous_message_count && "border-destructive focus-visible:ring-destructive")}
				/>
				{errors?.previous_message_count && <p className="text-destructive text-xs">{errors.previous_message_count.message}</p>}
			</div>
			<div className="space-y-2">
				<FieldLabel
					htmlFor="decision-timeout"
					tooltip="Maximum wait for the decision model's API call. If the limit is exceeded, we skip and fall back to the original request model. Typesafe Jev typically responds in about 600 - 800 ms."
				>
					Classification timeout (ms)
				</FieldLabel>
				<Controller
					control={control}
					name="decision.timeout"
					render={({ field }) => (
						<Input
							id="decision-timeout"
							data-testid="complexity-router-decision-timeout-input"
							type="number"
							min={1}
							step={10}
							disabled={!canUpdate}
							value={decisionTimeoutFieldValue(field.value)}
							onChange={(event) => {
								const raw = event.target.value;
								field.onChange(raw === "" ? "" : `${raw}ms`);
							}}
							aria-invalid={errors?.timeout ? true : undefined}
							className={cn("font-mono", errors?.timeout && "border-destructive focus-visible:ring-destructive")}
						/>
					)}
				/>
				{errors?.timeout && <p className="text-destructive text-xs">{errors.timeout.message}</p>}
			</div>
		</div>
	);
}

// The recommendation matches the semantic phrase callout, so both classifiers
// open the same way. The line under it says where the cards' content goes.
const DECISION_GUIDANCE_NOTE = "We recommend starting with these defaults, then refining them to match what each tier means for you.";
const DECISION_GUIDANCE_USAGE = "Each tier's definition, signals and examples are sent to the decision model with every request.";

type DecisionListField = "signals" | "examples";

// Signals are traits to look for; examples are requests that have them. The
// tooltips say so because the two lists otherwise read as interchangeable.
const DECISION_LIST_FIELDS: Array<{
	field: DecisionListField;
	label: string;
	placeholder: string;
	tooltip: string;
}> = [
	{
		field: "signals",
		label: "Signals",
		placeholder: "Type a signal and press Enter",
		tooltip: "What makes a request belong in this tier: the knowledge, reasoning, or kind of work it needs.",
	},
	{
		field: "examples",
		label: "Examples",
		placeholder: "Type an example and press Enter",
		tooltip: "Sample requests that belong in this tier, showing what the signals look like in practice. Write them as concrete tasks.",
	},
];

// sameList compares lists by value and order, matching the gateway's check
// for whether an override equals the shipped default.
function sameList(a: string[] | undefined, b: string[] | undefined) {
	if (!a || !b || a.length !== b.length) return false;
	return a.every((value, index) => value === b[index]);
}

interface DecisionDefinitionFieldProps {
	tier: DecisionTier;
	control: Control<AnalyzerFormValues>;
	error: string | undefined;
	canUpdate: boolean;
	edited: boolean;
	onReset: () => void;
}

// DecisionDefinitionField shows a tier's definition as plain text, like the
// semantic cards' tier descriptions, and turns into a textarea only while it
// is being edited. An invalid definition stays open so its error is visible.
function DecisionDefinitionField({ tier, control, error, canUpdate, edited, onReset }: DecisionDefinitionFieldProps) {
	const [editing, setEditing] = useState(false);
	const open = editing || Boolean(error);
	const id = `decision-${tier.toLowerCase()}-definition`;
	return (
		<Controller
			control={control}
			name={`decision.criteria.${tier}.definition` as const}
			render={({ field }) => (
				<div className="flex flex-1 flex-col space-y-1.5">
					<div className="flex h-6 items-center justify-between">
						<label htmlFor={id} className="text-xs font-medium">
							{DECISION_TIER_LABELS[tier]}
						</label>
						<div className="flex items-center gap-1">
							{canUpdate && !open && (
								<Tooltip>
									<TooltipTrigger asChild>
										<Button
											type="button"
											variant="ghost"
											size="icon"
											className="size-6"
											aria-label={`Edit ${tier.toLowerCase()} definition`}
											onClick={() => setEditing(true)}
											data-testid={`complexity-router-decision-${tier.toLowerCase()}-definition-edit-button`}
										>
											<Pencil className="size-3" />
										</Button>
									</TooltipTrigger>
									<TooltipContent className="max-w-xs leading-relaxed">
										Describe what makes a request {DECISION_TIER_LABELS[tier]}. The decision model uses this to decide which requests belong
										in this tier.
									</TooltipContent>
								</Tooltip>
							)}
							{edited && (
								<Tooltip>
									<TooltipTrigger asChild>
										<Button
											type="button"
											variant="ghost"
											size="icon"
											className="size-6"
											aria-label={`Reset ${tier.toLowerCase()} definition to default`}
											onClick={onReset}
											disabled={!canUpdate}
											data-testid={`complexity-router-decision-${tier.toLowerCase()}-definition-reset-button`}
										>
											<RotateCcw className="size-3" />
										</Button>
									</TooltipTrigger>
									<TooltipContent className="max-w-xs leading-relaxed">Reset to the default definition</TooltipContent>
								</Tooltip>
							)}
						</div>
					</div>
					{open ? (
						<Textarea
							id={id}
							data-testid={`complexity-router-decision-${tier.toLowerCase()}-definition-input`}
							rows={4}
							maxLength={MAX_DECISION_DEFINITION_CHARACTERS}
							autoFocus={editing}
							{...field}
							onBlur={() => {
								field.onBlur();
								setEditing(false);
							}}
							disabled={!canUpdate}
							aria-invalid={error ? true : undefined}
							className={cn("resize-none text-xs leading-relaxed", error && "border-destructive focus-visible:ring-destructive")}
						/>
					) : (
						// Grows like the phrase grid's description, so a definition that
						// wraps to an extra line does not push this card's lists below its
						// neighbours'.
						<p
							className={cn("text-muted-foreground grow text-xs leading-relaxed", canUpdate && "cursor-text")}
							onClick={() => canUpdate && setEditing(true)}
							data-testid={`complexity-router-decision-${tier.toLowerCase()}-definition-text`}
						>
							{field.value}
						</p>
					)}
					{error && <p className="text-destructive text-xs">{error}</p>}
				</div>
			)}
		/>
	);
}

interface DecisionGuidanceSectionProps {
	control: Control<AnalyzerFormValues>;
	setValue: UseFormSetValue<AnalyzerFormValues>;
	errors: FieldErrors<AnalyzerFormValues>["decision"];
	canUpdate: boolean;
	// Undefined until the status endpoint answers, or when the gateway predates
	// decision_defaults; the editors stay hidden rather than showing empty lists.
	defaults: DecisionGuidanceDefaults | undefined;
	defaultsLoading: boolean;
	// Primary sits directly under the step header, which already titles it;
	// the fallback shares the page with other sections, so it keeps a heading.
	variant: "primary" | "fallback";
}

// DecisionGuidanceSection edits the half of Decision's request an operator may tune:
// each tier's definition, signals, and examples. The question, decision and
// context rules, and answer contract stay fixed server-side. It mirrors the phrase grid
// and the llm prompt editor so the page keeps one visual language.
export function DecisionGuidanceSection({
	control,
	setValue,
	errors,
	canUpdate,
	defaults,
	defaultsLoading,
	variant,
}: DecisionGuidanceSectionProps) {
	const criteria = useWatch({ control, name: "decision.criteria" });
	const [fallbackOpen, setFallbackOpen] = useState(false);

	const isDefaultDefinition = (tier: DecisionTier) => criteria?.[tier]?.definition === defaults?.criteria[tier].definition;
	const isDefaultList = (tier: DecisionTier, field: DecisionListField) =>
		!!defaults && sameList(criteria?.[tier]?.[field], defaults.criteria[tier][field]);
	const isAllDefault = COMPLEXITY_TIER_VALUES.every(
		(tier) => isDefaultDefinition(tier) && DECISION_LIST_FIELDS.every(({ field }) => isDefaultList(tier, field)),
	);

	const resetList = (tier: DecisionTier, field: DecisionListField) => {
		if (!defaults) return;
		setValue(`decision.criteria.${tier}.${field}`, [...defaults.criteria[tier][field]], { shouldDirty: true, shouldValidate: true });
	};
	const resetDefinition = (tier: DecisionTier) => {
		if (!defaults) return;
		setValue(`decision.criteria.${tier}.definition`, defaults.criteria[tier].definition, { shouldDirty: true, shouldValidate: true });
	};
	const resetAll = () => {
		for (const tier of COMPLEXITY_TIER_VALUES) {
			resetDefinition(tier);
			for (const { field } of DECISION_LIST_FIELDS) resetList(tier, field);
		}
	};

	// Empty editors would read as "the decision model receives nothing" and invite saving that. Until
	// the shipped guidance is known, say so instead; saving meanwhile keeps
	// whatever is stored, because unseeded guidance is omitted from the payload.
	// Shared by both placements: the recommendation, what the decision model receives, and the
	// tier cards (or why they cannot be shown yet). The tier cards sit where the semantic phrase grid
	// does, so both classifiers read the same way; each holds its tier's only
	// definition.
	const body = (
		<>
			<Alert variant="info" data-testid="complexity-router-decision-guidance-defaults-callout">
				<Info className="h-4 w-4" />
				<AlertDescription>{DECISION_GUIDANCE_NOTE}</AlertDescription>
			</Alert>
			<p className="text-muted-foreground text-xs leading-relaxed">{DECISION_GUIDANCE_USAGE}</p>
			{defaults ? (
				<div className="grid items-stretch gap-3 md:grid-cols-3">
					{COMPLEXITY_TIER_VALUES.map((tier) => (
						<div
							key={tier}
							className="bg-card flex flex-col rounded-sm border"
							data-testid={`complexity-router-decision-tier-${tier.toLowerCase()}`}
						>
							<div className="flex flex-1 flex-col space-y-3 p-4 pl-5">
								<DecisionDefinitionField
									tier={tier}
									control={control}
									error={errors?.criteria?.[tier]?.definition?.message}
									canUpdate={canUpdate}
									edited={!isDefaultDefinition(tier)}
									onReset={() => resetDefinition(tier)}
								/>
								{DECISION_LIST_FIELDS.map(({ field: listField, label, placeholder, tooltip }) => {
									const fieldError = errors?.criteria?.[tier]?.[listField];
									const errorId = `decision-${tier.toLowerCase()}-${listField}-error`;
									const count = criteria?.[tier]?.[listField]?.length ?? 0;
									const atLimit = count >= MAX_DECISION_CRITERIA_ITEMS;
									const edited = !isDefaultList(tier, listField);
									return (
										<Controller
											key={listField}
											control={control}
											name={`decision.criteria.${tier}.${listField}` as const}
											render={({ field }) => (
												<div className="space-y-1.5">
													{/* Fixed height so a row with a reset icon lines up with one without. */}
													<div className="flex h-6 items-center justify-between">
														<div className="flex items-center gap-1.5">
															<span className="text-muted-foreground text-xs">{label}</span>
															<InfoTip label={`About ${label.toLowerCase()}`}>{tooltip}</InfoTip>
														</div>
														<div className="flex items-center gap-1">
															<span
																className={cn("font-mono text-[11px] tabular-nums", atLimit ? "text-amber-600" : "text-muted-foreground")}
															>
																{count} / {MAX_DECISION_CRITERIA_ITEMS}
															</span>
															{/* Doubles as the edited marker: present only where this list differs from the default. */}
															{edited && (
																<Button
																	type="button"
																	variant="ghost"
																	size="icon"
																	className="size-6"
																	aria-label={`Reset ${tier.toLowerCase()} ${listField} to default`}
																	onClick={() => resetList(tier, listField)}
																	disabled={!canUpdate}
																	data-testid={`complexity-router-decision-${tier.toLowerCase()}-${listField}-reset-button`}
																>
																	<RotateCcw className="size-3" />
																</Button>
															)}
														</div>
													</div>
													<TagInput
														data-testid={`complexity-router-decision-${tier.toLowerCase()}-${listField}-input`}
														value={field.value}
														onValueChange={field.onChange}
														listHeight={DECISION_LIST_HEIGHT}
														submitOnComma={false}
														placeholder={atLimit ? `Limit of ${MAX_DECISION_CRITERIA_ITEMS} reached` : placeholder}
														readOnly={!canUpdate}
														disabled={!canUpdate || atLimit}
														aria-label={`${DECISION_TIER_LABELS[tier]} ${label.toLowerCase()}`}
														aria-invalid={fieldError ? true : undefined}
														aria-describedby={fieldError ? errorId : undefined}
														className={cn(fieldError && "border-destructive")}
													/>
													{fieldError && (
														<p id={errorId} className="text-destructive text-xs">
															{fieldError.message ?? fieldError.root?.message}
														</p>
													)}
												</div>
											)}
										/>
									);
								})}
							</div>
						</div>
					))}
				</div>
			) : (
				<div
					className="bg-card text-muted-foreground flex items-center gap-2 rounded-sm border p-4 text-xs"
					data-testid="complexity-router-decision-guidance-unavailable"
				>
					{defaultsLoading ? (
						<>
							<LoaderCircle className="size-3.5 animate-spin" />
							Loading the default guidance…
						</>
					) : (
						<>
							<TriangleAlert className="size-3.5" />
							This gateway did not return its default guidance, so it cannot be edited here. The decision model keeps using the saved or
							shipped guidance.
						</>
					)}
				</div>
			)}
		</>
	);

	// The primary guidance is the step's main content and restores from the
	// page footer, as the semantic phrases do.
	if (variant === "primary") {
		return (
			<div className="space-y-3" data-testid="complexity-router-decision-guidance-primary">
				{body}
			</div>
		);
	}

	// The fallback shares the page with the semantic phrase grid, so it folds
	// away until opened; an invalid field forces it open so its error is never
	// hidden. The footer's Restore defaults resets the phrases, so the fallback
	// keeps its own Reset all.
	const open = fallbackOpen || Boolean(errors?.criteria);
	return (
		<Collapsible
			open={open}
			onOpenChange={setFallbackOpen}
			className="bg-card rounded-sm border"
			data-testid="complexity-router-decision-guidance-fallback"
		>
			<div className="flex items-center justify-between gap-2 px-4 py-3">
				<CollapsibleTrigger asChild>
					<button
						type="button"
						className="flex min-w-0 items-start gap-2 text-left"
						data-testid="complexity-router-decision-guidance-fallback-toggle"
					>
						<ChevronRight className={cn("text-muted-foreground mt-0.5 size-4 shrink-0 transition-transform", open && "rotate-90")} />
						<span className="space-y-1">
							<span className="block text-sm font-semibold">Fallback Tier Guidance</span>
							<span className="text-muted-foreground block text-xs">Used by the decision model when no reference phrase matches.</span>
						</span>
					</button>
				</CollapsibleTrigger>
				{defaults && !isAllDefault && (
					<Button
						type="button"
						variant="ghost"
						size="sm"
						onClick={resetAll}
						disabled={!canUpdate}
						data-testid="complexity-router-decision-guidance-reset-button"
					>
						<RotateCcw className="h-3.5 w-3.5" />
						Reset all
					</Button>
				)}
			</div>
			<CollapsibleContent className="space-y-3 border-t p-4">{body}</CollapsibleContent>
		</Collapsible>
	);
}

const DECISION_PROVIDER_PROBLEMS: Record<
	Exclude<DecisionProviderState, "configured">,
	(provider: string) => { message: string; action: string }
> = {
	missing: (provider) => ({
		message: `The decision model runs through the ${provider} provider, and it is not set up yet.`,
		action: `Set up ${provider}`,
	}),
	failing: (provider) => ({
		message: `The ${provider} provider is failing its checks, so decision-model calls will fail. Check its key and settings.`,
		action: `Review ${provider} provider`,
	}),
	"no-enabled-key": (provider) => ({
		message: `The ${provider} provider has no enabled key, so decision-model calls will fail.`,
		action: `Review ${provider} keys`,
	}),
};

// DecisionProviderAlert explains why the decision model cannot run, and links
// straight to its provider's page. That page opens a blank setup form when the
// provider does not exist yet, so one click lands on the fix either way.
export function DecisionProviderAlert({ state, provider }: { state: DecisionProviderState; provider: string }) {
	if (state === "configured" || !provider) return null;
	const problem = DECISION_PROVIDER_PROBLEMS[state](provider);
	return (
		<Alert variant="warning" data-testid="complexity-router-decision-provider-alert">
			<TriangleAlert className="h-4 w-4" />
			<AlertDescription className="gap-2">
				<span>{problem.message}</span>
				<Button asChild variant="outline" size="sm" data-testid="complexity-router-decision-provider-link">
					<Link to="/workspace/providers" search={{ provider }}>
						{problem.action}
						<ArrowRight className="size-3.5" />
					</Link>
				</Button>
			</AlertDescription>
		</Alert>
	);
}

type Classifier = AnalyzerFormValues["classifier"];

const CLASSIFIER_OPTIONS: {
	value: Classifier;
	title: string;
	description: string;
}[] = [
	{
		value: "decision",
		title: "Decision model",
		description:
			"A decision model judges the complexity of each request and picks the tier whose definition, signals, and examples fit it best. Use Typesafe Jev or OpenAI Decisions, or run Laya, Nimble, or Clef. No phrases to write or maintain.",
	},
	{
		value: "semantic",
		title: "Semantic",
		description:
			"Matches each request against reference phrases you write for each tier, through an embedding model you choose. You decide exactly what each tier means.",
	},
];

// ClassifierChoice is the one decision everything else depends on, so each
// option is an explained card rather than an entry in a dropdown. Anything a
// choice still needs is reported once, below the cards, after it is picked.
export function ClassifierChoice({ value, onChange }: { value: Classifier | undefined; onChange: (value: Classifier) => void }) {
	return (
		<div role="radiogroup" aria-label="Classifier" className="grid gap-3 sm:grid-cols-2">
			{CLASSIFIER_OPTIONS.map((option) => {
				const selected = option.value === value;
				return (
					<button
						key={option.value}
						type="button"
						role="radio"
						aria-checked={selected}
						onClick={() => onChange(option.value)}
						data-testid={`complexity-router-classifier-option-${option.value}`}
						className={cn(
							"hover:bg-accent/50 flex h-full flex-col gap-2 rounded-md border p-4 text-left transition-colors",
							selected && "border-primary bg-primary/5",
						)}
					>
						<div className="flex w-full items-center gap-2">
							{option.value === "decision" ? (
								<Scale className="text-muted-foreground size-4" />
							) : (
								<Waypoints className="text-muted-foreground size-4" />
							)}
							<span className="text-sm font-medium">{option.title}</span>
							{selected && <Check className="ml-auto size-4 text-green-600" />}
						</div>
						<p className="text-muted-foreground text-xs leading-relaxed">{option.description}</p>
					</button>
				);
			})}
		</div>
	);
}

interface StepRailProps {
	steps: { key: string; title: string; enabled: boolean }[];
	currentIndex: number;
	onSelect: (index: number) => void;
}

// StepRail is the page's step nav. It takes the row style of the Edge inventory
// nav, and is narrower still: two short labels need little width, and every
// pixel it gives back goes to the phrase lists.
export function StepRail({ steps, currentIndex, onSelect }: StepRailProps) {
	return (
		<nav className="flex w-full shrink-0 gap-1 overflow-x-auto md:block md:w-44" aria-label="Complexity Router steps">
			{steps.map((step, index) => {
				const current = index === currentIndex;
				return (
					<button
						key={step.key}
						type="button"
						onClick={() => !current && onSelect(index)}
						disabled={!step.enabled}
						aria-current={current ? "step" : undefined}
						data-testid={`complexity-router-step-${step.key}`}
						className={cn(
							"mb-1 flex h-8 w-auto shrink-0 items-center gap-2 rounded-sm border px-3 text-sm md:w-full md:min-w-0",
							current
								? "bg-secondary font-medium"
								: "text-muted-foreground hover:bg-secondary border-transparent hover:border enabled:cursor-pointer",
							!step.enabled && "cursor-not-allowed opacity-50 hover:border-transparent hover:bg-transparent",
						)}
					>
						<span
							className={cn(
								"flex size-5 shrink-0 items-center justify-center rounded-full text-[11px] font-medium",
								current ? "bg-primary text-primary-foreground" : "bg-muted text-muted-foreground",
							)}
						>
							{index + 1}
						</span>
						{step.title}
					</button>
				);
			})}
		</nav>
	);
}