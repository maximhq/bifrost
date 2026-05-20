"use client";

import { Alert, AlertDescription } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { EnvVarInput } from "@/components/ui/envVarInput";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Switch } from "@/components/ui/switch";
import { getErrorMessage, useGetCoreConfigQuery, useGetVectorStoreConfigQuery, useUpdateVectorStoreConfigMutation } from "@/lib/store";
import { vectorStoreFormSchema } from "@/lib/schemas/vectorStoreForm";
import { EnvVar } from "@/lib/types/schemas";
import { isRedacted } from "@/lib/utils/validation";
import { AlertTriangle, CircleCheck } from "lucide-react";
import { useEffect, useMemo, useState } from "react";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { toast } from "sonner";
import PluginsForm from "./pluginsForm";

/** Returns true when a boolean-like EnvVar should be considered enabled. */
function isEnvVarTrue(ev: EnvVar): boolean {
	return ev.from_env || ev.value?.toLowerCase() === "true" || ev.value === "1";
}

function isEnvVarPopulated(ev: EnvVar): boolean {
	if (ev.from_env) return true;
	if (!ev.value || isRedacted(ev.value)) return false;
	return true;
}

const clearedEnvVar: EnvVar = { value: "", env_var: "", from_env: false };

type VectorStoreProvider = "redis" | "weaviate" | "qdrant" | "pinecone";

const providerLabels: Record<VectorStoreProvider, string> = {
	redis: "Redis / Valkey",
	weaviate: "Weaviate",
	qdrant: "Qdrant",
	pinecone: "Pinecone",
};

type RedisFormState = {
	addr: EnvVar;
	username: EnvVar;
	password: EnvVar;
	db: EnvVar;
	pool_size: number | "";
	use_tls: EnvVar;
	insecure_skip_verify: EnvVar;
	ca_cert_pem: EnvVar;
	cluster_mode: EnvVar;
};

type WeaviateFormState = {
	scheme: string;
	host: EnvVar;
	api_key: EnvVar;
	grpc_host: EnvVar;
	grpc_secured: boolean;
};

type QdrantFormState = {
	host: EnvVar;
	port: EnvVar;
	api_key: EnvVar;
	use_tls: EnvVar;
};

type PineconeFormState = {
	api_key: EnvVar;
	index_host: EnvVar;
};

type FormStates = {
	redis: RedisFormState;
	weaviate: WeaviateFormState;
	qdrant: QdrantFormState;
	pinecone: PineconeFormState;
};

const emptyEnvVar: EnvVar = { value: "", env_var: "", from_env: false };

const defaultFormStates: FormStates = {
	redis: {
		addr: { ...emptyEnvVar },
		username: { ...emptyEnvVar },
		password: { ...emptyEnvVar },
		db: { value: "0", env_var: "", from_env: false },
		pool_size: 10,
		use_tls: { ...emptyEnvVar },
		insecure_skip_verify: { ...emptyEnvVar },
		ca_cert_pem: { ...emptyEnvVar },
		cluster_mode: { ...emptyEnvVar },
	},
	weaviate: {
		scheme: "http",
		host: { ...emptyEnvVar },
		api_key: { ...emptyEnvVar },
		grpc_host: { ...emptyEnvVar },
		grpc_secured: false,
	},
	qdrant: {
		host: { ...emptyEnvVar },
		port: { value: "6334", env_var: "", from_env: false },
		api_key: { ...emptyEnvVar },
		use_tls: { ...emptyEnvVar },
	},
	pinecone: {
		api_key: { ...emptyEnvVar },
		index_host: { ...emptyEnvVar },
	},
};

