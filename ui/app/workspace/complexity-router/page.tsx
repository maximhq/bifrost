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
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { ModelMultiselect } from "@/components/ui/modelMultiselect";
import { Progress } from "@/components/ui/progress";
import { ScrollArea } from "@/components/ui/scrollArea";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Switch } from "@/components/ui/switch";
import { TagInput } from "@/components/ui/tagInput";
import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { ProviderIconType, RenderProviderIcon } from "@/lib/constants/icons";
import { EmbeddingSupportedProviders, getProviderLabel } from "@/lib/constants/logs";
import { getErrorMessage, useGetCoreConfigQuery, useGetProvidersQuery } from "@/lib/store";
import {
	useGetComplexityAnalyzerConfigQuery,
	useGetComplexitySemanticStatusQuery,
	useResetComplexityAnalyzerConfigMutation,
	useUpdateComplexityAnalyzerConfigMutation,
} from "@/lib/store/apis/governanceApi";
import {
	AnalyzerConfig,
	DEFAULT_SEMANTIC_CONFIG,
	DEFAULT_TIER_BOUNDARIES,
	formatSemanticTimeout,
	KEYWORD_LIST_DEFINITIONS,
	KeywordListKey,
	MAX_SEMANTIC_MESSAGE_HISTORY,
	MAX_SEMANTIC_PHRASE_CHARACTERS,
	MIN_SEMANTIC_MESSAGE_HISTORY,
	parseSemanticTimeoutMs,
	SEMANTIC_STATUS_LABELS,
	SEMANTIC_VECTOR_STORE_OPTIONS,
	SemanticStatusInfo,
	TierBoundaries,
} from "@/lib/types/complexityRouter";
import { ModelProvider, ModelProviderName } from "@/lib/types/config";
import { cn } from "@/lib/utils";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { zodResolver } from "@hookform/resolvers/zod";
import { CircleAlert, CircleCheck, ExternalLink, LoaderCircle, RotateCcw, Save } from "lucide-react";
import { type ChangeEvent, type ClipboardEvent, type DragEvent, type KeyboardEvent, useEffect, useMemo, useRef, useState } from "react";
import { Controller, useForm } from "react-hook-form";
import { toast } from "sonner";
import { z } from "zod";

type TierBoundaryKey = keyof TierBoundaries;

// The page classifies with exactly one mechanism. Semantic keeps the keyword
// lists in play (as exemplars, and for the lexical fallback path), so the two
// modes share every other control on this screen.
type ClassificationMode = "lexical" | "semantic";

// Embedding-capable providers gate semantic mode, matching the local cache
// screen's rule: built-ins are listed in EmbeddingSupportedProviders, custom
// providers declare support through allowed_requests.embedding.
const supportsEmbedding = (provider: ModelProvider): boolean => {
	if (provider.custom_provider_config) {
		return provider.custom_provider_config.allowed_requests?.embedding === true;
	}
	return (EmbeddingSupportedProviders as readonly string[]).includes(provider.name);
};

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

