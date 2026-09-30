import { Alert, AlertDescription } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Switch } from "@/components/ui/switch";
import { TagInput } from "@/components/ui/tagInput";
import { Textarea } from "@/components/ui/textarea";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { RenderProviderIcon } from "@/lib/constants/icons";
import {
	KeywordListKey,
	MAX_JEV_PREVIOUS_MESSAGE_COUNT,
	MAX_LLM_PROMPT_CHARACTERS,
	TIER_PHRASE_LIST_DEFINITIONS,
} from "@/lib/types/complexityRouter";
import { cn } from "@/lib/utils";
import { Link } from "@tanstack/react-router";
import { ArrowRight, Check, Info, RotateCcw, TriangleAlert, Waypoints } from "lucide-react";
import { Controller, type Control, type FieldError, type FieldErrors, type UseFormRegister } from "react-hook-form";
import { jevTimeoutFieldValue, type AnalyzerFormValues, type TypesafeState } from "../formSchema";
import { FieldLabel, SectionHeading } from "./formPrimitives";

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

interface JevFieldsProps {
	control: Control<AnalyzerFormValues>;
	register: UseFormRegister<AnalyzerFormValues>;
	errors: FieldErrors<AnalyzerFormValues>["jev"];
	canUpdate: boolean;
}

// JevFields holds Jev's two settings. They apply wherever Jev runs, as the
// primary classifier or as the semantic fallback, so both places render this.
export function JevFields({ control, register, errors, canUpdate }: JevFieldsProps) {
	return (
		<div className="grid gap-4 sm:grid-cols-2" data-testid="complexity-router-jev-fields">
			<div className="space-y-2">
				<FieldLabel
					htmlFor="jev-previous-message-count"
					tooltip="The number of previous user messages to send Jev as context. 1 is the default"
				>
					Previous user messages
				</FieldLabel>
				<Input
					id="jev-previous-message-count"
					data-testid="complexity-router-jev-previous-message-count-input"
					type="number"
					min={0}
					max={MAX_JEV_PREVIOUS_MESSAGE_COUNT}
					step={1}
					{...register("jev.previous_message_count", { valueAsNumber: true })}
					disabled={!canUpdate}
					aria-invalid={errors?.previous_message_count ? true : undefined}
					className={cn("font-mono", errors?.previous_message_count && "border-destructive focus-visible:ring-destructive")}
				/>
				{errors?.previous_message_count && <p className="text-destructive text-xs">{errors.previous_message_count.message}</p>}
			</div>
			<div className="space-y-2">
				<FieldLabel
					htmlFor="jev-timeout"
					tooltip="Maximum wait for Jev's api call. If the limit is exceeded, we skip and fallback to original request model. Jev typically responds in about 600 - 800 ms."
				>
					Classification timeout (ms)
				</FieldLabel>
				<Controller
					control={control}
					name="jev.timeout"
					render={({ field }) => (
						<Input
							id="jev-timeout"
							data-testid="complexity-router-jev-timeout-input"
							type="number"
							min={1}
							step={10}
							disabled={!canUpdate}
							value={jevTimeoutFieldValue(field.value)}
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

const TYPESAFE_PROBLEMS: Record<Exclude<TypesafeState, "configured">, { message: string; action: string }> = {
	missing: { message: "Jev runs through your Typesafe provider, and none is set up yet.", action: "Set up Typesafe" },
	failing: {
		message: "Your Typesafe provider is failing its checks, so Jev calls will fail. Check its key and settings.",
		action: "Review Typesafe provider",
	},
	"no-enabled-key": { message: "Your Typesafe provider has no enabled key, so Jev calls will fail.", action: "Review Typesafe keys" },
};

// TypesafeAlert explains why Jev cannot run, and links straight to the
// Typesafe provider page. That page opens a blank Typesafe setup form when the
// provider does not exist yet, so one click lands on the fix either way.
export function TypesafeAlert({ state }: { state: TypesafeState }) {
	if (state === "configured") return null;
	const problem = TYPESAFE_PROBLEMS[state];
	return (
		<Alert variant="warning" data-testid="complexity-router-typesafe-alert">
			<TriangleAlert className="h-4 w-4" />
			<AlertDescription className="gap-2">
				<span>{problem.message}</span>
				<Button asChild variant="outline" size="sm" data-testid="complexity-router-typesafe-provider-link">
					<Link to="/workspace/providers" search={{ provider: "typesafe" }}>
						{problem.action}
						<ArrowRight className="size-3.5" />
					</Link>
				</Button>
			</AlertDescription>
		</Alert>
	);
}

type Classifier = AnalyzerFormValues["classifier"];

const CLASSIFIER_OPTIONS: { value: Classifier; title: string; description: string }[] = [
	{
		value: "jev",
		title: "Jev by Typesafe",
		description:
			"A decision model that judges how much reasoning each prompt needs and picks the cheapest complexity tier that can answer it correctly. No phrases to write or maintain.",
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
							{option.value === "jev" ? (
								<RenderProviderIcon provider="typesafe" size="sm" />
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