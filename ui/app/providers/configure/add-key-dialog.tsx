import { Button } from "@/components/ui/button";
import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip";
import { DEFAULT_ALLOWED_REQUESTS, DEFAULT_NETWORK_CONFIG, DEFAULT_PERFORMANCE_CONFIG } from "@/lib/constants/config";
import { ProviderFormData, ProviderFormSchema } from "@/lib/schemas/provider-form";
import { zodResolver } from "@hookform/resolvers/zod";
import { useEffect, useMemo, useState } from "react";
import { useForm } from "react-hook-form";
import { ApiKeysTab, ApiStructureTab, NetworkTab, PerformanceTab } from "./tabs";

interface Props {
	isCustomProvider: boolean;
	keysRequired: boolean;
}

export function AddKeyDialog({ isCustomProvider, keysRequired }: Props) {
	const tabs = useMemo(() => {
		const availableTabs = [];

		// Custom Settings tab is available for custom providers
		if (isCustomProvider) {
			availableTabs.push({
				id: "api-structure",
				label: "API Structure",
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
	}, [keysRequired, isCustomProvider]);

	// Initialize react-hook-form with Zod validation
	const form = useForm<ProviderFormData>({
		resolver: zodResolver(ProviderFormSchema),
		defaultValues: {
			selectedProvider: "",
			customProviderName: "",
			baseProviderType: "",
			keys: [],
			networkConfig: DEFAULT_NETWORK_CONFIG,
			performanceConfig: DEFAULT_PERFORMANCE_CONFIG,
			proxyConfig: undefined,
			sendBackRawResponse: false,
			allowedRequests: DEFAULT_ALLOWED_REQUESTS,
			isDirty: false,
		},
		mode: "onChange",
	});

	const {
		handleSubmit: hookFormHandleSubmit,
		watch,
		setValue,
		getValues,
		formState: { errors, isValid, isDirty: formIsDirty },
	} = form;

	// Watch all form values for compatibility with existing code
	const formData = watch();

	const {
		selectedProvider,
		customProviderName,
		baseProviderType,
		keys,
		networkConfig,
		performanceConfig,
		proxyConfig,
		sendBackRawResponse,
		allowedRequests,
	} = formData;

	const isDirty = formIsDirty;

	// Check if we're editing an existing provider
	const isEditingExisting = useMemo(
		() => allProviders.some((p) => p.name === (customProviderName.trim() || selectedProvider)),
		[allProviders, customProviderName, selectedProvider],
	);

	// For custom providers, use the base provider type to determine validation and configuration
	const effectiveProviderType = useMemo(
		() => (baseProviderType ? baseProviderType : selectedProvider),
		[selectedProvider, baseProviderType],
	);

	const baseURLRequired = selectedProvider === "ollama" || selectedProvider === "sgl";
	const keysRequired = selectedProvider === "custom" || !["ollama", "sgl"].includes(selectedProvider); // Custom providers and most others need keys

	const isCustomProvider =
		selectedProvider === "custom" || !!customProviderName || !!baseProviderType || !Providers.includes(selectedProvider as KnownProvider);

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

	const [selectedTab, setSelectedTab] = useState(tabs[0]?.id || "api-keys");

	useEffect(() => {
		if (!tabs.map((t) => t.id).includes(selectedTab)) {
			setSelectedTab(tabs[0]?.id || "api-keys");
		}
	}, [tabs, selectedTab]);

	/* Key-level configuration validation for Azure and Vertex */
	const getKeyValidation = () => {
		let valid = true;
		let message = "";

		// effectiveProviderType is now defined at the component level

		for (const key of keys) {
			if (effectiveProviderType === "azure" && key.azure_key_config) {
				const endpointValid = !!key.azure_key_config.endpoint && key.azure_key_config.endpoint.trim() !== "";

				// Validate deployments using utility function
				const deploymentsValid = isValidDeployments(key.azure_key_config.deployments);

				if (!endpointValid || !deploymentsValid) {
					valid = false;
					message = "Endpoint and valid Deployments (JSON object) are required for Azure keys";
					break;
				}
			} else if (effectiveProviderType === "vertex" && key.vertex_key_config) {
				const projectValid = !!key.vertex_key_config.project_id && key.vertex_key_config.project_id.trim() !== "";
				const regionValid = !!key.vertex_key_config.region && key.vertex_key_config.region.trim() !== "";

				// Validate auth credentials using utility function
				const credsValid = isValidVertexAuthCredentials(key.vertex_key_config.auth_credentials);

				if (!projectValid || !credsValid || !regionValid) {
					valid = false;
					message = "Project ID, valid Auth Credentials (JSON object or env.VAR), and Region are required for Vertex AI keys";
					break;
				}
			} else if (effectiveProviderType === "bedrock" && key.bedrock_key_config) {
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

	const updateField = (field: string, value: any) => {
		setValue(field as any, value, { shouldDirty: true, shouldValidate: true });
	};

	const addKey = () => {
		const newKey: KeyType = { id: "", value: "", models: [], weight: 1.0 };

		// effectiveProviderType is now defined at the component level

		if (effectiveProviderType === "azure") {
			newKey.azure_key_config = {
				endpoint: "",
				deployments: {},
				api_version: "2024-02-01",
			};
		} else if (effectiveProviderType === "vertex") {
			newKey.vertex_key_config = {
				project_id: "",
				region: "",
				auth_credentials: "",
			};
		} else if (effectiveProviderType === "bedrock") {
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

	return (
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
						{/* API Structure Tab */}
						{selectedTab === "api-structure" && (
							<ApiStructureTab
								customProviderName={customProviderName}
								baseProviderType={baseProviderType}
								allowedRequests={allowedRequests}
								isCustomProvider={isCustomProvider}
								selectedProvider={selectedProvider}
								onUpdateCustomProviderName={(value) => updateField("customProviderName", value)}
								onUpdateBaseProviderType={(value) => updateField("baseProviderType", value)}
								onUpdateAllowedRequest={updateAllowedRequest}
							/>
						)}

						{/* API Keys Tab */}
						{selectedTab === "api-keys" && (
							<ApiKeysTab
								keys={keys}
								effectiveProviderType={effectiveProviderType}
								keysRequired={keysRequired}
								onAddKey={addKey}
								onRemoveKey={removeKey}
								onUpdateKey={updateKey}
								onUpdateKeyAzureConfig={updateKeyAzureConfig}
								onUpdateKeyVertexConfig={updateKeyVertexConfig}
								onUpdateKeyBedrockConfig={updateKeyBedrockConfig}
							/>
						)}

						{/* Network Tab */}
						{selectedTab === "network" && (
							<NetworkTab
								networkConfig={networkConfig}
								proxyConfig={proxyConfig}
								baseURLRequired={baseURLRequired}
								networkChanged={networkChanged}
								onUpdateNetworkConfig={(config) => updateField("networkConfig", config)}
								onUpdateProxyConfig={(config) => updateField("proxyConfig", config)}
							/>
						)}

						{/* Performance Tab */}
						{selectedTab === "performance" && (
							<PerformanceTab
								performanceConfig={performanceConfig}
								sendBackRawResponse={sendBackRawResponse}
								performanceChanged={performanceChanged}
								performanceValid={performanceValid}
								onUpdatePerformanceConfig={(config) => updateField("performanceConfig", config)}
								onUpdateSendBackRawResponse={(value) => updateField("sendBackRawResponse", value)}
							/>
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
										{isLoading ? "Saving..." : isEditingExisting ? "Update Provider" : "Add Provider"}
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
	);
}
