"use client";

import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import {
	Breadcrumb,
	BreadcrumbItem,
	BreadcrumbLink,
	BreadcrumbList,
	BreadcrumbPage,
	BreadcrumbSeparator,
} from "@/components/ui/breadcrumb";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Switch } from "@/components/ui/switch";
import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { TagInput } from "@/components/ui/tag-input";
import { Textarea } from "@/components/ui/textarea";
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip";
import { DEFAULT_NETWORK_CONFIG, DEFAULT_PERFORMANCE_CONFIG } from "@/lib/constants/config";
import { ProviderIconType, RenderProviderIcon } from "@/lib/constants/icons";
import { PROVIDER_LABELS, PROVIDERS as Providers } from "@/lib/constants/logs";
import { getErrorMessage, useCreateProviderMutation, useUpdateProviderMutation } from "@/lib/store";
import {
	AddProviderRequest,
	AzureKeyConfig,
	BedrockKeyConfig,
	ConcurrencyAndBufferSize,
	Key as KeyType,
	ModelProvider,
	NetworkConfig,
	ProviderResponse,
	ProxyConfig,
	ProxyType,
	UpdateProviderRequest,
	VertexKeyConfig,
} from "@/lib/types/config";
import { cn } from "@/lib/utils";
import { isRedacted, isValidDeployments, isValidVertexAuthCredentials, Validator } from "@/lib/utils/validation";
import isEqual from "lodash.isequal";
import { AlertTriangle, Info, Plus, Save, X } from "lucide-react";
import { useEffect, useMemo, useState } from "react";
import { toast } from "sonner";

interface ProviderFormProps {
	provider?: ProviderResponse | null;
	onSave: () => void;
	onCancel: () => void;
	existingProviders: string[];
	allProviders?: ProviderResponse[];
}

interface ProviderFormData {
	selectedProvider: string;
	customProviderName: string;
	baseProviderType: string;
	keys: KeyType[];
	networkConfig: NetworkConfig;
	performanceConfig: ConcurrencyAndBufferSize;
	proxyConfig: ProxyConfig;
	sendBackRawResponse: boolean;
	isDirty: boolean;
	allowedRequests: {
		chat_completion: boolean;
		chat_completion_stream: boolean;
		embedding: boolean;
		speech: boolean;
		speech_stream: boolean;
		transcription: boolean;
		transcription_stream: boolean;
	};
}

// A helper function to create a clean initial state
const createInitialState = (provider?: ProviderResponse | null, defaultProvider?: string): Omit<ProviderFormData, "isDirty"> => {
	const isNewProvider = !provider;
	const providerName = provider?.name || defaultProvider || "";
	const keysRequired = !["ollama", "sgl"].includes(providerName); // Vertex needs keys for config

	// Create default key based on provider type
	const createDefaultKey = (): KeyType => {
		const baseKey: KeyType = { id: "", value: "", models: [], weight: 1.0 };

		if (providerName === "azure") {
			baseKey.azure_key_config = {
				endpoint: "",
				deployments: {},
				api_version: "2024-02-01",
			};
		} else if (providerName === "vertex") {
			baseKey.vertex_key_config = {
				project_id: "",
				region: "",
				auth_credentials: "",
			};
		} else if (providerName === "bedrock") {
			baseKey.bedrock_key_config = {
				access_key: "",
				secret_key: "",
				session_token: "",
				region: "us-east-1",
				arn: "",
				deployments: {},
			};
		}

		return baseKey;
	};

	// Check if this is a custom provider
	const isCustomProvider = provider && !Providers.includes(provider.name as any);

	return {
		selectedProvider: providerName,
		customProviderName: isCustomProvider ? provider?.custom_provider_config?.custom_provider_key || provider.name : "",
		baseProviderType: provider?.custom_provider_config?.base_provider_type || "",
		keys: isNewProvider && keysRequired ? [createDefaultKey()] : !isNewProvider && keysRequired && provider?.keys ? provider.keys : [],
		networkConfig: provider?.network_config || DEFAULT_NETWORK_CONFIG,
		performanceConfig: provider?.concurrency_and_buffer_size || DEFAULT_PERFORMANCE_CONFIG,
		proxyConfig: provider?.proxy_config || {
			type: "none",
			url: "",
			username: "",
			password: "",
		},
		sendBackRawResponse: provider?.send_back_raw_response || false,
		allowedRequests: provider?.custom_provider_config?.allowed_requests || {
			chat_completion: true,
			chat_completion_stream: true,
			embedding: true,
			speech: true,
			speech_stream: true,
			transcription: true,
			transcription_stream: true,
		},
	};
};

