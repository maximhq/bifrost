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
import { CredentialFields } from "../fragments/credentialFields";
import { HeaderPairsTable } from "../fragments/headerPairsTable";
import { InfoLabel } from "../fragments/infoLabel";
import { UsageFields } from "../fragments/usageFields";

const TABS = [
	{ id: "general", label: "General" },
	{ id: "authentication", label: "Authentication" },
	{ id: "advanced", label: "Advanced" },
	{ id: "network", label: "Network" },
	{ id: "proxy", label: "Proxy" },
] as const;

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
			toast.success("Relay configuration updated successfully");
		} catch (e) {
			toast.error("Failed to update relay configuration", { description: getErrorMessage(e) });
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
		toast.error("Relay configuration is invalid", { description: `Fix the "${first}" field before saving.` });
	};

	const saveButton = (label: string) => (
		<div className="bg-card sticky bottom-0 z-10 -mx-4 flex justify-end space-x-2 rounded-b-sm border-t px-4 py-4 md:-mx-6 md:px-6">
			<Button
				type="submit"
				disabled={!form.formState.isDirty || !canUpdate || isLoading}
				isLoading={isLoading}
				data-testid={`relay-save-${label.toLowerCase()}`}
			>
				Save {label} Configuration
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
							Relay configuration
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
											{TABS.map((t) => (
												<TabsTrigger key={t.id} value={t.id} data-testid={`relay-tab-${t.id}`} className="flex-none px-3 whitespace-nowrap">
													{t.label}
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
														label="Base URL (Required)"
														info="Every request is pinned under this origin and path; callers cannot reach another host."
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
													<InfoLabel label="Description (Optional)" info="Shown in the relay list; for your team only." />
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
											label="Relay Any Path"
											description="Relay requests that match no route instead of refusing them with 404. Unrouted POSTs use the relay pricing."
											disabled={!canUpdate}
											testId="relay-skip-route-check"
										/>
										{skipRouteCheck && (
											<Alert variant="warning" data-testid="relay-all-routes-warning">
												<AlertTriangle className="h-4 w-4" />
												<AlertDescription>
													Every path on the upstream is reachable with this relay&apos;s credential. Prefer listing routes.
												</AlertDescription>
											</Alert>
										)}
										<Separator />
										<div className="space-y-1">
											<div className="text-sm font-medium">Pricing</div>
											<p className="text-muted-foreground text-sm">
												The default price of a counted call. Routes can override any field. The cost is charged to the caller&apos;s virtual
												key, team and customer budgets.
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
										{saveButton("General")}
									</TabsContent>

									<TabsContent value="authentication" className="space-y-6 px-4 md:px-6">
										<CredentialFields form={form} disabled={!canUpdate} />
										{saveButton("Authentication")}
									</TabsContent>

									<TabsContent value="advanced" className="space-y-6 px-4 md:px-6">
										<div className="text-sm font-medium">Streaming</div>
										<FormField
											control={form.control}
											name="streaming.paths"
											render={({ field }) => (
												<FormItem>
													<InfoLabel
														label="Always-Stream Paths (Optional)"
														info="One route glob per line; matching paths are always relayed as streams. Otherwise an event-stream reply from the upstream is streamed and anything else is read and priced."
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
										<div className="text-sm font-medium">Usage Extraction</div>
										<div className="flex w-full flex-row items-start gap-4">
											{(
												[
													[
														"cost_path",
														"Reported Cost Path (Optional)",
														"usage.cost",
														"A USD cost the upstream reports; added to the formula.",
													],
													["model_path", "Model Path (Optional)", "model", "Where the model is read from, for the logs."],
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
															label="Model Source"
															info="Whether Model Path names a field of the request body (the default) or a query parameter, as Deepgram's ?model=."
														/>
														<Select onValueChange={field.onChange} value={field.value ?? "body"} disabled={!canUpdate}>
															<FormControl>
																<SelectTrigger data-testid="relay-model-source">
																	<SelectValue />
																</SelectTrigger>
															</FormControl>
															<SelectContent>
																<SelectItem value="body">Body</SelectItem>
																<SelectItem value="query">Query</SelectItem>
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
											label="Ignore Reported Cost"
											description="Charge only your fee and unit pricing, never the cost the upstream reports."
											disabled={!canUpdate}
											testId="relay-ignore-upstream-cost"
										/>
										<div className="flex w-full flex-row items-start gap-4">
											{(
												[
													["input_tokens_path", "Input Tokens Path"],
													["output_tokens_path", "Output Tokens Path"],
													["total_tokens_path", "Total Tokens Path"],
												] as const
											).map(([key, label]) => (
												<FormField
													key={key}
													control={form.control}
													name={`usage.${key}`}
													render={({ field }) => (
														<FormItem className="flex-1">
															<InfoLabel label={label} info="Recorded on Relay Logs, never priced." />
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
												label="Extra Headers (Optional)"
												info="Sent on every request; a caller's copy of the same header is dropped."
												inForm={false}
											/>
											<HeaderPairsTable
												value={form.watch("extra_headers")}
												onChange={(pairs) => form.setValue("extra_headers", pairs, { shouldDirty: true, shouldValidate: true })}
												errors={form.formState.errors.extra_headers}
												keyPlaceholder="X-Tenant"
												valuePlaceholder="value or env.HEADER_VALUE"
												disabled={!canUpdate}
												useSecretVarInput
												testIdPrefix="relay-extra-header"
											/>
										</div>
										{saveButton("Advanced")}
									</TabsContent>

									<TabsContent value="network" className="space-y-6 px-4 md:px-6">
										<Alert>
											<Info className="h-4 w-4" />
											<AlertDescription>
												How Bifrost talks to the upstream directly: request and stream timeouts, the connection pool and TLS trust. To send
												the traffic through a forward proxy instead, use the Proxy tab.
											</AlertDescription>
										</Alert>
										{/* Laid out as the provider config's Network tab: plain labels, the help text under each input. */}
										<div className="flex w-full flex-row items-start gap-4">
											<FormField
												control={form.control}
												name="network.timeout_seconds"
												render={({ field }) => (
													<FormItem className="flex-1">
														<FormLabel>Timeout (seconds)</FormLabel>
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
															{field.value ? `${secondsToHumanReadable(field.value)}. ` : ""}Longest a buffered call may take, end to end.
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
														<FormLabel>Stream Idle Timeout (seconds)</FormLabel>
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
															{field.value ? `${secondsToHumanReadable(field.value)}. ` : ""}A stream is closed when no data arrives for
															this long.
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
														<FormLabel>Max Connections per Host</FormLabel>
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
														<FormDescription>Upper bound on open connections to the upstream.</FormDescription>
														<FormMessage />
													</FormItem>
												)}
											/>
										</div>
										<SwitchField
											control={form.control}
											name="network.insecure_skip_verify"
											label="Insecure Skip Verify"
											description="Skip TLS verification of the upstream. Only for self-signed certificates."
											disabled={!canUpdate}
											testId="relay-insecure-skip-verify"
										/>
										<FormField
											control={form.control}
											name="network.ca_cert_pem"
											render={({ field }) => (
												<FormItem>
													<InfoLabel
														label="CA Certificate (PEM) (Optional)"
														info="A CA bundle to trust when the upstream uses a private or self-signed certificate."
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
										{saveButton("Network")}
									</TabsContent>

									<TabsContent value="proxy" className="space-y-6 px-4 md:px-6">
										<Alert>
											<Info className="h-4 w-4" />
											<AlertDescription>
												Sends this relay&apos;s outbound traffic through a forward proxy (HTTP, SOCKS5, or the HTTP_PROXY environment). With
												no proxy set, the global proxy is used when it is enabled for inference. Timeouts and TLS live on the Network tab.
											</AlertDescription>
										</Alert>
										<FormField
											control={form.control}
											name="proxy.type"
											render={({ field }) => (
												<FormItem>
													<InfoLabel
														label="Proxy Type"
														info="HTTP or SOCKS5 sends traffic through the proxy below; Environment uses HTTP_PROXY, HTTPS_PROXY and NO_PROXY."
													/>
													<Select
														onValueChange={field.onChange}
														value={field.value === "inherit" ? (inheritsGlobalProxy ? "" : "none") : field.value}
														disabled={!canUpdate}
													>
														<FormControl>
															<SelectTrigger className="w-48" data-testid="relay-proxy-type">
																<SelectValue placeholder="Global proxy" />
															</SelectTrigger>
														</FormControl>
														<SelectContent>
															<SelectItem value="none">None (direct)</SelectItem>
															<SelectItem value="http">HTTP</SelectItem>
															<SelectItem value="socks5">SOCKS5</SelectItem>
															<SelectItem value="environment">Environment</SelectItem>
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
														<InfoLabel label="Proxy URL" info="The proxy address, or an env. reference to it." />
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
															<FormLabel>Username</FormLabel>
															<FormControl>
																<SecretVarInput
																	placeholder="Proxy username or env.PROXY_USERNAME"
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
															<FormLabel>Password</FormLabel>
															<FormControl>
																<SecretVarInput
																	type="password"
																	placeholder="Proxy password or env.PROXY_PASSWORD"
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
															label="CA Certificate (PEM) (Optional)"
															info="A PEM-encoded CA certificate to trust for TLS connections through an SSL-intercepting proxy, or an env. reference to it."
														/>
														<FormControl>
															<SecretVarInput
																variant="textarea"
																rows={6}
																className="font-mono text-xs"
																placeholder={"-----BEGIN CERTIFICATE-----\n...\n-----END CERTIFICATE----- or env.PROXY_CA_CERT_PEM"}
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
												Remove configuration
											</Button>
											<Button
												type="submit"
												disabled={!form.formState.isDirty || !canUpdate || isLoading}
												isLoading={isLoading}
												data-testid="relay-save-proxy"
											>
												Save Proxy Configuration
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