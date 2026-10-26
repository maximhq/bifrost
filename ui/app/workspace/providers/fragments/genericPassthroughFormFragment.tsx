import { Button } from "@/components/ui/button";
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from "@/components/ui/collapsible";
import { Form, FormControl, FormDescription, FormField, FormItem, FormLabel, FormMessage } from "@/components/ui/form";
import { Input } from "@/components/ui/input";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Switch } from "@/components/ui/switch";
import { Textarea } from "@/components/ui/textarea";
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip";
import { getErrorMessage, setProviderFormDirtyState, useAppDispatch } from "@/lib/store";
import { useGetProviderKeysQuery, usePreviewGenericUsageMutation, useUpdateProviderMutation } from "@/lib/store/apis/providersApi";
import {
	AllowedRequests,
	GenericAuthTypeLabels,
	GenericAuthTypes,
	GenericProviderConfig,
	GenericRouteMethods,
	GenericUnitsMeasureLabels,
	type GenericUsagePreview,
	genericRouteBillableByDefault,
	GenericUnitsSourceLabels,
	genericUnitsMeasureReadsWholeBody,
	ModelProvider,
} from "@/lib/types/config";
import { genericProviderFormSchema, genericUsagePreviewSampleSchema } from "@/lib/types/schemas";
import { importOpenAPI, parseOpenAPIText, type OpenAPIImport } from "@/lib/utils/openapiImport";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { zodResolver } from "@hookform/resolvers/zod";
import { ChevronRight, Info, Trash2 } from "lucide-react";
import { type KeyboardEvent, useEffect, useRef, useState } from "react";
import { type FieldErrors, useFieldArray, useForm } from "react-hook-form";
import { toast } from "sonner";
import { z } from "zod";
import { buildProviderUpdatePayload } from "../views/utils";

// The tab also owns the provider's two request switches: it replaces the API Structure tab that
// held them, and a generic provider allows no other request type.
const passthroughFormSchema = genericProviderFormSchema.extend({
	allowed: z.object({ passthrough: z.boolean(), passthrough_stream: z.boolean() }),
});

type FormValues = z.infer<typeof passthroughFormSchema>;

interface Props {
	provider: ModelProvider;
}

// A route's stream flag is tri-state: inherit the provider's detection, force streaming, or force buffering.
const STREAM_CHOICES = [
	{ value: "inherit", label: "Detect" },
	{ value: "stream", label: "Always stream" },
	{ value: "buffer", label: "Never stream" },
] as const;

const streamChoice = (stream?: boolean) => (stream === undefined ? "inherit" : stream ? "stream" : "buffer");
const streamFlag = (choice: string) => (choice === "inherit" ? undefined : choice === "stream");

const emptyUsage = {
	model_path: "",
	input_tokens_path: "",
	output_tokens_path: "",
	total_tokens_path: "",
	cost_path: "",
	cost_per_request: 0,
	units_path: "",
	units_source: "response" as const,
	units_measure: "value" as const,
	cost_per_unit: 0,
	units_block_size: undefined,
	units_minimum: undefined,
	disable_optimistic_parsing: false,
};

// A route's usage starts with its fee and unit price unset, not 0: a route-level 0 is saved and
// waives the provider's fee or unit price, which a new route must never do on its own.
// The units fields too: the tab does not edit them per route, and a value there overrides the
// provider's, so a stored route keeps exactly what it set and a new one sets nothing.
const emptyRouteUsage = {
	...emptyUsage,
	cost_per_request: undefined,
	cost_per_unit: undefined,
	units_source: undefined,
	units_measure: undefined,
	units_block_size: undefined,
	units_minimum: undefined,
};

// Route header matchers travel as a record in config and as name/value pairs in the form.
const headersToPairs = (headers?: Record<string, string>) => Object.entries(headers ?? {}).map(([name, value]) => ({ name, value }));
// The record has no prototype, so every name is stored as typed: on a plain object a matcher named
// __proto__ would set the prototype instead and be dropped from the saved route.
const pairsToHeaders = (pairs?: { name: string; value: string }[]) => {
	const out: Record<string, string> = Object.create(null);
	for (const p of pairs ?? []) {
		if (p.name.trim() && p.value.trim()) out[p.name.trim()] = p.value.trim();
	}
	return Object.keys(out).length > 0 ? out : undefined;
};

// A nil allowed_requests allows everything; a set one allows only the fields that are true.
function toFormValues(cfg: GenericProviderConfig | undefined, allowed?: AllowedRequests): FormValues {
	return {
		allowed: { passthrough: allowed ? !!allowed.passthrough : true, passthrough_stream: allowed ? !!allowed.passthrough_stream : true },
		auth: {
			type: cfg?.auth?.type ?? "bearer",
			header_name: cfg?.auth?.header_name ?? "",
			query_param: cfg?.auth?.query_param ?? "",
			value_prefix: cfg?.auth?.value_prefix ?? "",
			aws: { service: cfg?.auth?.aws?.service ?? "" },
		},
		usage: { ...emptyUsage, ...(cfg?.usage ?? {}) },
		streaming: {
			paths: cfg?.streaming?.paths ?? [],
			detect_from_accept_header: cfg?.streaming?.detect_from_accept_header ?? false,
			detect_from_body: cfg?.streaming?.detect_from_body ?? false,
		},
		routes: (cfg?.routes ?? []).map((r) => ({
			...r,
			// The server matches methods case-insensitively and accepts "post"; the form's selector
			// holds the upper-case names, so a stored lower-case one would block every save.
			method: r.method.toUpperCase() as typeof r.method,
			headers: headersToPairs(r.headers),
			// An unset billable stays unset in the form: the switch shows the current method's default
			// until the operator touches it, so changing an imported GET to POST keeps the POST default
			// rather than freezing the GET one as an explicit override.
			billable: r.billable,
			usage: { ...emptyRouteUsage, ...(r.usage ?? {}) },
		})),
		strict_routes: cfg?.strict_routes ?? false,
	};
}

// Empty strings are form conveniences, not config: strip them so the stored block only carries
// what the operator set, and a usage block with nothing in it is dropped altogether.
// route is set for route usage: a route's explicit 0 waives the provider fee or unit price and
// must be sent, while the provider-level fields treat 0 as "none" and drop it. Units fields the
// form does not expose per route (set through the API or config.json) are kept as they came in.
function cleanUsage(usage: FormValues["usage"], route = false) {
	if (!usage) return undefined;
	const out: NonNullable<GenericProviderConfig["usage"]> = {};
	if (usage.model_path) out.model_path = usage.model_path;
	if (usage.input_tokens_path) out.input_tokens_path = usage.input_tokens_path;
	if (usage.output_tokens_path) out.output_tokens_path = usage.output_tokens_path;
	if (usage.total_tokens_path) out.total_tokens_path = usage.total_tokens_path;
	if (usage.cost_path) out.cost_path = usage.cost_path;
	if (usage.cost_per_request !== undefined && (route || usage.cost_per_request > 0)) out.cost_per_request = usage.cost_per_request;
	if (usage.cost_per_unit !== undefined && (route || usage.cost_per_unit > 0)) out.cost_per_unit = usage.cost_per_unit;
	if (usage.units_block_size !== undefined && usage.units_block_size > 0) out.units_block_size = usage.units_block_size;
	if (usage.units_minimum !== undefined && (route || usage.units_minimum > 0)) out.units_minimum = usage.units_minimum;
	if (usage.units_path) out.units_path = usage.units_path;
	if (usage.units_source && (route || usage.units_source !== "response")) out.units_source = usage.units_source;
	if (usage.units_measure && (route || usage.units_measure !== "value")) out.units_measure = usage.units_measure;
	if (usage.disable_optimistic_parsing) out.disable_optimistic_parsing = true;
	return Object.keys(out).length > 0 ? out : undefined;
}

