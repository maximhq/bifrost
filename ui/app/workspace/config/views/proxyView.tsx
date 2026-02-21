"use client";

import { Alert, AlertDescription } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Form, FormControl, FormDescription, FormField, FormItem, FormLabel, FormMessage } from "@/components/ui/form";
import { Input } from "@/components/ui/input";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Switch } from "@/components/ui/switch";
import { Textarea } from "@/components/ui/textarea";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { IS_ENTERPRISE } from "@/lib/constants/config";
import { getErrorMessage, useGetCoreConfigQuery, useUpdateProxyConfigMutation } from "@/lib/store";
import { DefaultGlobalProxyConfig, GlobalProxyConfig } from "@/lib/types/config";
import { globalProxyConfigSchema } from "@/lib/types/schemas";
import { cn } from "@/lib/utils";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { zodResolver } from "@hookform/resolvers/zod";
import { AlertTriangle, Info } from "lucide-react";
import { useEffect } from "react";
import { useForm } from "react-hook-form";
import { toast } from "sonner";

export default function ProxyView() {
	const hasSettingsUpdateAccess = useRbac(RbacResource.Settings, RbacOperation.Update);
	const { data: bifrostConfig } = useGetCoreConfigQuery({ fromDB: true });
	const proxyConfig = bifrostConfig?.proxy_config;
	const [updateProxyConfig, { isLoading }] = useUpdateProxyConfigMutation();

	const form = useForm<GlobalProxyConfig>({
		resolver: zodResolver(globalProxyConfigSchema),
		mode: "onChange",
		reValidateMode: "onChange",
		defaultValues: DefaultGlobalProxyConfig,
	});

	useEffect(() => {
		if (proxyConfig) {
			form.reset({
				...DefaultGlobalProxyConfig,
				...proxyConfig,
			});
		}
	}, [proxyConfig, form]);

	const watchedEnabled = form.watch("enabled");
	const watchedType = form.watch("type");

	const onSubmit = async (data: GlobalProxyConfig) => {
		try {
			await updateProxyConfig(data).unwrap();
			toast.success("Proxy configuration updated successfully.");
		} catch (error) {
			toast.error(getErrorMessage(error));
		}
	};

	const isTypeUnsupported = watchedType === "socks5" || watchedType === "tcp";

	return (
		<div className="mx-auto w-full max-w-4xl space-y-4">
			<Form {...form}>
				<form onSubmit={form.handleSubmit(onSubmit)} className="space-y-4">
					<div>
						<h2 className="text-lg font-semibold tracking-tight">Proxy Settings</h2>
						<p className="text-muted-foreground text-sm">Configure global proxy settings for outbound requests.</p>
					</div>

					<fieldset disabled={!hasSettingsUpdateAccess} className="space-y-4">
						{/* Enable Proxy */}
						<div className="flex items-center justify-between space-x-2 rounded-lg border p-4">
							<div className="space-y-0.5">
								<FormLabel className="text-sm font-medium">Enable Proxy</FormLabel>
								<p className="text-muted-foreground text-sm">Enable global proxy for outbound HTTP requests.</p>
							</div>
							<FormField
								control={form.control}
								name="enabled"
								render={({ field }) => (
									<FormItem>
										<FormControl>
											<Switch checked={field.value} onCheckedChange={field.onChange} />
										</FormControl>
									</FormItem>
								)}
							/>
						</div>

						{/* Proxy Configuration Section */}
						<div className={cn("space-y-4 rounded-lg border p-4 transition-opacity", !watchedEnabled && "pointer-events-none opacity-50")}>
							<h3 className="text-lg font-medium">Proxy Configuration</h3>

							{/* Proxy Type */}
							<FormField
								control={form.control}
								name="type"
								render={({ field }) => (
									<FormItem>
										<FormLabel>Proxy Type</FormLabel>
										<Select onValueChange={field.onChange} value={field.value} disabled={!watchedEnabled}>
											<FormControl>
												<SelectTrigger className="w-48">
													<SelectValue placeholder="Select type" />
												</SelectTrigger>
											</FormControl>
											<SelectContent>
												<SelectItem value="http">HTTP / HTTPS</SelectItem>
												<SelectItem value="socks5" disabled>
													SOCKS5{" "}
													<Badge variant="outline" className="ml-2 text-xs">
														Coming soon
													</Badge>
												</SelectItem>
												<SelectItem value="tcp" disabled>
													TCP{" "}
													<Badge variant="outline" className="ml-2 text-xs">
														Coming soon
													</Badge>
												</SelectItem>
											</SelectContent>
										</Select>
										<FormDescription>Select the proxy protocol type. Currently only HTTP proxy is supported.</FormDescription>
										<FormMessage />
									</FormItem>
								)}
							/>

							{isTypeUnsupported && watchedEnabled && (
								<Alert variant="destructive">
									<AlertTriangle className="h-4 w-4" />
									<AlertDescription>{watchedType.toUpperCase()} proxy is not yet supported. Please use HTTP proxy.</AlertDescription>
								</Alert>
							)}

							{/* Proxy URL */}
							<FormField
								control={form.control}
								name="url"
								render={({ field }) => (
									<FormItem>
										<FormLabel>Proxy URL</FormLabel>
										<FormControl>
											<Input placeholder="http://proxy.example.com:8080" disabled={!watchedEnabled} {...field} />
										</FormControl>
										<FormDescription>Full URL of the proxy server including protocol and port.</FormDescription>
										<FormMessage />
									</FormItem>
								)}
							/>

							{/* Authentication Section */}
							<div className="bg-muted/20 space-y-4 rounded-md border p-4">
								<h4 className="text-sm font-medium">Authentication (Optional)</h4>
								<div className="grid grid-cols-2 gap-4">
									<FormField
										control={form.control}
										name="username"
										render={({ field }) => (
											<FormItem>
												<FormLabel>Username</FormLabel>
												<FormControl>
													<Input placeholder="Proxy username" disabled={!watchedEnabled} {...field} value={field.value || ""} />
												</FormControl>
												<FormMessage />
											</FormItem>
										)}
									/>
									<FormField
										control={form.control}
										name="password"
										render={({ field }) => (
											<FormItem>
												<FormLabel>Password</FormLabel>
												<FormControl>
													<Input
														type="password"
														placeholder="Proxy password"
														disabled={!watchedEnabled}
														{...field}
														value={field.value || ""}
													/>
												</FormControl>
												<FormMessage />
											</FormItem>
										)}
									/>
								</div>
							</div>

							{/* Advanced Settings */}
							<div className="bg-muted/20 space-y-4 rounded-md border p-4">
								<h4 className="text-sm font-medium">Advanced Settings</h4>

								{/* No Proxy */}
								<FormField
									control={form.control}
									name="no_proxy"
									render={({ field }) => (
										<FormItem>
											<FormLabel>No Proxy Hosts</FormLabel>
											<FormControl>
												<Textarea
													placeholder="localhost, 127.0.0.1, .internal.example.com"
													className="h-20"
													disabled={!watchedEnabled}
													{...field}
													value={field.value || ""}
												/>
											</FormControl>
											<FormDescription>Comma-separated list of hosts that should bypass the proxy.</FormDescription>
											<FormMessage />
										</FormItem>
									)}
								/>

								{/* Timeout */}
								<FormField
									control={form.control}
									name="timeout"
									render={({ field }) => (
										<FormItem>
											<FormLabel>Connection Timeout (seconds)</FormLabel>
											<FormControl>
												<Input
													type="number"
													min={0}
													max={300}
													placeholder="30"
													className="w-32"
													disabled={!watchedEnabled}
													{...field}
													value={field.value ?? ""}
													onChange={(e) => field.onChange(e.target.value !== "" ? parseInt(e.target.value, 10) : undefined)}
												/>
											</FormControl>
											<FormDescription>
												Timeout for establishing proxy connections. 0 means no timeout. Default is 60 seconds.
											</FormDescription>
											<FormMessage />
										</FormItem>
									)}
								/>

								{/* CA Certificate */}
								<FormField
									control={form.control}
									name="ca_cert_pem"
									render={({ field }) => (
										<FormItem>
											<FormLabel>CA Certificate (PEM) (Optional)</FormLabel>
											<FormControl>
												<Textarea
													placeholder="-----BEGIN CERTIFICATE-----&#10;...&#10;-----END CERTIFICATE-----"
													className="font-mono text-xs"
													rows={6}
													disabled={!watchedEnabled}
													{...field}
													value={field.value || ""}
												/>
											</FormControl>
											<FormDescription>
												PEM-encoded CA certificate to trust for TLS connections through SSL-intercepting proxies.
											</FormDescription>
											<FormMessage />
										</FormItem>
									)}
								/>

								{/* Skip TLS Verify */}
								<div className="flex items-center justify-between">
									<div className="space-y-0.5">
										<FormLabel className="text-sm font-medium">Skip TLS Verification</FormLabel>
										<p className="text-muted-foreground text-sm">
											Disable TLS certificate verification for HTTPS proxies. Not recommended for production.
										</p>
									</div>
									<FormField
										control={form.control}
										name="skip_tls_verify"
										render={({ field }) => (
											<FormItem>
												<FormControl>
													<Switch checked={field.value} onCheckedChange={field.onChange} disabled={!watchedEnabled} />
												</FormControl>
											</FormItem>
										)}
									/>
								</div>
							</div>
						</div>

						{/* Entity Enablement Section */}
						<div className={cn("space-y-4 rounded-lg border p-4 transition-opacity", !watchedEnabled && "pointer-events-none opacity-50")}>
							<div className="space-y-1">
								<h3 className="text-lg font-medium">Enable Proxy For</h3>
								<p className="text-muted-foreground text-sm">Select which components should use the proxy for outbound requests.</p>
							</div>

							{/* SCIM - Enterprise only */}
							{IS_ENTERPRISE && (
								<div className="flex items-center justify-between rounded-md border p-4">
									<div className="space-y-0.5">
										<div className="flex items-center gap-2">
											<FormLabel className="text-sm font-medium">SCIM</FormLabel>
											<Badge variant="secondary">Enterprise</Badge>
										</div>
										<p className="text-muted-foreground text-sm">Use proxy for SCIM directory sync requests.</p>
									</div>
									<FormField
										control={form.control}
										name="enable_for_scim"
										render={({ field }) => (
											<FormItem>
												<FormControl>
													<Switch checked={field.value} onCheckedChange={field.onChange} disabled={!watchedEnabled} />
												</FormControl>
											</FormItem>
										)}
									/>
								</div>
							)}

							{/* Inference - Coming Soon */}
							<div className="flex items-center justify-between rounded-md border p-4 opacity-60">
								<div className="space-y-0.5">
									<div className="flex items-center gap-2">
										<FormLabel className="text-sm font-medium">Inference</FormLabel>
										<Badge variant="outline">Coming soon</Badge>
									</div>
									<p className="text-muted-foreground text-sm">Use proxy for LLM inference requests to model providers.</p>
								</div>
								<Switch disabled checked={false} />
							</div>

							{/* API - Coming Soon */}
							<div className="flex items-center justify-between rounded-md border p-4 opacity-60">
								<div className="space-y-0.5">
									<div className="flex items-center gap-2">
										<FormLabel className="text-sm font-medium">API</FormLabel>
										<Badge variant="outline">Coming soon</Badge>
									</div>
									<p className="text-muted-foreground text-sm">Use proxy for external API calls and webhooks.</p>
								</div>
								<Switch disabled checked={false} />
							</div>

							{!IS_ENTERPRISE && (
								<Alert>
									<Info className="h-4 w-4" />
									<AlertDescription>SCIM proxy support is available in Bifrost Enterprise.</AlertDescription>
								</Alert>
							)}
						</div>
					</fieldset>
					<div className="flex justify-end pt-2">
						<Tooltip>
							<TooltipTrigger asChild>
								<span tabIndex={!hasSettingsUpdateAccess ? 0 : undefined}>
									<Button
										type="submit"
										disabled={!form.formState.isDirty || !form.formState.isValid || isLoading || !hasSettingsUpdateAccess}
									>
										{isLoading ? "Saving..." : "Save Changes"}
									</Button>
								</span>
							</TooltipTrigger>
							{!hasSettingsUpdateAccess && <TooltipContent>You don't have permission to update settings</TooltipContent>}
						</Tooltip>
					</div>
				</form>
			</Form>
		</div>
	);
}
