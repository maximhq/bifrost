import { Alert, AlertDescription } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { Form, FormControl, FormDescription, FormField, FormItem, FormLabel, FormMessage } from "@/components/ui/form";
import { Input } from "@/components/ui/input";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { SecretVarInput } from "@/components/ui/secretVarInput";
import { Sheet, SheetContent, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { Separator } from "@/components/ui/separator";
import { Switch } from "@/components/ui/switch";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { Textarea } from "@/components/ui/textarea";
import { getErrorMessage, useGetCoreConfigQuery, useUpdateRelayMutation } from "@/lib/store";
import type { Relay } from "@/lib/types/relays";
import { relayFormSchema, type RelayFormValues } from "@/lib/types/schemas";
import { cn } from "@/lib/utils";
import { toRelayConfig, toRelayForm } from "@/lib/utils/relayForm";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { zodResolver } from "@hookform/resolvers/zod";
import { AlertTriangle, Info, Waypoints } from "lucide-react";
import { useEffect, useRef, useState } from "react";
import { useForm, type Control, type FieldValues } from "react-hook-form";
import { secondsToHumanReadable } from "@/lib/utils/strings";
import { toast } from "sonner";
import { useTranslation } from "react-i18next";
import { CredentialFields } from "../fragments/credentialFields";
import { HeaderPairsTable } from "../fragments/headerPairsTable";
import { InfoLabel } from "../fragments/infoLabel";
import { UsageFields } from "../fragments/usageFields";

const TAB_IDS = ["general", "authentication", "advanced", "network", "proxy"] as const;

// Which tab a validation error belongs to, so a refused save opens the tab that needs fixing.
const FIELD_TAB: Record<string, string> = {
	name: "general",
	description: "general",
	base_url: "general",
	auth: "authentication",
	credential_value: "authentication",
	aws: "authentication",
	usage: "general",
	streaming: "advanced",
	extra_headers: "advanced",
	network: "network",
	proxy: "proxy",
};

function SwitchField({
	control,
	name,
	label,
	description,
	disabled,
	testId,
}: {
	control: Control<RelayFormValues>;
	name: "skip_route_check" | "ignore_upstream_cost" | "network.insecure_skip_verify";
	label: string;
	description: string;
	disabled?: boolean;
	testId: string;
}) {
	return (
		<FormField
			control={control}
			name={name}
			render={({ field }) => (
				<FormItem className="flex flex-row items-center justify-between">
					<InfoLabel label={label} info={description} />
					<FormControl>
						<Switch checked={!!field.value} onCheckedChange={field.onChange} disabled={disabled} data-testid={testId} />
					</FormControl>
				</FormItem>
			)}
		/>
	);
}

/** The endpoint's configuration, in the same tabbed sheet shape as a provider's configuration. */
export function EndpointConfigSheet({ show, onCancel, endpoint }: { show: boolean; onCancel: () => void; endpoint: Relay }) {
	const { t } = useTranslation("config");
	const tabLabels = {
		general: t("relays.endpointConfig.tabs.general"),
		authentication: t("relays.endpointConfig.tabs.authentication"),
		advanced: t("relays.endpointConfig.tabs.advanced"),
		network: t("relays.endpointConfig.tabs.network"),
		proxy: t("relays.endpointConfig.tabs.proxy"),
	};

	const canUpdate = useRbac(RbacResource.Relays, RbacOperation.Update);
	const [updateEndpoint, { isLoading }] = useUpdateRelayMutation();
	const [tab, setTab] = useState<string>("general");
	const form = useForm<RelayFormValues>({
		resolver: zodResolver(relayFormSchema),
		mode: "onChange",
		defaultValues: toRelayForm(endpoint),
	});
	const proxyType = form.watch("proxy.type");
	// A relay with no proxy set uses the global proxy only when it is on for inference (as providers
	// do); otherwise it connects directly, so that is what the unset select shows.
	const { data: coreConfig } = useGetCoreConfigQuery({ fromDB: true });
	const globalProxy = coreConfig?.proxy_config;
	const inheritsGlobalProxy = !!globalProxy?.enabled && !!globalProxy.enable_for_inference && !!globalProxy.url?.trim();
	const skipRouteCheck = form.watch("skip_route_check");

	// Every open starts from the latest endpoint, so edits abandoned on close (or routes changed
	// since) are never saved back. A refetch while the sheet stays open does not overwrite unsaved edits.
	const wasShown = useRef(false);
	useEffect(() => {
		const opened = show && !wasShown.current;
		wasShown.current = show;
		if (opened || (show && !form.formState.isDirty)) form.reset(toRelayForm(endpoint));
	}, [show, endpoint, form]);

	const onSubmit = async (values: RelayFormValues) => {
		try {
			const saved = await updateEndpoint({ name: endpoint.name, data: toRelayConfig(values) }).unwrap();
			form.reset(toRelayForm(saved));
			toast.success(t("relays.endpointConfig.toastUpdated"));
		} catch (e) {
			toast.error(t("relays.endpointConfig.toastFailed"), { description: getErrorMessage(e) });
		}
	};
	const onInvalid = (errors: Record<string, unknown>) => {
		const first = Object.keys(errors)[0];
		if (!first) return;
		if (FIELD_TAB[first]) {
			setTab(FIELD_TAB[first]);
			return;
		}
		// A field this sheet has no input for (routes are edited elsewhere): say so instead of refusing silently.
		toast.error(t("relays.endpointConfig.toastInvalid"), { description: t("relays.endpointConfig.fixField", { field: first }) });
	};

	const saveButton = (id: "general" | "authentication" | "advanced" | "network") => (
		<div className="bg-card sticky bottom-0 z-10 -mx-4 flex justify-end space-x-2 rounded-b-sm border-t px-4 py-4 md:-mx-6 md:px-6">
			<Button
				type="submit"
				disabled={!form.formState.isDirty || !canUpdate || isLoading}
				isLoading={isLoading}
				data-testid={`relay-save-${id}`}
			>
				{t("relays.endpointConfig.saveTab", { tab: tabLabels[id] })}
			</Button>
		</div>
	);
	const usageControl = form.control as unknown as Control<FieldValues>;

	return (
		<Sheet open={show} onOpenChange={(open) => !open && onCancel()}>
			<SheetContent className="p-0 pt-4 sm:max-w-[50%]" data-testid="relay-config-sheet">
				<SheetHeader className="flex flex-col items-start py-4" headerClassName="mb-0 sticky -top-4 bg-card z-10 px-4 md:px-8">
					<SheetTitle>
						<div className="font-lg flex items-center gap-2">
							<Waypoints className="h-5 w-5" />
								{t("relays.endpointConfig.title")}
						</div>
					</SheetTitle>
				</SheetHeader>
				<div className="px-4 py-4 md:px-8">
					<div className="w-full rounded-sm border">
						<Form {...form}>
							<form onSubmit={form.handleSubmit(onSubmit, onInvalid)}>
								<Tabs value={tab} onValueChange={setTab}>
									<div className="mb-4 w-full">
										<TabsList className="h-10 w-max min-w-full justify-start rounded-tl-sm rounded-tr-sm rounded-br-none rounded-bl-none">
											{TAB_IDS.map((id) => (
												<TabsTrigger key={id} value={id} data-testid={`relay-tab-${id}`} className="flex-none px-3 whitespace-nowrap">
													{tabLabels[id]}
												</TabsTrigger>
											))}
										</TabsList>
									</div>

									<TabsContent value="general" className="space-y-6 px-4 md:px-6">
										<FormField
											control={form.control}
											name="base_url"
											render={({ field }) => (
												<FormItem>
													<InfoLabel
														label={t("relays.fields.baseUrlLabel")}
														info={t("relays.fields.baseUrlInfo")}
													/>
													<FormControl>
														<Input placeholder="https://api.example.com" disabled={!canUpdate} data-testid="relay-base-url" {...field} />
													</FormControl>
													<FormMessage />
												</FormItem>
											)}
										/>
										<FormField
											control={form.control}
											name="description"
											render={({ field }) => (
												<FormItem>
													<InfoLabel label={t("relays.endpointConfig.descriptionLabel")} info={t("relays.endpointConfig.descriptionInfo")} />
													<FormControl>
														<Textarea rows={2} disabled={!canUpdate} {...field} value={field.value ?? ""} />
													</FormControl>
													<FormMessage />
												</FormItem>
											)}
										/>
										<SwitchField
											control={form.control}
											name="skip_route_check"
											label={t("relays.endpointConfig.anyPathLabel")}
											description={t("relays.endpointConfig.anyPathInfo")}
											disabled={!canUpdate}
											testId="relay-skip-route-check"
										/>
										{skipRouteCheck && (
											<Alert variant="warning" data-testid="relay-all-routes-warning">
												<AlertTriangle className="h-4 w-4" />
												<AlertDescription>
													{t("relays.endpointConfig.anyPathWarning")}
												</AlertDescription>
											</Alert>
										)}
										<Separator />
										<div className="space-y-1">
											<div className="text-sm font-medium">{t("relays.endpointConfig.pricingTitle")}</div>
											<p className="text-muted-foreground text-sm">
												{t("relays.endpointConfig.pricingInfo")}
											</p>
										</div>
										<UsageFields
											control={usageControl}
											name="usage"
											value={form.watch("usage")}
											disabled={!canUpdate}
											ignoreUpstreamCost={form.watch("ignore_upstream_cost")}
											testIdPrefix="relay-usage"
										/>
										{saveButton("general")}
									</TabsContent>

									<TabsContent value="authentication" className="space-y-6 px-4 md:px-6">
										<CredentialFields form={form} disabled={!canUpdate} />
										{saveButton("authentication")}
									</TabsContent>

									<TabsContent value="advanced" className="space-y-6 px-4 md:px-6">
										<div className="text-sm font-medium">{t("relays.endpointConfig.streamingTitle")}</div>
										<FormField
											control={form.control}
											name="streaming.paths"
											render={({ field }) => (
												<FormItem>
													<InfoLabel
														label={t("relays.endpointConfig.streamPathsLabel")}
														info={t("relays.endpointConfig.streamPathsInfo")}
													/>
													<FormControl>
														<Textarea
															rows={3}
															className="font-mono text-xs"
															placeholder="/v1/chat/**"
															disabled={!canUpdate}
															data-testid="relay-streaming-paths"
															value={field.value.join("\n")}
															onChange={(e) => field.onChange(e.target.value.split("\n"))}
															onBlur={() => field.onChange(field.value.map((p) => p.trim()).filter(Boolean))}
														/>
													</FormControl>
													<FormMessage />
												</FormItem>
											)}
										/>
										<Separator />
										<div className="text-sm font-medium">{t("relays.endpointConfig.usageTitle")}</div>
										<div className="flex w-full flex-row items-start gap-4">
											{(
												[
													[
														"cost_path",
														t("relays.endpointConfig.costPathLabel"),
											"usage.cost",
											t("relays.endpointConfig.costPathInfo"),
													],
													["model_path", t("relays.endpointConfig.modelPathLabel"), "model", t("relays.endpointConfig.modelPathInfo")],
												] as const
											).map(([key, label, placeholder, description]) => (
												<FormField
													key={key}
													control={form.control}
													name={`usage.${key}`}
													render={({ field }) => (
														<FormItem className="flex-1">
															<InfoLabel label={label} info={description} />
															<FormControl>
																<Input
																	placeholder={placeholder}
																	disabled={!canUpdate}
																	data-testid={`relay-${key.replaceAll("_", "-")}`}
																	value={field.value ?? ""}
																	onChange={(e) => field.onChange(e.target.value || undefined)}
																/>
															</FormControl>
															<FormMessage />
														</FormItem>
													)}
												/>
											))}
											<FormField
												control={form.control}
												name="usage.model_source"
												render={({ field }) => (
													<FormItem className="w-40">
														<InfoLabel
															label={t("relays.endpointConfig.modelSourceLabel")}
															info={t("relays.endpointConfig.modelSourceInfo")}
														/>
														<Select onValueChange={field.onChange} value={field.value ?? "body"} disabled={!canUpdate}>
															<FormControl>
																<SelectTrigger data-testid="relay-model-source">
																	<SelectValue />
																</SelectTrigger>
															</FormControl>
															<SelectContent>
																<SelectItem value="body">{t("relays.endpointConfig.sourceBody")}</SelectItem>
																<SelectItem value="query">{t("relays.endpointConfig.sourceQuery")}</SelectItem>
															</SelectContent>
														</Select>
														<FormMessage />
													</FormItem>
												)}
											/>
										</div>
										<SwitchField
											control={form.control}
											name="ignore_upstream_cost"
											label={t("relays.endpointConfig.ignoreCostLabel")}
											description={t("relays.endpointConfig.ignoreCostInfo")}
											disabled={!canUpdate}
											testId="relay-ignore-upstream-cost"
										/>
										<div className="flex w-full flex-row items-start gap-4">
											{(
												[
													["input_tokens_path", t("relays.endpointConfig.inputTokensPath")],
													["output_tokens_path", t("relays.endpointConfig.outputTokensPath")],
													["total_tokens_path", t("relays.endpointConfig.totalTokensPath")],
												] as const
											).map(([key, label]) => (
												<FormField
													key={key}
													control={form.control}
													name={`usage.${key}`}
													render={({ field }) => (
														<FormItem className="flex-1">
															<InfoLabel label={label} info={t("relays.endpointConfig.tokensPathInfo")} />
															<FormControl>
																<Input
																	placeholder="usage.prompt_tokens"
																	disabled={!canUpdate}
																	value={field.value ?? ""}
																	onChange={(e) => field.onChange(e.target.value || undefined)}
																/>
															</FormControl>
															<FormMessage />
														</FormItem>
													)}
												/>
											))}
										</div>
										<Separator />
										<div className="space-y-2">
											<InfoLabel
												label={t("relays.endpointConfig.extraHeadersLabel")}
												info={t("relays.endpointConfig.extraHeadersInfo")}
												inForm={false}
											/>
											<HeaderPairsTable
												value={form.watch("extra_headers")}
												onChange={(pairs) => form.setValue("extra_headers", pairs, { shouldDirty: true, shouldValidate: true })}
												errors={form.formState.errors.extra_headers}
												keyPlaceholder="X-Tenant"
												valuePlaceholder={t("relays.endpointConfig.extraHeaderValuePlaceholder")}
												disabled={!canUpdate}
												useSecretVarInput
												testIdPrefix="relay-extra-header"
											/>
										</div>
										{saveButton("advanced")}
									</TabsContent>

									<TabsContent value="network" className="space-y-6 px-4 md:px-6">
										<Alert>
											<Info className="h-4 w-4" />
											<AlertDescription>
												{t("relays.endpointConfig.networkInfo")}
											</AlertDescription>
										</Alert>
										{/* Laid out as the provider config's Network tab: plain labels, the help text under each input. */}
										<div className="flex w-full flex-row items-start gap-4">
											<FormField
												control={form.control}
												name="network.timeout_seconds"
												render={({ field }) => (
													<FormItem className="flex-1">
														<FormLabel>{t("relays.endpointConfig.timeoutLabel")}</FormLabel>
														<FormControl>
															<Input
																placeholder="60"
																disabled={!canUpdate}
																value={field.value === undefined || Number.isNaN(field.value) ? "" : field.value}
																onChange={(e) => {
																	const value = e.target.value;
																	if (value === "") {
																		field.onChange(undefined);
																		return;
																	}
																	const parsed = Number(value);
																	if (!Number.isNaN(parsed)) field.onChange(parsed);
																}}
															/>
														</FormControl>
														<FormDescription>
															{field.value ? `${secondsToHumanReadable(field.value)}. ` : ""}
														{t("relays.endpointConfig.timeoutHelp")}
														</FormDescription>
														<FormMessage />
													</FormItem>
												)}
											/>
											<FormField
												control={form.control}
												name="network.stream_idle_timeout_in_seconds"
												render={({ field }) => (
													<FormItem className="flex-1">
														<FormLabel>{t("relays.endpointConfig.streamIdleLabel")}</FormLabel>
														<FormControl>
															<Input
																placeholder="60"
																disabled={!canUpdate}
																value={field.value === undefined || Number.isNaN(field.value) ? "" : field.value}
																onChange={(e) => {
																	const value = e.target.value;
																	if (value === "") {
																		field.onChange(undefined);
																		return;
																	}
																	const parsed = Number(value);
																	if (!Number.isNaN(parsed)) field.onChange(parsed);
																}}
															/>
														</FormControl>
														<FormDescription>
															{field.value ? `${secondsToHumanReadable(field.value)}. ` : ""}
														{t("relays.endpointConfig.streamIdleHelp")}
														</FormDescription>
														<FormMessage />
													</FormItem>
												)}
											/>
											<FormField
												control={form.control}
												name="network.max_conns_per_host"
												render={({ field }) => (
													<FormItem className="flex-1">
														<FormLabel>{t("relays.endpointConfig.maxConnsLabel")}</FormLabel>
														<FormControl>
															<Input
																placeholder="5000"
																disabled={!canUpdate}
																value={field.value === undefined || Number.isNaN(field.value) ? "" : field.value}
																onChange={(e) => {
																	const value = e.target.value;
																	if (value === "") {
																		field.onChange(undefined);
																		return;
																	}
																	const parsed = Number(value);
																	if (!Number.isNaN(parsed)) field.onChange(parsed);
																}}
															/>
														</FormControl>
														<FormDescription>{t("relays.endpointConfig.maxConnsHelp")}</FormDescription>
														<FormMessage />
													</FormItem>
												)}
											/>
										</div>
										<SwitchField
											control={form.control}
											name="network.insecure_skip_verify"
											label={t("relays.endpointConfig.insecureLabel")}
											description={t("relays.endpointConfig.insecureInfo")}
											disabled={!canUpdate}
											testId="relay-insecure-skip-verify"
										/>
										<FormField
											control={form.control}
											name="network.ca_cert_pem"
											render={({ field }) => (
												<FormItem>
													<InfoLabel
														label={t("relays.endpointConfig.caCertLabel")}
														info={t("relays.endpointConfig.caCertInfo")}
													/>
													<FormControl>
														<Textarea
															rows={5}
															className="font-mono text-xs"
															placeholder={"-----BEGIN CERTIFICATE-----\n...\n-----END CERTIFICATE-----"}
															disabled={!canUpdate}
															{...field}
															value={field.value ?? ""}
														/>
													</FormControl>
													<FormMessage />
												</FormItem>
											)}
										/>
										{saveButton("network")}
									</TabsContent>

									<TabsContent value="proxy" className="space-y-6 px-4 md:px-6">
										<Alert>
											<Info className="h-4 w-4" />
											<AlertDescription>
												{t("relays.endpointConfig.proxyInfo")}
											</AlertDescription>
										</Alert>
										<FormField
											control={form.control}
											name="proxy.type"
											render={({ field }) => (
												<FormItem>
													<InfoLabel
														label={t("relays.endpointConfig.proxyTypeLabel")}
														info={t("relays.endpointConfig.proxyTypeInfo")}
													/>
													<Select
														onValueChange={field.onChange}
														value={field.value === "inherit" ? (inheritsGlobalProxy ? "" : "none") : field.value}
														disabled={!canUpdate}
													>
														<FormControl>
															<SelectTrigger className="w-48" data-testid="relay-proxy-type">
																<SelectValue placeholder={t("relays.endpointConfig.globalProxy")} />
															</SelectTrigger>
														</FormControl>
														<SelectContent>
															<SelectItem value="none">{t("relays.endpointConfig.proxyNone")}</SelectItem>
															<SelectItem value="http">HTTP</SelectItem>
															<SelectItem value="socks5">SOCKS5</SelectItem>
															<SelectItem value="environment">{t("relays.endpointConfig.proxyEnvironment")}</SelectItem>
														</SelectContent>
													</Select>
													<FormMessage />
												</FormItem>
											)}
										/>
										<div
											className={cn(
												"space-y-4 pt-2",
												(proxyType === "inherit" || proxyType === "none" || proxyType === "environment") && "hidden",
											)}
										>
											<FormField
												control={form.control}
												name="proxy.url"
												render={({ field }) => (
													<FormItem>
														<InfoLabel label={t("relays.endpointConfig.proxyUrlLabel")} info={t("relays.endpointConfig.proxyUrlInfo")} />
														<FormControl>
															<SecretVarInput
																placeholder="http://proxy.example.com or env.PROXY_URL"
																disabled={!canUpdate}
																data-testid="relay-proxy-url"
																{...field}
																value={field.value}
															/>
														</FormControl>
														<FormMessage />
													</FormItem>
												)}
											/>
											<div className="grid grid-cols-1 gap-4 md:grid-cols-2">
												<FormField
													control={form.control}
													name="proxy.username"
													render={({ field }) => (
														<FormItem>
															<FormLabel>{t("relays.endpointConfig.proxyUsername")}</FormLabel>
															<FormControl>
																<SecretVarInput
																	placeholder={t("relays.endpointConfig.proxyUsernamePlaceholder")}
																	disabled={!canUpdate}
																	{...field}
																	value={field.value}
																/>
															</FormControl>
															<FormMessage />
														</FormItem>
													)}
												/>
												<FormField
													control={form.control}
													name="proxy.password"
													render={({ field }) => (
														<FormItem>
															<FormLabel>{t("relays.endpointConfig.proxyPassword")}</FormLabel>
															<FormControl>
																<SecretVarInput
																	type="password"
																	placeholder={t("relays.endpointConfig.proxyPasswordPlaceholder")}
																	hideValueWhenEnv
																	redactNonEnvValue
																	disabled={!canUpdate}
																	{...field}
																	value={field.value}
																/>
															</FormControl>
															<FormMessage />
														</FormItem>
													)}
												/>
											</div>
											<FormField
												control={form.control}
												name="proxy.ca_cert_pem"
												render={({ field }) => (
													<FormItem>
														<InfoLabel
															label={t("relays.endpointConfig.caCertLabel")}
															info={t("relays.endpointConfig.proxyCaCertInfo")}
														/>
														<FormControl>
															<SecretVarInput
																variant="textarea"
																rows={6}
																className="font-mono text-xs"
																placeholder={t("relays.endpointConfig.proxyCaCertPlaceholder")}
																hideValueWhenEnv
																redactNonEnvValue
																disabled={!canUpdate}
																{...field}
																value={field.value}
																data-testid="relay-proxy-ca-cert-pem"
															/>
														</FormControl>
														<FormMessage />
													</FormItem>
												)}
											/>
										</div>
										<div className="bg-card sticky bottom-0 z-10 -mx-4 flex justify-end space-x-2 rounded-b-sm border-t px-4 py-4 md:-mx-6 md:px-6">
											<Button
												type="button"
												variant="outline"
												disabled={!canUpdate || isLoading || !endpoint.proxy_config}
												onClick={() => {
													form.setValue("proxy", { type: "inherit" }, { shouldDirty: true });
													void form.handleSubmit(onSubmit, onInvalid)();
												}}
											>
												
													{t("relays.endpointConfig.removeConfig")}

											</Button>
											<Button
												type="submit"
												disabled={!form.formState.isDirty || !canUpdate || isLoading}
												isLoading={isLoading}
												data-testid="relay-save-proxy"
											>
												
													{t("relays.endpointConfig.saveProxy")}

											</Button>
										</div>
									</TabsContent>
								</Tabs>
							</form>
						</Form>
					</div>
				</div>
			</SheetContent>
		</Sheet>
	);
}