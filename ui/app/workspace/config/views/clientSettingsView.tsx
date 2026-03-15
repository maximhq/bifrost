"use client";

import { Accordion, AccordionContent, AccordionItem, AccordionTrigger } from "@/components/ui/accordion";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Switch } from "@/components/ui/switch";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { getErrorMessage, useGetCoreConfigQuery, useGetDroppedRequestsQuery, useUpdateCoreConfigMutation } from "@/lib/store";
import { CoreConfig, DefaultCoreConfig, DefaultGlobalHeaderFilterConfig, GlobalHeaderFilterConfig } from "@/lib/types/config";
import { cn } from "@/lib/utils";
import LargePayloadSettingsFragment from "@enterprise/components/large-payload/largePayloadSettingsFragment";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { useGetLargePayloadConfigQuery, useUpdateLargePayloadConfigMutation } from "@enterprise/lib/store/apis/largePayloadApi";
import { DefaultLargePayloadConfig, LargePayloadConfig } from "@enterprise/lib/types/largePayload";
import { Info, Plus, X } from "lucide-react";
import { useCallback, useEffect, useMemo, useState } from "react";
import { toast } from "sonner";

// Security headers that cannot be configured in allowlist/denylist
// These headers are always blocked for security reasons regardless of configuration
const SECURITY_HEADERS = [
	"proxy-authorization",
	"cookie",
	"host",
	"content-length",
	"connection",
	"transfer-encoding",
	"x-api-key",
	"x-goog-api-key",
	"x-bf-api-key",
	"x-bf-vk",
];

// Helper to check if a header is a security header
function isSecurityHeader(header: string): boolean {
	return SECURITY_HEADERS.includes(header.toLowerCase().trim());
}

// Helper to compare header filter configs
function headerFilterConfigEqual(a?: GlobalHeaderFilterConfig, b?: GlobalHeaderFilterConfig): boolean {
	const aAllowlist = a?.allowlist || [];
	const bAllowlist = b?.allowlist || [];
	const aDenylist = a?.denylist || [];
	const bDenylist = b?.denylist || [];

	if (aAllowlist.length !== bAllowlist.length || aDenylist.length !== bDenylist.length) {
		return false;
	}

	return aAllowlist.every((v, i) => v === bAllowlist[i]) && aDenylist.every((v, i) => v === bDenylist[i]);
}

// Helper to compare large payload configs
function largePayloadConfigEqual(a: LargePayloadConfig, b: LargePayloadConfig): boolean {
	return (
		a.enabled === b.enabled &&
		a.request_threshold_bytes === b.request_threshold_bytes &&
		a.response_threshold_bytes === b.response_threshold_bytes &&
		a.prefetch_size_bytes === b.prefetch_size_bytes &&
		a.max_payload_bytes === b.max_payload_bytes &&
		a.truncated_log_bytes === b.truncated_log_bytes
	);
}

