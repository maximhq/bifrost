"use client";

import { useState, useEffect } from "react";
import Header from "@/components/header";
import { CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Settings, Database, Zap, Save, RefreshCw } from "lucide-react";
import { useToast } from "@/hooks/use-toast";
import { ProviderResponse } from "@/lib/types/config";
import { apiService } from "@/lib/api";
import CoreSettingsForm from "@/components/config/core-settings-form";
import ProvidersList from "@/components/config/providers-list";
import MCPClientsList from "@/components/config/mcp-clients-lists";
import { MCPClient } from "@/lib/types/mcp";

export default function ConfigPage() {
	const [activeTab, setActiveTab] = useState("providers");
	const [isLoading, setIsLoading] = useState(false);
	const [providers, setProviders] = useState<ProviderResponse[]>([]);
	const [mcpClients, setMcpClients] = useState<MCPClient[]>([]);

	const { toast } = useToast();

	// Load configuration data
	useEffect(() => {
		loadProviders();
		loadMcpClients();
	}, []);

	const loadProviders = async () => {
		setIsLoading(true);
		const [data, error] = await apiService.getProviders();
		setIsLoading(false);

		if (error) {
			toast({
				title: "Error",
				description: error,
				variant: "destructive",
			});
			return;
		}
		setProviders(data?.providers || []);
	};

	const loadMcpClients = async () => {
		const [data, error] = await apiService.getMCPClients();
		setMcpClients(data || []);

		if (error) {
			toast({
				title: "Error",
				description: error,
				variant: "destructive",
			});
			return;
		}
	};

	const handleSaveConfig = async () => {
		setIsLoading(true);
		const [, error] = await apiService.saveConfig();
		setIsLoading(false);

		if (error) {
			toast({
				title: "Error",
				description: error,
				variant: "destructive",
			});
		} else {
			toast({
				title: "Success",
				description: "Configuration saved successfully",
			});
		}
	};

	const handleResetConfig = async () => {
		setIsLoading(true);
		const [, error] = await apiService.reloadConfig();
		setIsLoading(false);

		if (error) {
			toast({
				title: "Error",
				description: error,
				variant: "destructive",
			});
		} else {
			toast({
				title: "Success",
				description: "Configuration reset successfully",
			});
			loadProviders();
		}
	};

	return (
		<div className="bg-background">
			<Header title="Configuration" />
			<div className="space-y-6">
				{/* Page Header */}
				<div className="flex items-center justify-between">
					<div>
						<h1 className="text-3xl font-bold">Configuration</h1>
						<p className="text-muted-foreground mt-2">Configure AI providers, API keys, and system settings for your Bifrost instance.</p>
					</div>
					<div className="flex gap-3">
						<Button variant="outline" onClick={handleResetConfig} disabled={isLoading}>
							<RefreshCw className="h-4 w-4" />
							Reset
						</Button>
						<Button onClick={handleSaveConfig} disabled={isLoading}>
							<Save className="h-4 w-4" />
							Save Config
						</Button>
					</div>
				</div>

				{/* Configuration Tabs */}
				<Tabs value={activeTab} onValueChange={setActiveTab} className="space-y-6">
					<TabsList className="grid h-12 w-full grid-cols-3">
						<TabsTrigger value="providers" className="flex items-center gap-2">
							<Database className="h-4 w-4" />
							Providers
							<Badge variant="default" className="ml-1">
								{providers.length}
							</Badge>
						</TabsTrigger>
						<TabsTrigger value="mcp" className="flex items-center gap-2">
							<Zap className="h-4 w-4" />
							MCP Clients
							{mcpClients.length > 0 && (
								<Badge variant="default" className="ml-1">
									{mcpClients.length}
								</Badge>
							)}
						</TabsTrigger>
						<TabsTrigger value="core" className="flex items-center gap-2">
							<Settings className="h-4 w-4" />
							Core Settings
						</TabsTrigger>
					</TabsList>

					{/* Providers Tab */}
					<TabsContent value="providers" className="space-y-4">
						<ProvidersList providers={providers} onRefresh={loadProviders} isLoading={isLoading} />
					</TabsContent>

					{/* MCP Tools Tab */}
					<TabsContent value="mcp" className="space-y-4">
						<MCPClientsList />
					</TabsContent>

					{/* Core Settings Tab */}
					<TabsContent value="core" className="space-y-4">
						<div>
							<CardHeader className="mb-4 px-0">
								<CardTitle className="flex items-center gap-2">Core System Settings</CardTitle>
								<CardDescription>Configure core Bifrost settings like request handling, pool sizes, and system behavior.</CardDescription>
							</CardHeader>
							<CoreSettingsForm />
						</div>
					</TabsContent>
				</Tabs>
			</div>
		</div>
	);
}