function toConfig(values: FormValues): GenericProviderConfig {
	const auth = values.auth;
	const cfg: GenericProviderConfig = {
		auth: {
			type: auth.type,
			...(auth.type === "header" ? { header_name: auth.header_name, value_prefix: auth.value_prefix || undefined } : {}),
			...(auth.type === "query" ? { query_param: auth.query_param } : {}),
			...(auth.type === "aws_sigv4" ? { aws: { service: auth.aws?.service ?? "" } } : {}),
		},
	};
	const usage = cleanUsage(values.usage);
	if (usage) cfg.usage = usage;
	const paths = (values.streaming?.paths ?? []).filter((p) => p.trim() !== "");
	if (paths.length > 0 || values.streaming?.detect_from_accept_header || values.streaming?.detect_from_body) {
		cfg.streaming = {
			paths: paths.length > 0 ? paths : undefined,
			detect_from_accept_header: values.streaming?.detect_from_accept_header || undefined,
			detect_from_body: values.streaming?.detect_from_body || undefined,
		};
	}
	if (values.routes && values.routes.length > 0) {
		cfg.routes = values.routes.map((r) => ({
			method: r.method,
			path: r.path,
			headers: pairsToHeaders(r.headers),
			job: r.job,
			// Stored only when it departs from the method default, so the config stays what the operator decided.
			billable: r.billable === undefined || r.billable === genericRouteBillableByDefault(r.method) ? undefined : r.billable,
			stream: r.stream,
			usage: cleanUsage(r.usage, true),
		}));
	}
	if (values.strict_routes) cfg.strict_routes = true;
	return cfg;
}

