import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from "@/components/ui/collapsible";
import { Form, FormControl, FormDescription, FormField, FormItem, FormLabel, FormMessage } from "@/components/ui/form";
import { HeadersTable } from "@/components/ui/headersTable";
import { Input } from "@/components/ui/input";
import { RequestHeadersTextarea } from "@/components/ui/requestHeadersTextarea";
import { SecretVarInput } from "@/components/ui/secretVarInput";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Switch } from "@/components/ui/switch";
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip";
import { otelFormSchema, type OtelFormSchema, type SecretVar } from "@/lib/types/schemas";
import { emptySecretVar, toSecretVarFormValue, toSecretVarMapFormValue } from "@/lib/utils/secretVarForm";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { zodResolver } from "@hookform/resolvers/zod";
import { ChevronDown, Plus, Trash2 } from "lucide-react";
import { useEffect, useState } from "react";
import { useFieldArray, useForm, type Control, type Resolver, type UseFormReturn } from "react-hook-form";

// ProfileForm is a single profile's form shape, derived from the form schema.
type ProfileForm = OtelFormSchema["profiles"][number];

// StoredOtelProfile is one profile as persisted/returned by the API (headers are strings,
// SecretVar fields may be plain strings or full objects).
interface StoredOtelProfile {
	enabled?: boolean;
	service_name?: string;
	collector_url?: string | SecretVar;
	headers?: Record<string, string | SecretVar>;
	trace_type?: "genai_extension" | "vercel" | "open_inference";
	protocol?: "http" | "grpc";
	tls_ca_cert?: string;
	insecure?: boolean;
	metrics_enabled?: boolean;
	metrics_endpoint?: string | SecretVar;
	metrics_push_interval?: number;
	request_headers?: string[];
	disable_content_logging?: boolean;
	group_traces_by_session?: boolean;
	disable_root_span_content?: boolean;
}

// StoredOtelConfig is either the canonical { profiles: [...] } wrapper or a legacy single
// profile object (no "profiles" key).
type StoredOtelConfig = (StoredOtelProfile & { profiles?: StoredOtelProfile[] }) | undefined;

interface OtelFormFragmentProps {
	currentConfig?: {
		enabled?: boolean;
		config?: StoredOtelConfig;
	};
	onSave: (config: OtelFormSchema) => Promise<void>;
	onDelete?: () => void;
	isDeleting?: boolean;
	isLoading?: boolean;
}

const traceTypeOptions: {
	value: string;
	label: string;
	disabled?: boolean;
	disabledReason?: string;
}[] = [
	{ value: "genai_extension", label: "OTel GenAI Extension (Recommended)" },
	{
		value: "vercel",
		label: "Vercel AI SDK",
		disabled: true,
		disabledReason: "Coming soon",
	},
	{
		value: "open_inference",
		label: "Arize OpenInference",
		disabled: true,
		disabledReason: "Coming soon",
	},
];
const protocolOptions: {
	value: string;
	label: string;
	disabled?: boolean;
	disabledReason?: string;
}[] = [
	{ value: "http", label: "HTTP" },
	{ value: "grpc", label: "GRPC" },
];

// emptyProfile returns a fresh profile with the same defaults a newly created collector uses.
const emptyProfile = (): ProfileForm => ({
	enabled: true,
	service_name: "bifrost",
	collector_url: emptySecretVar(),
	headers: {},
	trace_type: "genai_extension",
	protocol: "http",
	tls_ca_cert: "",
	insecure: true,
	metrics_enabled: false,
	metrics_endpoint: emptySecretVar(),
	metrics_push_interval: 15,
	request_headers: [],
	disable_content_logging: false,
	group_traces_by_session: false,
	disable_root_span_content: false,
});