const analyzerConfigSchema = z
	.object({
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
			medium_keywords: z.array(z.string()).min(1, "Medium keywords cannot be empty"),
			complex_keywords: z.array(z.string()).min(1, "Complex keywords cannot be empty"),
		}),
		// Present only in semantic mode; switching back to lexical drops the key so
		// the PUT (a full replace) disables the classifier server-side.
		semantic: z
			.object({
				provider: z.string().min(1, "Select an embedding provider"),
				embedding_model: z.string().min(1, "Select an embedding model"),
				timeout: z.string().min(1, "Enter an embedding timeout").optional(),
				min_similarity: z.number({ error: "Enter a number between 0 and 1" }).min(0, "Must be 0 or greater").lt(1, "Must be less than 1"),
				message_history_count: z
					.number({ error: "Select how many messages to embed" })
					.int("Must be a whole number")
					.min(MIN_SEMANTIC_MESSAGE_HISTORY, `Must be at least ${MIN_SEMANTIC_MESSAGE_HISTORY}`)
					.max(MAX_SEMANTIC_MESSAGE_HISTORY, `Must be at most ${MAX_SEMANTIC_MESSAGE_HISTORY}`),
				count_toward_budgets: z.boolean().optional(),
				vector_store: z.enum(["auto", "embedded", "external"]).optional(),
			})
			.optional(),
	})
	.superRefine((data, ctx) => {
		// The per-phrase bound and one-tier-per-phrase rule only apply when the
		// lists are also used as semantic exemplars. Mirrors
		// validateComplexitySemanticPhrases so invalid input fails in the form
		// instead of as an opaque 400.
		if (!data.semantic) return;

		const lists: Array<{ key: KeywordListKey; label: string }> = [
			{ key: "simple_keywords", label: "Simple" },
			{ key: "medium_keywords", label: "Medium" },
			{ key: "complex_keywords", label: "Complex" },
		];

		const seen = new Map<string, string>();
		for (const { key, label } of lists) {
			for (const phrase of data.keywords[key]) {
				if (phrase.length > MAX_SEMANTIC_PHRASE_CHARACTERS) {
					ctx.addIssue({
						code: "custom",
						message: `A ${label} phrase exceeds the ${MAX_SEMANTIC_PHRASE_CHARACTERS}-character limit.`,
						path: ["keywords", key],
					});
					break;
				}
				const normalized = phrase.trim().toLowerCase();
				const firstTier = seen.get(normalized);
				if (firstTier && firstTier !== label) {
					ctx.addIssue({
						code: "custom",
						message: `"${phrase}" is also in the ${firstTier} list. Each semantic phrase must belong to exactly one tier.`,
						path: ["keywords", key],
					});
				} else if (!firstTier) {
					seen.set(normalized, label);
				}
			}
		}
	});

// The form is stricter than the wire type: the API omits semantic fields left
// at their zero value (Go `omitempty`), but every control here is controlled and
// needs a concrete value, so the schema's inferred type is the source of truth.
type AnalyzerFormValues = z.infer<typeof analyzerConfigSchema>;
type SemanticFormValues = NonNullable<AnalyzerFormValues["semantic"]>;

const DEFAULT_SEMANTIC_FORM_VALUES: SemanticFormValues = {
	...DEFAULT_SEMANTIC_CONFIG,
	min_similarity: DEFAULT_SEMANTIC_CONFIG.min_similarity ?? 0,
	message_history_count: DEFAULT_SEMANTIC_CONFIG.message_history_count ?? MIN_SEMANTIC_MESSAGE_HISTORY,
};

const DEFAULT_FORM_VALUES: AnalyzerFormValues = {
	tier_boundaries: { ...DEFAULT_TIER_BOUNDARIES },
	keywords: {
		simple_keywords: [],
		medium_keywords: [],
		complex_keywords: [],
	},
};

// Fills in the fields the API omitted so the semantic controls stay controlled.
function toFormValues(config: AnalyzerConfig): AnalyzerFormValues {
	const base: AnalyzerFormValues = { tier_boundaries: config.tier_boundaries, keywords: config.keywords };
	if (!config.semantic) return base;
	return {
		...base,
		semantic: {
			...DEFAULT_SEMANTIC_FORM_VALUES,
			...config.semantic,
			min_similarity: config.semantic.min_similarity ?? 0,
			message_history_count: config.semantic.message_history_count ?? MIN_SEMANTIC_MESSAGE_HISTORY,
		},
	};
}

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