export default function ProviderForm({ provider, onSave, onCancel, existingProviders, allProviders = [] }: ProviderFormProps) {
	const getDefaultProvider = () => {
		if (provider) return provider.name;
		return Providers.find((p) => !existingProviders.includes(p)) || "";
	};

	const [initialState, setInitialState] = useState<Omit<ProviderFormData, "isDirty">>(createInitialState(provider, getDefaultProvider()));
	const [formData, setFormData] = useState<ProviderFormData>({
		...initialState,
		isDirty: false,
	});

	// RTK Query mutations
	const [createProvider, { isLoading: isCreating }] = useCreateProviderMutation();
	const [updateProvider, { isLoading: isUpdating }] = useUpdateProviderMutation();
	const isLoading = isCreating || isUpdating;

	// Update form when provider prop changes
	useEffect(() => {
		const newInitialState = createInitialState(provider, getDefaultProvider());

		// Debug logging for custom provider initialization
		if (provider && !Providers.includes(provider.name as any)) {
			console.log("Loading custom provider initial state:", {
				provider: provider.name,
				customProviderConfig: provider.custom_provider_config,
				initialState: newInitialState,
			});
		}

		setInitialState(newInitialState);
		setFormData({
			...newInitialState,
			isDirty: false,
		});
	}, [provider]);

	const {
		selectedProvider,
		customProviderName,
		baseProviderType,
		keys,
		networkConfig,
		performanceConfig,
		proxyConfig,
		sendBackRawResponse,
		isDirty,
		allowedRequests,
	} = formData;

	const baseURLRequired = selectedProvider === "ollama" || selectedProvider === "sgl";
	const keysRequired = selectedProvider === "custom" || !["ollama", "sgl"].includes(selectedProvider); // Custom providers and most others need keys
	const keysValid =
		!keysRequired || keys.every((k) => selectedProvider === "vertex" || selectedProvider === "bedrock" || k.value.trim() !== ""); // Vertex and Bedrock can have empty API key
	const keysPresent = !keysRequired || keys.length > 0;
	const isCustomProvider =
		selectedProvider === "custom" || customProviderName || baseProviderType || !Providers.includes(selectedProvider as any);

	const performanceValid =
		performanceConfig.concurrency > 0 && performanceConfig.buffer_size > 0 && performanceConfig.concurrency < performanceConfig.buffer_size;

	// Track if performance settings have changed
	const performanceChanged =
		performanceConfig.concurrency !== initialState.performanceConfig.concurrency ||
		performanceConfig.buffer_size !== initialState.performanceConfig.buffer_size;

	const networkChanged =
		networkConfig.base_url !== initialState.networkConfig.base_url ||
		networkConfig.default_request_timeout_in_seconds !== initialState.networkConfig.default_request_timeout_in_seconds ||
		networkConfig.max_retries !== initialState.networkConfig.max_retries;

	/* Key-level configuration validation for Azure and Vertex */
	const getKeyValidation = () => {
		let valid = true;
		let message = "";

		// For custom providers, use the base provider type to determine validation
		const providerType = selectedProvider === "custom" ? baseProviderType : selectedProvider;

		for (const key of keys) {
			if (providerType === "azure" && key.azure_key_config) {
				const endpointValid = !!key.azure_key_config.endpoint && key.azure_key_config.endpoint.trim() !== "";

				// Validate deployments using utility function
				const deploymentsValid = isValidDeployments(key.azure_key_config.deployments);

				if (!endpointValid || !deploymentsValid) {
					valid = false;
					message = "Endpoint and valid Deployments (JSON object) are required for Azure keys";
					break;
				}
			} else if (providerType === "vertex" && key.vertex_key_config) {
				const projectValid = !!key.vertex_key_config.project_id && key.vertex_key_config.project_id.trim() !== "";
				const regionValid = !!key.vertex_key_config.region && key.vertex_key_config.region.trim() !== "";

				// Validate auth credentials using utility function
				const credsValid = isValidVertexAuthCredentials(key.vertex_key_config.auth_credentials);

				if (!projectValid || !credsValid || !regionValid) {
					valid = false;
					message = "Project ID, valid Auth Credentials (JSON object or env.VAR), and Region are required for Vertex AI keys";
					break;
				}
			} else if (providerType === "bedrock" && key.bedrock_key_config) {
				const accessKey = key.bedrock_key_config.access_key?.trim() || "";
				const secretKey = key.bedrock_key_config.secret_key?.trim() || "";

				// Allow both empty (IAM role auth) or both provided (explicit credentials)
				// But not one empty and one provided
				const bothEmpty = accessKey === "" && secretKey === "";
				const bothProvided = accessKey !== "" && secretKey !== "";

				if (!bothEmpty && !bothProvided) {
					valid = false;
					message = "For Bedrock: either provide both Access Key and Secret Key, or leave both empty for IAM role authentication";
					break;
				}

				// Check for session token when using IAM role path (both keys empty)
				const sessionToken = key.bedrock_key_config.session_token?.trim() || "";
				if (bothEmpty && sessionToken !== "") {
					valid = false;
					message = "Session token cannot be provided when Access Key and Secret Key are empty; remove the token or supply both keys";
					break;
				}

				// Region is always required for Bedrock
				const regionValid = !!key.bedrock_key_config.region && key.bedrock_key_config.region.trim() !== "";
				if (!regionValid) {
					valid = false;
					message = "Region is required for Bedrock keys";
					break;
				}

				const deploymentsValid = isValidDeployments(key.bedrock_key_config.deployments);

				if (key.bedrock_key_config.deployments && Object.keys(key.bedrock_key_config.deployments).length > 0 && !deploymentsValid) {
					valid = false;
					message = "Valid Deployments (JSON object) are required for Bedrock keys";
					break;
				}
			}
		}

		return { valid, message };
	};

	const { valid: keyValid, message: keyErrorMessage } = getKeyValidation();

	useEffect(() => {
		const currentData = {
			selectedProvider,
			customProviderName,
			baseProviderType,
			keys: keysRequired ? keys : [],
			networkConfig,
			performanceConfig,
			proxyConfig,
			sendBackRawResponse,
			allowedRequests,
		};

		const isDirtyValue = !isEqual(initialState, currentData);

		setFormData((prev) => ({
			...prev,
			isDirty: isDirtyValue,
		}));
	}, [
		selectedProvider,
		customProviderName,
		baseProviderType,
		keys,
		networkConfig,
		performanceConfig,
		proxyConfig,
		sendBackRawResponse,
		allowedRequests,
		initialState,
		keysRequired,
	]);

	const updateField = <K extends keyof ProviderFormData>(field: K, value: ProviderFormData[K]) => {
		setFormData((prev) => ({ ...prev, [field]: value }));
	};

	const updateProxyField = <K extends keyof ProxyConfig>(field: K, value: ProxyConfig[K]) => {
		updateField("proxyConfig", { ...proxyConfig, [field]: value });
	};

	const handleSubmit = async (e: React.FormEvent) => {
		// Prevent form submission during custom provider setup
		if (selectedProvider === "custom" && (!customProviderName.trim() || !baseProviderType)) {
			e.preventDefault();
			return;
		}

		if (!validator.isValid()) {
			toast.error(validator.getFirstError());
			return;
		}

		e.preventDefault();

		try {
			// Determine the actual provider name to use
			const actualProviderName = customProviderName.trim() || selectedProvider;

			// Check if we're updating an existing provider
			// For custom providers, we need to check if the original provider exists
			const originalProviderName = provider?.name;
			const isUpdating = !!originalProviderName;

			if (isUpdating) {
				const data: UpdateProviderRequest = {
					keys: keysRequired
						? keys.filter((k) => {
								const providerType = selectedProvider === "custom" ? baseProviderType : selectedProvider;
								return providerType === "vertex" || providerType === "bedrock"
									? true // Include all Vertex and Bedrock keys (API key can be empty)
									: k.value.trim() !== "";
							})
						: [],
					network_config: networkConfig,
					concurrency_and_buffer_size: performanceConfig,
					proxy_config: proxyConfig,
					send_back_raw_response: sendBackRawResponse,
					custom_provider_config: isCustomProvider
						? {
								base_provider_type: baseProviderType as ModelProvider,
								custom_provider_key: customProviderName.trim(),
								allowed_requests: allowedRequests,
							}
						: undefined,
				};

				// Debug logging for custom provider updates
				if (isCustomProvider) {
					console.log("Updating custom provider with data:", {
						provider: actualProviderName,
						data,
						isCustomProvider,
						baseProviderType,
						customProviderKey: customProviderName.trim(),
						allowedRequests,
					});
				}

				await updateProvider({ provider: originalProviderName, data }).unwrap();
			} else {
				const data: AddProviderRequest = {
					provider: actualProviderName as ModelProvider,
					keys: keysRequired
						? keys.filter((k) => {
								const providerType = selectedProvider === "custom" ? baseProviderType : selectedProvider;
								return providerType === "vertex" || providerType === "bedrock"
									? true // Include all Vertex and Bedrock keys (API key can be empty)
									: k.value.trim() !== "";
							})
						: [],
					network_config: networkConfig,
					concurrency_and_buffer_size: performanceConfig,
					proxy_config: proxyConfig,
					custom_provider_config: isCustomProvider
						? {
								base_provider_type: baseProviderType as ModelProvider,
								allowed_requests: allowedRequests,
							}
						: undefined,
				};
				await createProvider(data).unwrap();
			}

			toast.success(`Provider ${isUpdating ? "updated" : "added"} successfully`);
			onSave();
		} catch (error) {
			toast.error(getErrorMessage(error));
		}
	};

	const validator = new Validator([
		// IsDirty validation - check for existing providers with no changes
		Validator.custom(
			!(allProviders.find((p) => p.name === (provider?.name || selectedProvider)) && !isDirty),
			"No changes to save",
		),

		// Provider selection
		Validator.required(selectedProvider, "Please select a provider"),

		// Custom provider validation
		...(isCustomProvider
			? [
					Validator.required(customProviderName.trim(), "Custom provider name is required"),
					Validator.required(baseProviderType, "Base provider type is required for custom providers"),
				]
			: []),

		// Base URL validation
		...(baseURLRequired
			? [
					Validator.required(networkConfig.base_url, "Base URL is required for this provider"),
					Validator.pattern(networkConfig.base_url || "", /^https?:\/\/.+/, "Base URL must start with http:// or https://"),
				]
			: []),

		// API Keys validation
		...(keysRequired
			? [
					Validator.minValue(keys.length, 1, "At least one API key is required"),
					Validator.custom(
						keys.every((k) => {
							const providerType = selectedProvider === "custom" ? baseProviderType : selectedProvider;
							return providerType === "vertex" || providerType === "bedrock" || k.value.trim() !== "";
						}),
						"API key value cannot be empty",
					),
					Validator.custom(
						keys.every((k) => k.weight >= 0 && k.weight <= 1),
						"Key weights must be between 0 and 1",
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

		// Key-level config validation
		Validator.custom(keyValid, keyErrorMessage),
	]);

	const addKey = () => {
		const newKey: KeyType = { id: "", value: "", models: [], weight: 1.0 };

		// For custom providers, use the base provider type to determine key configuration
		const providerType = selectedProvider === "custom" ? baseProviderType : selectedProvider;

		if (providerType === "azure") {
			newKey.azure_key_config = {
				endpoint: "",
				deployments: {},
				api_version: "2024-02-01",
			};
		} else if (providerType === "vertex") {
			newKey.vertex_key_config = {
				project_id: "",
				region: "",
				auth_credentials: "",
			};
		} else if (providerType === "bedrock") {
			newKey.bedrock_key_config = {
				access_key: "",
				secret_key: "",
				session_token: "",
				region: "us-east-1",
				arn: "",
				deployments: {},
			};
		}

		updateField("keys", [...keys, newKey]);

		// Scroll to bottom of API keys section after adding key
		setTimeout(() => {
			const apiKeysSection = document.querySelector('[data-tab="api-keys"]');
			if (apiKeysSection) {
				apiKeysSection.scrollTo({
					top: apiKeysSection.scrollHeight,
				});
			}
		}, 150);
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
			keyToUpdate.weight = Number.parseFloat(value) || 1.0;
		}

		newKeys[index] = keyToUpdate;
		updateField("keys", newKeys);
	};

	const updateKeyAzureConfig = (index: number, field: keyof AzureKeyConfig, value: string | Record<string, string>) => {
		const newKeys = [...keys];
		const keyToUpdate = { ...newKeys[index] };

		if (!keyToUpdate.azure_key_config) {
			keyToUpdate.azure_key_config = {
				endpoint: "",
				deployments: {},
				api_version: "2024-02-01",
			};
		}

		keyToUpdate.azure_key_config = {
			...keyToUpdate.azure_key_config,
			[field]: value,
		};

		newKeys[index] = keyToUpdate;
		updateField("keys", newKeys);
	};

	const updateKeyVertexConfig = (index: number, field: keyof VertexKeyConfig, value: string) => {
		const newKeys = [...keys];
		const keyToUpdate = { ...newKeys[index] };

		if (!keyToUpdate.vertex_key_config) {
			keyToUpdate.vertex_key_config = {
				project_id: "",
				region: "",
				auth_credentials: "",
			};
		}

		keyToUpdate.vertex_key_config = {
			...keyToUpdate.vertex_key_config,
			[field]: value,
		};

		newKeys[index] = keyToUpdate;
		updateField("keys", newKeys);
	};

	const updateKeyBedrockConfig = (index: number, field: keyof BedrockKeyConfig, value: string | Record<string, string>) => {
		const newKeys = [...keys];
		const keyToUpdate = { ...newKeys[index] };

		if (!keyToUpdate.bedrock_key_config) {
			keyToUpdate.bedrock_key_config = {
				access_key: "",
				secret_key: "",
				session_token: "",
				region: "us-east-1",
				arn: "",
				deployments: {},
			};
		}

		keyToUpdate.bedrock_key_config = {
			...keyToUpdate.bedrock_key_config,
			[field]: value,
		};

		newKeys[index] = keyToUpdate;
		updateField("keys", newKeys);
	};

	const updateAllowedRequest = (requestType: keyof typeof allowedRequests, value: boolean) => {
		updateField("allowedRequests", {
			...allowedRequests,
			[requestType]: value,
		});
	};

	const tabs = useMemo(() => {
		const availableTabs = [];

		// Custom Settings tab is available for custom providers
		if (isCustomProvider) {
			availableTabs.push({
				id: "custom-settings",
				label: "Custom Settings",
			});
		}

		// API Keys tab is available for providers that require keys
		if (keysRequired) {
			availableTabs.push({
				id: "api-keys",
				label: "API Keys",
			});
		}

		// Network tab is always available
		availableTabs.push({
			id: "network",
			label: "Network",
		});

		// Performance tab is always available
		availableTabs.push({
			id: "performance",
			label: "Performance",
		});

		return availableTabs;
	}, [keysRequired, selectedProvider, isCustomProvider]);

	const [selectedTab, setSelectedTab] = useState(tabs[0]?.id || "api-keys");

	useEffect(() => {
		if (!tabs.map((t) => t.id).includes(selectedTab)) {
			setSelectedTab(tabs[0]?.id || "api-keys");
		}
	}, [tabs]);

	return (
		<div className="-mt-6 flex w-full flex-col gap-6">
			<Breadcrumb>
				<BreadcrumbList>
					<BreadcrumbItem>
						<BreadcrumbLink href="/providers">Providers</BreadcrumbLink>
					</BreadcrumbItem>
					<BreadcrumbSeparator />
					<BreadcrumbItem>
						<BreadcrumbPage>Configure provider</BreadcrumbPage>
					</BreadcrumbItem>
				</BreadcrumbList>
			</Breadcrumb>
			<form
				onSubmit={handleSubmit}
				onKeyDown={(e) => {
					if (e.key === "Enter" && e.target !== e.currentTarget) {
						e.preventDefault();
					}
				}}
				className="dark:bg-card flex gap-4 bg-white"
			>
				{/* Provider Selection Sidebar */}
				<TooltipProvider>
					<div className="flex w-[250px] flex-col gap-1 pb-10">
						<div className="rounded-md bg-zinc-50/50 p-4 dark:bg-zinc-800/20">
							{/* Standard Providers */}
							<div className="mb-4">
								<div className="text-muted-foreground mb-2 text-xs font-medium">Standard Providers</div>
								{Providers.map((p) => {
									const existingProvider = allProviders.find((provider) => provider.name === p);
									return (
										<Tooltip key={p}>
											<TooltipTrigger
												className={cn(
													"mb-1 flex w-full items-center gap-2 rounded-lg border px-3 py-1 text-sm",
													selectedProvider === p
														? "bg-secondary opacity-100 hover:opacity-100"
														: "hover:bg-secondary cursor-pointer border-transparent opacity-100 hover:border",
												)}
												onClick={(e) => {
													e.preventDefault();
													if (existingProvider) {
														// Load existing provider data
														const initialState = createInitialState(existingProvider);
														setFormData({ ...initialState, isDirty: false });
														setInitialState(initialState);
													} else {
														// Reset form for new provider
														const initialState = createInitialState(null, p);
														setFormData({ ...initialState, isDirty: false });
														setInitialState(initialState);
													}
												}}
												asChild
											>
												<span>
													<RenderProviderIcon provider={p as ProviderIconType} size="sm" className="h-4 w-4" />
													<div className="text-sm">{PROVIDER_LABELS[p as keyof typeof PROVIDER_LABELS]}</div>
												</span>
											</TooltipTrigger>
										</Tooltip>
									);
								})}
							</div>

							{/* Custom Providers */}
							<div className="mb-4">
								<Button
									variant="outline"
									size="sm"
									className="w-full justify-start"
									onClick={(e) => {
										e.preventDefault();
										e.stopPropagation();

										// Create a new custom provider state without resetting the entire form
										const newFormData = {
											...formData,
											selectedProvider: "custom", // Use a placeholder that won't conflict
											customProviderName: `new-provider-${Date.now()}`,
											baseProviderType: "openai",
											keys: [],
											isDirty: true,
										};

										setFormData(newFormData);

										// Update initial state to match
										const newInitialState = {
											...initialState,
											selectedProvider: "custom",
											customProviderName: `new-provider-${Date.now()}`,
											baseProviderType: "openai",
											keys: [],
										};

										setInitialState(newInitialState);
									}}
								>
									<Plus className="mr-2 h-4 w-4" />
									Add Custom Provider
								</Button>
							</div>
							{allProviders.filter((p) => !Providers.includes(p.name as any)).length > 0 && (
								<div>
									<div className="text-muted-foreground mb-2 text-xs font-medium">Custom Providers</div>
									{allProviders
										.filter((p) => !Providers.includes(p.name as any))
										.map((provider) => (
											<Tooltip key={provider.name}>
												<TooltipTrigger
													className={cn(
														"mb-1 flex w-full items-center gap-2 rounded-lg border px-3 py-1 text-sm",
														selectedProvider === provider.name
															? "bg-secondary opacity-100 hover:opacity-100"
															: "hover:bg-secondary cursor-pointer border-transparent opacity-100 hover:border",
													)}
													onClick={(e) => {
														e.preventDefault();
														// Load existing custom provider data
														const initialState = createInitialState(provider);
														setFormData({ ...initialState, isDirty: false });
														setInitialState(initialState);
													}}
													asChild
												>
													<span>
														<RenderProviderIcon provider="openai" size="sm" className="h-4 w-4" />
														<div className="text-sm">{provider.name}</div>
													</span>
												</TooltipTrigger>
											</Tooltip>
										))}
								</div>
							)}
						</div>
					</div>
				</TooltipProvider>

				<div className="dark:bg-card flex h-full w-full flex-col justify-between bg-white px-2">
					<Tabs defaultValue={tabs[0]?.id} value={selectedTab} onValueChange={setSelectedTab} className="space-y-6">
						<TabsList style={{ gridTemplateColumns: `repeat(${tabs.length}, 1fr)` }} className={`mb-4 grid h-10 w-full`}>
							{tabs.map((tab) => (
								<TabsTrigger key={tab.id} value={tab.id} className="flex items-center gap-2">
									{tab.label}
								</TabsTrigger>
							))}
						</TabsList>

						{/* Container for Tab Content */}
						<div className="relative">
							<div>
								{/* Custom Settings Tab */}
								{isCustomProvider && selectedTab === "custom-settings" && (
									<div className="space-y-6">
										<div className="space-y-4">
											<div>
												<label className="mb-2 block text-sm font-medium">Provider Name</label>
												<Input
													placeholder="Enter custom provider name"
													value={customProviderName}
													onChange={(e) => updateField("customProviderName", e.target.value)}
													className=""
												/>
												<p className="text-muted-foreground mt-1 text-xs">A unique name for your custom provider</p>
											</div>
											<div>
												<label className="mb-2 block text-sm font-medium">Base Provider Type</label>
												<Select value={baseProviderType} onValueChange={(value) => updateField("baseProviderType", value)}>
													<SelectTrigger className="w-full">
														<SelectValue placeholder="Select base provider type" />
													</SelectTrigger>
													<SelectContent>
														<SelectItem value="openai">OpenAI</SelectItem>
														<SelectItem value="anthropic">Anthropic</SelectItem>
														<SelectItem value="azure">Azure OpenAI</SelectItem>
														<SelectItem value="bedrock">AWS Bedrock</SelectItem>
														<SelectItem value="cohere">Cohere</SelectItem>
													</SelectContent>
												</Select>
												<p className="text-muted-foreground mt-1 text-xs">The underlying provider type this custom provider will use</p>
											</div>
										</div>

										{/* Allowed Requests Configuration */}
										<div className="space-y-4">
											<div className="text-sm font-medium">Allowed Request Types</div>
											<p className="text-muted-foreground text-xs">Select which request types this custom provider can handle</p>

											<div className="grid grid-cols-2 gap-4">
												<div className="space-y-3">
													<div className="flex items-center justify-between">
														<label className="text-sm">Chat Completion</label>
														<Switch
															size="md"
															checked={allowedRequests.chat_completion}
															onCheckedChange={(checked) => updateAllowedRequest("chat_completion", checked)}
														/>
													</div>
													<div className="flex items-center justify-between">
														<label className="text-sm">Chat Completion Stream</label>
														<Switch
															size="md"
															checked={allowedRequests.chat_completion_stream}
															onCheckedChange={(checked) => updateAllowedRequest("chat_completion_stream", checked)}
														/>
													</div>
													<div className="flex items-center justify-between">
														<label className="text-sm">Embedding</label>
														<Switch
															size="md"
															checked={allowedRequests.embedding}
															onCheckedChange={(checked) => updateAllowedRequest("embedding", checked)}
														/>
													</div>
													<div className="flex items-center justify-between">
														<label className="text-sm">Speech</label>
														<Switch
															size="md"
															checked={allowedRequests.speech}
															onCheckedChange={(checked) => updateAllowedRequest("speech", checked)}
														/>
													</div>
												</div>
												<div className="space-y-3">
													<div className="flex items-center justify-between">
														<label className="text-sm">Speech Stream</label>
														<Switch
															size="md"
															checked={allowedRequests.speech_stream}
															onCheckedChange={(checked) => updateAllowedRequest("speech_stream", checked)}
														/>
													</div>
													<div className="flex items-center justify-between">
														<label className="text-sm">Transcription</label>
														<Switch
															size="md"
															checked={allowedRequests.transcription}
															onCheckedChange={(checked) => updateAllowedRequest("transcription", checked)}
														/>
													</div>
													<div className="flex items-center justify-between">
														<label className="text-sm">Transcription Stream</label>
														<Switch
															size="md"
															checked={allowedRequests.transcription_stream}
															onCheckedChange={(checked) => updateAllowedRequest("transcription_stream", checked)}
														/>
													</div>
												</div>
											</div>
										</div>
									</div>
								)}

								{/* API Keys Tab */}
								{keysRequired && selectedTab === "api-keys" && (
									<div data-tab="api-keys" className="max-h-[60vh] space-y-4 overflow-x-hidden overflow-y-auto">
										<div className="flex items-center justify-between">
											<Button className="ml-auto" type="button" variant="outline" size="sm" onClick={addKey}>
												<Plus className="h-4 w-4" />
												Add Key
											</Button>
										</div>
										{(selectedProvider === "bedrock" || (selectedProvider === "custom" && baseProviderType === "bedrock")) && (
											<Alert variant="default">
												<Info className="mt-0.5 h-4 w-4 flex-shrink-0 text-blue-600" />
												<AlertTitle>IAM Role Authentication</AlertTitle>
												<AlertDescription>
													Leave both Access Key and Secret Key empty to use IAM roles attached to your environment (EC2, Lambda, ECS, EKS).
													This is the recommended approach for production deployments.
												</AlertDescription>
											</Alert>
										)}
										<div className="space-y-4">
											{keys.map((key, index) => (
												<div key={index} className="space-y-4 rounded-sm border p-4">
													<div className="flex gap-4">
														{selectedProvider !== "vertex" &&
															selectedProvider !== "bedrock" &&
															!(selectedProvider === "custom" && (baseProviderType === "vertex" || baseProviderType === "bedrock")) && (
																<div className="flex-1">
																	<div className="mb-2 text-sm font-medium">API Key</div>
																	<Input
																		placeholder="API Key or env.MY_KEY"
																		value={key.value}
																		onChange={(e) => updateKey(index, "value", e.target.value)}
																		type="text"
																		className="flex-1"
																	/>
																</div>
															)}

														<div>
															<div className="mb-2 flex items-center gap-4">
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
																step="0.01"
																min="0"
																className={cn("w-20", keysRequired && (key.weight < 0 || key.weight > 1) && "border-destructive")}
															/>
														</div>
													</div>
													<div>
														<div className="mb-2 flex items-center gap-2">
															<label className="text-sm font-medium">Models</label>
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

													{/* Azure Key Configuration */}
													{(selectedProvider === "azure" || (selectedProvider === "custom" && baseProviderType === "azure")) && (
														<div className="space-y-4">
															<div>
																<label className="mb-2 block text-sm font-medium">Endpoint (Required)</label>
																<Input
																	placeholder="https://your-resource.openai.azure.com or env.AZURE_ENDPOINT"
																	value={key.azure_key_config?.endpoint || ""}
																	onChange={(e) => updateKeyAzureConfig(index, "endpoint", e.target.value)}
																	className=""
																/>
															</div>
															<div>
																<label className="mb-2 block text-sm font-medium">API Version (Optional)</label>
																<Input
																	placeholder="2024-02-01 or env.AZURE_API_VERSION"
																	value={key.azure_key_config?.api_version || ""}
																	onChange={(e) => updateKeyAzureConfig(index, "api_version", e.target.value)}
																/>
															</div>
															<div>
																<label className="mb-2 block text-sm font-medium">Deployments (Required)</label>
																<div className="text-muted-foreground mb-2 text-xs">
																	JSON object mapping model names to deployment names
																</div>
																<Textarea
																	placeholder='{"gpt-4": "my-gpt4-deployment", "gpt-3.5-turbo": "my-gpt35-deployment"}'
																	value={
																		typeof key.azure_key_config?.deployments === "string"
																			? key.azure_key_config.deployments
																			: JSON.stringify(key.azure_key_config?.deployments || {}, null, 2)
																	}
																	onChange={(e) => {
																		// Store as string during editing to allow intermediate invalid states
																		updateKeyAzureConfig(index, "deployments", e.target.value);
																	}}
																	onBlur={(e) => {
																		// Try to parse as JSON on blur, but keep as string if invalid
																		const value = e.target.value.trim();
																		if (value) {
																			try {
																				const parsed = JSON.parse(value);
																				if (typeof parsed === "object" && parsed !== null) {
																					updateKeyAzureConfig(index, "deployments", parsed);
																				}
																			} catch {
																				// Keep as string for validation on submit
																			}
																		}
																	}}
																	rows={3}
																	className="max-w-full font-mono text-sm wrap-anywhere"
																/>
															</div>
														</div>
													)}

													{/* Vertex Key Configuration */}
													{(selectedProvider === "vertex" || (selectedProvider === "custom" && baseProviderType === "vertex")) && (
														<div className="space-y-4 pt-2">
															<div>
																<label className="mb-2 block text-sm font-medium">Project ID (Required)</label>
																<Input
																	placeholder="your-gcp-project-id or env.VERTEX_PROJECT_ID"
																	value={key.vertex_key_config?.project_id || ""}
																	onChange={(e) => updateKeyVertexConfig(index, "project_id", e.target.value)}
																	className=""
																/>
															</div>
															<div>
																<label className="mb-2 block text-sm font-medium">Region (Required)</label>
																<Input
																	placeholder="us-central1 or env.VERTEX_REGION"
																	value={key.vertex_key_config?.region || ""}
																	onChange={(e) => updateKeyVertexConfig(index, "region", e.target.value)}
																	className=""
																/>
															</div>
															<div>
																<label className="mb-2 block text-sm font-medium">Auth Credentials (Required)</label>
																<div className="text-muted-foreground mb-2 text-xs">Service account JSON object or env.VAR_NAME</div>
																<Textarea
																	placeholder='{"type":"service_account","project_id":"your-gcp-project",...} or env.VERTEX_CREDENTIALS'
																	value={key.vertex_key_config?.auth_credentials || ""}
																	onChange={(e) => {
																		// Always store as string - backend expects string type
																		updateKeyVertexConfig(index, "auth_credentials", e.target.value);
																	}}
																	rows={4}
																	className="max-w-full font-mono text-sm wrap-anywhere"
																/>
																{isRedacted(key.vertex_key_config?.auth_credentials || "") && (
																	<div className="text-muted-foreground mt-1 flex items-center gap-1 text-xs">
																		<Info className="h-3 w-3" />
																		<span>Credentials are stored securely. Edit to update.</span>
																	</div>
																)}
															</div>
														</div>
													)}

													{/* Bedrock Key Configuration */}
													{(selectedProvider === "bedrock" || (selectedProvider === "custom" && baseProviderType === "bedrock")) && (
														<div className="space-y-4 pt-2">
															<div>
																<label className="mb-2 block text-sm font-medium">Access Key</label>
																<Input
																	placeholder="your-aws-access-key or env.AWS_ACCESS_KEY_ID"
																	value={key.bedrock_key_config?.access_key || ""}
																	onChange={(e) => updateKeyBedrockConfig(index, "access_key", e.target.value)}
																	className=""
																/>
															</div>
															<div>
																<label className="mb-2 block text-sm font-medium">Secret Key</label>
																<Input
																	placeholder="your-aws-secret-key or env.AWS_SECRET_ACCESS_KEY"
																	value={key.bedrock_key_config?.secret_key || ""}
																	onChange={(e) => updateKeyBedrockConfig(index, "secret_key", e.target.value)}
																	className=""
																/>
															</div>
															<div>
																<label className="mb-2 block text-sm font-medium">Session Token (Optional)</label>
																<Input
																	placeholder="your-aws-session-token or env.AWS_SESSION_TOKEN"
																	value={key.bedrock_key_config?.session_token || ""}
																	onChange={(e) => updateKeyBedrockConfig(index, "session_token", e.target.value)}
																	className=""
																/>
															</div>
															<div>
																<label className="mb-2 block text-sm font-medium">Region (Required)</label>
																<Input
																	placeholder="us-east-1 or env.AWS_REGION"
																	value={key.bedrock_key_config?.region || ""}
																	onChange={(e) => updateKeyBedrockConfig(index, "region", e.target.value)}
																	className=""
																/>
															</div>
															<div>
																<label className="mb-2 block text-sm font-medium">Deployments (Optional)</label>
																<div className="text-muted-foreground mb-2 text-xs">
																	JSON object mapping model names to inference profile names
																</div>
																<Textarea
																	placeholder='{"gpt-4": "my-gpt4-deployment", "gpt-3.5-turbo": "my-gpt35-deployment"}'
																	value={
																		typeof key.bedrock_key_config?.deployments === "string"
																			? key.bedrock_key_config.deployments
																			: JSON.stringify(key.bedrock_key_config?.deployments || {}, null, 2)
																	}
																	onChange={(e) => {
																		// Store as string during editing to allow intermediate invalid states
																		updateKeyBedrockConfig(index, "deployments", e.target.value);
																	}}
																	onBlur={(e) => {
																		// Try to parse as JSON on blur, but keep as string if invalid
																		const value = e.target.value.trim();
																		if (value) {
																			try {
																				const parsed = JSON.parse(value);
																				if (typeof parsed === "object" && parsed !== null) {
																					updateKeyBedrockConfig(index, "deployments", parsed);
																				}
																			} catch {
																				// Keep as string for validation on submit
																			}
																		}
																	}}
																	rows={3}
																	className="max-w-full font-mono text-sm wrap-anywhere"
																/>
															</div>
														</div>
													)}

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

								{/* Network Tab */}
								{selectedTab === "network" && (
									<div className="space-y-6">
										<div className={cn("hidden", networkChanged && "block")}>
											<Alert>
												<AlertTriangle className="h-4 w-4" />
												<AlertDescription>
													The settings below require a Bifrost service restart to take effect. Current connections will continue with
													existing settings until restart.
												</AlertDescription>
											</Alert>
										</div>

										{/* Network Configuration */}
										<div className="space-y-4">
											<div className="grid grid-cols-1 gap-4">
												<div>
													<label className="mb-2 block text-sm font-medium">Base URL {baseURLRequired ? "(Required)" : "(Optional)"}</label>
													<Input
														placeholder="https://api.example.com"
														value={networkConfig.base_url || ""}
														onChange={(e) =>
															updateField("networkConfig", {
																...networkConfig,
																base_url: e.target.value,
															})
														}
														className=""
													/>
												</div>
												<div className="grid grid-cols-2 gap-4">
													<div>
														<label className="mb-2 block text-sm font-medium">Timeout (seconds)</label>
														<Input
															type="number"
															placeholder="30"
															value={networkConfig.default_request_timeout_in_seconds}
															onChange={(e) => {
																updateField("networkConfig", {
																	...networkConfig,
																	default_request_timeout_in_seconds: Number.parseInt(e.target.value),
																});
															}}
															min={1}
															className=""
														/>
													</div>
													<div>
														<label className="mb-2 block text-sm font-medium">Max Retries</label>
														<Input
															type="number"
															placeholder="0"
															value={networkConfig.max_retries}
															onChange={(e) =>
																updateField("networkConfig", {
																	...networkConfig,
																	max_retries: Number.parseInt(e.target.value) || 0,
																})
															}
															min={0}
															className=""
														/>
													</div>
												</div>
											</div>
										</div>

										{/* Proxy Configuration */}
										<div className="space-y-4">
											<div className="space-y-4">
												<div>
													<label className="mb-2 block text-sm font-medium">Proxy Type</label>
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

												<div
													className={cn("overflow-hidden", (proxyConfig.type === "none" || proxyConfig.type === "environment") && "hidden")}
												>
													<div className="space-y-4 pt-2">
														<div>
															<label className="mb-2 block text-sm font-medium">Proxy URL</label>
															<Input
																placeholder="http://proxy.example.com"
																value={proxyConfig.url || ""}
																onChange={(e) => updateProxyField("url", e.target.value)}
																className=""
															/>
														</div>
														<div className="grid grid-cols-2 gap-4">
															<div>
																<label className="mb-2 block text-sm font-medium">Username</label>
																<Input
																	value={proxyConfig.username || ""}
																	onChange={(e) => updateProxyField("username", e.target.value)}
																	placeholder="Proxy username"
																	className=""
																/>
															</div>
															<div>
																<label className="mb-2 block text-sm font-medium">Password</label>
																<Input
																	type="password"
																	value={proxyConfig.password || ""}
																	onChange={(e) => updateProxyField("password", e.target.value)}
																	placeholder="Proxy password"
																	className=""
																/>
															</div>
														</div>
													</div>
												</div>
											</div>
										</div>
									</div>
								)}

								{/* Performance Tab */}
								{selectedTab === "performance" && (
									<div className="space-y-2">
										<div className={cn("overflow-hidden", !performanceChanged && "hidden")}>
											<Alert className="mb-4">
												<AlertTriangle className="h-4 w-4" />
												<AlertDescription>
													<strong>Heads up:</strong> Changing concurrency or buffer size may temporarily affect request latency for this
													provider while the new settings are being applied.
												</AlertDescription>
											</Alert>
										</div>
										<div className="grid grid-cols-2 gap-4">
											<div>
												<label className="mb-2 block text-sm font-medium">Concurrency</label>
												<Input
													type="number"
													value={performanceConfig.concurrency}
													onChange={(e) =>
														updateField("performanceConfig", {
															...performanceConfig,
															concurrency: Number.parseInt(e.target.value) || 1,
														})
													}
													className={`${!performanceValid ? "border-destructive" : ""}`}
												/>
											</div>
											<div>
												<label className="mb-2 block text-sm font-medium">Buffer Size</label>
												<Input
													type="number"
													value={performanceConfig.buffer_size}
													onChange={(e) =>
														updateField("performanceConfig", {
															...performanceConfig,
															buffer_size: Number.parseInt(e.target.value) || 10,
														})
													}
													className={`${!performanceValid ? "border-destructive" : ""}`}
												/>
											</div>
										</div>

										<div className="mt-6 space-y-4">
											<div className="flex items-center justify-between space-x-2">
												<div className="space-y-0.5">
													<label className="text-sm font-medium">Include Raw Response</label>
													<p className="text-muted-foreground text-xs">
														Include the raw provider response alongside the parsed response for debugging and advanced use cases
													</p>
												</div>
												<Switch
													size="md"
													checked={sendBackRawResponse}
													onCheckedChange={(checked) => updateField("sendBackRawResponse", checked)}
												/>
											</div>
										</div>
									</div>
								)}
							</div>
						</div>
					</Tabs>

					{/* Form Actions */}
					<div className="dark:bg-card sticky bottom-0 bg-white pt-10">
						<div className="flex justify-end space-x-3">
							<Button type="button" variant="outline" onClick={onCancel} className="">
								Cancel
							</Button>
							<TooltipProvider>
								<Tooltip>
									<TooltipTrigger asChild>
										<span>
											<Button type="submit" disabled={!validator.isValid() || isLoading} isLoading={isLoading} className="">
												<Save className="h-4 w-4" />
												{isLoading
													? "Saving..."
													: allProviders.find((p) => p.name === selectedProvider)
														? "Update Provider"
														: "Add Provider"}
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
					</div>
				</div>
			</form>
		</div>
	);
}
