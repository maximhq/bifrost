import FullPageLoader from "@/components/fullPageLoader";
import {
	AlertDialog,
	AlertDialogAction,
	AlertDialogCancel,
	AlertDialogContent,
	AlertDialogDescription,
	AlertDialogFooter,
	AlertDialogHeader,
	AlertDialogTitle,
} from "@/components/ui/alertDialog";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { ScrollArea } from "@/components/ui/scrollArea";
import { TagInput } from "@/components/ui/tagInput";
import { getErrorMessage } from "@/lib/store";
import {
	useGetComplexityAnalyzerConfigQuery,
	useResetComplexityAnalyzerConfigMutation,
	useUpdateComplexityAnalyzerConfigMutation,
} from "@/lib/store/apis/governanceApi";
import {
	AnalyzerConfig,
	DEFAULT_TIER_BOUNDARIES,
	KEYWORD_LIST_DEFINITIONS,
	KeywordListKey,
	TierBoundaries,
} from "@/lib/types/complexityRouter";
import { cn } from "@/lib/utils";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { zodResolver } from "@hookform/resolvers/zod";
import { ExternalLink, LoaderCircle, RotateCcw, Save } from "lucide-react";
import { type ChangeEvent, type ClipboardEvent, type DragEvent, type KeyboardEvent, useEffect, useState } from "react";
import { Controller, useForm } from "react-hook-form";
import { toast } from "sonner";
import { z } from "zod";

type TierBoundaryKey = keyof TierBoundaries;

const KEYWORD_COLLAPSED_LIMIT = 8;

// Three progressive shades of --primary: faintest → full
const P1 = "color-mix(in oklch, var(--primary) 30%, transparent)";
const P2 = "color-mix(in oklch, var(--primary) 55%, transparent)";
const P4 = "var(--primary)";

const TIER_PALETTE = {
	simple: { color: P1, name: "SIMPLE" },
	medium: { color: P2, name: "MEDIUM" },
	complex: { color: P4, name: "COMPLEX" },
} as const;

interface BoundaryFieldConfig {
	key: TierBoundaryKey;
	label: string;
	description: string;
	fromTier: string;
	toTier: string;
	fromColor: string;
	toColor: string;
}

const BOUNDARY_FIELDS: BoundaryFieldConfig[] = [
	{
		key: "simple_medium",
		label: "Simple → Medium",
		description: "Scores at or below this are classified as SIMPLE.",
		fromTier: "SIMPLE",
		toTier: "MEDIUM",
		fromColor: P1,
		toColor: P2,
	},
	{
		key: "medium_complex",
		label: "Medium → Complex",
		description: "Scores above simple_medium and at or below this are MEDIUM. Everything above is COMPLEX.",
		fromTier: "MEDIUM",
		toTier: "COMPLEX",
		fromColor: P2,
		toColor: P4,
	},
];

const boundaryField = z.number({ error: "Enter a number between 0 and 1" }).gt(0, "Must be greater than 0").lt(1, "Must be less than 1");

const analyzerConfigSchema = z.object({
	tier_boundaries: z
		.object({
			simple_medium: boundaryField,
			medium_complex: boundaryField,
		})
		.superRefine((data, ctx) => {
			if (Number.isFinite(data.medium_complex) && Number.isFinite(data.simple_medium) && data.medium_complex <= data.simple_medium) {
				ctx.addIssue({ code: "custom", message: "Must be greater than Simple → Medium", path: ["medium_complex"] });
			}
		}),
	keywords: z.object({
		simple_keywords: z.array(z.string()).min(1, "Simple keywords cannot be empty"),
		code_keywords: z.array(z.string()).min(1, "Code keywords cannot be empty"),
		technical_keywords: z.array(z.string()).min(1, "Technical keywords cannot be empty"),
		reasoning_keywords: z.array(z.string()).min(1, "Reasoning keywords cannot be empty"),
	}),
});

const DEFAULT_FORM_VALUES: AnalyzerConfig = {
	tier_boundaries: { ...DEFAULT_TIER_BOUNDARIES },
	keywords: {
		code_keywords: [],
		reasoning_keywords: [],
		technical_keywords: [],
		simple_keywords: [],
	},
};

