"use client";

import { useEffect, useState } from "react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Dialog, DialogContent, DialogHeader, DialogTitle, DialogDescription } from "@/components/ui/dialog";
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip";
import { TagInput } from "@/components/ui/tag-input";
import { Separator } from "@/components/ui/separator";
import { X, Plus, Save, Key, Globe, Zap, Edit, Info, AlertTriangle } from "lucide-react";
import { toast } from "sonner";
import {
	ProviderResponse,
	Key as KeyType,
	MetaConfig,
	NetworkConfig,
	ConcurrencyAndBufferSize,
	AddProviderRequest,
	UpdateProviderRequest,
	ModelProvider,
	ProxyConfig,
	ProxyType,
} from "@/lib/types/config";
import { apiService } from "@/lib/api";
import isEqual from "lodash.isequal";
import { PROVIDER_COLORS, PROVIDER_LABELS } from "@/lib/constants/logs";
import MetaConfigRenderer from "./meta-config-renderer";
import { Validator } from "@/lib/utils/validation";
import { ProviderIcons } from "@/lib/constants/icons";
import { PROVIDERS } from "@/lib/constants/logs";
import { cn } from "@/lib/utils";
import { Alert, AlertDescription } from "../ui/alert";
import { DEFAULT_NETWORK_CONFIG, DEFAULT_PERFORMANCE_CONFIG } from "@/lib/constants/config";

interface ProviderFormProps {
	provider?: ProviderResponse | null;
	onSave: () => void;
	onCancel: () => void;
	existingProviders: string[];
}

// A helper function to create a clean initial state
const createInitialState = (provider?: ProviderResponse | null, defaultProvider?: string): Omit<ProviderFormData, "isDirty"> => {
	const isNewProvider = !provider;
	const providerName = provider?.name || defaultProvider || "";
	const keysRequired = !["vertex", "ollama"].includes(providerName);

	return {
		selectedProvider: providerName,
		keys:
			isNewProvider && keysRequired
				? [{ value: "", models: [], weight: 1.0 }]
				: !isNewProvider && keysRequired && provider?.keys
					? provider.keys
					: [],
		networkConfig: provider?.network_config || DEFAULT_NETWORK_CONFIG,
		performanceConfig: provider?.concurrency_and_buffer_size || DEFAULT_PERFORMANCE_CONFIG,
		metaConfig: provider?.meta_config || {
			endpoint: "",
			deployments: {},
			api_version: "",
		},
		proxyConfig: provider?.proxy_config || {
			type: "none",
			url: "",
			username: "",
			password: "",
		},
	};
};

interface ProviderFormData {
	selectedProvider: string;
	keys: KeyType[];
	networkConfig: NetworkConfig;
	performanceConfig: ConcurrencyAndBufferSize;
	metaConfig: MetaConfig;
	proxyConfig: ProxyConfig;
	isDirty: boolean;
}

