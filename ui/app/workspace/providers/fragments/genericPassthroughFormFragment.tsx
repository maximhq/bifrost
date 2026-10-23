import { Button } from "@/components/ui/button";
import { Form, FormControl, FormDescription, FormField, FormItem, FormLabel, FormMessage } from "@/components/ui/form";
import { Input } from "@/components/ui/input";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Switch } from "@/components/ui/switch";
import { Textarea } from "@/components/ui/textarea";
import { getErrorMessage, setProviderFormDirtyState, useAppDispatch } from "@/lib/store";
import { useGetProviderKeysQuery, usePreviewGenericUsageMutation, useUpdateProviderMutation } from "@/lib/store/apis/providersApi";
import {
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
import { genericProviderFormSchema } from "@/lib/types/schemas";
import { importOpenAPI, parseOpenAPIText, type OpenAPIImport } from "@/lib/utils/openapiImport";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { zodResolver } from "@hookform/resolvers/zod";
import { Trash2 } from "lucide-react";
import { useEffect, useState } from "react";
import { useFieldArray, useForm } from "react-hook-form";
import { toast } from "sonner";
import { z } from "zod";
import { buildProviderUpdatePayload } from "../views/utils";

type FormValues = z.infer<typeof genericProviderFormSchema>;

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

// Route header matchers travel as a record in config and as name/value pairs in the form.
const headersToPairs = (headers?: Record<string, string>) => Object.entries(headers ?? {}).map(([name, value]) => ({ name, value }));
const pairsToHeaders = (pairs?: { name: string; value: string }[]) => {
	const out: Record<string, string> = {};
	for (const p of pairs ?? []) {
		if (p.name.trim() && p.value.trim()) out[p.name.trim()] = p.value.trim();
	}
	return Object.keys(out).length > 0 ? out : undefined;
};

function toFormValues(cfg: GenericProviderConfig | undefined): FormValues {
	return {
		auth: {
			type: cfg?.auth?.type ?? "bearer",
			header_name: cfg?.auth?.header_name ?? "",
			query_param: cfg?.auth?.query_param ?? "",
			value_prefix: cfg?.auth?.value_prefix ?? "",
			aws: { service: cfg?.auth?.aws?.service ?? "", region: cfg?.auth?.aws?.region ?? "" },
		},
		usage: { ...emptyUsage, ...(cfg?.usage ?? {}) },
		streaming: { paths: cfg?.streaming?.paths ?? [], detect_from_accept_header: cfg?.streaming?.detect_from_accept_header ?? false },
		routes: (cfg?.routes ?? []).map((r) => ({
			...r,
			headers: headersToPairs(r.headers),
			// An unset billable stays unset in the form: the switch shows the current method's default
			// until the operator touches it, so changing an imported GET to POST keeps the POST default
			// rather than freezing the GET one as an explicit override.
			billable: r.billable,
			usage: { ...emptyUsage, cost_per_request: undefined, cost_per_unit: undefined, ...(r.usage ?? {}) },
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
	if (usage.units_minimum !== undefined && usage.units_minimum > 0) out.units_minimum = usage.units_minimum;
	if (usage.units_path) out.units_path = usage.units_path;
	if (usage.units_source && usage.units_source !== "response") out.units_source = usage.units_source;
	if (usage.units_measure && usage.units_measure !== "value") out.units_measure = usage.units_measure;
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
			...(auth.type === "aws_sigv4" ? { aws: { service: auth.aws?.service ?? "", region: auth.aws?.region || undefined } } : {}),
		},
	};
	const usage = cleanUsage(values.usage);
	if (usage) cfg.usage = usage;
	const paths = (values.streaming?.paths ?? []).filter((p) => p.trim() !== "");
	if (paths.length > 0 || values.streaming?.detect_from_accept_header) {
		cfg.streaming = {
			paths: paths.length > 0 ? paths : undefined,
			detect_from_accept_header: values.streaming?.detect_from_accept_header || undefined,
		};
	}
	if (values.routes && values.routes.length > 0) {
		cfg.routes = values.routes.map((r) => ({
			method: r.method,
			path: r.path,
			headers: pairsToHeaders(r.headers),
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
	const [sample, setSample] = useState({
		method: "POST",
		path: "/",
		headers: "",
		request_content_type: "application/json",
		request_body: "",
		status_code: 200,
		response_content_type: "application/json",
		response_body: "",
	});
	const runPreview = async () => {
		try {
			const { headers: headerText, ...rest } = sample;
			const headers: Record<string, string> = {};
			for (const line of headerText.split("\n")) {
				const [name, ...valueParts] = line.split(":");
				if (name?.trim() && valueParts.length > 0) headers[name.trim()] = valueParts.join(":").trim();
			}
			const result = await previewUsage({
				provider: provider.name,
				body: { generic_config: toConfig(form.getValues()), ...rest, headers: Object.keys(headers).length > 0 ? headers : undefined },
			}).unwrap();
			setPreview(result);
		} catch (err) {
			setPreview(null);
			toast.error("Could not run the mapping", { description: getErrorMessage(err) });
		}
	};

	const form = useForm<FormValues>({
		resolver: zodResolver(genericProviderFormSchema),
		mode: "onChange",
		defaultValues: toFormValues(genericConfig),
	});
	const routes = useFieldArray({ control: form.control, name: "routes" });

	useEffect(() => {
		dispatch(setProviderFormDirtyState(form.formState.isDirty));
	}, [form.formState.isDirty]);

	useEffect(() => {
		form.reset(toFormValues(genericConfig));
		setStreamingPathsText((genericConfig?.streaming?.paths ?? []).join("\n"));
	}, [form, provider.name, genericConfig]);

	const authType = form.watch("auth.type");
	// The textarea keeps its own text so a newline the operator just typed survives until the next
	// glob is written; the form value only ever holds the non-empty lines.
	const [streamingPathsText, setStreamingPathsText] = useState(() => (genericConfig?.streaming?.paths ?? []).join("\n"));

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
					is_key_less: values.auth.type === "none" ? (provider.custom_provider_config?.is_key_less ?? false) : false,
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
			const existing = form.getValues("routes") ?? [];
			const seen = new Set(existing.map((r) => `${r.method} ${r.path}`));
			for (const route of result.routes) {
				if (seen.has(`${route.method} ${route.path}`)) continue;
				routes.append({
					method: route.method,
					path: route.path,
					headers: headersToPairs(route.headers),
					billable: undefined,
					stream: undefined,
					usage: { ...emptyUsage, cost_per_request: undefined },
				});
			}
			if (result.auth) {
				form.setValue("auth.type", result.auth.type, { shouldDirty: true });
				if (result.auth.header_name) form.setValue("auth.header_name", result.auth.header_name, { shouldDirty: true });
				if (result.auth.query_param) form.setValue("auth.query_param", result.auth.query_param, { shouldDirty: true });
			}
			if (result.routes.length > 0) form.setValue("strict_routes", true, { shouldDirty: true });
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

	const importFromFile = async (file: File | undefined) => {
		if (!file) return;
		await applyImport(await file.text());
	};

	const disabled = !hasUpdateProviderAccess;
	const textField = (
		name: Parameters<typeof form.register>[0],
		label: string,
		placeholder: string,
		testId: string,
		description?: string,
	) => (
		<FormField
			control={form.control}
			name={name}
			render={({ field }) => (
				<FormItem>
					<FormLabel>{label}</FormLabel>
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
	const switchField = (name: Parameters<typeof form.register>[0], label: string, description: string, testId: string) => (
		<FormField
			control={form.control}
			name={name}
			render={({ field }) => (
				<FormItem>
					<div className="bg-muted/50 flex items-center justify-between space-x-2 rounded-sm border p-3">
						<div className="space-y-0.5">
							<p className="text-sm font-medium">{label}</p>
							<p className="text-muted-foreground text-sm">{description}</p>
						</div>
						<Switch size="md" checked={!!field.value} onCheckedChange={field.onChange} disabled={disabled} data-testid={testId} />
					</div>
				</FormItem>
			)}
		/>
	);

	return (
		<Form {...form}>
			<form onSubmit={form.handleSubmit(onSubmit)} className="space-y-8 px-4 pb-6 md:px-6" data-testid="generic-passthrough-form">
				<p className="text-muted-foreground text-sm">
					Requests to <code className="font-mono text-xs">/passthrough/{provider.name}/&lt;path&gt;</code> are relayed to{" "}
					<code className="font-mono text-xs">{provider.network_config?.base_url || "the configured base URL"}</code> with the selected key
					injected. Every call is logged; calls on billable routes are counted and charged. Only POST is billable unless a route says
					otherwise.
				</p>

				{/* Auth */}
				<section className="space-y-4">
					<h3 className="text-sm font-semibold">Upstream authentication</h3>
					<FormField
						control={form.control}
						name="auth.type"
						render={({ field }) => (
							<FormItem>
								<FormLabel>Auth type</FormLabel>
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
					{authType === "header" && textField("auth.header_name", "Header name", "x-api-key", "generic-auth-header-name-input")}
					{authType === "header" && textField("auth.value_prefix", "Value prefix (optional)", "Token ", "generic-auth-value-prefix-input")}
					{authType === "query" && textField("auth.query_param", "Query parameter", "api_key", "generic-auth-query-param-input")}
					{authType === "aws_sigv4" &&
						textField(
							"auth.aws.service",
							"AWS service",
							"textract",
							"generic-aws-service-input",
							"SigV4 signing name, e.g. textract or translate",
						)}
					{authType === "aws_sigv4" &&
						textField(
							"auth.aws.region",
							"AWS region (optional)",
							"us-east-1",
							"generic-aws-region-input",
							"Falls back to the key's bedrock_key_config.region",
						)}
				</section>

				{/* Usage mapping */}
				<section className="space-y-4">
					<h3 className="text-sm font-semibold">Usage and cost mapping</h3>
					<p className="text-muted-foreground text-sm">
						gjson paths read from the request (model) and the JSON response (tokens, cost). Left empty, Bifrost reads the common shapes:
						request <code className="font-mono text-xs">model</code>, response{" "}
						<code className="font-mono text-xs">usage.prompt_tokens|input_tokens</code>,{" "}
						<code className="font-mono text-xs">usage.completion_tokens|output_tokens</code>,{" "}
						<code className="font-mono text-xs">usage.total_tokens</code>, <code className="font-mono text-xs">usage.cost</code>.
					</p>
					<div className="grid gap-4 md:grid-cols-2">
						{textField("usage.model_path", "Model path", "model", "generic-usage-model-path-input")}
						{textField("usage.cost_path", "Cost path", "usage.cost", "generic-usage-cost-path-input")}
						{textField("usage.input_tokens_path", "Input tokens path", "usage.prompt_tokens", "generic-usage-input-tokens-path-input")}
						{textField(
							"usage.output_tokens_path",
							"Output tokens path",
							"usage.completion_tokens",
							"generic-usage-output-tokens-path-input",
						)}
						{textField("usage.total_tokens_path", "Total tokens path", "usage.total_tokens", "generic-usage-total-tokens-path-input")}
						<FormField
							control={form.control}
							name="usage.cost_per_request"
							render={({ field }) => (
								<FormItem>
									<FormLabel>Cost per request (USD)</FormLabel>
									<FormControl>
										<Input
											type="number"
											step="any"
											min={0}
											placeholder="0"
											data-testid="generic-usage-cost-per-request-input"
											disabled={disabled}
											value={field.value ?? 0}
											onChange={(e) => field.onChange(e.target.value === "" ? 0 : Number(e.target.value))}
										/>
									</FormControl>
									<FormDescription>Flat fee added to every billable call, on top of any extracted cost</FormDescription>
									<FormMessage />
								</FormItem>
							)}
						/>
					</div>
					<div className="space-y-3 rounded-sm border p-3" data-testid="generic-units-section">
						<div>
							<p className="text-sm font-medium">Per-unit pricing</p>
							<p className="text-muted-foreground text-sm">
								For APIs that bill by quantity: pages the response reports, characters of the text you sent, or the size of the request
								body. Cost per call becomes units × rate, plus the flat fee and any extracted cost.
							</p>
						</div>
						<div className="grid gap-4 md:grid-cols-2">
							<FormField
								control={form.control}
								name="usage.units_source"
								render={({ field }) => (
									<FormItem>
										<FormLabel>Read the quantity from</FormLabel>
										<Select value={field.value ?? "response"} onValueChange={field.onChange} disabled={disabled}>
											<FormControl>
												<SelectTrigger data-testid="generic-units-source-select">
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
										<FormLabel>Measure</FormLabel>
										<Select value={field.value ?? "value"} onValueChange={field.onChange} disabled={disabled}>
											<FormControl>
												<SelectTrigger data-testid="generic-units-measure-select">
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
							{!genericUnitsMeasureReadsWholeBody(form.watch("usage.units_measure")) &&
								textField(
									"usage.units_path",
									"Units path",
									"DocumentMetadata.Pages",
									"generic-units-path-input",
									"e.g. DocumentMetadata.Pages (Textract), usage.credits (Tavily), Text (Translate)",
								)}
							<FormField
								control={form.control}
								name="usage.cost_per_unit"
								render={({ field }) => (
									<FormItem>
										<FormLabel>Cost per unit (USD)</FormLabel>
										<FormControl>
											<Input
												type="number"
												step="any"
												min={0}
												placeholder="0"
												data-testid="generic-units-cost-per-unit-input"
												disabled={disabled}
												value={field.value ?? 0}
												onChange={(e) => field.onChange(e.target.value === "" ? 0 : Number(e.target.value))}
											/>
										</FormControl>
										<FormDescription>e.g. 0.0015 per Textract page, 0.000015 per Translate character</FormDescription>
										<FormMessage />
									</FormItem>
								)}
							/>
							<FormField
								control={form.control}
								name="usage.units_block_size"
								render={({ field }) => (
									<FormItem>
										<FormLabel>Block size (optional)</FormLabel>
										<FormControl>
											<Input
												type="number"
												step="any"
												min={0}
												placeholder="exact"
												data-testid="generic-units-block-size-input"
												disabled={disabled}
												value={field.value ?? ""}
												onChange={(e) => field.onChange(e.target.value === "" ? undefined : Number(e.target.value))}
											/>
										</FormControl>
										<FormDescription>
											Bill per started block of this many units, e.g. 100 for APIs priced per 100 characters
										</FormDescription>
										<FormMessage />
									</FormItem>
								)}
							/>
							<FormField
								control={form.control}
								name="usage.units_minimum"
								render={({ field }) => (
									<FormItem>
										<FormLabel>Minimum units (optional)</FormLabel>
										<FormControl>
											<Input
												type="number"
												step="any"
												min={0}
												placeholder="0"
												data-testid="generic-units-minimum-input"
												disabled={disabled}
												value={field.value ?? ""}
												onChange={(e) => field.onChange(e.target.value === "" ? undefined : Number(e.target.value))}
											/>
										</FormControl>
										<FormDescription>Least number of billed units once a quantity was read</FormDescription>
										<FormMessage />
									</FormItem>
								)}
							/>
						</div>
					</div>
					{switchField(
						"usage.disable_optimistic_parsing",
						"Only read the configured paths",
						"Turn off the common-shape fallbacks so nothing is inferred from a response you did not map",
						"generic-usage-disable-optimistic-switch",
					)}
				</section>

				{/* Streaming */}
				<section className="space-y-4">
					<h3 className="text-sm font-semibold">Streaming</h3>
					<FormField
						control={form.control}
						name="streaming.paths"
						render={({ field }) => (
							<FormItem>
								<FormLabel>Streaming paths</FormLabel>
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
								<FormDescription>
									One rooted glob per line; a trailing /** matches everything below. These routes are always served as streams.
								</FormDescription>
								<FormMessage />
							</FormItem>
						)}
					/>
					{switchField(
						"streaming.detect_from_accept_header",
						"Stream on Accept: text/event-stream",
						"Treat a request whose Accept header asks for SSE as a streaming request",
						"generic-streaming-accept-switch",
					)}
				</section>

				{/* Routes */}
				<section className="space-y-4">
					<div className="flex items-center justify-between">
						<div>
							<h3 className="text-sm font-semibold">Routes</h3>
							<p className="text-muted-foreground text-sm">
								Per-operation overrides; the first matching route wins. Non-billable routes are relayed but not counted.
							</p>
						</div>
						<Button
							type="button"
							variant="outline"
							size="sm"
							disabled={disabled}
							data-testid="generic-route-add-btn"
							onClick={() =>
								routes.append({
									method: "POST",
									path: "/",
									headers: [],
									billable: undefined,
									stream: undefined,
									usage: { ...emptyUsage, cost_per_request: undefined },
								})
							}
						>
							Add route
						</Button>
					</div>
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
											<Select value={streamChoice(field.value)} onValueChange={(v) => field.onChange(streamFlag(v))} disabled={disabled}>
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
										</FormItem>
									)}
								/>
								<Button
									type="button"
									variant="ghost"
									size="icon"
									disabled={disabled}
									data-testid={`generic-route-remove-btn-${index}`}
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
												<p className="text-sm font-medium">Billable</p>
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
											<FormControl>
												<Input
													type="number"
													step="any"
													min={0}
													placeholder="Cost per request override (USD); empty inherits, 0 waives the fee"
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
						</div>
					))}
				</section>

				{/* Import from OpenAPI */}
				<section className="space-y-4">
					<h3 className="text-sm font-semibold">Import from an OpenAPI document</h3>
					<p className="text-muted-foreground text-sm">
						Paste, upload or fetch the API&apos;s OpenAPI or Swagger document. Its operations become routes, its security scheme becomes the
						auth type, and strict routes is switched on so only those operations are relayed. The document itself is not stored.
					</p>
					<div className="grid gap-3 md:grid-cols-[1fr_auto]">
						<Input
							placeholder="https://api.example.com/openapi.json"
							data-testid="generic-openapi-import-url-input"
							disabled={disabled}
							value={importUrl}
							onChange={(e) => setImportUrl(e.target.value)}
						/>
						<Button
							type="button"
							variant="outline"
							size="sm"
							isLoading={importing}
							disabled={disabled || !importUrl.trim()}
							data-testid="generic-openapi-import-url-btn"
							onClick={importFromUrl}
						>
							Fetch and import
						</Button>
					</div>
					<Textarea
						placeholder="openapi: 3.0.0 …"
						data-testid="generic-openapi-import-textarea"
						disabled={disabled}
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
							disabled={disabled || !importText.trim()}
							data-testid="generic-openapi-import-btn"
							onClick={() => applyImport(importText)}
						>
							Import pasted document
						</Button>
						<label className="text-muted-foreground cursor-pointer text-sm underline">
							Upload a file
							<input
								type="file"
								accept=".json,.yaml,.yml,application/json"
								className="hidden"
								data-testid="generic-openapi-import-file"
								disabled={disabled}
								onChange={(e) => importFromFile(e.target.files?.[0])}
							/>
						</label>
						{imported && (
							<p className="text-muted-foreground text-xs" data-testid="generic-openapi-import-summary">
								{imported.title || "Document"} {imported.version ? `v${imported.version}` : ""}: {imported.routes.length} operations
								{imported.baseUrl ? `, server ${imported.baseUrl} (set it on the Network tab)` : ""}
								{imported.warnings.length > 0 ? `. ${imported.warnings.join(". ")}` : ""}
							</p>
						)}
					</div>
					{switchField(
						"strict_routes",
						"Strict routes",
						"Refuse any request that matches no route above with 404, so the routes list is an allowlist",
						"generic-strict-routes-switch",
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
						<Input
							placeholder="/search"
							value={sample.path}
							onChange={(e) => setSample({ ...sample, path: e.target.value })}
							disabled={disabled}
							data-testid="generic-preview-path-input"
						/>
						<Input
							type="number"
							min={100}
							max={599}
							value={sample.status_code}
							onChange={(e) => setSample({ ...sample, status_code: Number(e.target.value) || 200 })}
							disabled={disabled}
							data-testid="generic-preview-status-input"
							aria-label="Response status code"
						/>
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
							disabled={disabled}
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
									{preview.counted ? "yes, one request" : preview.billable ? "no (status not 2xx)" : "no (route not billable)"}
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
										? `${preview.tokens.prompt_tokens} in / ${preview.tokens.completion_tokens} out / ${preview.tokens.total_tokens} total`
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
					>
						<Trash2 className="h-4 w-4" />
					</Button>
				</div>
			))}
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
		</div>
	);
}