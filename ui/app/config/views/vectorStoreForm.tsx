"use client";

import { Alert, AlertDescription } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Switch } from "@/components/ui/switch";
import { Textarea } from "@/components/ui/textarea";
import { useGetVectorStoreConfigQuery, useUpdateVectorStoreConfigMutation } from "@/lib/store";
import { VectorStoreConfig, WeaviateConfig, RedisConfig, VectorStoreType } from "@/lib/types/config";
import { getErrorMessage } from "@/lib/store";
import { AlertTriangle, Database, Save } from "lucide-react";
import { useCallback, useEffect, useState } from "react";
import { toast } from "sonner";

const defaultWeaviateConfig: WeaviateConfig = {
	scheme: "http",
	host: "localhost:8080",
	api_key: "",
};

const defaultRedisConfig: RedisConfig = {
	addr: "localhost:6379",
	username: "",
	password: "",
	db: 0
};

export default function VectorStoreForm() {
	const { data: vectorStoreConfig, isLoading } = useGetVectorStoreConfigQuery();
	const [updateVectorStoreConfig] = useUpdateVectorStoreConfigMutation();

	const [localConfig, setLocalConfig] = useState<VectorStoreConfig>({
		enabled: false,
		type: "weaviate",
		config: defaultWeaviateConfig,
	});

	const [needsRestart, setNeedsRestart] = useState(false);
	const [hasChanges, setHasChanges] = useState(false);

	// Update local config when data is loaded
	useEffect(() => {
		if (vectorStoreConfig) {
			setLocalConfig(vectorStoreConfig);
			setHasChanges(false);
		}
	}, [vectorStoreConfig]);

	// Track changes
	useEffect(() => {
		if (vectorStoreConfig) {
			const hasConfigChanges = JSON.stringify(localConfig) !== JSON.stringify(vectorStoreConfig);
			setHasChanges(hasConfigChanges);
			setNeedsRestart(hasConfigChanges);
		}
	}, [localConfig, vectorStoreConfig]);

	const handleEnabledChange = useCallback((enabled: boolean) => {
		setLocalConfig(prev => ({ ...prev, enabled }));
	}, []);

	const handleTypeChange = useCallback((type: VectorStoreType) => {
		const defaultConfig = type === "weaviate" ? defaultWeaviateConfig : defaultRedisConfig;
		setLocalConfig(prev => ({ ...prev, type, config: defaultConfig }));
	}, []);

	const handleWeaviateConfigChange = useCallback((field: keyof WeaviateConfig, value: string | number | boolean | Record<string, string>) => {
		setLocalConfig(prev => ({
			...prev,
			config: {
				...(prev.config as WeaviateConfig),
				[field]: value,
			},
		}));
	}, []);

	const handleRedisConfigChange = useCallback((field: keyof RedisConfig, value: string | number) => {
		setLocalConfig(prev => ({
			...prev,
			config: {
				...(prev.config as RedisConfig),
				[field]: value,
			},
		}));
	}, []);

	const handleSave = useCallback(async () => {
		try {
			await updateVectorStoreConfig(localConfig).unwrap();
			toast.success("Vector store configuration updated successfully.");
			setHasChanges(false);
			setNeedsRestart(false);
		} catch (error) {
			toast.error(getErrorMessage(error));
		}
	}, [localConfig, updateVectorStoreConfig]);

	const renderWeaviateConfig = () => {
		const config = localConfig.config as WeaviateConfig;
		return (
			<div className="space-y-4">
				<div className="grid grid-cols-2 gap-4">
					<div className="space-y-2">
						<Label htmlFor="weaviate-scheme">Scheme</Label>
						<Select value={config.scheme} onValueChange={(value) => handleWeaviateConfigChange("scheme", value)}>
							<SelectTrigger>
								<SelectValue />
							</SelectTrigger>
							<SelectContent>
								<SelectItem value="http">HTTP</SelectItem>
								<SelectItem value="https">HTTPS</SelectItem>
							</SelectContent>
						</Select>
					</div>
					<div className="space-y-2">
						<Label htmlFor="weaviate-host">Host</Label>
						<Input
							id="weaviate-host"
							value={config.host}
							onChange={(e) => handleWeaviateConfigChange("host", e.target.value)}
							placeholder="localhost:8080"
						/>
					</div>
				</div>
				<div className="space-y-2">
					<Label htmlFor="weaviate-api-key">API Key</Label>
					<Input
						id="weaviate-api-key"
						type="password"
						value={config.api_key || ""}
						onChange={(e) => handleWeaviateConfigChange("api_key", e.target.value)}
						placeholder="Enter API key if required"
					/>
				</div>
			</div>
		);
	};

	const renderRedisConfig = () => {
		const config = localConfig.config as RedisConfig;
		return (
			<div className="space-y-4">
				<div className="grid grid-cols-2 gap-4">
					<div className="space-y-2">
						<Label htmlFor="redis-addr">Address</Label>
						<Input
							id="redis-addr"
							value={config.addr}
							onChange={(e) => handleRedisConfigChange("addr", e.target.value)}
							placeholder="localhost:6379"
						/>
					</div>
					<div className="space-y-2">
						<Label htmlFor="redis-db">Database</Label>
						<Input
							id="redis-db"
							type="number"
							value={config.db || 0}
							onChange={(e) => handleRedisConfigChange("db", parseInt(e.target.value) || 0)}
							min="0"
						/>
					</div>
				</div>
				<div className="grid grid-cols-2 gap-4">
					<div className="space-y-2">
						<Label htmlFor="redis-username">Username</Label>
						<Input
							id="redis-username"
							value={config.username || ""}
							onChange={(e) => handleRedisConfigChange("username", e.target.value)}
							placeholder="Redis username"
						/>
					</div>
					<div className="space-y-2">
						<Label htmlFor="redis-password">Password</Label>
						<Input
							id="redis-password"
							type="password"
							value={config.password || ""}
							onChange={(e) => handleRedisConfigChange("password", e.target.value)}
							placeholder="Redis password"
						/>
					</div>
				</div>
			</div>
		);
	};

	if (isLoading) {
		return <div>Loading vector store configuration...</div>;
	}

	return (
		<Card>
			<CardHeader>
				<CardTitle className="flex items-center gap-2">
					<Database className="h-5 w-5" />
					Vector Store Configuration
				</CardTitle>
				<CardDescription>
					Configure vector store for semantic caching and embeddings storage.
				</CardDescription>
			</CardHeader>
			<CardContent className="space-y-6">
				<div className="flex items-center justify-between space-x-2 rounded-lg border p-4">
					<div className="space-y-0.5">
						<Label htmlFor="vector-store-enabled" className="text-sm font-medium">
							Enable Vector Store
						</Label>
						<p className="text-muted-foreground text-sm">
							Enable vector store for semantic caching and embeddings storage.
						</p>
					</div>
					<Switch
						id="vector-store-enabled"
						size="md"
						checked={localConfig.enabled}
						onCheckedChange={handleEnabledChange}
					/>
				</div>

				{localConfig.enabled && (
					<>
						<div className="space-y-2">
							<Label htmlFor="vector-store-type">Vector Store Type</Label>
							<Select value={localConfig.type} onValueChange={handleTypeChange}>
								<SelectTrigger>
									<SelectValue />
								</SelectTrigger>
								<SelectContent>
									<SelectItem value="weaviate">Weaviate</SelectItem>
									<SelectItem value="redis">Redis</SelectItem>
								</SelectContent>
							</Select>
						</div>

						<div className="space-y-4">
							<h4 className="font-medium">
								{localConfig.type === "weaviate" ? "Weaviate Configuration" : "Redis Configuration"}
							</h4>
							{localConfig.type === "weaviate" ? renderWeaviateConfig() : renderRedisConfig()}
						</div>

						{needsRestart && (
							<Alert>
								<AlertTriangle className="h-4 w-4" />
								<AlertDescription>
									Vector store configuration changes require a Bifrost service restart to take effect.
								</AlertDescription>
							</Alert>
						)}

						{hasChanges && (
							<div className="flex justify-end">
								<Button onClick={handleSave} className="flex items-center gap-2">
									<Save className="h-4 w-4" />
									Save Configuration
								</Button>
							</div>
						)}
					</>
				)}
			</CardContent>
		</Card>
	);
}