export default function ProviderForm({ provider, onSave, onCancel, existingProviders }: ProviderFormProps) {
	// Find the first available provider if adding a new provider
	const firstAvailableProvider = !provider ? PROVIDERS.find((p) => !existingProviders.includes(p)) || "" : undefined;
	const [initialState] = useState<Omit<ProviderFormData, "isDirty">>(createInitialState(provider, firstAvailableProvider));
	const [formData, setFormData] = useState<ProviderFormData>({
		...initialState,
		isDirty: false,
	});
	const [isLoading, setIsLoading] = useState(false);

	const { selectedProvider, keys, networkConfig, performanceConfig, metaConfig, proxyConfig, isDirty } = formData;

	const baseURLRequired = selectedProvider === "ollama";
	const keysRequired = !["vertex", "ollama"].includes(selectedProvider);
	const keysValid = !keysRequired || keys.every((k) => k.value.trim() !== "");
	const keysPresent = !keysRequired || keys.length > 0;

	const performanceValid =
		performanceConfig.concurrency > 0 && performanceConfig.buffer_size > 0 && performanceConfig.concurrency < performanceConfig.buffer_size;

	// Track if performance settings have changed
	const performanceChanged =
		performanceConfig.concurrency !== initialState.performanceConfig.concurrency ||
		performanceConfig.buffer_size !== initialState.performanceConfig.buffer_size;

	/* Meta configuration validation based on provider requirements */
	const getMetaValidation = () => {
		let valid = true;
		let message = "";

		if (selectedProvider === "azure") {
			const endpointValid = !!metaConfig.endpoint && (metaConfig.endpoint as string).trim() !== "";
			const deploymentsValid = !!(
				metaConfig.deployments &&
				typeof metaConfig.deployments === "object" &&
				Object.keys(metaConfig.deployments as Record<string, string>).length > 0
			);
			valid = endpointValid && deploymentsValid;
			if (!valid) {
				message = "Endpoint and at least one Deployment are required for Azure";
			}
		} else if (selectedProvider === "bedrock") {
			const regionValid = !!metaConfig.region && (metaConfig.region as string).trim() !== "";
			valid = regionValid;
			if (!valid) {
				message = "Region is required for AWS Bedrock";
			}
		} else if (selectedProvider === "vertex") {
			const projectValid = !!metaConfig.project_id && (metaConfig.project_id as string).trim() !== "";
			const credsValid = !!metaConfig.auth_credentials && (metaConfig.auth_credentials as string).trim() !== "";
			const regionValid = !!metaConfig.region && (metaConfig.region as string).trim() !== "";
			valid = projectValid && credsValid && regionValid;
			if (!valid) {
				message = "Project ID, Auth Credentials, and Region are required for Vertex AI";
			}
		}

		return { valid, message };
	};

	const { valid: metaValid, message: metaErrorMessage } = getMetaValidation();

	const showConfigSections = !!provider || selectedProvider !== "";

	useEffect(() => {
		const currentData = {
			selectedProvider,
			keys: keysRequired ? keys : [],
			networkConfig,
			performanceConfig,
			metaConfig,
			proxyConfig,
		};
		setFormData((prev) => ({
			...prev,
			isDirty: !isEqual(initialState, currentData),
		}));
	}, [selectedProvider, keys, networkConfig, performanceConfig, metaConfig, proxyConfig, initialState, keysRequired]);

	const updateField = <K extends keyof ProviderFormData>(field: K, value: ProviderFormData[K]) => {
		setFormData((prev) => ({ ...prev, [field]: value }));
	};

	const updateProxyField = <K extends keyof ProxyConfig>(field: K, value: ProxyConfig[K]) => {
		updateField("proxyConfig", { ...proxyConfig, [field]: value });
	};

	const availableProviders = provider ? PROVIDERS : PROVIDERS.filter((p) => !existingProviders.includes(p));

	const handleSubmit = async (e: React.FormEvent) => {
		if (!validator.isValid()) {
			toast.error(validator.getFirstError());
			return;
		}

		e.preventDefault();
		setIsLoading(true);

		let error: string | null = null;

		if (provider) {
			const data: UpdateProviderRequest = {
				keys: keysRequired ? keys.filter((k) => k.value.trim() !== "") : [],
				network_config: networkConfig,
				concurrency_and_buffer_size: performanceConfig,
				meta_config: metaConfig,
				proxy_config: proxyConfig,
			};
			[, error] = await apiService.updateProvider(provider.name, data);
		} else {
			const data: AddProviderRequest = {
				provider: selectedProvider as ModelProvider,
				keys: keysRequired ? keys.filter((k) => k.value.trim() !== "") : [],
				network_config: networkConfig,
				concurrency_and_buffer_size: performanceConfig,
				meta_config: metaConfig,
				proxy_config: proxyConfig,
			};
			[, error] = await apiService.createProvider(data);
		}

		setIsLoading(false);

		if (error) {
			toast.error(error);
		} else {
			toast.success(`Provider ${provider ? "updated" : "added"} successfully`);
			onSave();
		}
	};

	const validator = new Validator([
		// Provider selection
		Validator.required(selectedProvider, "Please select a provider"),

		// Check if anything is dirty
		Validator.custom(isDirty, "No changes to save"),

		// Base URL validation
		...(baseURLRequired
			? [
					Validator.required(networkConfig.base_url, "Base URL is required for Ollama provider"),
					Validator.pattern(networkConfig.base_url || "", /^https?:\/\/.+/, "Base URL must start with http:// or https://"),
				]
			: []),

		// API Keys validation
		...(keysRequired
			? [
					Validator.minValue(keys.length, 1, "At least one API key is required"),
					Validator.custom(
						keys.every((k) => k.value.trim() !== ""),
						"API key value cannot be empty",
					),
				]
			: []),

		// Network config validation
		Validator.minValue(networkConfig.default_request_timeout_in_seconds, 1, "Timeout must be greater than 0 seconds"),
		Validator.minValue(networkConfig.max_retries, 0, "Max retries cannot be negative"),

		// Performance config validation
		Validator.minValue(performanceConfig.concurrency, 1, "Concurrency must be greater than 0"),
		Validator.minValue(performanceConfig.buffer_size, 1, "Buffer size must be greater than 0"),
		Validator.custom(performanceConfig.concurrency < performanceConfig.buffer_size, "Buffer size must be greater than concurrency"),

		// Meta config validation
		Validator.custom(metaValid, metaErrorMessage),

		// Meta config validation for Azure
		...(selectedProvider === "azure"
			? [
					Validator.required(metaConfig.endpoint, "Azure endpoint is required"),
					Validator.minValue(
						Object.keys((metaConfig.deployments as Record<string, string>) || {}).length,
						1,
						"At least one Azure deployment is required",
					),
				]
			: []),

		// Meta config validation for Bedrock
		...(selectedProvider === "bedrock" ? [Validator.required(metaConfig.region, "AWS region is required")] : []),

		// Meta config validation for Vertex
		...(selectedProvider === "vertex"
			? [
					Validator.required(metaConfig.project_id, "Project ID is required for Vertex AI"),
					Validator.required(metaConfig.auth_credentials, "Auth credentials are required for Vertex AI"),
					Validator.required(metaConfig.region, "Region is required for Vertex AI"),
				]
			: []),
	]);

	const addKey = () => {
		updateField("keys", [...keys, { value: "", models: [], weight: 1.0 }]);
	};

	const removeKey = (index: number) => {
		updateField(
			"keys",
			keys.filter((_, i) => i !== index),
		);
	};

	const updateKey = (index: number, field: keyof KeyType, value: string | number | string[]) => {
		const newKeys = [...keys];
		const keyToUpdate = { ...newKeys[index] };

		if (field === "models" && Array.isArray(value)) {
			keyToUpdate.models = value;
		} else if (field === "value" && typeof value === "string") {
			keyToUpdate.value = value;
		} else if (field === "weight" && typeof value === "string") {
			keyToUpdate.weight = parseFloat(value) || 1.0;
		}

		newKeys[index] = keyToUpdate;
		updateField("keys", newKeys);
	};

	const handleMetaConfigChange = (field: keyof MetaConfig, value: string | Record<string, string>) => {
		updateField("metaConfig", { ...metaConfig, [field]: value });
	};

	return (
		<Dialog open={true} onOpenChange={onCancel}>
			<DialogContent className="max-h-[90vh] overflow-y-auto sm:max-w-3xl">
				<DialogHeader>
					<DialogTitle>
						{provider ? (
							<div className="flex items-center gap-2">
								Edit Provider{" "}
								<span className={`font-semibold ${PROVIDER_COLORS[provider.name]} rounded-md px-2 py-1`}>
									{PROVIDER_LABELS[provider.name]}
								</span>
							</div>
						) : (
							<div className="flex items-center gap-2">Add Provider</div>
						)}
					</DialogTitle>
					<DialogDescription>Configure AI provider settings, API keys, and network options.</DialogDescription>
				</DialogHeader>

				<Separator />

				<form onSubmit={handleSubmit} className="space-y-6">
					<div className="space-y-8">
						{/* Provider Selection */}
						{!provider &&
							(availableProviders.length === 0 ? (
								<div className="text-muted-foreground py-8 text-center font-medium">All providers have been configured.</div>
							) : (
								<div className="grid grid-cols-4 gap-4">
									{PROVIDERS.map((p) => (
										<div
											key={p}
											className={cn(
												"flex w-full items-center gap-2 rounded-lg border px-4 py-3 text-sm",
												PROVIDER_COLORS[p as keyof typeof PROVIDER_COLORS],
												selectedProvider === p
													? "border-primary/20 opacity-100 hover:opacity-100"
													: availableProviders.includes(p)
														? "cursor-pointer border-transparent opacity-60 hover:opacity-80 hover:shadow-md"
														: "cursor-not-allowed border-transparent opacity-30",
											)}
											onClick={() => {
												if (availableProviders.includes(p)) {
													updateField("selectedProvider", p);
												}
											}}
										>
											{ProviderIcons[p as keyof typeof ProviderIcons]}
											<div className="text-sm">{PROVIDER_LABELS[p as keyof typeof PROVIDER_LABELS]}</div>
										</div>
									))}
								</div>
							))}

						{/* Remaining sections appear only after provider is chosen */}
						{showConfigSections && (
							<>
								{/* API Keys */}
								{keysRequired && (
									<div className="space-y-2">
										<CardHeader className="mb-2 px-0">
											<CardTitle className="flex items-center justify-between text-base">
												<div className="flex items-center gap-2">
													<Key className="h-4 w-4" />
													API Keys
													<TooltipProvider>
														<Tooltip>
															<TooltipTrigger asChild>
																<span>
																	<Info className="text-muted-foreground ml-1 h-3 w-3" />
																</span>
															</TooltipTrigger>
															<TooltipContent className="max-w-fit">
																<p>
																	Use <code className="rounded bg-neutral-100 px-1 py-0.5 text-neutral-800">env.&lt;VAR&gt;</code> to read
																	the value from an environment variable.
																</p>
															</TooltipContent>
														</Tooltip>
													</TooltipProvider>
												</div>
												<Button type="button" variant="outline" size="sm" onClick={addKey}>
													<Plus className="h-4 w-4" />
													Add Key
												</Button>
											</CardTitle>
										</CardHeader>
										<div className="space-y-4">
											{keys.map((key, index) => (
												<div key={index} className="space-y-4 rounded-md border p-4">
													<div className="flex gap-4">
														<div className="flex-1">
															<div className="text-sm font-medium">API Key</div>
															<Input
																placeholder="API Key or env.MY_KEY"
																value={key.value}
																onChange={(e) => updateKey(index, "value", e.target.value)}
																type="text"
																className={`flex-1 ${keysRequired && key.value.trim() === "" ? "border-destructive" : ""}`}
															/>
														</div>
														<div>
															<div className="flex items-center gap-2">
																<label className="text-sm font-medium">Weight</label>
																<TooltipProvider>
																	<Tooltip>
																		<TooltipTrigger asChild>
																			<span>
																				<Info className="text-muted-foreground h-3 w-3" />
																			</span>
																		</TooltipTrigger>
																		<TooltipContent>
																			<p>Determines traffic distribution between keys. Higher weights receive more requests.</p>
																		</TooltipContent>
																	</Tooltip>
																</TooltipProvider>
															</div>
															<Input
																placeholder="1.0"
																value={key.weight}
																onChange={(e) => updateKey(index, "weight", e.target.value)}
																type="number"
																step="0.1"
																min="0.1"
																className="w-20"
															/>
														</div>
													</div>
													<div>
														<div className="flex items-center gap-2">
															<label className="text-sm font-medium">Models (Optional)</label>
															<TooltipProvider>
																<Tooltip>
																	<TooltipTrigger asChild>
																		<span>
																			<Info className="text-muted-foreground h-3 w-3" />
																		</span>
																	</TooltipTrigger>
																	<TooltipContent>
																		<p>Comma-separated list of models this key applies to. Leave blank for all models.</p>
																	</TooltipContent>
																</Tooltip>
															</TooltipProvider>
														</div>
														<TagInput
															placeholder="e.g. gpt-4, gpt-3.5-turbo"
															value={key.models || []}
															onValueChange={(newModels) => updateKey(index, "models", newModels)}
														/>
													</div>
													{keys.length > 1 && (
														<Button type="button" variant="destructive" size="sm" onClick={() => removeKey(index)} className="mt-2">
															<X className="h-4 w-4" />
															Remove Key
														</Button>
													)}
												</div>
											))}
										</div>
									</div>
								)}

								{/* Meta Config */}
								<MetaConfigRenderer provider={selectedProvider} metaConfig={metaConfig} onMetaConfigChange={handleMetaConfigChange} />

								{/* Network Configuration */}
								<div>
									<CardHeader className="mb-2 px-0">
										<CardTitle className="flex items-center gap-2 text-base">
											<Globe className="h-4 w-4" />
											Network Configuration
										</CardTitle>
									</CardHeader>
									<CardContent className="space-y-4 px-0">
										<div className="grid grid-cols-1 gap-4">
											<div>
												<label className="text-sm font-medium">Base URL {baseURLRequired ? "(Required)" : "(Optional)"}</label>
												<Input
													placeholder="https://api.example.com"
													value={networkConfig.base_url || ""}
													onChange={(e) =>
														updateField("networkConfig", {
															...networkConfig,
															base_url: e.target.value,
														})
													}
													className={baseURLRequired && !networkConfig.base_url ? "border-destructive" : ""}
												/>
											</div>
											<div className="grid grid-cols-2 gap-4">
												<div>
													<label className="text-sm font-medium">Timeout (seconds)</label>
													<Input
														type="number"
														placeholder="30"
														value={networkConfig.default_request_timeout_in_seconds}
														onChange={(e) =>
															updateField("networkConfig", {
																...networkConfig,
																default_request_timeout_in_seconds: parseInt(e.target.value) || 30,
															})
														}
													/>
												</div>
												<div>
													<label className="text-sm font-medium">Max Retries</label>
													<Input
														type="number"
														placeholder="0"
														value={networkConfig.max_retries}
														onChange={(e) =>
															updateField("networkConfig", {
																...networkConfig,
																max_retries: parseInt(e.target.value) || 0,
															})
														}
													/>
												</div>
											</div>
										</div>
									</CardContent>
								</div>

								{/* Performance Configuration */}
								<div>
									<CardHeader className="mb-2 px-0">
										<CardTitle className="flex items-center gap-2 text-base">
											<Zap className="h-4 w-4" />
											Performance Settings
										</CardTitle>
									</CardHeader>
									{performanceChanged && (
										<Alert className="mb-3">
											<AlertTriangle className="h-4 w-4" />
											<AlertDescription>
												<strong>Heads up:</strong> Changing concurrency or buffer size may temporarily affect request latency for this
												provider while the new settings are being applied.
											</AlertDescription>
										</Alert>
									)}
									<CardContent className="space-y-4 px-0">
										<div className="grid grid-cols-2 gap-4">
											<div>
												<label className="text-sm font-medium">Concurrency</label>
												<Input
													type="number"
													value={performanceConfig.concurrency}
													onChange={(e) =>
														updateField("performanceConfig", {
															...performanceConfig,
															concurrency: parseInt(e.target.value) || 0,
														})
													}
													className={!performanceValid ? "border-destructive" : ""}
												/>
											</div>
											<div>
												<label className="text-sm font-medium">Buffer Size</label>
												<Input
													type="number"
													value={performanceConfig.buffer_size}
													onChange={(e) =>
														updateField("performanceConfig", {
															...performanceConfig,
															buffer_size: parseInt(e.target.value) || 0,
														})
													}
													className={!performanceValid ? "border-destructive" : ""}
												/>
											</div>
										</div>
									</CardContent>
								</div>

								{/* Proxy Configuration */}
								<div className="space-y-4">
									<CardHeader className="mb-2 px-0">
										<CardTitle className="flex items-center gap-2 text-base">
											<Globe className="h-4 w-4" />
											Proxy Settings
										</CardTitle>
									</CardHeader>
									<div className="space-y-4">
										<div className="space-y-2">
											<label className="text-sm font-medium">Proxy Type</label>
											<Select value={proxyConfig.type} onValueChange={(value) => updateProxyField("type", value as ProxyType)}>
												<SelectTrigger className="w-48">
													<SelectValue placeholder="Select type" />
												</SelectTrigger>
												<SelectContent>
													<SelectItem value="none">None</SelectItem>
													<SelectItem value="http">HTTP</SelectItem>
													<SelectItem value="socks5">SOCKS5</SelectItem>
													<SelectItem value="environment">Environment</SelectItem>
												</SelectContent>
											</Select>
										</div>

										{proxyConfig.type !== "none" && proxyConfig.type !== "environment" && (
											<div className="space-y-4">
												<div>
													<label className="text-sm font-medium">Proxy URL</label>
													<Input
														placeholder="http://proxy.example.com:8080"
														value={proxyConfig.url || ""}
														onChange={(e) => updateProxyField("url", e.target.value)}
													/>
												</div>
												<div className="grid grid-cols-2 gap-4">
													<div>
														<label className="text-sm font-medium">Username</label>
														<Input
															value={proxyConfig.username || ""}
															onChange={(e) => updateProxyField("username", e.target.value)}
															placeholder="Proxy username"
														/>
													</div>
													<div>
														<label className="text-sm font-medium">Password</label>
														<Input
															type="password"
															value={proxyConfig.password || ""}
															onChange={(e) => updateProxyField("password", e.target.value)}
															placeholder="Proxy password"
														/>
													</div>
												</div>
											</div>
										)}
									</div>
								</div>
								{/* End Proxy Configuration */}
							</> /* end fragment shown when provider selected */
						)}
					</div>

					{/* Form Actions */}
					{availableProviders.length > 0 && (
						<div className="flex justify-end space-x-3">
							<Button type="button" variant="outline" onClick={onCancel}>
								Cancel
							</Button>
							{/* Save button with tooltip explaining disabled state */}
							<TooltipProvider>
								<Tooltip>
									<TooltipTrigger asChild>
										<span>
											<Button type="submit" disabled={!validator.isValid() || isLoading} isLoading={isLoading}>
												<Save className="h-4 w-4" />
												{isLoading ? "Saving..." : "Save Provider"}
											</Button>
										</span>
									</TooltipTrigger>
									{(!validator.isValid() || isLoading) && (
										<TooltipContent>
											<p>{isLoading ? "Saving..." : validator.getFirstError() || "Please fix validation errors"}</p>
										</TooltipContent>
									)}
								</Tooltip>
							</TooltipProvider>
						</div>
					)}
				</form>
			</DialogContent>
		</Dialog>
	);
}
