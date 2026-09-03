import PageTitle from "@/components/pageTitle";
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
import { useT } from "@/lib/i18n";
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
	const t = useT();
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
			toast.success(t("configForms.proxy.updated"));
		} catch (error) {
			toast.error(getErrorMessage(error));
		}
	};

	const isTypeUnsupported = watchedType === "socks5" || watchedType === "tcp";

	return (
		<div className="mx-auto w-full max-w-4xl space-y-4">
			<Form {...form}>
				<form onSubmit={form.handleSubmit(onSubmit)} className="space-y-4">
					<PageTitle title="Proxy Settings">{t("configForms.proxy.pageDescription")}</PageTitle>

					<fieldset disabled={!hasSettingsUpdateAccess} className="space-y-4">
						{/* Enable Proxy */}
						<div className="flex items-center justify-between space-x-2 rounded-sm border p-4">
							<div className="space-y-0.5">
								<FormLabel className="text-sm font-medium">{t("configForms.proxy.enableProxy")}</FormLabel>
								<p className="text-muted-foreground text-sm">{t("configForms.proxy.enableProxyDescription")}</p>
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
						<div className={cn("space-y-4 rounded-sm border p-4 transition-opacity", !watchedEnabled && "pointer-events-none opacity-50")}>
							<h3 className="text-lg font-medium">{t("configForms.proxy.configuration")}</h3>

							{/* Proxy Type */}
							<FormField
								control={form.control}
								name="type"
								render={({ field }) => (
									<FormItem>
										<FormLabel>{t("configForms.proxy.proxyType")}</FormLabel>
										<Select onValueChange={field.onChange} value={field.value} disabled={!watchedEnabled}>
											<FormControl>
												<SelectTrigger className="w-48">
													<SelectValue placeholder={t("configForms.proxy.selectType")} />
												</SelectTrigger>
											</FormControl>
											<SelectContent>
												<SelectItem value="http">{t("configForms.proxy.httpHttps")}</SelectItem>
												<SelectItem value="socks5" disabled>
													{t("configForms.proxy.socks5")}{" "}
													<Badge variant="outline" className="ml-2 text-xs">
														{t("configForms.comingSoon")}
													</Badge>
												</SelectItem>
												<SelectItem value="tcp" disabled>
													{t("configForms.proxy.tcp")}{" "}
													<Badge variant="outline" className="ml-2 text-xs">
														{t("configForms.comingSoon")}
													</Badge>
												</SelectItem>
											</SelectContent>
										</Select>
										<FormDescription>{t("configForms.proxy.typeDescription")}</FormDescription>
										<FormMessage />
									</FormItem>
								)}
							/>

							{isTypeUnsupported && watchedEnabled && (
								<Alert variant="destructive">
									<AlertTriangle className="h-4 w-4" />
									<AlertDescription>{t("configForms.proxy.typeUnsupported", { type: watchedType.toUpperCase() })}</AlertDescription>
								</Alert>
							)}

							{/* Proxy URL */}
							<FormField
								control={form.control}
								name="url"
								render={({ field }) => (
									<FormItem>
										<FormLabel>{t("configForms.proxy.url")}</FormLabel>
										<FormControl>
											<Input placeholder={t("configForms.proxy.urlPlaceholder")} disabled={!watchedEnabled} {...field} />
										</FormControl>
										<FormDescription>{t("configForms.proxy.urlDescription")}</FormDescription>
										<FormMessage />
									</FormItem>
								)}
							/>

							{/* Authentication Section */}
							<div className="bg-muted/20 space-y-4 rounded-sm border p-4">
								<h4 className="text-sm font-medium">{t("configForms.proxy.authentication")}</h4>
								<div className="grid grid-cols-1 gap-4 md:grid-cols-2">
									<FormField
										control={form.control}
										name="username"
										render={({ field }) => (
											<FormItem>
												<FormLabel>{t("configForms.proxy.username")}</FormLabel>
												<FormControl>
													<Input
														placeholder={t("configForms.proxy.usernamePlaceholder")}
														disabled={!watchedEnabled}
														{...field}
														value={field.value || ""}
													/>
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
												<FormLabel>{t("configForms.proxy.password")}</FormLabel>
												<FormControl>
													<Input
														type="password"
														placeholder={t("configForms.proxy.passwordPlaceholder")}
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
							<div className="bg-muted/20 space-y-4 rounded-sm border p-4">
								<h4 className="text-sm font-medium">{t("configForms.proxy.advanced")}</h4>

								{/* No Proxy */}
								<FormField
									control={form.control}
									name="no_proxy"
									render={({ field }) => (
										<FormItem>
											<FormLabel>{t("configForms.proxy.noProxyHosts")}</FormLabel>
											<FormControl>
												<Textarea
													placeholder={t("configForms.proxy.noProxyPlaceholder")}
													className="h-20"
													disabled={!watchedEnabled}
													{...field}
													value={field.value || ""}
												/>
											</FormControl>
											<FormDescription>{t("configForms.proxy.noProxyDescription")}</FormDescription>
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
											<FormLabel>{t("configForms.proxy.timeout")}</FormLabel>
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
											<FormDescription>{t("configForms.proxy.timeoutDescription")}</FormDescription>
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
											<FormLabel>{t("configForms.proxy.caCert")}</FormLabel>
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
											<FormDescription>{t("configForms.proxy.caCertDescription")}</FormDescription>
											<FormMessage />
										</FormItem>
									)}
								/>

								{/* Skip TLS Verify */}
								<div className="flex items-center justify-between">
									<div className="space-y-0.5">
										<FormLabel className="text-sm font-medium">{t("configForms.proxy.skipTls")}</FormLabel>
										<p className="text-muted-foreground text-sm">{t("configForms.proxy.skipTlsDescription")}</p>
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
						<div className={cn("space-y-4 rounded-sm border p-4 transition-opacity", !watchedEnabled && "pointer-events-none opacity-50")}>
							<div className="space-y-1">
								<h3 className="text-lg font-medium">{t("configForms.proxy.enableFor")}</h3>
								<p className="text-muted-foreground text-sm">{t("configForms.proxy.enableForDescription")}</p>
							</div>

							{/* SCIM - Enterprise only */}
							{IS_ENTERPRISE && (
								<div className="flex items-center justify-between rounded-sm border p-4">
									<div className="space-y-0.5">
										<div className="flex items-center gap-2">
											<FormLabel className="text-sm font-medium">{t("configForms.proxy.scim")}</FormLabel>
											<Badge variant="secondary">{t("configForms.enterprise")}</Badge>
										</div>
										<p className="text-muted-foreground text-sm">{t("configForms.proxy.scimDescription")}</p>
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
							<div className="flex items-center justify-between rounded-sm border p-4 opacity-60">
								<div className="space-y-0.5">
									<div className="flex items-center gap-2">
										<FormLabel className="text-sm font-medium">{t("configForms.proxy.inference")}</FormLabel>
										<Badge variant="outline">{t("configForms.comingSoon")}</Badge>
									</div>
									<p className="text-muted-foreground text-sm">{t("configForms.proxy.inferenceDescription")}</p>
								</div>
								<Switch disabled checked={false} />
							</div>

							{/* API - Coming Soon */}
							<div className="flex items-center justify-between rounded-sm border p-4 opacity-60">
								<div className="space-y-0.5">
									<div className="flex items-center gap-2">
										<FormLabel className="text-sm font-medium">{t("configForms.proxy.api")}</FormLabel>
										<Badge variant="outline">{t("configForms.comingSoon")}</Badge>
									</div>
									<p className="text-muted-foreground text-sm">{t("configForms.proxy.apiDescription")}</p>
								</div>
								<Switch disabled checked={false} />
							</div>

							{!IS_ENTERPRISE && (
								<Alert>
									<Info className="h-4 w-4" />
									<AlertDescription>{t("configForms.proxy.scimEnterprise")}</AlertDescription>
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
										{isLoading ? t("common.saving") : t("common.saveChanges")}
									</Button>
								</span>
							</TooltipTrigger>
							{!hasSettingsUpdateAccess && <TooltipContent>{t("configForms.noPermissionUpdate")}</TooltipContent>}
						</Tooltip>
					</div>
				</form>
			</Form>
		</div>
	);
}