function buildConfigPayload(
	provider: VectorStoreProvider,
	forms: FormStates,
	serverConfig?: Record<string, unknown> | null,
): Record<string, unknown> {
	const base: Record<string, unknown> = serverConfig ? { ...serverConfig } : {};
	switch (provider) {
		case "redis": {
			const redis: Record<string, unknown> = {
				...base,
				addr: forms.redis.addr,
				db: forms.redis.db,
				pool_size: forms.redis.pool_size === "" ? 1 : forms.redis.pool_size,
				use_tls: forms.redis.use_tls,
				cluster_mode: forms.redis.cluster_mode,
				username: forms.redis.username,
				password: forms.redis.password,
			};
			const tlsEnabled = isEnvVarTrue(forms.redis.use_tls);
			if (tlsEnabled) {
				redis.insecure_skip_verify = forms.redis.insecure_skip_verify;
				redis.ca_cert_pem = forms.redis.ca_cert_pem;
			} else {
				redis.insecure_skip_verify = { ...clearedEnvVar };
				redis.ca_cert_pem = { ...clearedEnvVar };
			}
			return redis;
		}
		case "weaviate": {
			const weaviate: Record<string, unknown> = {
				...base,
				scheme: forms.weaviate.scheme,
				host: forms.weaviate.host,
				api_key: forms.weaviate.api_key,
			};
			if (isEnvVarPopulated(forms.weaviate.grpc_host)) {
				weaviate.grpc_config = {
					host: forms.weaviate.grpc_host,
					secured: forms.weaviate.grpc_secured,
				};
			} else {
				delete weaviate.grpc_config;
			}
			return weaviate;
		}
		case "qdrant":
			return {
				...base,
				host: forms.qdrant.host,
				port: forms.qdrant.port,
				api_key: forms.qdrant.api_key,
				use_tls: forms.qdrant.use_tls,
			};
		case "pinecone":
			return {
				...base,
				api_key: forms.pinecone.api_key,
				index_host: forms.pinecone.index_host,
			};
	}
}

function parseServerConfig(type: string, config: Record<string, unknown> | null): { provider: VectorStoreProvider; forms: FormStates } {
	const forms = structuredClone(defaultFormStates);
	const provider = (["redis", "weaviate", "qdrant", "pinecone"].includes(type) ? type : "redis") as VectorStoreProvider;

	if (!config) return { provider, forms };

	switch (provider) {
		case "redis":
			forms.redis = {
				addr: (config.addr as EnvVar) ?? { ...emptyEnvVar },
				username: (config.username as EnvVar) ?? { ...emptyEnvVar },
				password: (config.password as EnvVar) ?? { ...emptyEnvVar },
				db: (config.db as EnvVar) ?? { value: "0", env_var: "", from_env: false },
				pool_size: (config.pool_size as number) ?? 10,
				use_tls: (config.use_tls as EnvVar) ?? { ...emptyEnvVar },
				insecure_skip_verify: (config.insecure_skip_verify as EnvVar) ?? { ...emptyEnvVar },
				ca_cert_pem: (config.ca_cert_pem as EnvVar) ?? { ...emptyEnvVar },
				cluster_mode: (config.cluster_mode as EnvVar) ?? { ...emptyEnvVar },
			};
			break;
		case "weaviate": {
			const grpcConfig = config.grpc_config as Record<string, unknown> | undefined;
			forms.weaviate = {
				scheme: (config.scheme as string) ?? "http",
				host: (config.host as EnvVar) ?? { ...emptyEnvVar },
				api_key: (config.api_key as EnvVar) ?? { ...emptyEnvVar },
				grpc_host: (grpcConfig?.host as EnvVar) ?? { ...emptyEnvVar },
				grpc_secured: (grpcConfig?.secured as boolean) ?? false,
			};
			break;
		}
		case "qdrant":
			forms.qdrant = {
				host: (config.host as EnvVar) ?? { ...emptyEnvVar },
				port: (config.port as EnvVar) ?? { value: "6334", env_var: "", from_env: false },
				api_key: (config.api_key as EnvVar) ?? { ...emptyEnvVar },
				use_tls: (config.use_tls as EnvVar) ?? { ...emptyEnvVar },
			};
			break;
		case "pinecone":
			forms.pinecone = {
				api_key: (config.api_key as EnvVar) ?? { ...emptyEnvVar },
				index_host: (config.index_host as EnvVar) ?? { ...emptyEnvVar },
			};
			break;
	}

	return { provider, forms };
}

