import { Accordion, AccordionContent, AccordionItem, AccordionTrigger } from "@/components/ui/accordion";
import { Button } from "@/components/ui/button";
import { SecretVarInput } from "@/components/ui/secretVarInput";
import { Form, FormControl, FormDescription, FormField, FormItem, FormLabel, FormMessage } from "@/components/ui/form";
import { HeadersTable } from "@/components/ui/headersTable";
import { Input } from "@/components/ui/input";
import { Switch } from "@/components/ui/switch";
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip";
import { DefaultNetworkConfig } from "@/lib/constants/config";
import { usePlural, useT, type MessageKey } from "@/lib/i18n";
import { getErrorMessage, setProviderFormDirtyState, useAppDispatch } from "@/lib/store";
import { useUpdateProviderMutation } from "@/lib/store/apis/providersApi";
import { ModelProvider, isKnownProvider } from "@/lib/types/config";
import { networkOnlyFormSchema, type SecretVar, type NetworkOnlyFormSchema } from "@/lib/types/schemas";
import { toSecretVarFormValue, toOptionalSecretVarPayload } from "@/lib/utils/secretVarForm";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { zodResolver } from "@hookform/resolvers/zod";
import { useEffect } from "react";
import { useForm, type Resolver } from "react-hook-form";
import { toast } from "sonner";
import { buildProviderUpdatePayload } from "../views/utils";

interface NetworkFormFragmentProps {
	provider: ModelProvider;
}

type PluralFn = (key: MessageKey, count: number) => string;

// seconds to human readable time
const secondsToHumanReadable = (seconds: number, plural: PluralFn) => {
	// Handle edge cases
	if (!seconds || seconds < 0 || isNaN(seconds)) {
		return plural("providers.networkConfig.second", 0);
	}
	seconds = Math.floor(seconds);
	if (seconds < 60) {
		return plural("providers.networkConfig.second", seconds);
	}
	if (seconds < 3600) {
		const minutes = Math.floor(seconds / 60);
		const remainingSeconds = seconds % 60;
		const parts: string[] = [plural("providers.networkConfig.minute", minutes)];
		if (remainingSeconds > 0) parts.push(plural("providers.networkConfig.second", remainingSeconds));
		return parts.join(" ");
	}
	if (seconds < 86400) {
		const hours = Math.floor(seconds / 3600);
		const minutes = Math.floor((seconds % 3600) / 60);
		const remainingSeconds = seconds % 60;
		const parts: string[] = [plural("providers.networkConfig.hour", hours)];
		if (minutes > 0) parts.push(plural("providers.networkConfig.minute", minutes));
		if (remainingSeconds > 0) parts.push(plural("providers.networkConfig.second", remainingSeconds));
		return parts.join(" ");
	}
	// For >= 1 day, only show non-zero components
	const days = Math.floor(seconds / 86400);
	const hours = Math.floor((seconds % 86400) / 3600);
	const minutes = Math.floor((seconds % 3600) / 60);
	const remainingSeconds = seconds % 60;
	const parts: string[] = [];
	parts.push(plural("providers.networkConfig.day", days));
	if (hours > 0) parts.push(plural("providers.networkConfig.hour", hours));
	if (minutes > 0) parts.push(plural("providers.networkConfig.minute", minutes));
	if (remainingSeconds > 0) parts.push(plural("providers.networkConfig.second", remainingSeconds));
	return parts.join(" ");
};