function boundaryValueAsNumber(value: unknown): number {
	let numericValue = Number.NaN;
	if (typeof value === "number") {
		numericValue = value;
	} else if (typeof value === "string" && value.trim() !== "") {
		numericValue = Number(value);
	}
	return Number.isFinite(numericValue) ? Math.max(0, numericValue) : Number.NaN;
}

function finiteBoundaryValue(value: number | undefined, fallback: number) {
	return typeof value === "number" && Number.isFinite(value) ? value : fallback;
}

function clampUnit(value: number) {
	return Math.min(1, Math.max(0, value));
}

function testIdPart(value: string) {
	return value.replace(/_/g, "-");
}

function preventNegativeBoundaryKey(event: KeyboardEvent<HTMLInputElement>) {
	if (event.key === "-") {
		event.preventDefault();
	}
}

function preventNegativeBoundaryPaste(event: ClipboardEvent<HTMLInputElement>) {
	if (/^\s*-/.test(event.clipboardData.getData("text"))) {
		event.preventDefault();
	}
}

function preventNegativeBoundaryDrop(event: DragEvent<HTMLInputElement>) {
	if (/^\s*-/.test(event.dataTransfer.getData("text"))) {
		event.preventDefault();
	}
}

function normalizeBoundaryInput(event: ChangeEvent<HTMLInputElement>) {
	const { value } = event.currentTarget;
	if (!/^\s*-/.test(value)) return;

	const numericValue = Number(value);
	event.currentTarget.value = Number.isFinite(numericValue) ? "0" : "";
}

function TierSpectrumBar({ boundaries }: { boundaries: TierBoundaries }) {
	const sm = clampUnit(finiteBoundaryValue(boundaries?.simple_medium, DEFAULT_TIER_BOUNDARIES.simple_medium));
	const mc = clampUnit(finiteBoundaryValue(boundaries?.medium_complex, DEFAULT_TIER_BOUNDARIES.medium_complex));

	const segments = [
		{ tier: "SIMPLE", width: Math.max(0, sm * 100), color: TIER_PALETTE.simple.color },
		{ tier: "MEDIUM", width: Math.max(0, (mc - sm) * 100), color: TIER_PALETTE.medium.color },
		{ tier: "COMPLEX", width: Math.max(0, (1 - mc) * 100), color: TIER_PALETTE.complex.color },
	];

	const markers = [
		{ key: "simple-medium", pos: sm, value: sm.toFixed(2) },
		{ key: "medium-complex", pos: mc, value: mc.toFixed(2) },
	];

	return (
		<div className="space-y-1.5">
			<div className="relative flex h-9 w-full gap-[1.5px] overflow-hidden rounded-sm">
				{segments.map(({ tier, width, color }) => (
					<div
						key={tier}
						style={{ width: `${width}%`, backgroundColor: color }}
						className="relative flex items-center justify-center overflow-hidden transition-[width] duration-300 ease-in-out"
					>
						{width > 7 && (
							<span className="pointer-events-none absolute font-mono text-[8px] font-bold tracking-[0.12em] text-white select-none">
								{tier}
							</span>
						)}
					</div>
				))}
				{/* Boundary dividers */}
				{markers.map(({ key, pos }) => (
					<div
						key={key}
						className="bg-background/70 absolute inset-y-0 w-px transition-[left] duration-300 ease-in-out"
						style={{ left: `${pos * 100}%` }}
					/>
				))}
			</div>
			{/* Axis labels */}
			<div className="relative h-3.5 w-full">
				<span className="text-muted-foreground/50 absolute left-0 font-mono text-[9px]">0</span>
				{markers.map(({ key, pos, value }) => (
					<span
						key={key}
						className="text-muted-foreground absolute -translate-x-1/2 font-mono text-[9px] transition-[left] duration-300 ease-in-out"
						style={{ left: `${pos * 100}%` }}
					>
						{value}
					</span>
				))}
				<span className="text-muted-foreground/50 absolute right-0 font-mono text-[9px]">1</span>
			</div>
		</div>
	);
}

