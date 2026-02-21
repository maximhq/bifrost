"use client";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Form, FormControl, FormDescription, FormField, FormItem, FormLabel, FormMessage } from "@/components/ui/form";
import { HeadersTable } from "@/components/ui/headersTable";
import { Input } from "@/components/ui/input";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Switch } from "@/components/ui/switch";
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip";
import { otelFormSchema, type OtelFormSchema } from "@/lib/types/schemas";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { zodResolver } from "@hookform/resolvers/zod";
import { Trash2 } from "lucide-react";
import { useEffect, useState } from "react";
import { useForm, type Resolver } from "react-hook-form";

interface OtelFormFragmentProps {
	currentConfig?: {
		enabled?: boolean;
		service_name?: string;
		collector_url?: string;
		headers?: Record<string, string>;
		trace_type?: "otel" | "genai_extension" | "vercel" | "arize_otel";
		protocol?: "http" | "grpc";
		// TLS configuration
		tls_ca_cert?: string;
		insecure?: boolean;
		// Metrics push configuration
		metrics_enabled?: boolean;
		metrics_endpoint?: string;
		metrics_push_interval?: number;
	};
	onSave: (config: OtelFormSchema) => Promise<void>;
	onDelete?: () => void;
	isDeleting?: boolean;
	isLoading?: boolean;
	enableToggle?: { enabled: boolean; onToggle: () => void; disabled?: boolean };
}