export function NetworkFormFragment({ provider }: NetworkFormFragmentProps) {
	const t = useT();
	const plural = usePlural();
	const dispatch = useAppDispatch();
	const hasUpdateProviderAccess = useRbac(RbacResource.ModelProvider, RbacOperation.Update);
	const [updateProvider, { isLoading: isUpdatingProvider }] = useUpdateProviderMutation();
	const isCustomProvider = !isKnownProvider(provider.name as string);

	const form = useForm<NetworkOnlyFormSchema, any, NetworkOnlyFormSchema>({
		resolver: zodResolver(networkOnlyFormSchema) as Resolver<NetworkOnlyFormSchema, any, NetworkOnlyFormSchema>,
		mode: "onChange",
		reValidateMode: "onChange",
		defaultValues: {
			network_config: {
				base_url: provider.network_config?.base_url || undefined,
				extra_headers: provider.network_config?.extra_headers,
				default_request_timeout_in_seconds:
					provider.network_config?.default_request_timeout_in_seconds ?? DefaultNetworkConfig.default_request_timeout_in_seconds,
				max_retries: provider.network_config?.max_retries ?? DefaultNetworkConfig.max_retries,
				retry_backoff_initial: provider.network_config?.retry_backoff_initial ?? DefaultNetworkConfig.retry_backoff_initial,
				retry_backoff_max: provider.network_config?.retry_backoff_max ?? DefaultNetworkConfig.retry_backoff_max,
				insecure_skip_verify: provider.network_config?.insecure_skip_verify ?? DefaultNetworkConfig.insecure_skip_verify,
				ca_cert_pem: toSecretVarFormValue(provider.network_config?.ca_cert_pem as SecretVar | string | undefined),
				stream_idle_timeout_in_seconds:
					provider.network_config?.stream_idle_timeout_in_seconds ?? DefaultNetworkConfig.stream_idle_timeout_in_seconds,
				keep_alive_timeout_in_seconds:
					provider.network_config?.keep_alive_timeout_in_seconds ?? DefaultNetworkConfig.keep_alive_timeout_in_seconds,
				max_conns_per_host: provider.network_config?.max_conns_per_host ?? DefaultNetworkConfig.max_conns_per_host,
				enforce_http2: provider.network_config?.enforce_http2 ?? DefaultNetworkConfig.enforce_http2,
				http2_ping_interval_in_seconds:
					provider.network_config?.http2_ping_interval_in_seconds ?? DefaultNetworkConfig.http2_ping_interval_in_seconds,
				allow_private_network: provider.network_config?.allow_private_network ?? DefaultNetworkConfig.allow_private_network,
			},
		},
	});

	useEffect(() => {
		dispatch(setProviderFormDirtyState(form.formState.isDirty));
	}, [form.formState.isDirty, dispatch]);

	const onSubmit = (data: NetworkOnlyFormSchema) => {
		const requiresBaseUrl = isCustomProvider;
		if (requiresBaseUrl && (data.network_config?.base_url ?? "").trim() === "") {
			if ((provider.network_config?.base_url ?? "").trim() !== "") {
				toast.error(t("providers.networkConfig.cannotRemove"));
			} else {
				toast.error(t("providers.networkConfig.baseUrlRequired"));
			}
			return;
		}
		// Create updated provider configuration
		const updatedProvider = buildProviderUpdatePayload(provider, {
			network_config: {
				...provider.network_config,
				base_url: data.network_config?.base_url || undefined,
				extra_headers: data.network_config?.extra_headers || undefined,
				default_request_timeout_in_seconds:
					data.network_config?.default_request_timeout_in_seconds ?? DefaultNetworkConfig.default_request_timeout_in_seconds,
				max_retries: data.network_config?.max_retries ?? 0,
				retry_backoff_initial: data.network_config?.retry_backoff_initial ?? 500,
				retry_backoff_max: data.network_config?.retry_backoff_max ?? 10000,
				insecure_skip_verify: data.network_config?.insecure_skip_verify ?? false,
				ca_cert_pem: toOptionalSecretVarPayload(data.network_config?.ca_cert_pem),
				stream_idle_timeout_in_seconds:
					data.network_config?.stream_idle_timeout_in_seconds ?? DefaultNetworkConfig.stream_idle_timeout_in_seconds,
				keep_alive_timeout_in_seconds:
					data.network_config?.keep_alive_timeout_in_seconds ?? DefaultNetworkConfig.keep_alive_timeout_in_seconds,
				max_conns_per_host: data.network_config?.max_conns_per_host ?? DefaultNetworkConfig.max_conns_per_host,
				enforce_http2: data.network_config?.enforce_http2 ?? DefaultNetworkConfig.enforce_http2,
				http2_ping_interval_in_seconds:
					data.network_config?.http2_ping_interval_in_seconds ?? DefaultNetworkConfig.http2_ping_interval_in_seconds,
				allow_private_network: data.network_config?.allow_private_network ?? DefaultNetworkConfig.allow_private_network,
			},
		});
		updateProvider(updatedProvider)
			.unwrap()
			.then(() => {
				toast.success(t("providers.configUpdated"));
				form.reset(data);
			})
			.catch((err) => {
				toast.error(t("providers.configUpdateFailed"), {
					description: getErrorMessage(err),
				});
			});
	};

	useEffect(() => {
		// Reset form with new provider's network_config when provider.name changes
		form.reset({
			network_config: {
				base_url: provider.network_config?.base_url || undefined,
				extra_headers: provider.network_config?.extra_headers,
				default_request_timeout_in_seconds:
					provider.network_config?.default_request_timeout_in_seconds ?? DefaultNetworkConfig.default_request_timeout_in_seconds,
				max_retries: provider.network_config?.max_retries ?? DefaultNetworkConfig.max_retries,
				retry_backoff_initial: provider.network_config?.retry_backoff_initial ?? DefaultNetworkConfig.retry_backoff_initial,
				retry_backoff_max: provider.network_config?.retry_backoff_max ?? DefaultNetworkConfig.retry_backoff_max,
				insecure_skip_verify: provider.network_config?.insecure_skip_verify ?? DefaultNetworkConfig.insecure_skip_verify,
				ca_cert_pem: toSecretVarFormValue(provider.network_config?.ca_cert_pem as SecretVar | string | undefined),
				stream_idle_timeout_in_seconds:
					provider.network_config?.stream_idle_timeout_in_seconds ?? DefaultNetworkConfig.stream_idle_timeout_in_seconds,
				keep_alive_timeout_in_seconds:
					provider.network_config?.keep_alive_timeout_in_seconds ?? DefaultNetworkConfig.keep_alive_timeout_in_seconds,
				max_conns_per_host: provider.network_config?.max_conns_per_host ?? DefaultNetworkConfig.max_conns_per_host,
				enforce_http2: provider.network_config?.enforce_http2 ?? DefaultNetworkConfig.enforce_http2,
				http2_ping_interval_in_seconds:
					provider.network_config?.http2_ping_interval_in_seconds ?? DefaultNetworkConfig.http2_ping_interval_in_seconds,
				allow_private_network: provider.network_config?.allow_private_network ?? DefaultNetworkConfig.allow_private_network,
			},
		});
	}, [form, provider.name, provider.network_config]);

	// HTTP/2 PING keepalives only apply when HTTP/2 is enforced
	const enforceHTTP2 = form.watch("network_config.enforce_http2");

	const baseURLRequired = isCustomProvider;
	const hideBaseURL = provider.name === "vllm" || provider.name === "ollama" || provider.name === "sgl";

	return (
		<Form {...form}>
			<form onSubmit={form.handleSubmit(onSubmit)}>
				{/* Network Configuration */}
				<div className="space-y-4 px-4 pb-6 md:px-6">
					<div className="grid grid-cols-1 gap-4">
						{!hideBaseURL && (
							<FormField
								control={form.control}
								name="network_config.base_url"
								render={({ field }) => (
									<FormItem>
										<FormLabel>
											{t(baseURLRequired ? "providers.labelRequired" : "providers.labelOptional", { label: t("providers.baseURL") })}
										</FormLabel>
										<FormControl>
											<Input
												placeholder={isCustomProvider ? "https://api.your-provider.com" : "https://api.example.com"}
												{...field}
												value={field.value || ""}
												disabled={!hasUpdateProviderAccess}
											/>
										</FormControl>
										<FormMessage />
									</FormItem>
								)}
							/>
						)}
						<div className="flex w-full flex-row items-start gap-4">
							<FormField
								control={form.control}
								name="network_config.default_request_timeout_in_seconds"
								render={({ field }) => (
									<FormItem className="flex-1">
										<FormLabel>{t("providers.networkConfig.timeoutSeconds")}</FormLabel>
										<FormControl>
											<Input
												placeholder="30"
												{...field}
												value={field.value === undefined || Number.isNaN(field.value) ? "" : field.value}
												disabled={!hasUpdateProviderAccess}
												onChange={(e) => {
													const value = e.target.value;
													if (value === "") {
														field.onChange(undefined);
														return;
													}
													const parsed = Number(value);
													if (!Number.isNaN(parsed)) {
														field.onChange(parsed);
													}
													form.trigger("network_config");
												}}
											/>
										</FormControl>
										<FormDescription>{secondsToHumanReadable(field.value, plural)}</FormDescription>
										<FormMessage />
									</FormItem>
								)}
							/>
							<FormField
								control={form.control}
								name="network_config.stream_idle_timeout_in_seconds"
								render={({ field }) => (
									<FormItem className="flex-1">
										<FormLabel>{t("providers.networkConfig.streamIdleTimeout")}</FormLabel>
										<FormControl>
											<Input
												placeholder="60"
												data-testid="network-config-stream-idle-timeout-input"
												{...field}
												value={field.value === undefined || Number.isNaN(field.value) ? "" : field.value}
												disabled={!hasUpdateProviderAccess}
												onChange={(e) => {
													const value = e.target.value;
													if (value === "") {
														field.onChange(undefined);
														return;
													}
													const parsed = Number(value);
													if (!Number.isNaN(parsed)) {
														field.onChange(parsed);
													}
													form.trigger("network_config");
												}}
											/>
										</FormControl>
										<FormDescription>
											{field.value ? `${secondsToHumanReadable(field.value, plural)} ` : ""}
											{t("providers.networkConfig.streamIdleHint")}
										</FormDescription>
										<FormMessage />
									</FormItem>
								)}
							/>
							<FormField
								control={form.control}
								name="network_config.max_retries"
								render={({ field }) => (
									<FormItem className="flex-1">
										<FormLabel>{t("providers.maxRetries")}</FormLabel>
										<FormControl>
											<Input
												placeholder="0"
												{...field}
												value={field.value === undefined || Number.isNaN(field.value) ? "" : field.value}
												disabled={!hasUpdateProviderAccess}
												onChange={(e) => {
													const value = e.target.value;
													if (value === "") {
														field.onChange(undefined);
														return;
													}
													const parsed = Number(value);
													if (!Number.isNaN(parsed)) {
														field.onChange(parsed);
													}
													form.trigger("network_config");
												}}
											/>
										</FormControl>
										<FormMessage />
									</FormItem>
								)}
							/>
						</div>
						<div className="flex w-full flex-row items-start gap-4">
							<FormField
								control={form.control}
								name="network_config.retry_backoff_initial"
								render={({ field }) => (
									<FormItem className="flex-1">
										<FormLabel>{t("providers.networkConfig.initialBackoff")}</FormLabel>
										<FormControl>
											<Input
												placeholder="e.g 500"
												{...field}
												value={field.value === undefined || Number.isNaN(field.value) ? "" : field.value}
												disabled={!hasUpdateProviderAccess}
												onChange={(e) => {
													const value = e.target.value;
													if (value === "") {
														field.onChange(undefined);
														return;
													}
													const parsed = Number(value);
													if (!Number.isNaN(parsed)) {
														field.onChange(parsed);
													}
													form.trigger("network_config");
												}}
											/>
										</FormControl>
										<FormMessage />
									</FormItem>
								)}
							/>
							<FormField
								control={form.control}
								name="network_config.retry_backoff_max"
								render={({ field }) => (
									<FormItem className="flex-1">
										<FormLabel>{t("providers.networkConfig.maxBackoff")}</FormLabel>
										<FormControl>
											<Input
												placeholder="e.g 10000"
												{...field}
												value={field.value === undefined || Number.isNaN(field.value) ? "" : field.value}
												disabled={!hasUpdateProviderAccess}
												onChange={(e) => {
													const value = e.target.value;
													if (value === "") {
														field.onChange(undefined);
														return;
													}
													const parsed = Number(value);
													if (!Number.isNaN(parsed)) {
														field.onChange(parsed);
													}
													form.trigger("network_config");
												}}
											/>
										</FormControl>
										<FormMessage />
									</FormItem>
								)}
							/>
						</div>
						<div className="flex w-full flex-row items-start gap-4">
							<FormField
								control={form.control}
								name="network_config.max_conns_per_host"
								render={({ field }) => (
									<FormItem className="flex-1">
										<FormLabel>{t("providers.networkConfig.maxConnsPerHost")}</FormLabel>
										<FormControl>
											<Input
												data-testid="network-config-max-conns-per-host-input"
												placeholder="5000"
												{...field}
												value={field.value === undefined || Number.isNaN(field.value) ? "" : field.value}
												disabled={!hasUpdateProviderAccess}
												onChange={(e) => {
													const value = e.target.value;
													if (value === "") {
														field.onChange(undefined);
														return;
													}
													const parsed = Number(value);
													if (!Number.isNaN(parsed)) {
														field.onChange(parsed);
													}
													form.trigger("network_config");
												}}
											/>
										</FormControl>
										<FormDescription>{t("providers.networkConfig.maxConnsHint")}</FormDescription>
										<FormMessage />
									</FormItem>
								)}
							/>
							<FormField
								control={form.control}
								name="network_config.keep_alive_timeout_in_seconds"
								render={({ field }) => (
									<FormItem className="flex-1">
										<FormLabel>{t("providers.networkConfig.keepAliveTimeout")}</FormLabel>
										<FormControl>
											<Input
												data-testid="network-config-keep-alive-timeout-input"
												placeholder="30"
												{...field}
												value={field.value === undefined || Number.isNaN(field.value) ? "" : field.value}
												disabled={!hasUpdateProviderAccess}
												onChange={(e) => {
													const value = e.target.value;
													if (value === "") {
														field.onChange(undefined);
														return;
													}
													const parsed = Number(value);
													if (!Number.isNaN(parsed)) {
														field.onChange(parsed);
													}
													form.trigger("network_config");
												}}
											/>
										</FormControl>
										<FormDescription>
											{field.value ? `${secondsToHumanReadable(field.value, plural)} ` : ""}
											{t("providers.networkConfig.keepAliveHint")}
										</FormDescription>
										<FormMessage />
									</FormItem>
								)}
							/>
						</div>
						<FormField
							control={form.control}
							name="network_config.enforce_http2"
							render={({ field }) => (
								<FormItem className="flex flex-row items-center justify-between">
									<div className="space-y-0.5">
										<FormLabel>{t("providers.networkConfig.enforceHttp2")}</FormLabel>
										<FormDescription>{t("providers.networkConfig.enforceHttp2Hint")}</FormDescription>
									</div>
									<FormControl>
										<Switch
											checked={field.value ?? false}
											onCheckedChange={field.onChange}
											disabled={!hasUpdateProviderAccess}
											data-testid="network-config-enforce-http2"
										/>
									</FormControl>
								</FormItem>
							)}
						/>
						<FormField
							control={form.control}
							name="network_config.http2_ping_interval_in_seconds"
							render={({ field }) => (
								<FormItem>
									<FormLabel>{t("providers.networkConfig.http2PingInterval")}</FormLabel>
									<FormControl>
										<Input
											data-testid="network-config-http2-ping-interval-input"
											placeholder="0"
											{...field}
											value={field.value === undefined || Number.isNaN(field.value) ? "" : field.value}
											disabled={!hasUpdateProviderAccess || !enforceHTTP2}
											onChange={(e) => {
												const value = e.target.value;
												if (value === "") {
													field.onChange(undefined);
													return;
												}
												const parsed = Number(value);
												if (!Number.isNaN(parsed)) {
													field.onChange(parsed);
												}
												form.trigger("network_config");
											}}
										/>
									</FormControl>
									<FormDescription>
										{field.value ? `${secondsToHumanReadable(field.value, plural)} ` : ""}
										{t("providers.networkConfig.http2PingHint")}
									</FormDescription>
									<FormMessage />
								</FormItem>
							)}
						/>
						<FormField
							control={form.control}
							name="network_config.allow_private_network"
							render={({ field }) => (
								<FormItem className="flex flex-row items-center justify-between">
									<div className="space-y-0.5">
										<FormLabel>{t("providers.networkConfig.allowPrivateNetwork")}</FormLabel>
										<FormDescription>{t("providers.networkConfig.allowPrivateNetworkHint")}</FormDescription>
									</div>
									<FormControl>
										<Switch
											checked={field.value ?? false}
											onCheckedChange={field.onChange}
											disabled={!hasUpdateProviderAccess}
											data-testid="network-config-allow-private-network"
										/>
									</FormControl>
								</FormItem>
							)}
						/>
						<FormField
							control={form.control}
							name="network_config.extra_headers"
							render={({ field }) => (
								<FormItem>
									<FormControl>
										<HeadersTable
											value={field.value || {}}
											onChange={field.onChange}
											keyPlaceholder={t("providers.networkConfig.headerName")}
											valuePlaceholder={t("providers.networkConfig.headerValue")}
											label={t("providers.networkConfig.extraHeaders")}
											disabled={!hasUpdateProviderAccess}
										/>
									</FormControl>
									<FormMessage />
								</FormItem>
							)}
						/>
						<Accordion type="single" collapsible className="w-full">
							<AccordionItem value="tls-config" className="border-b-0">
								<AccordionTrigger className="py-0" data-testid="tls-config-trigger">
									<span className="text-sm font-medium">{t("providers.networkConfig.tlsCertificate")}</span>
								</AccordionTrigger>
								<AccordionContent className="space-y-4 pt-4 pb-0">
									<FormField
										control={form.control}
										name="network_config.insecure_skip_verify"
										render={({ field }) => (
											<FormItem className="flex flex-row items-center justify-between rounded-lg border p-4">
												<div className="space-y-0.5">
													<FormLabel>{t("providers.networkConfig.skipTls")}</FormLabel>
													<FormDescription>{t("providers.networkConfig.skipTlsHint")}</FormDescription>
												</div>
												<FormControl>
													<Switch
														checked={field.value ?? false}
														onCheckedChange={field.onChange}
														disabled={!hasUpdateProviderAccess}
														data-testid="network-config-insecure-skip-verify"
													/>
												</FormControl>
											</FormItem>
										)}
									/>
									<FormField
										control={form.control}
										name="network_config.ca_cert_pem"
										render={({ field }) => (
											<FormItem>
												<FormLabel>{t("providers.labelOptional", { label: t("providers.networkConfig.caCertPem") })}</FormLabel>
												<FormControl>
													<SecretVarInput
														variant="textarea"
														placeholder={`-----BEGIN CERTIFICATE-----
...
-----END CERTIFICATE----- or env.OPENAI_CA_CERT_PEM`}
														className="font-mono text-xs"
														rows={6}
														hideValueWhenEnv
														redactNonEnvValue
														{...field}
														value={field.value}
														disabled={!hasUpdateProviderAccess}
														data-testid="network-config-ca-cert-pem"
													/>
												</FormControl>
												<FormDescription>{t("providers.networkConfig.caCertPemHint")}</FormDescription>
												<FormMessage />
											</FormItem>
										)}
									/>
								</AccordionContent>
							</AccordionItem>
						</Accordion>
					</div>
				</div>

				{/* Form Actions */}
				<div className="bg-card sticky bottom-0 flex justify-end space-x-2 rounded-b-sm border-t px-4 py-4 md:px-6">
					{!hideBaseURL && (
						<Button
							type="button"
							variant="outline"
							onClick={() => {
								form.reset({
									network_config: undefined,
								});
								onSubmit(form.getValues());
							}}
							disabled={
								!hasUpdateProviderAccess ||
								isUpdatingProvider ||
								!provider.network_config ||
								!provider.network_config.base_url ||
								provider.network_config.base_url.trim() === ""
							}
						>
							{t("providers.removeConfig")}
						</Button>
					)}
					<TooltipProvider>
						<Tooltip>
							<TooltipTrigger asChild>
								<Button type="submit" disabled={!form.formState.isDirty || !hasUpdateProviderAccess} isLoading={isUpdatingProvider}>
									{t("providers.networkConfig.save")}
								</Button>
							</TooltipTrigger>
							{(!form.formState.isDirty || !form.formState.isValid) && (
								<TooltipContent>
									<p>
										{!form.formState.isDirty && !form.formState.isValid
											? t("providers.networkConfig.noChangesAndErrors")
											: !form.formState.isDirty
												? t("providers.networkConfig.noChanges")
												: t("providers.networkConfig.fixValidation")}
									</p>
								</TooltipContent>
							)}
						</Tooltip>
					</TooltipProvider>
				</div>
			</form>
		</Form>
	);
}