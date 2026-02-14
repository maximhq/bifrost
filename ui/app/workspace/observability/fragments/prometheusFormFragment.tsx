"use client";

import { Alert, AlertDescription } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Form, FormControl, FormDescription, FormField, FormItem, FormLabel, FormMessage } from "@/components/ui/form";
import { Input } from "@/components/ui/input";
import { Switch } from "@/components/ui/switch";
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip";
import { prometheusFormSchema, type PrometheusFormSchema } from "@/lib/types/schemas";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { zodResolver } from "@hookform/resolvers/zod";
import { AlertTriangle, Copy, Eye, EyeOff, Info, Plus, Trash } from "lucide-react";
import { useEffect, useState } from "react";
import { useForm, type Resolver } from "react-hook-form";

interface PrometheusFormFragmentProps {
	currentConfig?: {
		enabled?: boolean;
		push_gateway_url?: string;
		job_name?: string;
		instance_id?: string;
		push_interval?: number;
		basic_auth?: {
			username?: string;
			password?: string;
		};
	};
	onSave: (config: PrometheusFormSchema) => Promise<void>;
	isLoading?: boolean;
	metricsEndpoint?: string;
}

export function PrometheusFormFragment({
	currentConfig: initialConfig,
	onSave,
	isLoading = false,
	metricsEndpoint,
}: PrometheusFormFragmentProps) {
	const hasPrometheusAccess = useRbac(RbacResource.Observability, RbacOperation.Update);
	const [showPassword, setShowPassword] = useState(false);
	const [isSaving, setIsSaving] = useState(false);
	const [copied, setCopied] = useState(false);
	const [showBasicAuth, setShowBasicAuth] = useState(!!(initialConfig?.basic_auth?.username || initialConfig?.basic_auth?.password));

	const form = useForm<PrometheusFormSchema, any, PrometheusFormSchema>({
		resolver: zodResolver(prometheusFormSchema) as Resolver<PrometheusFormSchema, any, PrometheusFormSchema>,
		mode: "onChange",
		reValidateMode: "onChange",
		defaultValues: {
			enabled: initialConfig?.enabled ?? false,
			prometheus_config: {
				push_gateway_url: initialConfig?.push_gateway_url ?? "",
				job_name: initialConfig?.job_name ?? "bifrost",
				instance_id: initialConfig?.instance_id ?? "",
				push_interval: initialConfig?.push_interval ?? 15,
				basic_auth_username: initialConfig?.basic_auth?.username ?? "",
				basic_auth_password: initialConfig?.basic_auth?.password ?? "",
			},
		},
	});

	const onSubmit = (data: PrometheusFormSchema) => {
		setIsSaving(true);
		onSave(data).finally(() => setIsSaving(false));
	};

	useEffect(() => {
		form.reset({
			enabled: initialConfig?.enabled ?? false,
			prometheus_config: {
				push_gateway_url: initialConfig?.push_gateway_url ?? "",
				job_name: initialConfig?.job_name ?? "bifrost",
				instance_id: initialConfig?.instance_id ?? "",
				push_interval: initialConfig?.push_interval ?? 15,
				basic_auth_username: initialConfig?.basic_auth?.username ?? "",
				basic_auth_password: initialConfig?.basic_auth?.password ?? "",
			},
		});
		setShowBasicAuth(!!(initialConfig?.basic_auth?.username || initialConfig?.basic_auth?.password));
	}, [form, initialConfig]);

	const handleCopyEndpoint = () => {
		if (metricsEndpoint) {
			navigator.clipboard.writeText(metricsEndpoint);
			setCopied(true);
			setTimeout(() => setCopied(false), 2000);
		}
	};

	const handleRemoveBasicAuth = () => {
		form.setValue("prometheus_config.basic_auth_username", "", { shouldDirty: true });
		form.setValue("prometheus_config.basic_auth_password", "", { shouldDirty: true });
		setShowBasicAuth(false);
	};

	return (
		<Form {...form}>
			<form onSubmit={form.handleSubmit(onSubmit)} className="space-y-6">
				{/* Pull-based Scraping Section */}
				<div className="space-y-4">
					<div className="flex flex-col gap-2">
						<h3 className="text-sm font-medium">Pull-based Scraping</h3>
						<p className="text-muted-foreground text-xs">Prometheus can scrape metrics from the /metrics endpoint</p>
					</div>

					<div className="bg-muted/50 rounded-md p-4">
						<div className="flex items-center justify-between">
							<div className="flex flex-col gap-1">
								<span className="text-sm font-medium">Metrics Endpoint</span>
								<code className="text-muted-foreground text-xs">{metricsEndpoint || "http://<bifrost-host>:<port>/metrics"}</code>
							</div>
							{metricsEndpoint && (
								<Button type="button" variant="outline" size="sm" onClick={handleCopyEndpoint} className="shrink-0">
									<Copy className="mr-2 h-3 w-3" />
									{copied ? "Copied!" : "Copy"}
								</Button>
							)}
						</div>
						<p className="text-muted-foreground mt-2 text-xs">
							Configure your Prometheus server to scrape this endpoint. This is always available when Bifrost is running.
						</p>
					</div>
				</div>

				{/* Push-based Section */}
				<div className="space-y-4 border-t pt-4">
					<div className="flex flex-col gap-2">
						<h3 className="flex flex-row items-center gap-2 text-sm font-medium">
							Push-based (Push Gateway) <Badge variant="secondary">BETA</Badge>
						</h3>
						<p className="text-muted-foreground text-xs">
							Push metrics to a Prometheus Push Gateway for proper aggregation in cluster deployments
						</p>
					</div>

					{/* Warning note for multi-node deployments */}
					<Alert variant="info">
						<AlertTriangle className="" />
						<AlertDescription className="text-xs">
							If you are running multiple Bifrost nodes, use push gateway for accurate metrics. Pull-based /metrics scraping may miss nodes
							behind a load balancer.
						</AlertDescription>
					</Alert>

					<div className="space-y-4">
						<FormField
							control={form.control}
							name="prometheus_config.push_gateway_url"
							render={({ field }) => (
								<FormItem className="w-full">
									<FormLabel>Push Gateway URL</FormLabel>
									<FormControl>
										<Input placeholder="http://pushgateway:9091" disabled={!hasPrometheusAccess} {...field} />
									</FormControl>
									<FormDescription>URL of your Prometheus Push Gateway</FormDescription>
									<FormMessage />
								</FormItem>
							)}
						/>

						<div className="grid grid-cols-2 gap-4">
							<FormField
								control={form.control}
								name="prometheus_config.job_name"
								render={({ field }) => (
									<FormItem>
										<FormLabel>Job Name</FormLabel>
										<FormControl>
											<Input placeholder="bifrost" disabled={!hasPrometheusAccess} {...field} />
										</FormControl>
										<FormDescription>Job label for metrics</FormDescription>
										<FormMessage />
									</FormItem>
								)}
							/>

							<FormField
								control={form.control}
								name="prometheus_config.push_interval"
								render={({ field }) => (
									<FormItem>
										<FormLabel>Push Interval (seconds)</FormLabel>
										<FormControl>
											<Input
												type="number"
												min={1}
												max={300}
												disabled={!hasPrometheusAccess}
												{...field}
												onChange={(e) => field.onChange(parseInt(e.target.value) || 15)}
											/>
										</FormControl>
										<FormDescription>How often to push (1-300s)</FormDescription>
										<FormMessage />
									</FormItem>
								)}
							/>
						</div>

						<FormField
							control={form.control}
							name="prometheus_config.instance_id"
							render={({ field }) => (
								<FormItem>
									<FormLabel className="flex items-center gap-2">
										Instance ID
										<TooltipProvider>
											<Tooltip>
												<TooltipTrigger asChild>
													<Info className="text-muted-foreground h-3 w-3" />
												</TooltipTrigger>
												<TooltipContent>
													<p className="max-w-xs text-xs">
														Used to identify this Bifrost instance in metrics. If not set, hostname is used automatically.
													</p>
												</TooltipContent>
											</Tooltip>
										</TooltipProvider>
									</FormLabel>
									<FormControl>
										<Input
											placeholder="Auto-generated from hostname"
											disabled={!hasPrometheusAccess}
											{...field}
											value={field.value ?? ""}
										/>
									</FormControl>
									<FormMessage />
								</FormItem>
							)}
						/>

						{/* Basic Auth Section */}
						<div className="space-y-4 border-t pt-4">
							{!showBasicAuth ? (
								<Button type="button" variant="outline" size="sm" onClick={() => setShowBasicAuth(true)} disabled={!hasPrometheusAccess}>
									<Plus className="mr-2 h-3 w-3" />
									Add Basic Auth
								</Button>
							) : (
								<>
									<div className="flex items-center justify-between">
										<span className="text-sm font-medium">Basic Authentication</span>
										<Button
											type="button"
											variant="ghost"
											size="sm"
											onClick={handleRemoveBasicAuth}
											disabled={!hasPrometheusAccess}
											className="text-muted-foreground hover:text-destructive h-auto p-1"
										>
											<Trash className="h-4 w-4" />
										</Button>
									</div>
									<div className="border-muted grid grid-cols-2 gap-4">
										<FormField
											control={form.control}
											name="prometheus_config.basic_auth_username"
											render={({ field }) => (
												<FormItem>
													<FormLabel>Username</FormLabel>
													<FormControl>
														<Input placeholder="Username" disabled={!hasPrometheusAccess} {...field} />
													</FormControl>
													<FormMessage />
												</FormItem>
											)}
										/>

										<FormField
											control={form.control}
											name="prometheus_config.basic_auth_password"
											render={({ field }) => (
												<FormItem>
													<FormLabel>Password</FormLabel>
													<FormControl>
														<div className="relative">
															<Input
																type={showPassword ? "text" : "password"}
																placeholder="Password"
																disabled={!hasPrometheusAccess}
																{...field}
																className="pr-10"
															/>
															<Button
																type="button"
																variant="ghost"
																size="sm"
																className="absolute top-0 right-0 h-full px-3 py-2 hover:bg-transparent"
																onClick={() => setShowPassword(!showPassword)}
																disabled={!hasPrometheusAccess}
															>
																{showPassword ? <EyeOff className="h-4 w-4" /> : <Eye className="h-4 w-4" />}
															</Button>
														</div>
													</FormControl>
													<FormMessage />
												</FormItem>
											)}
										/>
									</div>
								</>
							)}
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
								<FormLabel>Enable Push Gateway</FormLabel>
								<Switch
									checked={form.watch("enabled")}
									onCheckedChange={field.onChange}
									disabled={!hasPrometheusAccess || isLoading || !form.formState.isValid}
								/>
							</FormItem>
						)}
					/>
					<div className="ml-auto flex justify-end space-x-2 py-2">
						<Button
							type="button"
							variant="outline"
							onClick={() => {
								form.reset({
									enabled: initialConfig?.enabled ?? false,
									prometheus_config: {
										push_gateway_url: initialConfig?.push_gateway_url ?? "",
										job_name: initialConfig?.job_name ?? "bifrost",
										instance_id: initialConfig?.instance_id ?? "",
										push_interval: initialConfig?.push_interval ?? 15,
										basic_auth_username: initialConfig?.basic_auth?.username ?? "",
										basic_auth_password: initialConfig?.basic_auth?.password ?? "",
									},
								});
								setShowBasicAuth(!!(initialConfig?.basic_auth?.username || initialConfig?.basic_auth?.password));
							}}
							disabled={!hasPrometheusAccess || isLoading || !form.formState.isDirty}
						>
							Reset
						</Button>
						<TooltipProvider>
							<Tooltip>
								<TooltipTrigger asChild>
									<Button
										type="submit"
										disabled={!hasPrometheusAccess || !form.formState.isDirty || !form.formState.isValid}
										isLoading={isSaving}
									>
										Save Prometheus Configuration
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