// SemanticStatusPanel surfaces warmup readiness, which is otherwise only
// visible in server logs. Without it a failed warmup looks identical to a
// working lexical deployment, because the request log records mechanism=lexical
// in both cases.
function SemanticStatusPanel({
	status,
	isLoading,
	isNotSaved,
	hasUnsavedChanges,
}: {
	status: SemanticStatusInfo | undefined;
	isLoading: boolean;
	isNotSaved: boolean;
	hasUnsavedChanges: boolean;
}) {
	if (isNotSaved) {
		return (
			<div className="bg-card space-y-3 rounded-sm border p-4" data-testid="complexity-router-semantic-status">
				<div className="flex items-center justify-between">
					<span className="text-xs font-medium">Classifier status</span>
					<Badge
						className="border-0 bg-blue-100 py-0.5 text-[10px] text-blue-800 uppercase dark:bg-blue-900 dark:text-blue-300"
						data-testid="complexity-router-semantic-status-badge"
					>
						Not saved
					</Badge>
				</div>
				<p className="text-muted-foreground text-xs">
					Save this configuration to embed the tier phrases and activate semantic classification.
				</p>
			</div>
		);
	}

	if (isLoading && !status) {
		return (
			<div className="bg-card text-muted-foreground flex items-center gap-2 rounded-sm border p-4 text-xs">
				<LoaderCircle className="size-3.5 animate-spin" />
				Checking semantic classifier status…
			</div>
		);
	}
	if (!status) return null;

	const tone = {
		ready: "bg-green-100 text-green-800 dark:bg-green-900 dark:text-green-300",
		warming: "bg-amber-100 text-amber-800 dark:bg-amber-900 dark:text-amber-300",
		failed: "bg-red-100 text-red-800 dark:bg-red-900 dark:text-red-300",
		disabled: "bg-gray-100 text-gray-800 dark:bg-gray-800 dark:text-gray-300",
	}[status.state];

	const percent = status.total > 0 ? Math.round((status.loaded / status.total) * 100) : 0;

	return (
		<div className="bg-card space-y-3 rounded-sm border p-4" data-testid="complexity-router-semantic-status">
			<div className="flex items-center justify-between">
				<div className="flex items-center gap-2">
					{status.state === "ready" ? (
						<CircleCheck className="size-3.5 text-green-600" />
					) : status.state === "failed" ? (
						<CircleAlert className="text-destructive size-3.5" />
					) : status.state === "warming" ? (
						<LoaderCircle className="size-3.5 animate-spin text-amber-600" />
					) : null}
					<span className="text-xs font-medium">Classifier status</span>
				</div>
				<Badge className={cn("border-0 py-0.5 text-[10px] uppercase", tone)} data-testid="complexity-router-semantic-status-badge">
					{SEMANTIC_STATUS_LABELS[status.state]}
				</Badge>
			</div>

			{status.state === "warming" && (
				<div className="space-y-1.5">
					<Progress value={percent} className="h-1.5" />
					<p className="text-muted-foreground font-mono text-[11px] tabular-nums">
						Embedding exemplars: {status.loaded}/{status.total}
					</p>
				</div>
			)}

			{status.state === "ready" && (
				<p className="text-muted-foreground text-xs">
					{status.total} exemplar{status.total === 1 ? "" : "s"} embedded and serving.
				</p>
			)}

			{status.serving_previous && (
				<p className="text-xs text-amber-700 dark:text-amber-400">
					The previous exemplar generation is still serving requests while this one prepares. Routing is unaffected.
				</p>
			)}

			{hasUnsavedChanges && (
				<p className="text-xs text-amber-700 dark:text-amber-400">
					The saved classifier is still serving. Save to prepare and activate these phrase or model changes.
				</p>
			)}

			{status.error && (
				<p className="text-destructive text-xs" data-testid="complexity-router-semantic-status-error">
					{status.error}
				</p>
			)}

			{status.state === "failed" && (
				<p className="text-muted-foreground text-xs">Requests are being classified by the keyword analyzer until warmup succeeds.</p>
			)}
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
	const [mode, setMode] = useState<ClassificationMode>("lexical");
	// Remembers the semantic block across a lexical round-trip so toggling back
	// does not discard a filled-in provider/model.
	const lastSemanticRef = useRef<SemanticFormValues | undefined>(undefined);

	const { data: providersData, isLoading: providersLoading } = useGetProvidersQuery();
	const embeddingProviders = useMemo(() => (providersData || []).filter(supportsEmbedding), [providersData]);

	const { data: coreConfig } = useGetCoreConfigQuery({ fromDB: true });
	const isVectorStoreConnected = coreConfig?.is_cache_connected ?? false;

	// Poll only while warmup is in flight; a ready or failed classifier is
	// steady state until the next save, which refetches through the cache tag.
	const [statusPollInterval, setStatusPollInterval] = useState(0);
	const { data: semanticStatus, isLoading: statusLoading } = useGetComplexitySemanticStatusQuery(undefined, {
		skip: mode !== "semantic",
		pollingInterval: statusPollInterval,
	});
	useEffect(() => {
		setStatusPollInterval(semanticStatus?.state === "warming" ? 2000 : 0);
	}, [semanticStatus?.state]);

	const {
		register,
		handleSubmit,
		reset,
		control,
		watch,
		setValue,
		getValues,
		formState: { errors, isDirty, isSubmitted },
	} = useForm<AnalyzerFormValues>({
		resolver: zodResolver(analyzerConfigSchema),
		defaultValues: DEFAULT_FORM_VALUES,
		mode: "onSubmit",
		reValidateMode: "onChange",
	});

	const liveBoundaries = watch("tier_boundaries");
	const liveSemantic = watch("semantic");
	const liveKeywords = watch("keywords");

	const totalPhrases = useMemo(
		() =>
			(liveKeywords?.simple_keywords?.length ?? 0) +
			(liveKeywords?.medium_keywords?.length ?? 0) +
			(liveKeywords?.complex_keywords?.length ?? 0),
		[liveKeywords],
	);

	// Saving re-runs warmup. It is a no-op when only the threshold or timeout
	// changed (the exemplar fingerprint is unchanged), but re-embeds every
	// phrase when the provider, model, or a list changed.
	const willReembed = useMemo(() => {
		if (mode !== "semantic" || !data) return false;
		const saved = data.semantic;
		if (!saved) return true;
		return (
			saved.provider !== liveSemantic?.provider ||
			saved.embedding_model !== liveSemantic?.embedding_model ||
			JSON.stringify(data.keywords) !== JSON.stringify(liveKeywords)
		);
	}, [mode, data, liveSemantic, liveKeywords]);

	useEffect(() => {
		if (!data || isDirty) return;
		const formValues = toFormValues(data);
		reset(formValues);
		setMode(formValues.semantic ? "semantic" : "lexical");
		if (formValues.semantic) lastSemanticRef.current = formValues.semantic;
		setSubmitError(null);
	}, [data, isDirty, reset]);

	const handleModeChange = (next: ClassificationMode) => {
		if (next === mode || !canUpdate) return;
		if (next === "semantic") {
			setValue("semantic", lastSemanticRef.current ?? DEFAULT_SEMANTIC_FORM_VALUES, { shouldDirty: true });
		} else {
			const current = getValues("semantic");
			if (current) lastSemanticRef.current = current;
			setValue("semantic", undefined, { shouldDirty: true });
		}
		setMode(next);
	};

	const handleDiscard = () => {
		if (data) {
			const formValues = toFormValues(data);
			reset(formValues);
			setMode(formValues.semantic ? "semantic" : "lexical");
		}
		setSubmitError(null);
	};

	const handleRestoreDefaults = () => {
		if (!canUpdate) return;
		setSubmitError(null);
		resetConfig()
			.unwrap()
			.then((defaults) => {
				const formValues = toFormValues(defaults);
				reset(formValues);
				// The factory defaults carry no semantic block, so a reset also
				// turns semantic classification off.
				setMode(formValues.semantic ? "semantic" : "lexical");
				lastSemanticRef.current = formValues.semantic;
				toast.success("Reset to defaults", { position: "top-right" });
			})
			.catch((err) => {
				setSubmitError(getErrorMessage(err));
			});
	};

	const onValid = (values: AnalyzerFormValues) => {
		if (!canUpdate) return;
		setSubmitError(null);
		// The endpoint replaces the whole record and rejects unknown fields, so
		// lexical mode must omit the semantic block entirely rather than send a
		// blank one.
		const payload: AnalyzerConfig = mode === "semantic" ? values : { tier_boundaries: values.tier_boundaries, keywords: values.keywords };
		updateConfig(payload)
			.unwrap()
			.then((res) => {
				const formValues = toFormValues(res);
				reset(formValues);
				setMode(formValues.semantic ? "semantic" : "lexical");
				if (formValues.semantic) lastSemanticRef.current = formValues.semantic;
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
	const semanticErrors = errors.semantic;
	const hasErrors = Boolean(boundaryErrors || keywordErrors || semanticErrors);
	const isSemantic = mode === "semantic";
	// An in-flight providers query yields an empty list too, which is not the
	// same as having none: gating on it would disable semantic mode on every
	// page load and blame the operator for a provider they already configured.
	const noEmbeddingProviders = !providersLoading && embeddingProviders.length === 0;

	return (
		<ScrollArea className="no-padding-parent h-[calc(100vh_-_16px)] w-full px-14 pt-4">
			<form className="mx-auto w-full max-w-7xl space-y-8" onSubmit={handleSubmit(onValid)} noValidate>
				{/* ── Page header ── */}
				<div className="flex flex-col gap-3 sm:flex-row sm:items-start sm:justify-between">
					<div className="space-y-1.5">
						<h1 className="text-2xl font-semibold tracking-tight">Complexity Router</h1>
						<p className="text-muted-foreground max-w-2xl text-sm leading-relaxed">
							Tune how incoming requests are classified into three tiers, by keyword scoring or by embedding similarity. The result feeds
							the <code className="bg-muted rounded-sm px-1 py-0.5 font-mono text-xs">complexity_tier</code> field that routing rules can
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

				{/* ── Classification Mode ── */}
				<div className="space-y-3">
					<div className="flex items-baseline gap-2.5">
						<h2 className="text-sm font-semibold">Classification Mode</h2>
						<span className="text-muted-foreground text-xs">How each request is assigned a tier.</span>
					</div>

					<Tabs value={mode} onValueChange={(value) => handleModeChange(value as ClassificationMode)}>
						<TabsList className="grid w-full grid-cols-2">
							<TabsTrigger value="lexical" data-testid="complexity-router-mode-lexical-tab" disabled={!canUpdate}>
								Lexical
							</TabsTrigger>
							<TabsTrigger
								value="semantic"
								data-testid="complexity-router-mode-semantic-tab"
								disabled={!canUpdate || noEmbeddingProviders}
								title={noEmbeddingProviders ? "Configure an embedding-capable provider to enable semantic mode." : undefined}
							>
								Semantic
							</TabsTrigger>
						</TabsList>
					</Tabs>

					<p className="text-muted-foreground text-xs leading-relaxed">
						{isSemantic ? (
							<>
								Each request is embedded and compared with the tier examples. The nearest example above the minimum similarity determines
								the tier. This adds one embedding request for each classified request.
							</>
						) : (
							<>
								Requests are scored using matching phrases, message length, and recent conversation context. No embedding request is made.
							</>
						)}
					</p>
				</div>

				{/* ── Semantic settings ── */}
				{isSemantic && (
					<div className="space-y-3">
						<div className="flex items-baseline gap-2.5">
							<h2 className="text-sm font-semibold">Semantic Classifier</h2>
							<span className="text-muted-foreground text-xs">API keys are inherited from the provider&apos;s main configuration.</span>
						</div>

						<SemanticStatusPanel
							status={semanticStatus}
							isLoading={statusLoading}
							isNotSaved={!data.semantic}
							hasUnsavedChanges={willReembed}
						/>

						{willReembed && (
							<div className="rounded-sm border border-amber-200 bg-amber-50 p-3 text-xs text-amber-900 dark:border-amber-900 dark:bg-amber-950 dark:text-amber-200">
								<b>Heads up:</b> saving will embed all {totalPhrases} tier phrases through the selected provider. This uses embedding tokens
								and may take a short time. Changes only to similarity or timeout reuse the current embeddings.
							</div>
						)}

						<div className="bg-card space-y-5 rounded-sm border p-5">
							{providersLoading ? (
								<div className="flex items-center justify-center py-4">
									<LoaderCircle className="text-muted-foreground size-4 animate-spin" />
								</div>
							) : (
								<>
									{/* Provider + model */}
									<div className="grid gap-4 md:grid-cols-2">
										<div className="space-y-2">
											<Label htmlFor="semantic-provider">Embedding provider</Label>
											<Controller
												control={control}
												name="semantic.provider"
												render={({ field }) => (
													<Select
														value={field.value || undefined}
														onValueChange={(value: ModelProviderName) => {
															if (value === field.value) return;
															field.onChange(value);
															// A model name is only meaningful for its own provider.
															setValue("semantic.embedding_model", "", { shouldDirty: true });
														}}
														disabled={!canUpdate}
													>
														<SelectTrigger
															className="w-full"
															id="semantic-provider"
															data-testid="complexity-router-semantic-provider-select"
														>
															<SelectValue placeholder="Select provider" />
														</SelectTrigger>
														<SelectContent>
															{embeddingProviders
																.filter((provider) => provider.name)
																.map((provider) => (
																	<SelectItem key={provider.name} value={provider.name}>
																		<div className="flex items-center gap-2">
																			<RenderProviderIcon provider={provider.name as ProviderIconType} size="sm" className="h-4 w-4" />
																			<span>{getProviderLabel(provider.name)}</span>
																		</div>
																	</SelectItem>
																))}
														</SelectContent>
													</Select>
												)}
											/>
											{semanticErrors?.provider && <p className="text-destructive text-xs">{semanticErrors.provider.message}</p>}
										</div>

										<div className="space-y-2">
											<Label htmlFor="semantic-embedding-model">Embedding model</Label>
											<Controller
												control={control}
												name="semantic.embedding_model"
												render={({ field }) => (
													<ModelMultiselect
														inputId="semantic-embedding-model"
														data-testid="complexity-router-semantic-model-select"
														isSingleSelect
														provider={liveSemantic?.provider || undefined}
														value={field.value ?? ""}
														onChange={(model) => field.onChange(model)}
														placeholder={liveSemantic?.provider ? "Search or type an embedding model…" : "Select a provider first"}
														disabled={!canUpdate || !liveSemantic?.provider}
													/>
												)}
											/>
											{semanticErrors?.embedding_model && (
												<p className="text-destructive text-xs">{semanticErrors.embedding_model.message}</p>
											)}
										</div>
									</div>

									{/* Similarity floor */}
									<div className="grid gap-4 md:grid-cols-2">
										<div className="space-y-2">
											<Label htmlFor="semantic-min-similarity">Minimum similarity</Label>
											<Input
												id="semantic-min-similarity"
												data-testid="complexity-router-semantic-min-similarity-input"
												type="number"
												min={0}
												max={0.99}
												step={0.05}
												disabled={!canUpdate}
												aria-invalid={semanticErrors?.min_similarity ? true : undefined}
												className={cn("font-mono", semanticErrors?.min_similarity && "border-destructive focus-visible:ring-destructive")}
												{...register("semantic.min_similarity", { valueAsNumber: true })}
											/>
											{semanticErrors?.min_similarity ? (
												<p className="text-destructive text-xs">{semanticErrors.min_similarity.message}</p>
											) : (
												<p className="text-muted-foreground text-xs leading-relaxed">
													How close the nearest phrase must be before its tier is used. Below this, the request is treated as unclassified
													and falls back to keyword scoring. <code className="font-mono">0</code> accepts the nearest phrase however
													unrelated it is.
												</p>
											)}
										</div>
									</div>

									{/* Conversation window */}
									<div className="space-y-2">
										<Label htmlFor="semantic-message-history">Messages to embed</Label>
										<Controller
											control={control}
											name="semantic.message_history_count"
											render={({ field }) => (
												<Select
													value={String(field.value ?? MIN_SEMANTIC_MESSAGE_HISTORY)}
													onValueChange={(value) => field.onChange(Number(value))}
													disabled={!canUpdate}
												>
													<SelectTrigger
														className="w-full"
														id="semantic-message-history"
														data-testid="complexity-router-semantic-message-history-select"
													>
														<SelectValue />
													</SelectTrigger>
													<SelectContent>
														{Array.from({ length: MAX_SEMANTIC_MESSAGE_HISTORY }, (_, index) => index + MIN_SEMANTIC_MESSAGE_HISTORY).map(
															(count) => (
																<SelectItem key={count} value={String(count)}>
																	{count === 1 ? "Latest message only" : `Last ${count} messages`}
																</SelectItem>
															),
														)}
													</SelectContent>
												</Select>
											)}
										/>
										{semanticErrors?.message_history_count ? (
											<p className="text-destructive text-xs">{semanticErrors.message_history_count.message}</p>
										) : (
											<p className="text-muted-foreground text-xs leading-relaxed">
												The most recent user messages, joined oldest to newest, are embedded as one text. Widening this lets a short
												follow-up like <em>&ldquo;and make it faster&rdquo;</em> inherit the intent of earlier turns, but dilutes the latest
												message and embeds more input tokens per request. System prompts and assistant replies are never embedded.
											</p>
										)}
									</div>

									{/* Timeout + vector store */}
									<div className="grid gap-4 md:grid-cols-2">
										<div className="space-y-2">
											<Label htmlFor="semantic-timeout">Embedding timeout (ms)</Label>
											<Controller
												control={control}
												name="semantic.timeout"
												render={({ field }) => (
													<Input
														id="semantic-timeout"
														data-testid="complexity-router-semantic-timeout-input"
														type="number"
														min={1}
														step={10}
														disabled={!canUpdate}
														value={field.value === "" ? "" : parseSemanticTimeoutMs(field.value)}
														onChange={(event) => {
															const raw = event.target.value;
															field.onChange(raw === "" ? "" : formatSemanticTimeout(Number(raw)));
														}}
														aria-invalid={semanticErrors?.timeout ? true : undefined}
														className={cn("font-mono", semanticErrors?.timeout && "border-destructive focus-visible:ring-destructive")}
													/>
												)}
											/>
											{semanticErrors?.timeout ? (
												<p className="text-destructive text-xs">{semanticErrors.timeout.message}</p>
											) : (
												<p className="text-muted-foreground text-xs leading-relaxed">
													Ceiling on the embedding call, which runs inline on the request path. Exceeding it falls back to keyword scoring.
												</p>
											)}
										</div>

										<div className="space-y-2">
											<Label htmlFor="semantic-vector-store">Exemplar storage</Label>
											<Controller
												control={control}
												name="semantic.vector_store"
												render={({ field }) => (
													<Select value={field.value ?? "embedded"} onValueChange={field.onChange} disabled={!canUpdate}>
														<SelectTrigger
															className="w-full"
															id="semantic-vector-store"
															data-testid="complexity-router-semantic-vector-store-select"
														>
															<SelectValue />
														</SelectTrigger>
														<SelectContent>
															{SEMANTIC_VECTOR_STORE_OPTIONS.map((option) => (
																<SelectItem
																	key={option.value}
																	value={option.value}
																	disabled={option.value === "external" && !isVectorStoreConnected}
																>
																	{option.label}
																</SelectItem>
															))}
														</SelectContent>
													</Select>
												)}
											/>
											<p className="text-muted-foreground text-xs leading-relaxed">
												{
													SEMANTIC_VECTOR_STORE_OPTIONS.find((option) => option.value === (liveSemantic?.vector_store ?? "embedded"))
														?.description
												}
												{!isVectorStoreConnected && (
													<>
														{" "}
														<span className="text-muted-foreground/80">
															External requires a vector store configured and enabled in <code className="font-mono">config.json</code>.
														</span>
													</>
												)}
											</p>
										</div>
									</div>

									{/* Budget attribution */}
									<div className="flex items-start justify-between gap-6 border-t pt-4">
										<div className="space-y-0.5">
											<Label htmlFor="semantic-count-toward-budgets" className="text-sm font-medium">
												Count embedding cost toward budgets
											</Label>
											<p className="text-muted-foreground text-xs leading-relaxed">
												Bills each classification embedding to the same budgets as the request that triggered it, and warmup embeddings to
												the provider and model budgets. Cost is always reported to telemetry either way.
											</p>
										</div>
										<Controller
											control={control}
											name="semantic.count_toward_budgets"
											render={({ field }) => (
												<Switch
													id="semantic-count-toward-budgets"
													data-testid="complexity-router-semantic-budgets-switch"
													checked={field.value ?? false}
													onCheckedChange={field.onChange}
													disabled={!canUpdate}
												/>
											)}
										/>
									</div>
								</>
							)}
						</div>
					</div>
				)}

				{/* ── Complexity Spectrum ── */}
				{!isSemantic && (
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
				)}

				{/* ── Tier Boundaries ── */}
				<div className="space-y-3">
					<div className="flex items-baseline gap-2.5">
						<h2 className="text-sm font-semibold">{isSemantic ? "Lexical fallback boundaries" : "Tier Boundaries"}</h2>
						{isSemantic && (
							<span className="text-muted-foreground text-xs">Used only when semantic classification falls back to lexical scoring.</span>
						)}
					</div>

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

				{/* ── Tier Phrases ── */}
				<div className="space-y-3">
					<div className="flex flex-wrap items-baseline justify-between gap-2.5">
						<div className="flex flex-wrap items-baseline gap-2.5">
							<h2 className="text-sm font-semibold">Tier Phrases</h2>
							<span className="text-muted-foreground text-xs">
								{isSemantic
									? "Each phrase is a labeled example. The nearest example above the minimum similarity determines the tier. Each phrase must belong to exactly one tier."
									: "Phrases contribute to the lexical score. They are lowercased and deduplicated on save."}
							</span>
						</div>
						{isSemantic && (
							<span className="text-muted-foreground font-mono text-[11px] tabular-nums" data-testid="complexity-router-phrase-total">
								{totalPhrases} phrases
							</span>
						)}
					</div>

					{/* Root-level keyword issues such as cross-tier duplicates have no
					    single field to attach to, so they render above the lists. */}
					{keywordErrors?.message && (
						<p className="text-destructive text-xs" data-testid="complexity-router-keywords-error">
							{keywordErrors.message}
						</p>
					)}

					<div className="grid gap-3 md:grid-cols-2">
						{KEYWORD_LIST_DEFINITIONS.map(({ key, label, lexicalDescription, semanticDescription }) => {
							const fieldError = keywordErrors?.[key as KeywordListKey];
							const errorId = `keywords-${key}-error`;
							return (
								<div
									key={key}
									className={cn("bg-card relative overflow-hidden rounded-sm border", key === "complex_keywords" && "md:col-span-2")}
								>
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
												<p className="text-muted-foreground text-xs leading-relaxed">
													{isSemantic ? semanticDescription : lexicalDescription}
												</p>
												<TagInput
													data-testid={`complexity-router-keywords-${testIdPart(key)}-input`}
													value={field.value}
													onValueChange={field.onChange}
													collapsedTagLimit={KEYWORD_COLLAPSED_LIMIT}
													expandButtonTestId={`complexity-router-keywords-${testIdPart(key)}-expand-button`}
													placeholder={isSemantic ? "Type an example request and press Enter" : "Type a keyword or phrase and press Enter"}
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
				<div className="bg-card sticky bottom-0 z-10 flex flex-wrap items-center justify-end gap-2.5 border-t py-4">
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
							This will reset tier boundaries and tier phrases to the factory defaults
							{isSemantic ? " and turn semantic classification off, discarding its provider, model, and threshold settings" : ""}. Your
							current configuration will be lost. This action cannot be undone.
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