export function OtelFormFragment({ currentConfig: initialConfig, onSave, onDelete, isDeleting = false, isLoading = false, enableToggle }: OtelFormFragmentProps) {
	const hasOtelAccess = useRbac(RbacResource.Observability, RbacOperation.Update);
	const [isSaving, setIsSaving] = useState(false);
	const form = useForm<OtelFormSchema, any, OtelFormSchema>({
		resolver: zodResolver(otelFormSchema) as Resolver<OtelFormSchema, any, OtelFormSchema>,
		mode: "onChange",
		reValidateMode: "onChange",
		defaultValues: {
			enabled: initialConfig?.enabled ?? false,
			otel_config: {
				service_name: initialConfig?.service_name ?? "bifrost",
				collector_url: initialConfig?.collector_url ?? "",
				headers: initialConfig?.headers ?? {},
				trace_type: initialConfig?.trace_type ?? "otel",
				protocol: initialConfig?.protocol ?? "http",
				tls_ca_cert: initialConfig?.tls_ca_cert ?? "",
				insecure: initialConfig?.insecure ?? true,
				metrics_enabled: initialConfig?.metrics_enabled ?? false,
				metrics_endpoint: initialConfig?.metrics_endpoint ?? "",
				metrics_push_interval: initialConfig?.metrics_push_interval ?? 15,
			},
		},
	});

	const onSubmit = (data: OtelFormSchema) => {
		setIsSaving(true);
		onSave(data).finally(() => setIsSaving(false));
	};

	// Re-run validation on collector_url when protocol changes so cross-field
	// refinement in the schema is applied immediately
	const protocol = form.watch("otel_config.protocol");
	const metricsEnabled = form.watch("otel_config.metrics_enabled");
	useEffect(() => {
		if (initialConfig === undefined || initialConfig === null || (initialConfig.enabled ?? false) === false) return;
		form.trigger("otel_config.collector_url");
		// Also re-validate metrics_endpoint when protocol changes
		if (metricsEnabled) {
			form.trigger("otel_config.metrics_endpoint");
		}
	}, [protocol, form, metricsEnabled]);

	// Re-run validation on metrics_endpoint when metrics_enabled changes
	useEffect(() => {
		if (metricsEnabled) {
			form.trigger("otel_config.metrics_endpoint");
		}
	}, [metricsEnabled, form]);

	useEffect(() => {
		// Reset form with new initial config when it changes
		form.reset({
			enabled: initialConfig?.enabled || false,
			otel_config: {
				service_name: initialConfig?.service_name ?? "bifrost",
				collector_url: initialConfig?.collector_url || "",
				headers: initialConfig?.headers || {},
				trace_type: initialConfig?.trace_type || "otel",
				protocol: initialConfig?.protocol || "http",
				tls_ca_cert: initialConfig?.tls_ca_cert ?? "",
				insecure: initialConfig?.insecure ?? true,
				metrics_enabled: initialConfig?.metrics_enabled ?? false,
				metrics_endpoint: initialConfig?.metrics_endpoint ?? "",
				metrics_push_interval: initialConfig?.metrics_push_interval ?? 15,
			},
		});
	}, [form, initialConfig]);

	const traceTypeOptions: { value: string; label: string; disabled?: boolean; disabledReason?: string }[] = [
		{ value: "otel", label: "OTEL - GenAI Extension" },
	];
	const protocolOptions: { value: string; label: string; disabled?: boolean; disabledReason?: string }[] = [
		{ value: "http", label: "HTTP" },
		{ value: "grpc", label: "GRPC" },
	];

	return (
		<Form {...form}>
			<form onSubmit={form.handleSubmit(onSubmit)} className="space-y-6">
				{/* OTEL Configuration */}
				<div className="space-y-4">
					<div className="flex flex-col gap-4">
						<FormField
							control={form.control}
							name="otel_config.service_name"
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
							control={form.control}
							name="otel_config.collector_url"
							render={({ field }) => (
								<FormItem className="w-full">
									<FormLabel>OTLP Collector URL</FormLabel>
									<div className="text-muted-foreground text-xs">
										<code>{form.watch("otel_config.protocol") === "http" ? "http(s)://<host>:<port>/v1/traces" : "<host>:<port>"}</code>
									</div>
									<FormControl>
										<Input
											placeholder={
												form.watch("otel_config.protocol") === "http"
													? "https://otel-collector.example.com:4318/v1/traces"
													: "otel-collector.example.com:4317"
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
							control={form.control}
							name="otel_config.headers"
							render={({ field }) => (
								<FormItem className="w-full">
									<FormControl>
										<HeadersTable value={field.value || {}} onChange={field.onChange} disabled={!hasOtelAccess} />
									</FormControl>
									<FormMessage />
								</FormItem>
							)}
						/>
						<div className="flex flex-row gap-4">
							<FormField
								control={form.control}
								name="otel_config.trace_type"
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
								control={form.control}
								name="otel_config.protocol"
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
								control={form.control}
								name="otel_config.insecure"
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
															form.setValue("otel_config.tls_ca_cert", "");
														}
													}}
													disabled={!hasOtelAccess}
												/>
											</div>
										</div>
									</FormItem>
								)}
							/>
							{!form.watch("otel_config.insecure") && (
								<FormField
									control={form.control}
									name="otel_config.tls_ca_cert"
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
					</div>
				</div>

				{/* Metrics Push Configuration */}
				<div className="space-y-4 border-t pt-4">
					<FormField
						control={form.control}
						name="otel_config.metrics_enabled"
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
											data-testid="otel-metrics-export-toggle"
											checked={field.value}
											onCheckedChange={field.onChange}
											disabled={!hasOtelAccess}
										/>
									</div>
								</div>
							</FormItem>
						)}
					/>

					{form.watch("otel_config.metrics_enabled") && (
						<div className="border-muted flex flex-col gap-4">
							<FormField
								control={form.control}
								name="otel_config.metrics_endpoint"
								render={({ field }) => (
									<FormItem className="w-full">
										<FormLabel>Metrics Endpoint</FormLabel>
										<div className="text-muted-foreground text-xs">
											<code>{form.watch("otel_config.protocol") === "http" ? "http(s)://<host>:<port>/v1/metrics" : "<host>:<port>"}</code>
										</div>
										<FormControl>
											<Input
												placeholder={
													form.watch("otel_config.protocol") === "http" ? "https://otel-collector:4318/v1/metrics" : "otel-collector:4317"
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
								control={form.control}
								name="otel_config.metrics_push_interval"
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

				{/* Form Actions */}
				<div className="flex w-full flex-row items-center">
					{enableToggle && (
						<div className="flex items-center gap-2 py-2">
							<span className="text-muted-foreground text-sm font-medium">Enabled</span>
							<Switch
								checked={enableToggle.enabled}
								onCheckedChange={enableToggle.onToggle}
								disabled={enableToggle.disabled}
								title={enableToggle.enabled ? "Enabled" : "Disabled"}
								aria-label={enableToggle.enabled ? "Enabled" : "Disabled"}
							/>
						</div>
					)}
					<div className="ml-auto flex justify-end space-x-2 py-2">
						{onDelete && (
							<Button
								type="button"
								variant="outline"
								onClick={onDelete}
								disabled={isDeleting}
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
								form.reset({
									enabled: false,
									otel_config: undefined,
								});
							}}
							disabled={!hasOtelAccess || isLoading || !form.formState.isDirty}
						>
							Reset
						</Button>
						<TooltipProvider>
							<Tooltip>
								<TooltipTrigger asChild>
									<Button
										type="submit"
										disabled={!hasOtelAccess || !form.formState.isDirty || !form.formState.isValid}
										isLoading={isSaving}
									>
										Save OTEL Configuration
									</Button>
								</TooltipTrigger>
								{(!form.formState.isDirty || !form.formState.isValid) && (
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