// toProfileForm normalizes a stored profile into the SecretVar-based form representation.
const toProfileForm = (p?: StoredOtelProfile): ProfileForm => ({
	enabled: p?.enabled ?? true,
	service_name: p?.service_name ?? "bifrost",
	collector_url: toSecretVarFormValue(p?.collector_url),
	headers: toSecretVarMapFormValue(p?.headers),
	trace_type: p?.trace_type ?? "genai_extension",
	protocol: p?.protocol ?? "http",
	tls_ca_cert: p?.tls_ca_cert ?? "",
	insecure: p?.insecure ?? true,
	metrics_enabled: p?.metrics_enabled ?? false,
	metrics_endpoint: toSecretVarFormValue(p?.metrics_endpoint),
	metrics_push_interval: p?.metrics_push_interval ?? 15,
	request_headers: p?.request_headers ?? [],
	disable_content_logging: p?.disable_content_logging ?? false,
	group_traces_by_session: p?.group_traces_by_session ?? false,
	disable_root_span_content: p?.disable_root_span_content ?? false,
});

// buildDefaults handles both stored shapes: the { profiles: [...] } wrapper and the legacy
// single-object config. Always yields at least one profile.
const buildDefaults = (initial?: OtelFormFragmentProps["currentConfig"]): OtelFormSchema => {
	const cfg = initial?.config;
	let profiles: ProfileForm[];
	if (cfg && Array.isArray(cfg.profiles)) {
		profiles = cfg.profiles.map(toProfileForm);
	} else if (cfg && (cfg.collector_url || cfg.service_name || cfg.protocol || cfg.trace_type)) {
		// Legacy single-object config.
		profiles = [toProfileForm(cfg)];
	} else {
		profiles = [];
	}
	if (profiles.length === 0) profiles = [emptyProfile()];
	return { enabled: initial?.enabled ?? true, profiles };
};

export function OtelFormFragment({
	currentConfig: initialConfig,
	onSave,
	onDelete,
	isDeleting = false,
	isLoading = false,
}: OtelFormFragmentProps) {
	const hasOtelAccess = useRbac(RbacResource.Observability, RbacOperation.Update);
	const [isSaving, setIsSaving] = useState(false);
	const [profileOpenState, setProfileOpenState] = useState<Record<number, boolean>>({});
	const form = useForm<OtelFormSchema, unknown, OtelFormSchema>({
		resolver: zodResolver(otelFormSchema) as Resolver<OtelFormSchema, unknown, OtelFormSchema>,
		mode: "onChange",
		reValidateMode: "onChange",
		defaultValues: buildDefaults(initialConfig),
	});

	const { fields, append, remove } = useFieldArray({
		control: form.control,
		name: "profiles",
	});

	const onSubmit = (data: OtelFormSchema) => {
		setIsSaving(true);
		onSave(data).finally(() => setIsSaving(false));
	};

	const handleProfileOpenChange = (index: number, open: boolean) => {
		setProfileOpenState((prev) => ({ ...prev, [index]: open }));
	};

	const handleRemoveProfile = (index: number) => {
		remove(index);
		setProfileOpenState((prev) => {
			const next: Record<number, boolean> = {};
			for (const [key, value] of Object.entries(prev)) {
				const profileIndex = Number(key);
				if (profileIndex < index) {
					next[profileIndex] = value;
				} else if (profileIndex > index) {
					next[profileIndex - 1] = value;
				}
			}
			return next;
		});
	};

	useEffect(() => {
		form.reset(buildDefaults(initialConfig));
	}, [form, initialConfig]);

	return (
		<Form {...form}>
			<form onSubmit={form.handleSubmit(onSubmit)} className="space-y-6">
				<div className="flex flex-col gap-3">
					{fields.map((field, index) => (
						<OtelProfileSection
							key={field.id}
							form={form}
							control={form.control}
							index={index}
							hasOtelAccess={hasOtelAccess}
							canRemove={fields.length > 1}
							open={profileOpenState[index] ?? true}
							onOpenChange={(open) => handleProfileOpenChange(index, open)}
							onRemove={() => handleRemoveProfile(index)}
						/>
					))}
				</div>

				<Button
					type="button"
					variant="outline"
					size="sm"
					onClick={() => append(emptyProfile())}
					disabled={!hasOtelAccess}
					data-testid="otel-add-profile-btn"
				>
					<Plus className="size-4" /> Add Profile
				</Button>

				{/* Form Actions */}
				<div className="flex w-full flex-row items-center border-t pt-4">
					<FormField
						control={form.control}
						name="enabled"
						render={({ field }) => (
							<FormItem className="flex items-center gap-2 py-2">
								<FormLabel className="text-muted-foreground text-sm font-medium">Enabled</FormLabel>
								<FormControl>
									<Switch
										checked={field.value}
										onCheckedChange={field.onChange}
										disabled={!hasOtelAccess}
										data-testid="otel-connector-enable-toggle"
									/>
								</FormControl>
							</FormItem>
						)}
					/>
					<div className="ml-auto flex justify-end space-x-2 py-2">
						{onDelete && (
							<Button
								type="button"
								variant="outline"
								onClick={onDelete}
								disabled={isDeleting || !hasOtelAccess}
								data-testid="otel-connector-delete-btn"
								title="Delete connector"
								aria-label="Delete connector"
							>
								<Trash2 className="size-4" />
							</Button>
						)}
						<Button
							type="button"
							variant="outline"
							onClick={() => {
								form.reset(buildDefaults(initialConfig));
							}}
							disabled={!hasOtelAccess || isLoading || !form.formState.isDirty}
						>
							Reset
						</Button>
						<TooltipProvider>
							<Tooltip>
								<TooltipTrigger asChild>
									<Button type="submit" disabled={!hasOtelAccess || !form.formState.isDirty} isLoading={isSaving}>
										Save OTEL Configuration
									</Button>
								</TooltipTrigger>
								{!form.formState.isDirty && (
									<TooltipContent>
										<p>
											{!form.formState.isDirty && !form.formState.isValid
												? "No changes made and validation errors present"
												: !form.formState.isDirty
													? "No changes made"
													: "Please fix validation errors"}
										</p>
									</TooltipContent>
								)}
							</Tooltip>
						</TooltipProvider>
					</div>
				</div>
			</form>
		</Form>
	);
}