export default function ClientSettingsView() {
	const hasSettingsUpdateAccess = useRbac(RbacResource.Settings, RbacOperation.Update);
	const [droppedRequests, setDroppedRequests] = useState<number>(0);
	const { data: droppedRequestsData } = useGetDroppedRequestsQuery();
	const { data: bifrostConfig, isLoading: isCoreConfigLoading } = useGetCoreConfigQuery({ fromDB: true });
	const config = bifrostConfig?.client_config;
	const [updateCoreConfig, { isLoading: isSavingCoreConfig }] = useUpdateCoreConfigMutation();
	const [localConfig, setLocalConfig] = useState<CoreConfig>(DefaultCoreConfig);

	// Large payload config state
	const { data: serverLargePayloadConfig, isLoading: isLargePayloadConfigLoading } = useGetLargePayloadConfigQuery();
	const [updateLargePayloadConfig, { isLoading: isSavingLargePayload }] = useUpdateLargePayloadConfigMutation();
	const [localLargePayloadConfig, setLocalLargePayloadConfig] = useState<LargePayloadConfig>(DefaultLargePayloadConfig);

	const isQueriesLoading = isCoreConfigLoading || isLargePayloadConfigLoading;
	const isLoading = isSavingCoreConfig || isSavingLargePayload;

	useEffect(() => {
		if (droppedRequestsData) {
			setDroppedRequests(droppedRequestsData.dropped_requests);
		}
	}, [droppedRequestsData]);

	useEffect(() => {
		if (config) {
			setLocalConfig({
				...config,
				header_filter_config: config.header_filter_config || DefaultGlobalHeaderFilterConfig,
			});
		}
	}, [config]);

	useEffect(() => {
		if (serverLargePayloadConfig) {
			setLocalLargePayloadConfig(serverLargePayloadConfig);
		}
	}, [serverLargePayloadConfig]);

	const hasCoreConfigChanges = useMemo(() => {
		if (!config) return false;
		return (
			localConfig.drop_excess_requests !== config.drop_excess_requests ||
			localConfig.enable_litellm_fallbacks !== config.enable_litellm_fallbacks ||
			localConfig.disable_db_pings_in_health !== config.disable_db_pings_in_health ||
			localConfig.async_job_result_ttl !== config.async_job_result_ttl ||
			!headerFilterConfigEqual(localConfig.header_filter_config, config.header_filter_config)
		);
	}, [config, localConfig]);

	const hasLargePayloadChanges = useMemo(() => {
		const baseline = serverLargePayloadConfig ?? DefaultLargePayloadConfig;
		return !largePayloadConfigEqual(localLargePayloadConfig, baseline);
	}, [serverLargePayloadConfig, localLargePayloadConfig]);

	const hasChanges = hasCoreConfigChanges || hasLargePayloadChanges;

	// Detect security headers in allowlist/denylist
	const invalidSecurityHeaders = useMemo(() => {
		const allowlist = localConfig.header_filter_config?.allowlist || [];
		const denylist = localConfig.header_filter_config?.denylist || [];
		const invalidInAllowlist = allowlist.filter((h) => h && isSecurityHeader(h));
		const invalidInDenylist = denylist.filter((h) => h && isSecurityHeader(h));
		return [...new Set([...invalidInAllowlist, ...invalidInDenylist])];
	}, [localConfig.header_filter_config]);

	const hasSecurityHeaderError = invalidSecurityHeaders.length > 0;

	const handleConfigChange = useCallback((field: keyof CoreConfig, value: boolean | number | string[] | GlobalHeaderFilterConfig) => {
		setLocalConfig((prev) => ({ ...prev, [field]: value }));
	}, []);

	const handleLargePayloadConfigChange = useCallback((newConfig: LargePayloadConfig) => {
		setLocalLargePayloadConfig(newConfig);
	}, []);

	const handleSave = useCallback(async () => {
		// Defense in depth - don't save if security headers are present
		if (hasSecurityHeaderError) {
			return;
		}

		// Validate large payload config if it has changes
		if (hasLargePayloadChanges) {
			const minBytes = 1024;
			if (
				localLargePayloadConfig.request_threshold_bytes < minBytes ||
				localLargePayloadConfig.response_threshold_bytes < minBytes ||
				localLargePayloadConfig.prefetch_size_bytes < minBytes ||
				localLargePayloadConfig.max_payload_bytes < minBytes ||
				localLargePayloadConfig.truncated_log_bytes < minBytes
			) {
				toast.error("All byte values must be at least 1024 (1 KB).");
				return;
			}
			if (localLargePayloadConfig.max_payload_bytes < localLargePayloadConfig.request_threshold_bytes) {
				toast.error("Max payload size must be greater than or equal to the request threshold.");
				return;
			}
			if (localLargePayloadConfig.max_payload_bytes < localLargePayloadConfig.response_threshold_bytes) {
				toast.error("Max payload size must be greater than or equal to the response threshold.");
				return;
			}
		}

		let coreConfigSaved = false;
		let largePayloadSaved = false;

		// Save core config if changed
		if (hasCoreConfigChanges) {
			if (!bifrostConfig) {
				toast.error("Configuration not loaded. Please refresh and try again.");
				return;
			}
			// Clean up empty strings from header filter config
			const cleanedConfig = {
				...localConfig,
				header_filter_config: {
					allowlist: (localConfig.header_filter_config?.allowlist || []).filter((h) => h && h.trim().length > 0),
					denylist: (localConfig.header_filter_config?.denylist || []).filter((h) => h && h.trim().length > 0),
				},
			};

			try {
				await updateCoreConfig({ ...bifrostConfig!, client_config: cleanedConfig }).unwrap();
				coreConfigSaved = true;
			} catch (error) {
				toast.error(`Failed to save core config: ${getErrorMessage(error)}`);
			}
		}

		// Save large payload config if changed
		if (hasLargePayloadChanges) {
			try {
				await updateLargePayloadConfig(localLargePayloadConfig).unwrap();
				largePayloadSaved = true;
			} catch (error) {
				toast.error(`Failed to save large payload config: ${getErrorMessage(error)}`);
			}
		}

		if (coreConfigSaved || largePayloadSaved) {
			if (largePayloadSaved) {
				toast.success("Settings updated. Large payload changes require a restart to apply.");
			} else {
				toast.success("Client settings updated successfully.");
			}
		}
	}, [
		bifrostConfig,
		hasSecurityHeaderError,
		hasCoreConfigChanges,
		hasLargePayloadChanges,
		localConfig,
		localLargePayloadConfig,
		updateCoreConfig,
		updateLargePayloadConfig,
	]);

	// Header filter list handlers
	const handleAddAllowlistHeader = useCallback(() => {
		setLocalConfig((prev) => ({
			...prev,
			header_filter_config: {
				...prev.header_filter_config,
				allowlist: [...(prev.header_filter_config?.allowlist || []), ""],
			},
		}));
	}, []);

	const handleRemoveAllowlistHeader = useCallback((index: number) => {
		setLocalConfig((prev) => ({
			...prev,
			header_filter_config: {
				...prev.header_filter_config,
				allowlist: (prev.header_filter_config?.allowlist || []).filter((_, i) => i !== index),
			},
		}));
	}, []);

	const handleAllowlistChange = useCallback((index: number, value: string) => {
		const lowerValue = value.toLowerCase();
		setLocalConfig((prev) => ({
			...prev,
			header_filter_config: {
				...prev.header_filter_config,
				allowlist: (prev.header_filter_config?.allowlist || []).map((h, i) => (i === index ? lowerValue : h)),
			},
		}));
	}, []);

	const handleAddDenylistHeader = useCallback(() => {
		setLocalConfig((prev) => ({
			...prev,
			header_filter_config: {
				...prev.header_filter_config,
				denylist: [...(prev.header_filter_config?.denylist || []), ""],
			},
		}));
	}, []);

	const handleRemoveDenylistHeader = useCallback((index: number) => {
		setLocalConfig((prev) => ({
			...prev,
			header_filter_config: {
				...prev.header_filter_config,
				denylist: (prev.header_filter_config?.denylist || []).filter((_, i) => i !== index),
			},
		}));
	}, []);

	const handleDenylistChange = useCallback((index: number, value: string) => {
		const lowerValue = value.toLowerCase();
		setLocalConfig((prev) => ({
			...prev,
			header_filter_config: {
				...prev.header_filter_config,
				denylist: (prev.header_filter_config?.denylist || []).map((h, i) => (i === index ? lowerValue : h)),
			},
		}));
	}, []);

	return (
		<div className="mx-auto w-full max-w-4xl space-y-6">
			<div>
				<h2 className="text-lg font-semibold tracking-tight">Client Settings</h2>
				<p className="text-muted-foreground text-sm">Configure client behavior and request handling.</p>
			</div>

			<div className="space-y-4">
				{/* Drop Excess Requests */}
				<div className="flex items-center justify-between space-x-2">
					<div className="space-y-0.5">
						<label htmlFor="drop-excess-requests" className="text-sm font-medium">
							Drop Excess Requests
						</label>
						<p className="text-muted-foreground text-sm">
							If enabled, Bifrost will drop requests that exceed pool capacity.{" "}
							{localConfig.drop_excess_requests && droppedRequests > 0 ? (
								<span>
									Have dropped <b>{droppedRequests} requests</b> since last restart.
								</span>
							) : (
								<></>
							)}
						</p>
					</div>
					<Switch
						id="drop-excess-requests"
						size="md"
						checked={localConfig.drop_excess_requests}
						onCheckedChange={(checked) => handleConfigChange("drop_excess_requests", checked)}
						disabled={!hasSettingsUpdateAccess}
					/>
				</div>

				{/* Enable LiteLLM Fallbacks */}
				<div className="flex items-center justify-between space-x-2">
					<div className="space-y-0.5">
						<label htmlFor="enable-litellm-fallbacks" className="text-sm font-medium">
							Enable LiteLLM Fallbacks
						</label>
						<p className="text-muted-foreground text-sm">
							Enable litellm-specific fallbacks.{" "}
							<a
								className="text-primary cursor-pointer underline"
								href="https://docs.getbifrost.ai/features/litellm-compat"
								target="_blank"
								rel="noopener noreferrer"
								data-testid="litellm-docs-link"
							>
								Learn more
							</a>
						</p>
					</div>
					<Switch
						id="enable-litellm-fallbacks"
						size="md"
						checked={localConfig.enable_litellm_fallbacks}
						onCheckedChange={(checked) => handleConfigChange("enable_litellm_fallbacks", checked)}
						disabled={!hasSettingsUpdateAccess}
					/>
				</div>

				{/* Disable DB Pings in Health */}
				<div className="flex items-center justify-between space-x-2">
					<div className="space-y-0.5">
						<label htmlFor="disable-db-pings-in-health" className="text-sm font-medium">
							Disable DB Pings in Health Check
						</label>
						<p className="text-muted-foreground text-sm">
							If enabled, the /health endpoint will skip database connectivity checks and return OK immediately.
						</p>
					</div>
					<Switch
						id="disable-db-pings-in-health"
						size="md"
						checked={localConfig.disable_db_pings_in_health}
						onCheckedChange={(checked) => handleConfigChange("disable_db_pings_in_health", checked)}
						disabled={!hasSettingsUpdateAccess}
					/>
				</div>
				{/* Async Job Result TTL */}
				<div className="flex items-center justify-between space-x-2">
					<div className="space-y-0.5">
						<label htmlFor="async-job-result-ttl" className="text-sm font-medium">
							Async Job Result TTL (seconds)
						</label>
						<p className="text-muted-foreground text-sm">
							Default time-to-live for async job results in seconds. Results are automatically cleaned up after expiry.
						</p>
					</div>
					<Input
						id="async-job-result-ttl"
						type="number"
						min={1}
						className="w-32"
						value={localConfig.async_job_result_ttl}
						onChange={(e) => handleConfigChange("async_job_result_ttl", parseInt(e.target.value) || 0)}
						disabled={!hasSettingsUpdateAccess}
						data-testid="client-settings-async-job-result-ttl-input"
					/>
				</div>
			</div>

			{/* Header Filter Section */}
			<div className="space-y-4">
				<div>
					<h3 className="text-lg font-semibold tracking-tight">Header Forwarding</h3>
					<p className="text-muted-foreground text-sm">Control which extra headers are forwarded to LLM providers.</p>
				</div>

				<Accordion type="multiple" className="w-full rounded-sm border px-4">
					<AccordionItem value="about-extra-headers">
						<AccordionTrigger>
							<span className="flex items-center gap-2">
								<Info className="h-4 w-4" />
								About Header Forwarding
							</span>
						</AccordionTrigger>
						<AccordionContent className="space-y-3">
							<div>
								<p className="mb-2 font-medium">Two ways to forward headers:</p>
								<ul className="text-muted-foreground list-inside list-disc space-y-1 text-sm">
									<li>
										<span className="font-medium">Prefixed headers:</span> Use{" "}
										<code className="bg-muted rounded px-1 py-0.5 font-mono text-xs">x-bf-eh-*</code> prefix. For example,{" "}
										<code className="bg-muted rounded px-1 py-0.5 font-mono text-xs">x-bf-eh-custom-id</code> is forwarded as{" "}
										<code className="bg-muted rounded px-1 py-0.5 font-mono text-xs">custom-id</code>.
									</li>
									<li>
										<span className="font-medium">Direct headers:</span> Any header explicitly added to the allowlist can be forwarded
										directly without the prefix (e.g.,{" "}
										<code className="bg-muted rounded px-1 py-0.5 font-mono text-xs">anthropic-beta</code>).
									</li>
								</ul>
							</div>
							<div>
								<p className="mb-2 font-medium">How allowlist and denylist work:</p>
								<ul className="text-muted-foreground list-inside list-disc space-y-1 text-sm">
									<li>
										<span className="font-medium">Allowlist empty:</span> Only{" "}
										<code className="bg-muted rounded px-1 py-0.5 font-mono text-xs">x-bf-eh-*</code> prefixed headers are forwarded
										(default behavior)
									</li>
									<li>
										<span className="font-medium">Allowlist configured:</span> Prefixed headers filtered by allowlist, plus any direct
										header in the allowlist is forwarded
									</li>
									<li>
										<span className="font-medium">Denylist:</span> Headers in the denylist are always blocked from forwarding
									</li>
								</ul>
							</div>
							<div>
								<p className="mb-2 font-medium">Important:</p>
								<ul className="text-muted-foreground list-inside list-disc space-y-1 text-sm">
									<li>
										Allowlist/denylist entries should be the header name <span className="font-medium">without</span> the{" "}
										<code className="bg-muted rounded px-1 py-0.5 font-mono text-xs">x-bf-eh-</code> prefix
									</li>
									<li>
										Example: To allow <code className="bg-muted rounded px-1 py-0.5 font-mono text-xs">x-bf-eh-custom-id</code> or direct{" "}
										<code className="bg-muted rounded px-1 py-0.5 font-mono text-xs">custom-id</code>, add{" "}
										<code className="bg-muted rounded px-1 py-0.5 font-mono text-xs">custom-id</code> to the allowlist
									</li>
								</ul>
							</div>
						</AccordionContent>
					</AccordionItem>

					<AccordionItem value="security-note">
						<AccordionTrigger>
							<span className="flex items-center gap-2">
								<Info className="h-4 w-4" />
								Security Note
							</span>
						</AccordionTrigger>
						<AccordionContent>
							<p className="text-sm">
								Some headers are always blocked for security reasons regardless of configuration. These headers cannot be added to the
								allowlist or denylist:
							</p>
							<p className="text-muted-foreground mt-1 font-mono text-xs">
								proxy-authorization, cookie, host, content-length, connection, transfer-encoding, x-api-key, x-goog-api-key, x-bf-api-key,
								x-bf-vk
							</p>
						</AccordionContent>
					</AccordionItem>
				</Accordion>

				{/* Allowlist Section */}
				<div className="space-y-3">
					<div className="space-y-1">
						<h4 className="text-sm font-medium">Allowlist</h4>
						<p className="text-muted-foreground text-xs">
							Headers to allow. Enter names without the <code className="bg-muted rounded px-1 font-mono">x-bf-eh-</code> prefix. Any header
							in this list can also be sent directly without the prefix.
						</p>
					</div>

					<div className="space-y-2">
						{(localConfig.header_filter_config?.allowlist || []).map((header, index) => (
							<div key={index} className="flex items-center gap-2">
								<Input
									placeholder="e.g. custom-id, anthropic-beta"
									className={cn(
										"font-mono lowercase",
										isSecurityHeader(header) &&
											"border-destructive focus:border-destructive focus-visible:border-destructive focus-visible:ring-destructive/50",
									)}
									value={header}
									onChange={(e) => handleAllowlistChange(index, e.target.value)}
									disabled={!hasSettingsUpdateAccess}
								/>
								<Button
									type="button"
									variant="ghost"
									size="icon"
									onClick={() => handleRemoveAllowlistHeader(index)}
									className="text-muted-foreground hover:text-destructive"
									disabled={!hasSettingsUpdateAccess}
								>
									<X className="h-4 w-4" />
								</Button>
							</div>
						))}
						<Button type="button" variant="outline" size="sm" onClick={handleAddAllowlistHeader} disabled={!hasSettingsUpdateAccess}>
							<Plus className="mr-2 h-4 w-4" />
							Add Header
						</Button>
					</div>
				</div>

				{/* Denylist Section */}
				<div className="space-y-3">
					<div className="space-y-1">
						<h4 className="text-sm font-medium">Denylist</h4>
						<p className="text-muted-foreground text-xs">
							Headers to block. Enter names without the <code className="bg-muted rounded px-1 font-mono">x-bf-eh-</code> prefix. Applies to
							both prefixed and direct header forwarding.
						</p>
					</div>

					<div className="space-y-2">
						{(localConfig.header_filter_config?.denylist || []).map((header, index) => (
							<div key={index} className="flex items-center gap-2">
								<Input
									placeholder="e.g. x-internal-id"
									className={cn(
										"font-mono lowercase",
										isSecurityHeader(header) &&
											"border-destructive focus:border-destructive focus-visible:border-destructive focus-visible:ring-destructive/50",
									)}
									value={header}
									onChange={(e) => handleDenylistChange(index, e.target.value)}
									disabled={!hasSettingsUpdateAccess}
								/>
								<Button
									type="button"
									variant="ghost"
									size="icon"
									onClick={() => handleRemoveDenylistHeader(index)}
									className="text-muted-foreground hover:text-destructive"
									disabled={!hasSettingsUpdateAccess}
								>
									<X className="h-4 w-4" />
								</Button>
							</div>
						))}
						<Button type="button" variant="outline" size="sm" onClick={handleAddDenylistHeader} disabled={!hasSettingsUpdateAccess}>
							<Plus className="mr-2 h-4 w-4" />
							Add Header
						</Button>
					</div>
				</div>
			</div>

			{/* Large Payload Optimization - Enterprise only */}
			<LargePayloadSettingsFragment
				config={localLargePayloadConfig}
				onConfigChange={handleLargePayloadConfigChange}
				controlsDisabled={isLoading || !hasSettingsUpdateAccess}
			/>

			<div className="flex justify-end pt-2">
				{hasSecurityHeaderError ? (
					<Tooltip>
						<TooltipTrigger asChild>
							<span>
								<Button disabled>{isLoading ? "Saving..." : "Save Changes"}</Button>
							</span>
						</TooltipTrigger>
						<TooltipContent>
							Remove security header{invalidSecurityHeaders.length > 1 ? "s" : ""}: {invalidSecurityHeaders.join(", ")}
						</TooltipContent>
					</Tooltip>
				) : (
					<Button onClick={handleSave} disabled={!hasChanges || isLoading || isQueriesLoading || !hasSettingsUpdateAccess}>
						{isLoading ? "Saving..." : "Save Changes"}
					</Button>
				)}
			</div>
		</div>
	);
}