// GenericPassthroughFormFragment is the "Passthrough" tab of a generic relay provider: upstream
// auth, usage/cost mapping, stream detection and per-route overrides. An OpenAPI document can be
// imported into those fields in the browser; nothing about the document is stored.
export function GenericPassthroughFormFragment({ provider }: Props) {
	const hasUpdateProviderAccess = useRbac(RbacResource.ModelProvider, RbacOperation.Update);
	const dispatch = useAppDispatch();
	const [updateProvider, { isLoading: isUpdatingProvider }] = useUpdateProviderMutation();
	const { data: providerKeys = [] } = useGetProviderKeysQuery(provider.name);
	const genericConfig = provider.custom_provider_config?.generic_config;
	// OpenAPI import: the document is parsed in the browser and resolved into routes, auth and a
	// base URL hint; only those fields are saved.
	const [importText, setImportText] = useState("");
	const [importUrl, setImportUrl] = useState("");
	const [importing, setImporting] = useState(false);
	const [imported, setImported] = useState<OpenAPIImport | null>(null);
	// Test this mapping: a sample call run through the mapping as it stands in the form.
	const [previewUsage, { isLoading: previewing }] = usePreviewGenericUsageMutation();
	const [preview, setPreview] = useState<GenericUsagePreview | null>(null);
	// Each run is numbered so a slow earlier run cannot overwrite a later result, and the result
	// is cleared whenever the sample or the mapping changes, so it never describes stale inputs.
	const previewRun = useRef(0);
	const fileInputRef = useRef<HTMLInputElement>(null);
	const [sampleState, setSampleState] = useState({
		method: "POST",
		path: "/",
		headers: "",
		request_content_type: "application/json",
		request_body: "",
		status_code: 200,
		response_content_type: "application/json",
		response_body: "",
	});
	const [sampleErrors, setSampleErrors] = useState<Record<string, string>>({});
	const sample = sampleState;
	const setSample = (next: typeof sampleState) => {
		// Only the edited field's error goes stale; an error on a field left as it was still stands.
		setSampleErrors((errors) => {
			const kept = { ...errors };
			for (const key of Object.keys(kept)) {
				if (next[key as keyof typeof next] !== sampleState[key as keyof typeof sampleState]) delete kept[key];
			}
			return kept;
		});
		setSampleState(next);
		setPreview(null);
		previewRun.current++; // a run still in flight belongs to the old sample: its result is dropped
	};
	const runPreview = async () => {
		const checked = genericUsagePreviewSampleSchema.safeParse(sample);
		if (!checked.success) {
			const errors: Record<string, string> = {};
			for (const issue of checked.error.issues) {
				const key = String(issue.path[0] ?? "");
				if (!errors[key]) errors[key] = issue.message;
			}
			setSampleErrors(errors);
			return;
		}
		setSampleErrors({});
		const run = ++previewRun.current;
		try {
			const { headers: headerText, ...rest } = sample;
			const headers: Record<string, string> = {};
			for (const line of headerText.split("\n")) {
				const [name, ...valueParts] = line.split(":");
				if (name?.trim() && valueParts.length > 0) headers[name.trim()] = valueParts.join(":").trim();
			}
			const result = await previewUsage({
				provider: provider.name,
				body: {
					generic_config: toConfig(form.getValues()),
					...rest,
					path: checked.data.path,
					headers: Object.keys(headers).length > 0 ? headers : undefined,
				},
			}).unwrap();
			if (run === previewRun.current) setPreview(result);
		} catch (err) {
			// A run that an edit has already invalidated is not shown, its failure included.
			if (run !== previewRun.current) return;
			setPreview(null);
			toast.error("Could not run the mapping", { description: getErrorMessage(err) });
		}
	};
	// The preview inputs sit inside the mapping form, whose submit button is Save: Enter in one of
	// them runs the preview instead of saving an unfinished mapping.
	const runPreviewOnEnter = (e: KeyboardEvent<HTMLInputElement>) => {
		if (e.key === "Enter") {
			e.preventDefault();
			void runPreview();
		}
	};

	const form = useForm<FormValues>({
		resolver: zodResolver(passthroughFormSchema),
		mode: "onChange",
		defaultValues: toFormValues(genericConfig, provider.custom_provider_config?.allowed_requests),
	});
	const routes = useFieldArray({ control: form.control, name: "routes" });
	useEffect(() => {
		const subscription = form.watch(() => {
			setPreview(null);
			previewRun.current++; // same for a run started against the previous mapping
		});
		return () => subscription.unsubscribe();
	}, [form]);
	// A streaming path that is not rooted fails per item, so its error sits under streaming.paths.<n>
	// where FormMessage finds no message; surface the first one so a refused save is never silent.
	const streamingPathErrors = form.formState.errors.streaming?.paths as unknown as
		| ({ message?: string } | undefined)[]
		| { message?: string }
		| undefined;
	const streamingPathError = Array.isArray(streamingPathErrors) ? streamingPathErrors.find((e) => e?.message)?.message : undefined;

	useEffect(() => {
		dispatch(setProviderFormDirtyState(form.formState.isDirty));
	}, [form.formState.isDirty]);

	useEffect(() => {
		form.reset(toFormValues(genericConfig, provider.custom_provider_config?.allowed_requests));
		setStreamingPathsText((genericConfig?.streaming?.paths ?? []).join("\n"));
	}, [form, provider.name, genericConfig]);

	const authType = form.watch("auth.type");
	// The textarea keeps its own text so a newline the operator just typed survives until the next
	// glob is written; the form value only ever holds the non-empty lines.
	const [streamingPathsText, setStreamingPathsText] = useState(() => (genericConfig?.streaming?.paths ?? []).join("\n"));

	// A refused save must show its reason: the routes list is collapsed by default, so a route
	// error inside it opens the list instead of failing silently.
	const onInvalid = (errors: FieldErrors<FormValues>) => {
		if (errors.routes) setRoutesOpen(true);
	};

	const onSubmit = (values: FormValues) => {
		// Under SigV4 the relay signs with the key's AWS credentials and never reads its value, so
		// keys that were plain values for another auth type stop doing anything without a word.
		if (values.auth.type === "aws_sigv4" && genericConfig?.auth?.type !== "aws_sigv4") {
			const valued = providerKeys.filter((k) => k.value?.value || k.value?.ref).map((k) => k.name);
			if (valued.length > 0) {
				toast.warning("Existing key values are ignored under AWS Signature V4", {
					description: `${valued.join(", ")}: add AWS credentials to these keys, or they will sign with the gateway's own AWS identity.`,
				});
			}
		}
		updateProvider(
			buildProviderUpdatePayload(provider, {
				custom_provider_config: {
					...provider.custom_provider_config,
					base_provider_type: "generic",
					allowed_requests: {
						...(provider.custom_provider_config?.allowed_requests ?? {}),
						passthrough: values.allowed.passthrough,
						passthrough_stream: values.allowed.passthrough_stream,
					} as AllowedRequests,
					// Auth "none" injects nothing, so the provider needs no key; this tab replaces the one
					// that held the keyless switch, so the flag follows the auth type.
					is_key_less: values.auth.type === "none",
					generic_config: toConfig(values),
				},
			}),
		)
			.unwrap()
			.then(() => {
				toast.success("Passthrough configuration updated successfully");
				form.reset(values);
			})
			.catch((err) => {
				toast.error("Failed to update passthrough configuration", { description: getErrorMessage(err) });
			});
	};

	const applyImport = async (text: string) => {
		setImporting(true);
		try {
			const result = importOpenAPI(await parseOpenAPIText(text));
			setImported(result);
			// The document is the list: it replaces the routes there were, manual ones included. Routes
			// added by hand afterwards are kept until the next import.
			routes.replace(
				result.routes.map((route) => ({
					method: route.method,
					path: route.path,
					headers: headersToPairs(route.headers),
					billable: undefined,
					stream: undefined,
					usage: { ...emptyRouteUsage },
				})),
			);
			if (result.auth) {
				form.setValue("auth.type", result.auth.type, { shouldDirty: true });
				// The imported scheme replaces the auth fields together: a prefix from the old scheme
				// would otherwise be sent in front of the key.
				form.setValue("auth.value_prefix", result.auth.value_prefix ?? "", { shouldDirty: true });
				if (result.auth.header_name) form.setValue("auth.header_name", result.auth.header_name, { shouldDirty: true });
				if (result.auth.query_param) form.setValue("auth.query_param", result.auth.query_param, { shouldDirty: true });
			}
			if (result.routes.length > 0 && !result.partial) form.setValue("strict_routes", true, { shouldDirty: true });
			toast.success(`Imported ${result.routes.length} operations from ${result.title || "the document"}`);
		} catch (err) {
			setImported(null);
			toast.error("Could not import the OpenAPI document", { description: err instanceof Error ? err.message : String(err) });
		} finally {
			setImporting(false);
		}
	};

	const importFromUrl = async () => {
		setImporting(true);
		try {
			// Fetched by the browser, not the gateway: no server-side reach, and CORS decides whether it works.
			const res = await fetch(importUrl.trim());
			if (!res.ok) throw new Error(`${res.status} ${res.statusText}`);
			await applyImport(await res.text());
		} catch (err) {
			setImporting(false);
			toast.error("Could not fetch the document from the browser; paste it or upload the file instead", {
				description: err instanceof Error ? err.message : String(err),
			});
		}
	};

	const importFromFile = async (input: HTMLInputElement) => {
		const file = input.files?.[0];
		// Cleared so choosing the same file again (after editing it, or removing its routes) fires a change.
		input.value = "";
		if (!file) return;
		let text: string;
		try {
			text = await file.text();
		} catch (err) {
			toast.error("Could not read the file", { description: err instanceof Error ? err.message : String(err) });
			return;
		}
		await applyImport(text);
	};

	const disabled = !hasUpdateProviderAccess;
	// The routes list starts collapsed under the import panel; adding a route by hand opens it.
	const [routesOpen, setRoutesOpen] = useState(false);
	// Open the advanced section when it holds anything, so a configured path or stream rule is never hidden.
	const [advancedOpen, setAdvancedOpen] = useState(() => advancedHasSettings(form.getValues()));
	// The provider sheet reuses this tab when another provider is selected, so the sections are set
	// again for that provider's settings; a save of the same provider leaves them as the operator had them.
	useEffect(() => {
		setAdvancedOpen(advancedHasSettings(form.getValues()));
		setRoutesOpen(false);
	}, [provider.name]);
	const fieldLabel = (label: string, info?: string) => (
		<div className="flex items-center gap-1.5">
			<FormLabel>{label}</FormLabel>
			{info && (
				<TooltipProvider>
					<Tooltip>
						<TooltipTrigger asChild>
							<span tabIndex={0} aria-label={`About ${label}`}>
								<Info className="text-muted-foreground h-3 w-3" />
							</span>
						</TooltipTrigger>
						<TooltipContent className="max-w-sm">
							<p>{info}</p>
						</TooltipContent>
					</Tooltip>
				</TooltipProvider>
			)}
		</div>
	);
	const textField = (
		name: Parameters<typeof form.register>[0],
		label: string,
		placeholder: string,
		testId: string,
		info?: string,
		description?: string,
	) => (
		<FormField
			control={form.control}
			name={name}
			render={({ field }) => (
				<FormItem>
					{fieldLabel(label, info)}
					<FormControl>
						<Input
							placeholder={placeholder}
							data-testid={testId}
							disabled={disabled}
							{...field}
							value={(field.value as string | undefined) ?? ""}
						/>
					</FormControl>
					{description && <FormDescription>{description}</FormDescription>}
					<FormMessage />
				</FormItem>
			)}
		/>
	);
	const numberField = (
		name: Parameters<typeof form.register>[0],
		label: string,
		placeholder: string,
		testId: string,
		info: string,
		emptyAs: 0 | undefined,
	) => (
		<FormField
			control={form.control}
			name={name}
			render={({ field }) => (
				<FormItem>
					{fieldLabel(label, info)}
					<FormControl>
						<Input
							type="number"
							step="any"
							min={0}
							placeholder={placeholder}
							data-testid={testId}
							disabled={disabled}
							value={(field.value as number | undefined) ?? (emptyAs === 0 ? 0 : "")}
							onChange={(e) => field.onChange(e.target.value === "" ? emptyAs : Number(e.target.value))}
						/>
					</FormControl>
					<FormMessage />
				</FormItem>
			)}
		/>
	);
	const switchField = (name: Parameters<typeof form.register>[0], label: string, description: string, testId: string, info?: string) => (
		<FormField
			control={form.control}
			name={name}
			render={({ field }) => (
				<FormItem>
					<div className="bg-muted/50 flex items-center justify-between space-x-2 rounded-sm border p-3">
						<div className="space-y-0.5">
							<div className="flex items-center gap-1.5">
								<p className="text-sm font-medium">{label}</p>
								{info && (
									<TooltipProvider>
										<Tooltip>
											<TooltipTrigger asChild>
												<span tabIndex={0} aria-label={`About ${label}`}>
													<Info className="text-muted-foreground h-3 w-3" />
												</span>
											</TooltipTrigger>
											<TooltipContent className="max-w-sm">
												<p>{info}</p>
											</TooltipContent>
										</Tooltip>
									</TooltipProvider>
								)}
							</div>
							<p className="text-muted-foreground text-sm">{description}</p>
						</div>
						<Switch size="md" checked={!!field.value} onCheckedChange={field.onChange} disabled={disabled} data-testid={testId} />
					</div>
				</FormItem>
			)}
		/>
	);
	const unitsMeasure = form.watch("usage.units_measure");

	return (
		<Form {...form}>
			<form
				onSubmit={form.handleSubmit(onSubmit, onInvalid)}
				className="space-y-8 px-4 pb-6 md:px-6"
				data-testid="generic-passthrough-form"
			>
				<p className="text-muted-foreground text-sm">
					Requests to <code className="font-mono text-xs">/passthrough/{provider.name}/&lt;path&gt;</code> are sent on to{" "}
					<code className="font-mono text-xs">{provider.network_config?.base_url || "the configured base URL"}</code> unchanged, with your
					key added. Every call is logged; calls on billable routes are counted and charged. Only POST calls are billable unless a route
					below says otherwise.
				</p>

				{/* Auth */}
				<section className="space-y-4">
					<h3 className="text-sm font-semibold">How Bifrost signs in to the API</h3>
					<FormField
						control={form.control}
						name="auth.type"
						render={({ field }) => (
							<FormItem>
								{fieldLabel(
									"Auth type",
									"How the selected key is attached to every request. Any credential the caller sends is removed first, so only your key reaches the API.",
								)}
								<Select value={field.value} onValueChange={field.onChange} disabled={disabled}>
									<FormControl>
										<SelectTrigger data-testid="generic-auth-type-select">
											<SelectValue placeholder="Select auth type" />
										</SelectTrigger>
									</FormControl>
									<SelectContent>
										{GenericAuthTypes.map((t) => (
											<SelectItem key={t} value={t} data-testid={`generic-auth-type-option-${t}`}>
												{GenericAuthTypeLabels[t]}
											</SelectItem>
										))}
									</SelectContent>
								</Select>
								<FormMessage />
							</FormItem>
						)}
					/>
					{authType === "header" &&
						textField(
							"auth.header_name",
							"Header name",
							"x-api-key",
							"generic-auth-header-name-input",
							"The header the API reads its key from, e.g. x-api-key or Ocp-Apim-Subscription-Key.",
						)}
					{authType === "header" &&
						textField(
							"auth.value_prefix",
							"Value prefix (optional)",
							"Token ",
							"generic-auth-value-prefix-input",
							'Text put before the key in the header, e.g. "Token " sends Token <key>. Leave empty to send the key as is.',
						)}
					{authType === "query" &&
						textField(
							"auth.query_param",
							"Query parameter",
							"api_key",
							"generic-auth-query-param-input",
							"The query parameter the API reads its key from. A caller's own value for it is replaced.",
						)}
					{authType === "aws_sigv4" &&
						textField(
							"auth.aws.service",
							"AWS service",
							"textract",
							"generic-aws-service-input",
							"The service name AWS signs requests for, e.g. textract, translate, s3, comprehendmedical. The region comes from each key, so set it to the region in the base URL.",
						)}
				</section>

				{/* Routes */}
				<section className="space-y-4">
					<div>
						<h3 className="text-sm font-semibold">Routes</h3>
						<p className="text-muted-foreground text-sm">
							The operations this API offers, by method and path (and header, for APIs like AWS that name the operation in one). Each route
							can be priced or skipped; the first route that matches a call wins.
						</p>
					</div>
					<div className="space-y-3 rounded-sm border p-3" data-testid="generic-openapi-import-section">
						<p className="text-muted-foreground text-sm">
							Import the API&apos;s OpenAPI or Swagger document (paste, upload or fetch it). Its operations become the routes below, its
							security scheme sets the auth type, and &quot;Only allow the routes above&quot; is switched on. After a partial import (see
							the warnings), add the missing routes and switch it on yourself. The document itself is not stored.
							{routes.fields.length > 0 && (
								<span className="text-foreground" data-testid="generic-openapi-import-replace-note">
									{" "}
									Importing replaces the {routes.fields.length} route{routes.fields.length === 1 ? "" : "s"} below.
								</span>
							)}
						</p>
						<div className="grid gap-3 md:grid-cols-[1fr_auto]">
							<Input
								placeholder="https://api.example.com/openapi.json"
								data-testid="generic-openapi-import-url-input"
								disabled={disabled || importing}
								value={importUrl}
								onChange={(e) => setImportUrl(e.target.value)}
							/>
							<Button
								type="button"
								variant="outline"
								size="sm"
								isLoading={importing}
								disabled={disabled || importing || !importUrl.trim()}
								data-testid="generic-openapi-import-url-btn"
								onClick={importFromUrl}
							>
								Fetch and import
							</Button>
						</div>
						<Textarea
							placeholder="openapi: 3.0.0 …"
							data-testid="generic-openapi-import-textarea"
							disabled={disabled || importing}
							rows={5}
							value={importText}
							onChange={(e) => setImportText(e.target.value)}
						/>
						<div className="flex flex-wrap items-center gap-3">
							<Button
								type="button"
								variant="outline"
								size="sm"
								isLoading={importing}
								disabled={disabled || importing || !importText.trim()}
								data-testid="generic-openapi-import-btn"
								onClick={() => applyImport(importText)}
							>
								Import pasted document
							</Button>
							<Button
								type="button"
								variant="outline"
								size="sm"
								disabled={disabled || importing}
								data-testid="generic-openapi-import-file-btn"
								onClick={() => fileInputRef.current?.click()}
							>
								Upload a file
							</Button>
							<input
								ref={fileInputRef}
								type="file"
								accept=".json,.yaml,.yml,application/json"
								className="hidden"
								tabIndex={-1}
								data-testid="generic-openapi-import-file"
								disabled={disabled || importing}
								onChange={(e) => importFromFile(e.currentTarget)}
							/>
							{imported && (
								<p className="text-muted-foreground text-xs" data-testid="generic-openapi-import-summary">
									{imported.title || "Document"} {imported.version ? `v${imported.version}` : ""}: {imported.routes.length} operations
									{imported.baseUrl ? `, server ${imported.baseUrl} (set it on the Network tab)` : ""}
									{imported.warnings.length > 0 ? `. ${imported.warnings.join(". ")}` : ""}
								</p>
							)}
						</div>
					</div>
					<Collapsible open={routesOpen} onOpenChange={setRoutesOpen} className="space-y-3">
						<div className="flex items-center justify-between">
							<CollapsibleTrigger
								className="hover:text-foreground flex items-center gap-1 text-sm font-medium transition-colors"
								data-testid="generic-routes-toggle"
							>
								<ChevronRight className={`h-4 w-4 transition-transform ${routesOpen ? "rotate-90" : ""}`} />
								{routes.fields.length === 0 ? "No routes yet" : `${routes.fields.length} route${routes.fields.length === 1 ? "" : "s"}`}
							</CollapsibleTrigger>
							<Button
								type="button"
								variant="outline"
								size="sm"
								disabled={disabled}
								data-testid="generic-route-add-btn"
								onClick={() => {
									routes.append({
										method: "POST",
										path: "/",
										headers: [],
										billable: undefined,
										stream: undefined,
										usage: { ...emptyRouteUsage },
									});
									setRoutesOpen(true);
								}}
							>
								Add route manually
							</Button>
						</div>
						<CollapsibleContent className="space-y-3">
							{routes.fields.map((route, index) => (
								<div key={route.id} className="space-y-3 rounded-sm border p-3" data-testid={`generic-route-row-${index}`}>
									<div className="grid gap-3 md:grid-cols-[120px_1fr_140px_auto]">
										<FormField
											control={form.control}
											name={`routes.${index}.method`}
											render={({ field }) => (
												<FormItem>
													<Select value={field.value} onValueChange={field.onChange} disabled={disabled}>
														<FormControl>
															<SelectTrigger data-testid={`generic-route-method-select-${index}`}>
																<SelectValue />
															</SelectTrigger>
														</FormControl>
														<SelectContent>
															{GenericRouteMethods.map((m) => (
																<SelectItem key={m} value={m}>
																	{m}
																</SelectItem>
															))}
														</SelectContent>
													</Select>
												</FormItem>
											)}
										/>
										<FormField
											control={form.control}
											name={`routes.${index}.path`}
											render={({ field }) => (
												<FormItem>
													<FormControl>
														<Input placeholder="/search" data-testid={`generic-route-path-input-${index}`} disabled={disabled} {...field} />
													</FormControl>
													<FormMessage />
												</FormItem>
											)}
										/>
										<FormField
											control={form.control}
											name={`routes.${index}.stream`}
											render={({ field }) => (
												<FormItem>
													<div className="flex items-center gap-1.5">
														<Select
															value={streamChoice(field.value)}
															onValueChange={(v) => field.onChange(streamFlag(v))}
															disabled={disabled}
														>
															<FormControl>
																<SelectTrigger data-testid={`generic-route-stream-select-${index}`}>
																	<SelectValue />
																</SelectTrigger>
															</FormControl>
															<SelectContent>
																{STREAM_CHOICES.map((c) => (
																	<SelectItem key={c.value} value={c.value}>
																		{c.label}
																	</SelectItem>
																))}
															</SelectContent>
														</Select>
														<InfoTip
															label="Streaming"
															text="Detect: streamed only when a streaming path or an enabled stream signal says so (Advanced). Always stream: for calls with large or live replies, e.g. audio or SSE. Never stream: read the reply whole so it can be billed."
														/>
													</div>
												</FormItem>
											)}
										/>
										<Button
											type="button"
											variant="ghost"
											size="icon"
											disabled={disabled}
											data-testid={`generic-route-remove-btn-${index}`}
											aria-label={`Remove route ${index + 1}`}
											onClick={() => routes.remove(index)}
										>
											<Trash2 className="h-4 w-4" />
										</Button>
									</div>
									<div className="grid gap-3 md:grid-cols-2">
										<FormField
											control={form.control}
											name={`routes.${index}.billable`}
											render={({ field }) => (
												<FormItem>
													<div className="bg-muted/50 flex items-center justify-between space-x-2 rounded-sm border p-3">
														<div className="flex items-center gap-1.5">
															<p className="text-sm font-medium">Billable</p>
															<InfoTip
																label="Billable"
																text="On: calls to this route are counted against rate limits and charged by the pricing below. Off: they are passed through and logged but neither counted nor charged, e.g. status polls and listings. Defaults to on for POST and off for other methods."
															/>
														</div>
														<Switch
															size="md"
															checked={field.value ?? genericRouteBillableByDefault(form.watch(`routes.${index}.method`))}
															onCheckedChange={field.onChange}
															disabled={disabled}
															data-testid={`generic-route-billable-switch-${index}`}
														/>
													</div>
												</FormItem>
											)}
										/>
										<FormField
											control={form.control}
											name={`routes.${index}.usage.cost_per_request`}
											render={({ field }) => (
												<FormItem>
													<div className="flex items-center gap-1.5">
														<FormLabel>Flat fee for this route (USD)</FormLabel>
														<InfoTip
															label="Flat fee for this route"
															text="Replaces the flat fee per call for calls to this route. Leave empty to use the flat fee below; set 0 to charge this route only by quantity."
														/>
													</div>
													<FormControl>
														<Input
															type="number"
															step="any"
															min={0}
															placeholder="Same as the flat fee below"
															data-testid={`generic-route-cost-per-request-input-${index}`}
															disabled={disabled}
															value={field.value ?? ""}
															onChange={(e) => field.onChange(e.target.value === "" ? undefined : Number(e.target.value))}
														/>
													</FormControl>
												</FormItem>
											)}
										/>
									</div>
									<RouteHeaderMatchers control={form.control} index={index} disabled={disabled} />
									{form.watch(`routes.${index}.job`) && (
										<p className="text-muted-foreground text-xs" data-testid={`generic-route-job-note-${index}`}>
											Starts an async job (settled by the job accounting sweeper); edit the job block through config.json or the API.
										</p>
									)}
								</div>
							))}
						</CollapsibleContent>
					</Collapsible>
					{switchField(
						"strict_routes",
						"Only allow the routes above",
						"Any other call is refused with 404 before it reaches the API",
						"generic-strict-routes-switch",
						"Turns the routes list into an allowlist, so your key cannot be used for operations you have not priced. Recommended once the routes are set up.",
					)}
				</section>

				{/* Pricing */}
				<section className="space-y-4">
					<div>
						<h3 className="text-sm font-semibold">What a call costs</h3>
						<p className="text-muted-foreground text-sm">
							Each billable call is charged a flat fee plus, if the API bills by quantity, a per-unit charge. Leave both at 0 to log calls
							without a cost.
						</p>
					</div>
					{numberField(
						"usage.cost_per_request",
						"Flat fee per call (USD)",
						"0",
						"generic-usage-cost-per-request-input",
						"Charged on every successful billable call, whatever the response says. A route below can set its own fee, or 0 to waive it.",
						0,
					)}

					<div className="space-y-3 rounded-sm border p-3" data-testid="generic-units-section">
						<div>
							<p className="text-sm font-medium">Charge by quantity</p>
							<p className="text-muted-foreground text-sm">
								For APIs priced per page, credit, character or byte. Cost = quantity × price per unit.
							</p>
						</div>
						<div className="grid gap-4 md:grid-cols-2">
							<FormField
								control={form.control}
								name="usage.units_source"
								render={({ field }) => (
									<FormItem>
										{fieldLabel(
											"Count from",
											"Where the quantity is read. The API response body suits counts the API reports (Textract pages, Tavily credits). The caller request body suits APIs priced by what you send (Translate characters); such a request must be JSON, or it is refused.",
										)}
										<Select value={field.value ?? "response"} onValueChange={field.onChange} disabled={disabled}>
											<FormControl>
												<SelectTrigger className="w-full" data-testid="generic-units-source-select">
													<SelectValue />
												</SelectTrigger>
											</FormControl>
											<SelectContent>
												{(Object.keys(GenericUnitsSourceLabels) as (keyof typeof GenericUnitsSourceLabels)[]).map((k) => (
													<SelectItem key={k} value={k}>
														{GenericUnitsSourceLabels[k]}
													</SelectItem>
												))}
											</SelectContent>
										</Select>
									</FormItem>
								)}
							/>
							<FormField
								control={form.control}
								name="usage.units_measure"
								render={({ field }) => (
									<FormItem>
										{fieldLabel(
											"What to count",
											"Number in the JSON reads the value at the path below (pages, credits, seconds). Text length counts the characters of the text at that path. The whole-body options need no path and work for any content type (SSML, raw text, files).",
										)}
										<Select value={field.value ?? "value"} onValueChange={field.onChange} disabled={disabled}>
											<FormControl>
												<SelectTrigger className="w-full" data-testid="generic-units-measure-select">
													<SelectValue />
												</SelectTrigger>
											</FormControl>
											<SelectContent>
												{(Object.keys(GenericUnitsMeasureLabels) as (keyof typeof GenericUnitsMeasureLabels)[]).map((k) => (
													<SelectItem key={k} value={k}>
														{GenericUnitsMeasureLabels[k]}
													</SelectItem>
												))}
											</SelectContent>
										</Select>
									</FormItem>
								)}
							/>
							{!genericUnitsMeasureReadsWholeBody(unitsMeasure) &&
								textField(
									"usage.units_path",
									"Where to find it",
									"usage.credits",
									"generic-units-path-input",
									"A dot path into the JSON, e.g. usage.credits (Tavily), DocumentMetadata.Pages (Textract), Text (Translate). An array is added up.",
								)}
							{numberField(
								"usage.cost_per_unit",
								"Price per unit (USD)",
								"0",
								"generic-units-cost-per-unit-input",
								"What one unit costs, e.g. 0.008 per Tavily credit, 0.0015 per Textract page, 0.000015 per Translate character.",
								0,
							)}
							{numberField(
								"usage.units_block_size",
								"Bill in blocks of (optional)",
								"exact",
								"generic-units-block-size-input",
								"Round the quantity up to whole blocks, e.g. 100 for APIs priced per started 100 characters. Empty bills the exact quantity.",
								undefined,
							)}
							{numberField(
								"usage.units_minimum",
								"Minimum units per call (optional)",
								"0",
								"generic-units-minimum-input",
								"The least number of units billed once a quantity was found, e.g. 1 for APIs with a one-unit minimum.",
								undefined,
							)}
						</div>
					</div>
				</section>

				{/* Advanced: field paths and streaming */}
				<Collapsible className="space-y-4" open={advancedOpen} onOpenChange={setAdvancedOpen}>
					<CollapsibleTrigger
						className="text-muted-foreground hover:text-foreground flex items-center gap-1 text-sm font-medium transition-colors"
						data-testid="generic-advanced-toggle"
					>
						<ChevronRight className={`h-4 w-4 transition-transform ${advancedOpen ? "rotate-90" : ""}`} />
						Advanced: where usage is read from, and streaming
					</CollapsibleTrigger>
					<CollapsibleContent className="space-y-8">
						<section className="space-y-4">
							<div>
								<h3 className="text-sm font-semibold">Where usage is read from</h3>
								<p className="text-muted-foreground text-sm">
									Bifrost already reads the fields most LLM-style APIs use (the request&apos;s model, the reply&apos;s token counts and any
									cost it reports). Fill these in only when your API names them differently. Token counts are logged, not priced: for an LLM
									API, add a custom provider with an LLM base format (e.g. OpenAI) instead, which prices tokens from the model catalog.
								</p>
							</div>
							<div className="grid gap-4 md:grid-cols-2">
								{textField(
									"usage.model_path",
									"Model field in the request",
									"model",
									"generic-usage-model-path-input",
									"Where the model name sits in the caller's JSON request. It is logged and matched against virtual-key model limits. Default: model.",
								)}
								{textField(
									"usage.cost_path",
									"Cost the API reports",
									"usage.cost",
									"generic-usage-cost-path-input",
									"A dollar amount the reply carries, added to the call's cost (an array is added up). Default: usage.cost. Turn on Ignore Provider-Reported Cost (Pricing tab) to drop it.",
								)}
								{textField(
									"usage.input_tokens_path",
									"Input tokens",
									"usage.prompt_tokens",
									"generic-usage-input-tokens-path-input",
									"Default: usage.prompt_tokens, then usage.input_tokens.",
								)}
								{textField(
									"usage.output_tokens_path",
									"Output tokens",
									"usage.completion_tokens",
									"generic-usage-output-tokens-path-input",
									"Default: usage.completion_tokens, then usage.output_tokens.",
								)}
								{textField(
									"usage.total_tokens_path",
									"Total tokens",
									"usage.total_tokens",
									"generic-usage-total-tokens-path-input",
									"Default: usage.total_tokens.",
								)}
							</div>
							{switchField(
								"usage.disable_optimistic_parsing",
								"Only read the fields set here",
								"Stop reading the default fields above, so nothing is taken from a reply you did not map",
								"generic-usage-disable-optimistic-switch",
								"Use this when the API's replies happen to contain fields like usage.cost or usage.total_tokens that mean something else.",
							)}
						</section>

						{/* Streaming */}
						<section className="space-y-4">
							<div>
								<h3 className="text-sm font-semibold">Streaming</h3>
								<p className="text-muted-foreground text-sm">
									By default every call is read whole, so the reply can be billed. Streamed replies are passed through as they arrive; only
									SSE streams are read for usage.
								</p>
							</div>
							<FormField
								control={form.control}
								name="streaming.paths"
								render={({ field }) => (
									<FormItem>
										{fieldLabel(
											"Always stream these paths",
											"One path per line; * matches one segment and a trailing /** everything below, e.g. /v1/stream/**. Calls to these paths are always streamed.",
										)}
										<FormControl>
											<Textarea
												placeholder={"/v1/stream/**\n/events/*"}
												data-testid="generic-streaming-paths-textarea"
												disabled={disabled}
												value={streamingPathsText}
												onChange={(e) => {
													// Blank lines (a trailing newline while typing) are not globs; keep them out of the
													// value so they can never fail validation, and keep the raw text so the caret stays put.
													setStreamingPathsText(e.target.value);
													field.onChange(
														e.target.value
															.split("\n")
															.map((l) => l.trim())
															.filter((l) => l !== ""),
													);
												}}
												onBlur={field.onBlur}
												rows={3}
											/>
										</FormControl>
										<FormMessage />
										{streamingPathError && (
											<p className="text-destructive text-sm" data-testid="generic-streaming-paths-error">
												{streamingPathError}
											</p>
										)}
									</FormItem>
								)}
							/>
							{switchField(
								"streaming.detect_from_body",
								'Stream when the request says "stream": true',
								"Leave off unless the API is an LLM API",
								"generic-streaming-body-switch",
								'Treats "stream": true in a JSON body (or ?stream=true on a GET) as a streaming request. A streamed reply that is not SSE is passed through unread, so on an API billed from its reply a caller could add the flag and pay only the flat fee.',
							)}
							{switchField(
								"streaming.detect_from_accept_header",
								"Stream when the caller asks for SSE",
								"Requests with Accept: text/event-stream are streamed",
								"generic-streaming-accept-switch",
								"Off by default for the same reason as the stream flag: only turn it on for an API that really streams SSE.",
							)}
						</section>
					</CollapsibleContent>
				</Collapsible>

				{/* Requests */}
				<section className="space-y-4">
					<h3 className="text-sm font-semibold">Requests</h3>
					{switchField(
						"allowed.passthrough",
						"Allow normal requests",
						"Calls whose reply is read whole; off refuses them",
						"generic-allowed-passthrough-switch",
						"Normal calls are read whole before being returned, which is what lets Bifrost bill from the reply.",
					)}
					{switchField(
						"allowed.passthrough_stream",
						"Allow streamed requests",
						"Calls served as streams; off refuses them",
						"generic-allowed-passthrough-stream-switch",
						"A call is streamed when a streaming path, a stream route, or an enabled stream signal (Advanced) says so. Turn this off for an API that never streams.",
					)}
				</section>

				{/* Test this mapping */}
				<section className="space-y-4" data-testid="generic-usage-preview-section">
					<div>
						<h3 className="text-sm font-semibold">Test this mapping</h3>
						<p className="text-muted-foreground text-sm">
							Paste a request and the response the API gives for it. The mapping above, as it stands in this form (saved or not), is run
							against them and what the log row would carry is shown. Nothing is sent to the API.
						</p>
					</div>
					<div className="grid gap-3 md:grid-cols-[120px_1fr_1fr]">
						<Select value={sample.method} onValueChange={(v) => setSample({ ...sample, method: v })} disabled={disabled}>
							<SelectTrigger data-testid="generic-preview-method-select">
								<SelectValue />
							</SelectTrigger>
							<SelectContent>
								{GenericRouteMethods.map((m) => (
									<SelectItem key={m} value={m}>
										{m}
									</SelectItem>
								))}
							</SelectContent>
						</Select>
						<div className="space-y-1">
							<Input
								placeholder="/search"
								value={sample.path}
								onChange={(e) => setSample({ ...sample, path: e.target.value })}
								onKeyDown={runPreviewOnEnter}
								disabled={disabled}
								aria-invalid={Boolean(sampleErrors.path)}
								data-testid="generic-preview-path-input"
							/>
							{sampleErrors.path && (
								<p className="text-destructive text-sm" data-testid="generic-preview-path-error">
									{sampleErrors.path}
								</p>
							)}
						</div>
						<div className="space-y-1">
							<Input
								type="number"
								value={sample.status_code}
								onChange={(e) => setSample({ ...sample, status_code: Number(e.target.value) || 0 })}
								onKeyDown={runPreviewOnEnter}
								disabled={disabled}
								aria-invalid={Boolean(sampleErrors.status_code)}
								data-testid="generic-preview-status-input"
								aria-label="Response status code"
							/>
							{sampleErrors.status_code && (
								<p className="text-destructive text-sm" data-testid="generic-preview-status-error">
									{sampleErrors.status_code}
								</p>
							)}
						</div>
					</div>
					<Textarea
						placeholder={"Request headers, one per line, e.g.\nX-Amz-Target: Textract.AnalyzeDocument"}
						value={sample.headers}
						onChange={(e) => setSample({ ...sample, headers: e.target.value })}
						disabled={disabled}
						rows={2}
						data-testid="generic-preview-headers-textarea"
					/>
					<div className="grid gap-3 md:grid-cols-2">
						<div className="space-y-2">
							<Input
								placeholder="Request Content-Type, e.g. application/json"
								value={sample.request_content_type}
								onChange={(e) => setSample({ ...sample, request_content_type: e.target.value })}
								onKeyDown={runPreviewOnEnter}
								disabled={disabled}
								data-testid="generic-preview-request-content-type-input"
							/>
							<Textarea
								placeholder={'Request body, e.g. {"Text": "hello"}'}
								value={sample.request_body}
								onChange={(e) => setSample({ ...sample, request_body: e.target.value })}
								disabled={disabled}
								rows={5}
								data-testid="generic-preview-request-body-textarea"
							/>
						</div>
						<div className="space-y-2">
							<Input
								placeholder="Response Content-Type, e.g. application/json"
								value={sample.response_content_type}
								onChange={(e) => setSample({ ...sample, response_content_type: e.target.value })}
								onKeyDown={runPreviewOnEnter}
								disabled={disabled}
								data-testid="generic-preview-response-content-type-input"
							/>
							<Textarea
								placeholder={'Response body, e.g. {"DocumentMetadata": {"Pages": 7}}'}
								value={sample.response_body}
								onChange={(e) => setSample({ ...sample, response_body: e.target.value })}
								disabled={disabled}
								rows={5}
								data-testid="generic-preview-response-body-textarea"
							/>
						</div>
					</div>
					<div className="flex items-center gap-3">
						<Button
							type="button"
							variant="outline"
							size="sm"
							onClick={runPreview}
							isLoading={previewing}
							disabled={disabled || previewing}
							data-testid="generic-preview-run-btn"
						>
							Run
						</Button>
					</div>
					{preview && (
						<div className="space-y-2 rounded-sm border p-3 text-sm" data-testid="generic-preview-result">
							<div className="grid gap-x-6 gap-y-1 md:grid-cols-2">
								<span className="text-muted-foreground">Route</span>
								<span data-testid="generic-preview-route">
									{preview.route_index === null ? "none (provider defaults)" : `routes[${preview.route_index}]`}
									{!preview.route_allowed ? " - refused by strict routes" : ""}
								</span>
								<span className="text-muted-foreground">Served as</span>
								<span>{preview.streams ? "stream" : "buffered"}</span>
								<span className="text-muted-foreground">Counted</span>
								<span data-testid="generic-preview-counted">
									{preview.counted
										? "yes, one request"
										: !preview.route_allowed
											? "no (refused by strict routes)"
											: !preview.billable
												? "no (route not billable)"
												: "no (see notes)"}
								</span>
								<span className="text-muted-foreground">Model</span>
								<span>{preview.model || (preview.model_path ? `none at ${preview.model_path}` : "not read")}</span>
								<span className="text-muted-foreground">Units</span>
								<span data-testid="generic-preview-units">
									{preview.units === null
										? "none"
										: `${preview.units} (${preview.units_measure}, ${preview.units_source}${preview.units_path ? `, ${preview.units_path}` : ""})${
												preview.units_billed !== null && preview.units_billed !== preview.units
													? ` billed as ${preview.units_billed}${preview.units_block_size > 0 ? ` in blocks of ${preview.units_block_size}` : ""}${preview.units_minimum > 0 ? `, minimum ${preview.units_minimum}` : ""}`
													: ""
											} × ${preview.cost_per_unit}`}
								</span>
								<span className="text-muted-foreground">Tokens</span>
								<span>
									{preview.tokens
										? `${preview.tokens.prompt_tokens ?? 0} in / ${preview.tokens.completion_tokens ?? 0} out / ${preview.tokens.total_tokens ?? 0} total`
										: "none"}
								</span>
								<span className="text-muted-foreground">Extracted cost</span>
								<span>{preview.extracted_cost === null ? "none" : preview.extracted_cost}</span>
								<span className="text-muted-foreground">Flat fee</span>
								<span>{preview.cost_per_request}</span>
								<span className="font-medium">Cost on the log row</span>
								<span className="font-medium" data-testid="generic-preview-cost">
									{preview.cost} USD
								</span>
							</div>
							{preview.notes.length > 0 && (
								<ul className="text-muted-foreground list-disc pl-5" data-testid="generic-preview-notes">
									{preview.notes.map((n) => (
										<li key={n}>{n}</li>
									))}
								</ul>
							)}
						</div>
					)}
				</section>

				<div className="flex justify-end">
					<Button
						type="submit"
						isLoading={isUpdatingProvider}
						disabled={disabled || !form.formState.isDirty}
						data-testid="generic-passthrough-save-btn"
					>
						Save
					</Button>
				</div>
			</form>
		</Form>
	);
}

