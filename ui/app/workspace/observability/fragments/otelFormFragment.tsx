"use client";

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
	};
	onSave: (config: OtelFormSchema) => Promise<void>;
	isLoading?: boolean;
}

export function OtelFormFragment({ currentConfig: initialConfig, onSave, isLoading = false }: OtelFormFragmentProps) {
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
	useEffect(() => {
		if (initialConfig === undefined || initialConfig === null || (initialConfig.enabled ?? false) === false) return;
		form.trigger("otel_config.collector_url");
	}, [protocol, form]);

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
					</div>
				</div>

				{/* Form Actions */}
				<div className="flex w-full flex-row items-center">
					<FormField
						control={form.control}
						name="enabled"
						render={({ field }) => (
							<FormItem className="flex flex-row items-center gap-2">
								<FormLabel>Enabled</FormLabel>
								<Switch checked={form.watch("enabled")} onCheckedChange={field.onChange} disabled={!hasOtelAccess || isLoading || !form.formState.isValid} />
							</FormItem>
						)}
					/>
					<div className="ml-auto flex justify-end space-x-2 py-2">
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
									<Button type="submit" disabled={!hasOtelAccess || !form.formState.isDirty || !form.formState.isValid} isLoading={isSaving}>
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