interface OtelProfileSectionProps {
	form: UseFormReturn<OtelFormSchema, unknown, OtelFormSchema>;
	control: Control<OtelFormSchema, unknown, OtelFormSchema>;
	index: number;
	hasOtelAccess: boolean;
	canRemove: boolean;
	open: boolean;
	onOpenChange: (open: boolean) => void;
	onRemove: () => void;
}

// OtelProfileSection renders one collapsible profile. The header stays visible when collapsed
// and surfaces the profile identity plus its enable toggle and remove control.
function OtelProfileSection({ form, control, index, hasOtelAccess, canRemove, open, onOpenChange, onRemove }: OtelProfileSectionProps) {
	const base = `profiles.${index}` as const;
	const protocol = form.watch(`${base}.protocol`);
	const metricsEnabled = form.watch(`${base}.metrics_enabled`);
	const insecure = form.watch(`${base}.insecure`);
	const enabled = form.watch(`${base}.enabled`);
	const serviceName = form.watch(`${base}.service_name`);
	const collectorUrl = form.watch(`${base}.collector_url`);

	// Surface whether this profile currently has any validation errors so the user can find it
	// without expanding every collapsed section.
	const hasError = Boolean(form.formState.errors?.profiles?.[index]);

	const collectorPreview =
		typeof collectorUrl === "string"
			? collectorUrl
			: collectorUrl?.type === "env" || collectorUrl?.type === "vault"
				? collectorUrl.ref
				: collectorUrl?.value;

	return (
		<Collapsible open={open} onOpenChange={onOpenChange} className="rounded-sm border" data-testid={`otel-profile-${index}`}>
			<div className="flex flex-row items-center gap-2 px-4 py-3">
				<CollapsibleTrigger asChild>
					<button type="button" className="flex min-w-0 flex-1 items-center gap-2 text-left">
						<ChevronDown className={`size-4 shrink-0 transition-transform ${open ? "" : "-rotate-90"}`} />
						<div className="flex min-w-0 flex-col">
							<span className="flex items-center gap-2 truncate text-sm font-medium">
								{serviceName || `Profile ${index + 1}`}
								{!enabled && <Badge variant="secondary">Disabled</Badge>}
								{hasError && <Badge variant="destructive">Error</Badge>}
							</span>
							{collectorPreview && <span className="text-muted-foreground truncate text-xs">{collectorPreview}</span>}
						</div>
					</button>
				</CollapsibleTrigger>

				<FormField
					control={control}
					name={`${base}.enabled`}
					render={({ field }) => (
						<FormItem className="flex items-center">
							<FormControl>
								<Switch
									checked={field.value}
									onCheckedChange={field.onChange}
									disabled={!hasOtelAccess}
									data-testid={`otel-profile-${index}-enable-toggle`}
									aria-label="Enable profile"
								/>
							</FormControl>
						</FormItem>
					)}
				/>

				{canRemove && (
					<Button
						type="button"
						variant="ghost"
						size="icon"
						onClick={onRemove}
						disabled={!hasOtelAccess}
						data-testid={`otel-profile-${index}-remove-btn`}
						title="Remove profile"
						aria-label="Remove profile"
					>
						<Trash2 className="size-4" />
					</Button>
				)}
			</div>

			<CollapsibleContent className="border-t px-4 py-4">
				<div className="flex flex-col gap-4">
					<FormField
						control={control}
						name={`${base}.service_name`}
						render={({ field }) => (
							<FormItem className="w-full">
								<FormLabel>Service Name</FormLabel>
								<FormDescription>If kept empty, the service name will be set to "bifrost"</FormDescription>
								<FormControl>
									<Input placeholder="bifrost" disabled={!hasOtelAccess} {...field} />
								</FormControl>
								<FormMessage />
							</FormItem>
						)}
					/>
					<FormField
						control={control}
						name={`${base}.collector_url`}
						render={({ field }) => (
							<FormItem className="w-full">
								<FormLabel>OTLP Collector URL</FormLabel>
								<div className="text-muted-foreground text-xs">
									<code>{protocol === "http" ? "http(s)://<host>:<port>/v1/traces" : "<host>:<port>"}</code>
								</div>
								<FormControl>
									<SecretVarInput
										placeholder={
											protocol === "http"
												? "https://otel-collector.example.com:4318/v1/traces or env.OTEL_COLLECTOR_URL"
												: "otel-collector.example.com:4317 or env.OTEL_COLLECTOR_URL"
										}
										disabled={!hasOtelAccess}
										{...field}
									/>
								</FormControl>
								<FormMessage />
							</FormItem>
						)}
					/>
					<FormField
						control={control}
						name={`${base}.headers`}
						render={({ field }) => (
							<FormItem className="w-full">
								<FormControl>
									<HeadersTable value={field.value || {}} onChange={field.onChange} disabled={!hasOtelAccess} useSecretVarInput />
								</FormControl>
								<FormMessage />
							</FormItem>
						)}
					/>
					<FormField
						control={control}
						name={`${base}.request_headers`}
						render={({ field }) => (
							<FormItem className="w-full">
								<FormLabel>
									Request Headers <span className="text-muted-foreground font-normal">(Optional)</span>
								</FormLabel>
								<FormDescription>
									Comma-separated list of request headers to capture and emit as span attributes. Supports exact names and wildcard patterns
									(e.g. <code className="text-xs">x-custom-*</code> captures all headers with that prefix,{" "}
									<code className="text-xs">*</code> captures all headers; note that <code className="text-xs">*</code> will capture
									sensitive headers like Authorization).
								</FormDescription>
								<FormControl>
									<RequestHeadersTextarea
										className="h-24"
										placeholder="X-Tenant-ID, X-Request-Source, x-custom-*"
										disabled={!hasOtelAccess}
										value={field.value ?? []}
										onChange={field.onChange}
										data-testid={`request-headers-textarea-${index}`}
									/>
								</FormControl>
								<FormMessage />
							</FormItem>
						)}
					/>
					<FormField
						control={control}
						name={`${base}.disable_content_logging`}
						render={({ field }) => (
							<FormItem className="flex flex-row items-center justify-between">
								<div className="space-y-0.5">
									<FormLabel className="text-base">Disable Content Logging</FormLabel>
									<FormDescription>
										When enabled, message content (input/output messages, tool definitions, and tool call arguments/results) is dropped from
										exported spans. Only metadata such as model, tokens, and latency is sent to the collector.
									</FormDescription>
								</div>
								<FormControl>
									<Switch
										checked={field.value}
										onCheckedChange={field.onChange}
										disabled={!hasOtelAccess}
										data-testid={`otel-profile-${index}-disable-content-logging-toggle`}
									/>
								</FormControl>
							</FormItem>
						)}
					/>
					<FormField
						control={control}
						name={`${base}.group_traces_by_session`}
						render={({ field }) => (
							<FormItem className="flex flex-row items-center justify-between">
								<div className="space-y-0.5">
									<FormLabel className="text-base">Group Traces by Session</FormLabel>
									<FormDescription>
										When enabled, requests sharing the same x-bf-session-id header are grouped into a single trace, each request appearing
										as a top-level sibling span. A request carrying an inbound W3C traceparent stays on its own distributed trace and is
										unaffected.
									</FormDescription>
								</div>
								<FormControl>
									<Switch
										checked={field.value}
										onCheckedChange={field.onChange}
										disabled={!hasOtelAccess}
										data-testid={`otel-profile-${index}-group-traces-by-session-toggle`}
									/>
								</FormControl>
							</FormItem>
						)}
					/>
					<FormField
						control={control}
						name={`${base}.disable_root_span_content`}
						render={({ field }) => (
							<FormItem className="flex flex-row items-center justify-between">
								<div className="space-y-0.5">
									<FormLabel className="text-base">Disable Root Span Content</FormLabel>
									<FormDescription>
										When enabled, input/output message content is dropped from the root span only; the underlying generation (llm.call) span
										keeps the full content.
									</FormDescription>
								</div>
								<FormControl>
									<Switch
										checked={field.value}
										onCheckedChange={field.onChange}
										disabled={!hasOtelAccess}
										data-testid={`otel-profile-${index}-disable-root-span-content-toggle`}
									/>
								</FormControl>
							</FormItem>
						)}
					/>
					<div className="flex flex-row gap-4">
						<FormField
							control={control}
							name={`${base}.trace_type`}
							render={({ field }) => (
								<FormItem className="flex-1">
									<FormLabel>Format</FormLabel>
									<Select onValueChange={field.onChange} value={field.value ?? traceTypeOptions[0].value} disabled={!hasOtelAccess}>
										<FormControl>
											<SelectTrigger className="w-full">
												<SelectValue placeholder="Select trace type" />
											</SelectTrigger>
										</FormControl>
										<SelectContent>
											{traceTypeOptions.map((option) => (
												<SelectItem
													key={option.value}
													value={option.value}
													disabled={option.disabled}
													disabledReason={option.disabledReason}
												>
													{option.label}
												</SelectItem>
											))}
										</SelectContent>
									</Select>
									<FormMessage />
								</FormItem>
							)}
						/>

						<FormField
							control={control}
							name={`${base}.protocol`}
							render={({ field }) => (
								<FormItem className="flex-1">
									<FormLabel>Protocol</FormLabel>
									<Select onValueChange={field.onChange} value={field.value} disabled={!hasOtelAccess}>
										<FormControl>
											<SelectTrigger className="w-full">
												<SelectValue placeholder="Select protocol" />
											</SelectTrigger>
										</FormControl>
										<SelectContent>
											{protocolOptions.map((option) => (
												<SelectItem
													key={option.value}
													value={option.value}
													disabled={option.disabled}
													disabledReason={option.disabledReason}
												>
													{option.label}
												</SelectItem>
											))}
										</SelectContent>
									</Select>
									<FormMessage />
								</FormItem>
							)}
						/>
					</div>

					{/* TLS Configuration */}
					<div className="flex flex-col gap-4">
						<FormField
							control={control}
							name={`${base}.insecure`}
							render={({ field }) => (
								<FormItem className="flex flex-row items-center gap-2">
									<div className="flex w-full flex-row items-center gap-2">
										<div className="flex flex-col gap-1">
											<FormLabel>Insecure (Skip TLS)</FormLabel>
											<FormDescription>
												Skip TLS verification. Disable this to use TLS with system root CAs or a custom CA certificate.
											</FormDescription>
										</div>
										<div className="ml-auto">
											<Switch
												checked={field.value}
												onCheckedChange={(checked) => {
													field.onChange(checked);
													if (checked) {
														form.setValue(`${base}.tls_ca_cert`, "");
													}
												}}
												disabled={!hasOtelAccess}
											/>
										</div>
									</div>
								</FormItem>
							)}
						/>
						{!insecure && (
							<FormField
								control={control}
								name={`${base}.tls_ca_cert`}
								render={({ field }) => (
									<FormItem className="w-full">
										<FormLabel>TLS CA Certificate Path</FormLabel>
										<FormDescription>
											File path to the CA certificate on the Bifrost server. Leave empty to use system root CAs.
										</FormDescription>
										<FormControl>
											<Input placeholder="/path/to/ca.crt" disabled={!hasOtelAccess} {...field} />
										</FormControl>
										<FormMessage />
									</FormItem>
								)}
							/>
						)}
					</div>

					{/* Metrics Push Configuration */}
					<div className="flex flex-col gap-4 border-t pt-4">
						<FormField
							control={control}
							name={`${base}.metrics_enabled`}
							render={({ field }) => (
								<FormItem className="flex flex-row items-center gap-2">
									<div className="flex w-full flex-row items-center gap-2">
										<div className="flex flex-col gap-1">
											<h3 className="flex flex-row items-center gap-2 text-sm font-medium">
												Enable Metrics Export <Badge variant="secondary">BETA</Badge>
											</h3>
											<p className="text-muted-foreground text-xs">
												Push metrics to an OTEL Collector for proper aggregation in cluster deployments
											</p>
										</div>
										<div className="ml-auto">
											<Switch
												// First profile keeps the legacy testid for existing e2e coverage.
												data-testid={index === 0 ? "otel-metrics-export-toggle" : `otel-profile-${index}-metrics-export-toggle`}
												checked={field.value}
												onCheckedChange={field.onChange}
												disabled={!hasOtelAccess}
											/>
										</div>
									</div>
								</FormItem>
							)}
						/>

						{metricsEnabled && (
							<div className="border-muted flex flex-col gap-4">
								<FormField
									control={control}
									name={`${base}.metrics_endpoint`}
									render={({ field }) => (
										<FormItem className="w-full">
											<FormLabel>Metrics Endpoint</FormLabel>
											<div className="text-muted-foreground text-xs">
												<code>{protocol === "http" ? "http(s)://<host>:<port>/v1/metrics" : "<host>:<port>"}</code>
											</div>
											<FormControl>
												<SecretVarInput
													placeholder={
														protocol === "http"
															? "https://otel-collector:4318/v1/metrics or env.OTEL_METRICS_ENDPOINT"
															: "otel-collector:4317 or env.OTEL_METRICS_ENDPOINT"
													}
													disabled={!hasOtelAccess}
													{...field}
												/>
											</FormControl>
											<FormMessage />
										</FormItem>
									)}
								/>

								<FormField
									control={control}
									name={`${base}.metrics_push_interval`}
									render={({ field }) => (
										<FormItem className="w-full max-w-xs">
											<FormLabel>Push Interval (seconds)</FormLabel>
											<FormControl>
												<Input
													type="number"
													min={1}
													max={300}
													disabled={!hasOtelAccess}
													{...field}
													value={field.value ?? ""}
													onChange={(e) => field.onChange(e.target.value === "" ? null : Number(e.target.value))}
												/>
											</FormControl>
											<FormDescription>How often to push metrics (1-300 seconds)</FormDescription>
											<FormMessage />
										</FormItem>
									)}
								/>
							</div>
						)}
					</div>
				</div>
			</CollapsibleContent>
		</Collapsible>
	);
}