// advancedHasSettings reports whether the Advanced section holds anything, so it opens itself then
// and a configured path or stream rule is never hidden.
function advancedHasSettings(v: FormValues): boolean {
	const u = v.usage ?? {};
	return Boolean(
		u.model_path ||
		u.cost_path ||
		u.input_tokens_path ||
		u.output_tokens_path ||
		u.total_tokens_path ||
		u.disable_optimistic_parsing ||
		(v.streaming?.paths ?? []).length > 0 ||
		v.streaming?.detect_from_body ||
		v.streaming?.detect_from_accept_header,
	);
}

// InfoTip is the info icon whose hover text explains the control next to it.
function InfoTip({ label, text }: { label: string; text: string }) {
	return (
		<TooltipProvider>
			<Tooltip>
				<TooltipTrigger asChild>
					<span tabIndex={0} aria-label={`About ${label}`} className="inline-flex">
						<Info className="text-muted-foreground h-3 w-3" />
					</span>
				</TooltipTrigger>
				<TooltipContent className="max-w-sm">
					<p>{text}</p>
				</TooltipContent>
			</Tooltip>
		</TooltipProvider>
	);
}

// RouteHeaderMatchers edits a route's required request headers as name/value pairs. AWS JSON APIs
// send every operation to POST / and name it in X-Amz-Target, so this is how one route per
// operation is expressed; a route with no pairs matches on method and path alone.
function RouteHeaderMatchers({
	control,
	index,
	disabled,
}: {
	control: ReturnType<typeof useForm<FormValues>>["control"];
	index: number;
	disabled: boolean;
}) {
	const headers = useFieldArray({ control, name: `routes.${index}.headers` });
	return (
		<div className="space-y-2" data-testid={`generic-route-headers-${index}`}>
			{headers.fields.map((h, hi) => (
				<div key={h.id} className="grid gap-2 md:grid-cols-[1fr_1fr_auto]">
					<FormField
						control={control}
						name={`routes.${index}.headers.${hi}.name`}
						render={({ field }) => (
							<FormItem>
								<FormControl>
									<Input
										placeholder="Header name, e.g. X-Amz-Target"
										data-testid={`generic-route-header-name-input-${index}-${hi}`}
										disabled={disabled}
										{...field}
									/>
								</FormControl>
								<FormMessage />
							</FormItem>
						)}
					/>
					<FormField
						control={control}
						name={`routes.${index}.headers.${hi}.value`}
						render={({ field }) => (
							<FormItem>
								<FormControl>
									<Input
										placeholder="Value glob, e.g. Textract.Get*"
										data-testid={`generic-route-header-value-input-${index}-${hi}`}
										disabled={disabled}
										{...field}
									/>
								</FormControl>
								<FormMessage />
							</FormItem>
						)}
					/>
					<Button
						type="button"
						variant="ghost"
						size="icon"
						disabled={disabled}
						onClick={() => headers.remove(hi)}
						data-testid={`generic-route-header-remove-btn-${index}-${hi}`}
						aria-label={`Remove header matcher ${hi + 1}`}
					>
						<Trash2 className="h-4 w-4" />
					</Button>
				</div>
			))}
			<div className="flex items-center gap-1.5">
				<Button
					type="button"
					variant="outline"
					size="sm"
					disabled={disabled}
					onClick={() => headers.append({ name: "", value: "" })}
					data-testid={`generic-route-header-add-btn-${index}`}
				>
					Match a header
				</Button>
				<InfoTip
					label="Match a header"
					text="Only match calls that carry this header. Some APIs send every operation to the same path and name it in a header instead (AWS: POST / with X-Amz-Target: Textract.AnalyzeDocument), so a header lets each operation have its own route and price. The value may use * as a wildcard, e.g. Textract.Get* for every Get call. Add several headers to require all of them."
				/>
			</div>
		</div>
	);
}