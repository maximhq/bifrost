"use client";

import { Alert, AlertDescription } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Switch } from "@/components/ui/switch";
import { useGetLogStoreConfigQuery, useUpdateLogStoreConfigMutation } from "@/lib/store";
import { LogStoreConfig, SQLiteConfig } from "@/lib/types/config";
import { getErrorMessage } from "@/lib/store";
import { AlertTriangle, Database, Save } from "lucide-react";
import { useCallback, useEffect, useState } from "react";
import { toast } from "sonner";

const defaultSQLiteConfig: SQLiteConfig = {
	path: "./bifrost.db",
};

export default function LogStoreForm() {
	const { data: logStoreConfig, isLoading } = useGetLogStoreConfigQuery();
	const [updateLogStoreConfig] = useUpdateLogStoreConfigMutation();

	const [localConfig, setLocalConfig] = useState<LogStoreConfig>({
		enabled: false,
		type: "sqlite",
		config: defaultSQLiteConfig,
	});

	const [needsRestart, setNeedsRestart] = useState(false);
	const [hasChanges, setHasChanges] = useState(false);

	// Update local config when data is loaded
	useEffect(() => {
		if (logStoreConfig) {
			setLocalConfig(logStoreConfig);
			setHasChanges(false);
		}
	}, [logStoreConfig]);

	// Track changes
	useEffect(() => {
		if (logStoreConfig) {
			const hasConfigChanges = JSON.stringify(localConfig) !== JSON.stringify(logStoreConfig);
			setHasChanges(hasConfigChanges);
			setNeedsRestart(hasConfigChanges);
		}
	}, [localConfig, logStoreConfig]);

	const handleEnabledChange = useCallback((enabled: boolean) => {
		setLocalConfig(prev => ({ ...prev, enabled }));
	}, []);

	const handleSQLiteConfigChange = useCallback((field: keyof SQLiteConfig, value: string) => {
		setLocalConfig(prev => ({
			...prev,
			config: {
				...(prev.config as SQLiteConfig),
				[field]: value,
			},
		}));
	}, []);

	const handleSave = useCallback(async () => {
		try {
			await updateLogStoreConfig(localConfig).unwrap();
			toast.success("Log store configuration updated successfully.");
			setHasChanges(false);
			setNeedsRestart(false);
		} catch (error) {
			toast.error(getErrorMessage(error));
		}
	}, [localConfig, updateLogStoreConfig]);

	if (isLoading) {
		return <div>Loading log store configuration...</div>;
	}

	return (
		<Card>
			<CardHeader>
				<CardTitle className="flex items-center gap-2">
					<Database className="h-5 w-5" />
					Log Store Configuration
				</CardTitle>
				<CardDescription>
					Configure log store for request and response logging to a SQLite database.
				</CardDescription>
			</CardHeader>
			<CardContent className="space-y-6">
				<div className="flex items-center justify-between space-x-2 rounded-lg border p-4">
					<div className="space-y-0.5">
						<Label htmlFor="log-store-enabled" className="text-sm font-medium">
							Enable Log Store
						</Label>
						<p className="text-muted-foreground text-sm">
							Enable logging of requests and responses to a SQLite database. This can add 40-60mb of overhead to the system memory.
						</p>
					</div>
					<Switch
						id="log-store-enabled"
						size="md"
						checked={localConfig.enabled}
						onCheckedChange={handleEnabledChange}
					/>
				</div>

				{localConfig.enabled && (
					<>
						<div className="space-y-4">
							<h4 className="font-medium">SQLite Configuration</h4>
							<div className="space-y-2">
								<Label htmlFor="sqlite-path">Database Path</Label>
								<Input
									id="sqlite-path"
									value={(localConfig.config as SQLiteConfig).path}
									onChange={(e) => handleSQLiteConfigChange("path", e.target.value)}
									placeholder="./bifrost.db"
								/>
								<p className="text-muted-foreground text-xs">
									Path to the SQLite database file. Use relative path for current directory or absolute path.
								</p>
							</div>
						</div>

						{needsRestart && (
							<Alert>
								<AlertTriangle className="h-4 w-4" />
								<AlertDescription>
									Log store configuration changes require a Bifrost service restart to take effect.
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