export default function ComplexityRouterPage() {
	const canUpdate = useRbac(RbacResource.RoutingRules, RbacOperation.Update);
	const { data, isLoading, isFetching, error, refetch } = useGetComplexityAnalyzerConfigQuery();
	const [updateConfig, { isLoading: isSaving }] = useUpdateComplexityAnalyzerConfigMutation();
	const [resetConfig, { isLoading: isResetting }] = useResetComplexityAnalyzerConfigMutation();

	const [submitError, setSubmitError] = useState<string | null>(null);
	const [restoreDialogOpen, setRestoreDialogOpen] = useState(false);

	const {
		register,
		handleSubmit,
		reset,
		control,
		watch,
		formState: { errors, isDirty, isSubmitted },
	} = useForm<AnalyzerConfig>({
		resolver: zodResolver(analyzerConfigSchema),
		defaultValues: DEFAULT_FORM_VALUES,
		mode: "onSubmit",
		reValidateMode: "onChange",
	});

	const liveBoundaries = watch("tier_boundaries");

	useEffect(() => {
		if (!data || isDirty) return;
		reset(data);
		setSubmitError(null);
	}, [data, isDirty, reset]);

	const handleDiscard = () => {
		if (data) reset(data);
		setSubmitError(null);
	};

	const handleRestoreDefaults = () => {
		if (!canUpdate) return;
		setSubmitError(null);
		resetConfig()
			.unwrap()
			.then((defaults) => {
				reset(defaults);
				toast.success("Reset to defaults", { position: "top-right" });
			})
			.catch((err) => {
				setSubmitError(getErrorMessage(err));
			});
	};

	const onValid = (values: AnalyzerConfig) => {
		if (!canUpdate) return;
		setSubmitError(null);
		updateConfig(values)
			.unwrap()
			.then((res) => {
				reset(res);
				toast.success("Configuration saved", { position: "top-right" });
			})
			.catch((err) => {
				setSubmitError(getErrorMessage(err));
			});
	};

	if (isLoading && !data) {
		return <FullPageLoader />;
	}

	if (error && !data) {
		return (
			<div className="mx-auto w-full max-w-7xl space-y-4 px-14 pt-8">
				<p className="text-destructive font-mono text-sm">{getErrorMessage(error)}</p>
				<Button data-testid="complexity-router-fetch-retry-button" type="button" variant="outline" size="sm" onClick={() => refetch()}>
					Retry
				</Button>
			</div>
		);
	}

	if (!data) {
		return (
			<div className="mx-auto w-full max-w-7xl space-y-4 px-14 pt-8">
				<p className="text-muted-foreground font-mono text-sm">No complexity router configuration is available.</p>
				<Button data-testid="complexity-router-fetch-retry-button" type="button" variant="outline" size="sm" onClick={() => refetch()}>
					Retry
				</Button>
			</div>
		);
	}

	const boundaryErrors = errors.tier_boundaries;
	const keywordErrors = errors.keywords;
	const hasErrors = Boolean(boundaryErrors || keywordErrors);

	return (
		<ScrollArea className="no-padding-parent h-[calc(100vh_-_16px)] w-full px-14 pt-4">
			<form className="mx-auto w-full max-w-7xl space-y-8" onSubmit={handleSubmit(onValid)} noValidate>
				{/* ── Page header ── */}
				<div className="flex flex-col gap-3 sm:flex-row sm:items-start sm:justify-between">
					<div className="space-y-1.5">
						<h1 className="text-2xl font-semibold tracking-tight">Complexity Router</h1>
						<p className="text-muted-foreground max-w-2xl text-sm leading-relaxed">
							Tune how incoming requests are classified into four tiers. Thresholds and keyword lists feed the{" "}
							<code className="bg-muted rounded-sm px-1 py-0.5 font-mono text-xs">complexity_tier</code> field that routing rules can
							target.
						</p>
					</div>
					<Button asChild variant="outline" size="sm" className="w-fit shrink-0" data-testid="complexity-router-docs-link">
						<a href={"https://docs.getbifrost.ai/features/governance/complexity-router"} target="_blank" rel="noopener noreferrer">
							<ExternalLink className="size-3.5" />
							Docs
						</a>
					</Button>
				</div>

				{/* ── Complexity Spectrum ── */}
				<div className="bg-card space-y-4 rounded-sm border p-5">
					<div className="flex items-center justify-between">
						<p className="text-muted-foreground font-mono text-xs font-semibold tracking-widest uppercase">Complexity Spectrum</p>
						<div className="flex items-center gap-4">
							{Object.values(TIER_PALETTE).map(({ color, name }) => (
								<div key={name} className="flex items-center gap-1.5">
									<div className="h-1.5 w-1.5 rounded-full" style={{ backgroundColor: color }} />
									<span className="text-muted-foreground font-mono text-[9px] font-bold tracking-widest">{name}</span>
								</div>
							))}
						</div>
					</div>
					<TierSpectrumBar boundaries={liveBoundaries} />
				</div>

				{/* ── Tier Boundaries ── */}
				<div className="space-y-3">
					<h2 className="text-sm font-semibold">Tier Boundaries</h2>

					<div className="grid gap-3 md:grid-cols-2">
						{BOUNDARY_FIELDS.map(({ key, label, description, fromTier, toTier, fromColor, toColor }) => {
							const fieldError = boundaryErrors?.[key];
							const inputId = `boundary-${key}`;
							const errorId = `${inputId}-error`;
							const { onChange, ...boundaryInputProps } = register(`tier_boundaries.${key}`, {
								required: "Enter a number between 0 and 1",
								setValueAs: boundaryValueAsNumber,
								validate: (value) => {
									if (!Number.isFinite(value)) return "Enter a number between 0 and 1";
									if (value <= 0) return "Must be greater than 0";
									if (value >= 1) return "Must be less than 1";
									const { simple_medium } = liveBoundaries;
									if (key === "medium_complex" && Number.isFinite(simple_medium) && value <= simple_medium) {
										return "Must be greater than Simple → Medium";
									}
									return true;
								},
								deps: key === "simple_medium" ? ["tier_boundaries.medium_complex"] : undefined,
							});

							return (
								<div key={key} className="bg-card relative space-y-3 overflow-hidden rounded-sm border p-4">
									{/* Tier transition label */}
									<div className="flex items-center gap-1.5 pt-0.5">
										<span className="font-mono text-[10px] font-bold tracking-widest" style={{ color: fromColor }}>
											{fromTier}
										</span>
										<span className="text-muted-foreground/40 text-[10px]">→</span>
										<span className="font-mono text-[10px] font-bold tracking-widest" style={{ color: toColor }}>
											{toTier}
										</span>
									</div>

									<label htmlFor={inputId} className="sr-only">
										{label}
									</label>
									<Input
										data-testid={`complexity-router-boundary-${testIdPart(key)}-input`}
										id={inputId}
										type="number"
										inputMode="decimal"
										min={0}
										max={1}
										step={0.01}
										onKeyDown={preventNegativeBoundaryKey}
										onPaste={preventNegativeBoundaryPaste}
										onDrop={preventNegativeBoundaryDrop}
										onChange={(event) => {
											normalizeBoundaryInput(event);
											onChange(event);
										}}
										aria-invalid={fieldError ? true : undefined}
										aria-describedby={fieldError ? errorId : undefined}
										className={cn(
											"h-11 text-center text-lg font-mono font-medium",
											fieldError && "border-destructive focus-visible:ring-destructive",
										)}
										{...boundaryInputProps}
									/>

									{fieldError ? (
										<p id={errorId} className="text-destructive text-xs">
											{fieldError.message}
										</p>
									) : (
										<p className="text-muted-foreground text-xs leading-relaxed">{description}</p>
									)}
								</div>
							);
						})}
					</div>
				</div>

				{/* ── Keyword Lists ── */}
				<div className="space-y-3">
					<div className="flex items-baseline gap-2.5">
						<h2 className="text-sm font-semibold">Keyword Lists</h2>
						<span className="text-muted-foreground text-xs">
							Lowercased and deduplicated on save. Each list requires at least one entry.
						</span>
					</div>

					<div className="grid gap-3 md:grid-cols-2">
						{KEYWORD_LIST_DEFINITIONS.map(({ key, label, description }) => {
							const fieldError = keywordErrors?.[key as KeywordListKey];
							const errorId = `keywords-${key}-error`;
							return (
								<div key={key} className="bg-card relative overflow-hidden rounded-sm border">
									<Controller
										control={control}
										name={`keywords.${key}` as const}
										rules={{ validate: (value) => (value.length > 0 ? true : `${label} cannot be empty`) }}
										render={({ field }) => (
											<div className="space-y-2 p-4 pl-5">
												<div className="flex items-center justify-between">
													<span className="text-xs font-medium">{label}</span>
													<span className="text-muted-foreground font-mono text-[11px] tabular-nums">
														{field.value.length} {field.value.length === 1 ? "entry" : "entries"}
													</span>
												</div>
												<p className="text-muted-foreground text-xs leading-relaxed">{description}</p>
												<TagInput
													data-testid={`complexity-router-keywords-${testIdPart(key)}-input`}
													value={field.value}
													onValueChange={field.onChange}
													collapsedTagLimit={KEYWORD_COLLAPSED_LIMIT}
													expandButtonTestId={`complexity-router-keywords-${testIdPart(key)}-expand-button`}
													placeholder="Type a keyword and press Enter"
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

				{/* ── Submit error ── */}
				{submitError && (
					<div
						role="alert"
						className="border-destructive/40 bg-destructive/10 text-destructive rounded-sm border px-3 py-2 font-mono text-sm"
					>
						{submitError}
					</div>
				)}

				{/* ── Action footer ── */}
				<div className="bg-card sticky bottom-0 flex flex-wrap items-center justify-end gap-2.5 border-t py-4 z-10">
					<Button
						data-testid="complexity-router-restore-defaults-button"
						type="button"
						variant="ghost"
						size="sm"
						onClick={() => setRestoreDialogOpen(true)}
						disabled={!canUpdate || isSaving || isResetting}
					>
						{isResetting ? <LoaderCircle className="h-3.5 w-3.5 animate-spin" /> : <RotateCcw className="h-3.5 w-3.5" />}
						Restore defaults
					</Button>
					<Button
						data-testid="complexity-router-discard-changes-button"
						type="button"
						variant="outline"
						size="sm"
						onClick={handleDiscard}
						disabled={!isDirty || isSaving || isResetting || isFetching}
					>
						Discard changes
					</Button>
					<Button
						data-testid="complexity-router-save-changes-button"
						type="submit"
						size="sm"
						disabled={!canUpdate || !isDirty || isSaving || isResetting || (isSubmitted && hasErrors)}
					>
						{isSaving ? <LoaderCircle className="h-3.5 w-3.5 animate-spin" /> : <Save className="h-3.5 w-3.5" />}
						{isSaving ? "Saving…" : "Save changes"}
					</Button>
				</div>
			</form>

			<AlertDialog open={restoreDialogOpen} onOpenChange={setRestoreDialogOpen}>
				<AlertDialogContent>
					<AlertDialogHeader>
						<AlertDialogTitle>Restore defaults</AlertDialogTitle>
						<AlertDialogDescription>
							This will reset all tier boundaries and keyword lists to the factory defaults. Your current configuration will be lost. This
							action cannot be undone.
						</AlertDialogDescription>
					</AlertDialogHeader>
					<AlertDialogFooter>
						<AlertDialogCancel
							data-testid="complexity-router-restore-cancel-button"
							onClick={() => setRestoreDialogOpen(false)}
							disabled={isResetting}
						>
							Cancel
						</AlertDialogCancel>
						<AlertDialogAction
							data-testid="complexity-router-restore-confirm-button"
							onClick={() => {
								setRestoreDialogOpen(false);
								handleRestoreDefaults();
							}}
							disabled={!canUpdate || isResetting}
						>
							Restore defaults
						</AlertDialogAction>
					</AlertDialogFooter>
				</AlertDialogContent>
			</AlertDialog>
		</ScrollArea>
	);
}