"use client";

import { Badge } from "@/components/ui/badge";
import { FormControl, FormDescription, FormField, FormItem, FormLabel, FormMessage } from "@/components/ui/form";
import { HeadersTable } from "@/components/ui/headersTable";
import { Input } from "@/components/ui/input";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Switch } from "@/components/ui/switch";
import type { OtelFormSchema } from "@/lib/types/schemas";
import { useEffect } from "react";
import type { UseFormReturn } from "react-hook-form";

interface OtelFormFragmentProps {
	form: UseFormReturn<OtelFormSchema, any, OtelFormSchema>;
	hasAccess: boolean;
}

const traceTypeOptions = [{ value: "otel", label: "OTEL - GenAI Extension" }];
const protocolOptions = [
	{ value: "http", label: "HTTP" },
	{ value: "grpc", label: "GRPC" },
];

export function OtelFormFragment({ form, hasAccess }: OtelFormFragmentProps) {
	const protocol = form.watch("otel_profile.protocol");
	const metricsEnabled = form.watch("otel_profile.metrics_enabled");

	// Re-run validation on collector_url when protocol changes
	useEffect(() => {
		form.trigger("otel_profile.collector_url");
		if (metricsEnabled) {
			form.trigger("otel_profile.metrics_endpoint");
		}
	}, [protocol, form, metricsEnabled]);

	// Re-run validation on metrics_endpoint when metrics_enabled changes
	useEffect(() => {
		if (metricsEnabled) {
			form.trigger("otel_profile.metrics_endpoint");
		}
	}, [metricsEnabled, form]);

	return (
		<div className="flex flex-col gap-6">
			{/* Profile Name */}
			<FormField
				control={form.control}
				name="otel_profile.name"
				render={({ field }) => (
					<FormItem className="w-full">
						<FormLabel>Profile Name</FormLabel>
						<FormDescription>A name to identify this profile in logs</FormDescription>
						<FormControl>
							<Input placeholder="e.g. primary, datadog, vendor" disabled={!hasAccess} {...field} />
						</FormControl>
						<FormMessage />
					</FormItem>
				)}
			/>

			{/* Service Name */}
			<FormField
				control={form.control}
				name="otel_profile.service_name"
				render={({ field }) => (
					<FormItem className="w-full">
						<FormLabel>Service Name</FormLabel>
						<FormDescription>If kept empty, the service name will be set to "bifrost"</FormDescription>
						<FormControl>
							<Input placeholder="bifrost" disabled={!hasAccess} {...field} />
						</FormControl>
						<FormMessage />
					</FormItem>
				)}
			/>

			{/* Collector URL */}
			<FormField
				control={form.control}
				name="otel_profile.collector_url"
				render={({ field }) => (
					<FormItem className="w-full">
						<FormLabel>OTLP Collector URL</FormLabel>
						<div className="text-muted-foreground text-xs">
							<code>{protocol === "http" ? "http(s)://<host>:<port>/v1/traces" : "<host>:<port>"}</code>
						</div>
						<FormControl>
							<Input
								placeholder={protocol === "http" ? "https://otel-collector.example.com:4318/v1/traces" : "otel-collector.example.com:4317"}
								disabled={!hasAccess}
								{...field}
							/>
						</FormControl>
						<FormMessage />
					</FormItem>
				)}
			/>

			{/* Headers */}
			<FormField
				control={form.control}
				name="otel_profile.headers"
				render={({ field }) => (
					<FormItem className="w-full">
						<FormControl>
							<HeadersTable value={field.value || {}} onChange={field.onChange} disabled={!hasAccess} />
						</FormControl>
						<FormMessage />
					</FormItem>
				)}
			/>

			{/* Format + Protocol */}
			<div className="flex flex-row gap-4">
				<FormField
					control={form.control}
					name="otel_profile.trace_type"
					render={({ field }) => (
						<FormItem className="flex-1">
							<FormLabel>Format</FormLabel>
							<Select onValueChange={field.onChange} value={field.value ?? "otel"} disabled={!hasAccess}>
								<FormControl>
									<SelectTrigger className="w-full">
										<SelectValue placeholder="Select trace type" />
									</SelectTrigger>
								</FormControl>
								<SelectContent>
									{traceTypeOptions.map((option) => (
										<SelectItem key={option.value} value={option.value}>
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
					name="otel_profile.protocol"
					render={({ field }) => (
						<FormItem className="flex-1">
							<FormLabel>Protocol</FormLabel>
							<Select onValueChange={field.onChange} value={field.value} disabled={!hasAccess}>
								<FormControl>
									<SelectTrigger className="w-full">
										<SelectValue placeholder="Select protocol" />
									</SelectTrigger>
								</FormControl>
								<SelectContent>
									{protocolOptions.map((option) => (
										<SelectItem key={option.value} value={option.value}>
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
					name="otel_profile.insecure"
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
												form.setValue("otel_profile.tls_ca_cert", "");
											}
										}}
										disabled={!hasAccess}
									/>
								</div>
							</div>
						</FormItem>
					)}
				/>
				{!form.watch("otel_profile.insecure") && (
					<FormField
						control={form.control}
						name="otel_profile.tls_ca_cert"
						render={({ field }) => (
							<FormItem className="w-full">
								<FormLabel>TLS CA Certificate Path</FormLabel>
								<FormDescription>
									File path to the CA certificate on the Bifrost server. Leave empty to use system root CAs.
								</FormDescription>
								<FormControl>
									<Input placeholder="/path/to/ca.crt" disabled={!hasAccess} {...field} />
								</FormControl>
								<FormMessage />
							</FormItem>
						)}
					/>
				)}
			</div>

			{/* Metrics Push Configuration */}
			<div className="space-y-4 border-t pt-4">
				<FormField
					control={form.control}
					name="otel_profile.metrics_enabled"
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
									<Switch checked={field.value} onCheckedChange={field.onChange} disabled={!hasAccess} />
								</div>
							</div>
						</FormItem>
					)}
				/>

				{metricsEnabled && (
					<div className="border-muted flex flex-col gap-4">
						<FormField
							control={form.control}
							name="otel_profile.metrics_endpoint"
							render={({ field }) => (
								<FormItem className="w-full">
									<FormLabel>Metrics Endpoint</FormLabel>
									<div className="text-muted-foreground text-xs">
										<code>{protocol === "http" ? "http(s)://<host>:<port>/v1/metrics" : "<host>:<port>"}</code>
									</div>
									<FormControl>
										<Input
											placeholder={protocol === "http" ? "https://otel-collector:4318/v1/metrics" : "otel-collector:4317"}
											disabled={!hasAccess}
											{...field}
										/>
									</FormControl>
									<FormMessage />
								</FormItem>
							)}
						/>

						<FormField
							control={form.control}
							name="otel_profile.metrics_push_interval"
							render={({ field }) => (
								<FormItem className="w-full max-w-xs">
									<FormLabel>Push Interval (seconds)</FormLabel>
									<FormControl>
										<Input
											type="number"
											min={1}
											max={300}
											disabled={!hasAccess}
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
	);
}
