

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
	baseProviderType: KnownProvider | "";
	keys: KeyType[];
	networkConfig: NetworkConfig;
	performanceConfig: ConcurrencyAndBufferSize;
	proxyConfig: ProxyConfig;
	sendBackRawResponse: boolean;
	isDirty: boolean;
	allowedRequests: {
		text_completion: boolean;
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
	const isCustomProvider = provider && !Providers.includes(provider.name as KnownProvider);

	return {
		selectedProvider: providerName,
		customProviderName: isCustomProvider ? provider.name : "",
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
		allowedRequests: provider?.custom_provider_config?.allowed_requests || DEFAULT_ALLOWED_REQUESTS,
	};
};

export default function ProviderForm({ provider, onSave, onCancel, existingProviders, allProviders = [] }: ProviderFormProps) {
	
	const [initialState, setInitialState] = useState<Omit<ProviderFormData, "isDirty">>(createInitialState(provider, getDefaultProvider()));

	// RTK Query mutations
	const [createProvider, { isLoading: isCreating }] = useCreateProviderMutation();
	const [updateProvider, { isLoading: isUpdating }] = useUpdateProviderMutation();
	const isLoading = isCreating || isUpdating;

	return (
		
	);
}