export default function CachingView() {
	const { data: bifrostConfig, isLoading: configLoading, error: configError } = useGetCoreConfigQuery({ fromDB: true });
	const { data: vsConfig, isLoading: vsLoading, error: vsError } = useGetVectorStoreConfigQuery();
	const [updateVectorStoreConfig, { isLoading: isUpdating }] = useUpdateVectorStoreConfigMutation();
	const hasSettingsUpdateAccess = useRbac(RbacResource.Settings, RbacOperation.Update);

	const [enabled, setEnabled] = useState(false);
	const [provider, setProvider] = useState<VectorStoreProvider>("redis");
	const [formStates, setFormStates] = useState<FormStates>(structuredClone(defaultFormStates));
	const [synced, setSynced] = useState(false);
	const [serverSnapshot, setServerSnapshot] = useState<{ enabled: boolean; provider: VectorStoreProvider; forms: FormStates } | null>(null);
	const [needsRestart, setNeedsRestart] = useState(false);

	// Sync from server data once (also handles empty config for first-time setup)
	useEffect(() => {
		if (synced || vsLoading || vsError) return;

		if (!vsConfig) {
			const snapshot = {
				enabled: false,
				provider: "redis" as VectorStoreProvider,
				forms: structuredClone(defaultFormStates),
			};
			setServerSnapshot(snapshot);
			setEnabled(snapshot.enabled);
			setProvider(snapshot.provider);
			setFormStates(snapshot.forms);
			setSynced(true);
			return;
		}

		const { provider: p, forms } = parseServerConfig(vsConfig.type, vsConfig.config as Record<string, unknown> | null);
		const snapshot = { enabled: vsConfig.enabled, provider: p, forms };
		setServerSnapshot(snapshot);
		setEnabled(vsConfig.enabled);
		setProvider(p);
		setFormStates(forms);
		setSynced(true);
	}, [vsConfig, vsLoading, vsError, synced]);

	const hasChanges = useMemo(() => {
		if (serverSnapshot === null) return false;
		return (
			JSON.stringify({ enabled, provider, config: buildConfigPayload(provider, formStates) }) !==
			JSON.stringify({
				enabled: serverSnapshot.enabled,
				provider: serverSnapshot.provider,
				config: buildConfigPayload(serverSnapshot.provider, serverSnapshot.forms),
			})
		);
	}, [enabled, provider, formStates, serverSnapshot]);

	const handleProviderChange = (value: string) => {
		setProvider(value as VectorStoreProvider);
	};

	const handleSave = async () => {
		if (enabled) {
			const result = vectorStoreFormSchema.safeParse({ provider, ...formStates[provider] });
			if (!result.success) {
				toast.error(result.error.issues[0].message);
				return;
			}
		}

		try {
			const response = await updateVectorStoreConfig({
				enabled,
				type: provider,
				config: buildConfigPayload(
					provider,
					formStates,
					vsConfig?.type === provider ? (vsConfig?.config as Record<string, unknown> | null) : null,
				),
			}).unwrap();
			const snapshot = { enabled, provider, forms: structuredClone(formStates) };
			setServerSnapshot(snapshot);
			setNeedsRestart(response.restart_required);
			toast.success("Vector store configuration saved.");
		} catch (error) {
			toast.error(`Failed to save: ${getErrorMessage(error)}`);
		}
	};

	const isVectorStoreEnabled = bifrostConfig?.is_cache_connected ?? false;
	const isLoading = configLoading || vsLoading;

	const updateRedis = (update: Partial<RedisFormState>) => setFormStates((s) => ({ ...s, redis: { ...s.redis, ...update } }));
	const updateWeaviate = (update: Partial<WeaviateFormState>) => setFormStates((s) => ({ ...s, weaviate: { ...s.weaviate, ...update } }));
	const updateQdrant = (update: Partial<QdrantFormState>) => setFormStates((s) => ({ ...s, qdrant: { ...s.qdrant, ...update } }));
	const updatePinecone = (update: Partial<PineconeFormState>) => setFormStates((s) => ({ ...s, pinecone: { ...s.pinecone, ...update } }));

	return (
		<div className="mx-auto w-full max-w-4xl space-y-4">
			<div>
				<h2 className="text-lg font-semibold tracking-tight">Caching</h2>
				<p className="text-muted-foreground text-sm">Configure semantic caching for requests.</p>
			</div>

			{isLoading && (
				<div className="flex items-center justify-center py-8">
					<p className="text-muted-foreground">Loading configuration...</p>
				</div>
			)}

			{configError !== undefined && (
				<Alert variant="destructive">
					<AlertTriangle className="h-4 w-4" />
					<AlertDescription>{getErrorMessage(configError) || "Failed to load configuration. Please try again."}</AlertDescription>
				</Alert>
			)}

			{vsError !== undefined && (
				<Alert variant="destructive">
					<AlertTriangle className="h-4 w-4" />
					<AlertDescription>{getErrorMessage(vsError) || "Failed to load vector store configuration. Please try again."}</AlertDescription>
				</Alert>
			)}

			{!isLoading && !configError && !vsError && (
				<>
					{/* Vector Store Configuration Card */}
					<div className="space-y-4 rounded-lg border p-4" data-testid="vector-store-card">
						<div className="flex items-center justify-between space-x-2">
							<div className="flex-1 space-y-0.5">
								<Label className="flex items-center gap-2 text-sm font-medium">
									{isVectorStoreEnabled && <CircleCheck className="h-4 w-4 flex-shrink-0 text-green-600" aria-hidden="true" />}
									Vector Store
								</Label>
								<p className="text-muted-foreground text-sm">
									{isVectorStoreEnabled
										? "Vector store is connected and operational."
										: "Configure a vector store provider to enable semantic caching."}
								</p>
							</div>
							<div className="flex items-center gap-2">
								<Switch
									id="vs-enabled"
									size="md"
									checked={enabled}
									onCheckedChange={setEnabled}
									disabled={!hasSettingsUpdateAccess}
									data-testid="vs-enabled-switch"
								/>
								<Button
									onClick={handleSave}
									disabled={!hasChanges || isUpdating || !hasSettingsUpdateAccess}
									size="sm"
									data-testid="vs-save-btn"
								>
									{isUpdating ? "Saving..." : "Save"}
								</Button>
							</div>
						</div>

						{enabled && (
							<>
								<div className="space-y-2">
									<Label htmlFor="vs-provider">Provider</Label>
									<Select value={provider} onValueChange={handleProviderChange}>
										<SelectTrigger id="vs-provider" data-testid="vs-provider-select" className="w-full">
											<SelectValue />
										</SelectTrigger>
										<SelectContent>
											{(Object.keys(providerLabels) as VectorStoreProvider[]).map((key) => (
												<SelectItem key={key} value={key}>
													{providerLabels[key]}
												</SelectItem>
											))}
										</SelectContent>
									</Select>
								</div>

								{provider === "redis" && (
									<>
										<div className="grid grid-cols-2 gap-4">
											<div className="space-y-2">
												<Label htmlFor="vs-redis-addr">Address*</Label>
												<EnvVarInput
													id="vs-redis-addr"
													data-testid="vs-redis-addr"
													placeholder="redis:6379"
													value={formStates.redis.addr}
													onChange={(val) => updateRedis({ addr: val })}
												/>
											</div>
											<div className="space-y-2">
												<Label htmlFor="vs-redis-db">Database</Label>
												<EnvVarInput
													id="vs-redis-db"
													data-testid="vs-redis-db"
													placeholder="0"
													value={formStates.redis.db}
													onChange={(val) => updateRedis({ db: val })}
												/>
											</div>
										</div>
										<div className="grid grid-cols-2 gap-4">
											<div className="space-y-2">
												<Label htmlFor="vs-redis-username">Username</Label>
												<EnvVarInput
													id="vs-redis-username"
													data-testid="vs-redis-username"
													placeholder="Optional"
													value={formStates.redis.username}
													onChange={(val) => updateRedis({ username: val })}
												/>
											</div>
											<div className="space-y-2">
												<Label htmlFor="vs-redis-password">Password</Label>
												<EnvVarInput
													id="vs-redis-password"
													data-testid="vs-redis-password"
													type="password"
													placeholder="Optional"
													value={formStates.redis.password}
													onChange={(val) => updateRedis({ password: val })}
												/>
											</div>
										</div>
										<div className="grid grid-cols-2 gap-4">
											<div className="space-y-2">
												<Label htmlFor="vs-redis-pool">Pool Size</Label>
												<Input
													id="vs-redis-pool"
													data-testid="vs-redis-pool"
													type="number"
													min={1}
													value={formStates.redis.pool_size}
													onChange={(e) => {
														const raw = e.target.value;
														if (raw === "") {
															updateRedis({ pool_size: "" });
															return;
														}
														const val = parseInt(raw);
														if (isNaN(val)) return;
														updateRedis({ pool_size: val < 1 ? 1 : val });
													}}
												/>
											</div>
											<div className="space-y-2">
												<Label htmlFor="vs-redis-cluster">Cluster Mode</Label>
												<div className="flex h-9 items-center">
													<Switch
														id="vs-redis-cluster"
														data-testid="vs-redis-cluster"
														size="md"
														checked={isEnvVarTrue(formStates.redis.cluster_mode)}
														onCheckedChange={(checked) =>
															updateRedis({ cluster_mode: { value: checked ? "true" : "false", env_var: "", from_env: false } })
														}
													/>
												</div>
											</div>
										</div>
										<div className="grid grid-cols-2 gap-4">
											<div className="space-y-2">
												<Label htmlFor="vs-redis-tls">Use TLS</Label>
												<div className="flex h-9 items-center">
													<Switch
														id="vs-redis-tls"
														data-testid="vs-redis-tls"
														size="md"
														checked={isEnvVarTrue(formStates.redis.use_tls)}
														onCheckedChange={(checked) =>
															updateRedis({ use_tls: { value: checked ? "true" : "false", env_var: "", from_env: false } })
														}
													/>
												</div>
											</div>
											{isEnvVarTrue(formStates.redis.use_tls) && (
												<div className="space-y-2">
													<Label htmlFor="vs-redis-skip-verify">Skip TLS Verification</Label>
													<div className="flex h-9 items-center">
														<Switch
															id="vs-redis-skip-verify"
															data-testid="vs-redis-skip-verify"
															size="md"
															checked={isEnvVarTrue(formStates.redis.insecure_skip_verify)}
															onCheckedChange={(checked) =>
																updateRedis({ insecure_skip_verify: { value: checked ? "true" : "false", env_var: "", from_env: false } })
															}
														/>
													</div>
												</div>
											)}
										</div>
										{isEnvVarTrue(formStates.redis.use_tls) && (
											<div className="grid grid-cols-2 gap-4">
												<div className="space-y-2">
													<Label htmlFor="vs-redis-ca-cert">CA Certificate (PEM)</Label>
													<EnvVarInput
														id="vs-redis-ca-cert"
														data-testid="vs-redis-ca-cert"
														placeholder="Optional"
														value={formStates.redis.ca_cert_pem}
														onChange={(val) => updateRedis({ ca_cert_pem: val })}
													/>
												</div>
											</div>
										)}
									</>
								)}

								{provider === "weaviate" && (
									<>
										<div className="grid grid-cols-2 gap-4">
											<div className="space-y-2">
												<Label htmlFor="vs-weaviate-host">Host*</Label>
												<EnvVarInput
													id="vs-weaviate-host"
													data-testid="vs-weaviate-host"
													placeholder="localhost:8080"
													value={formStates.weaviate.host}
													onChange={(val) => updateWeaviate({ host: val })}
												/>
											</div>
											<div className="space-y-2">
												<Label htmlFor="vs-weaviate-scheme">Scheme</Label>
												<Select value={formStates.weaviate.scheme} onValueChange={(val) => updateWeaviate({ scheme: val })}>
													<SelectTrigger id="vs-weaviate-scheme" data-testid="vs-weaviate-scheme" className="w-full">
														<SelectValue />
													</SelectTrigger>
													<SelectContent>
														<SelectItem value="http">http</SelectItem>
														<SelectItem value="https">https</SelectItem>
													</SelectContent>
												</Select>
											</div>
										</div>
										<div className="grid grid-cols-2 gap-4">
											<div className="space-y-2">
												<Label htmlFor="vs-weaviate-apikey">API Key</Label>
												<EnvVarInput
													id="vs-weaviate-apikey"
													data-testid="vs-weaviate-apikey"
													placeholder="Optional"
													value={formStates.weaviate.api_key}
													onChange={(val) => updateWeaviate({ api_key: val })}
												/>
											</div>
										</div>
										<div className="grid grid-cols-2 gap-4">
											<div className="space-y-2">
												<Label htmlFor="vs-weaviate-grpc-host">gRPC Host</Label>
												<EnvVarInput
													id="vs-weaviate-grpc-host"
													data-testid="vs-weaviate-grpc-host"
													placeholder="localhost:50051"
													value={formStates.weaviate.grpc_host}
													onChange={(val) => updateWeaviate({ grpc_host: val })}
												/>
											</div>
											<div className="space-y-2">
												<Label htmlFor="vs-weaviate-grpc-secured">gRPC Secured</Label>
												<div className="flex h-9 items-center">
													<Switch
														id="vs-weaviate-grpc-secured"
														data-testid="vs-weaviate-grpc-secured"
														size="md"
														checked={formStates.weaviate.grpc_secured}
														onCheckedChange={(checked) => updateWeaviate({ grpc_secured: checked })}
													/>
												</div>
											</div>
										</div>
									</>
								)}

								{provider === "qdrant" && (
									<>
										<div className="grid grid-cols-2 gap-4">
											<div className="space-y-2">
												<Label htmlFor="vs-qdrant-host">Host*</Label>
												<EnvVarInput
													id="vs-qdrant-host"
													data-testid="vs-qdrant-host"
													placeholder="localhost"
													value={formStates.qdrant.host}
													onChange={(val) => updateQdrant({ host: val })}
												/>
											</div>
											<div className="space-y-2">
												<Label htmlFor="vs-qdrant-port">Port</Label>
												<EnvVarInput
													id="vs-qdrant-port"
													data-testid="vs-qdrant-port"
													placeholder="6334"
													value={formStates.qdrant.port}
													onChange={(val) => updateQdrant({ port: val })}
												/>
											</div>
										</div>
										<div className="grid grid-cols-2 gap-4">
											<div className="space-y-2">
												<Label htmlFor="vs-qdrant-apikey">API Key</Label>
												<EnvVarInput
													id="vs-qdrant-apikey"
													data-testid="vs-qdrant-apikey"
													placeholder="Optional"
													value={formStates.qdrant.api_key}
													onChange={(val) => updateQdrant({ api_key: val })}
												/>
											</div>
											<div className="space-y-2">
												<Label htmlFor="vs-qdrant-tls">Use TLS</Label>
												<div className="flex h-9 items-center">
													<Switch
														id="vs-qdrant-tls"
														data-testid="vs-qdrant-tls"
														size="md"
														checked={isEnvVarTrue(formStates.qdrant.use_tls)}
														onCheckedChange={(checked) =>
															updateQdrant({ use_tls: { value: checked ? "true" : "false", env_var: "", from_env: false } })
														}
													/>
												</div>
											</div>
										</div>
									</>
								)}

								{provider === "pinecone" && (
									<>
										<div className="grid grid-cols-2 gap-4">
											<div className="space-y-2">
												<Label htmlFor="vs-pinecone-apikey">API Key*</Label>
												<EnvVarInput
													id="vs-pinecone-apikey"
													data-testid="vs-pinecone-apikey"
													placeholder="pc-..."
													value={formStates.pinecone.api_key}
													onChange={(val) => updatePinecone({ api_key: val })}
												/>
											</div>
											<div className="space-y-2">
												<Label htmlFor="vs-pinecone-host">Index Host*</Label>
												<EnvVarInput
													id="vs-pinecone-host"
													data-testid="vs-pinecone-host"
													placeholder="your-index-xxxxxxx.svc.environment.pinecone.io"
													value={formStates.pinecone.index_host}
													onChange={(val) => updatePinecone({ index_host: val })}
												/>
											</div>
										</div>
									</>
								)}
							</>
						)}
					</div>

					{(hasChanges || needsRestart) && <RestartWarning />}
				</>
			)}

			{/* Semantic Cache Card — rendered independently of vector store config */}
			{!configLoading && !configError && <PluginsForm isVectorStoreEnabled={isVectorStoreEnabled} />}
		</div>
	);
}

const RestartWarning = () => {
	return <div className="text-muted-foreground mt-2 pl-4 text-xs font-semibold">Need to restart Bifrost to apply changes.</div>;
};