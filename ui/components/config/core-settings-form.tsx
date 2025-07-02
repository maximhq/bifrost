"use client";

import { useState, useEffect } from "react";
import { Input } from "@/components/ui/input";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Switch } from "@/components/ui/switch";
import { Badge } from "@/components/ui/badge";
import { Database, Info, AlertTriangle, Loader2 } from "lucide-react";
import { CoreConfig } from "@/lib/types/config";
import { apiService } from "@/lib/api";
import { toast } from "sonner";
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip";
import { Alert, AlertDescription } from "@/components/ui/alert";

export default function CoreSettingsForm() {
	const [config, setConfig] = useState<CoreConfig>({
		drop_excess_requests: false,
		initial_pool_size: 300,
	});
	const [isLoading, setIsLoading] = useState(true);

	useEffect(() => {
		const fetchConfig = async () => {
			const [coreConfig, error] = await apiService.getCoreConfig();
			if (error) {
				toast.error(error);
			} else if (coreConfig) {
				setConfig(coreConfig);
			}
			setIsLoading(false);
		};
		fetchConfig();
	}, []);

	const handleConfigChange = async (field: keyof CoreConfig, value: boolean | number) => {
		const newConfig = { ...config, [field]: value };
		setConfig(newConfig);

		const [, error] = await apiService.updateCoreConfig(newConfig);
		if (error) {
			toast.error(error);
		} else {
			toast.success("Core setting updated successfully.");
		}
	};

	if (isLoading) {
		return (
			<div className="flex h-64 items-center justify-center">
				<Loader2 className="h-4 w-4 animate-spin" />
			</div>
		);
	}

	return (
		<div className="space-y-6">
			{/* Drop Excess Requests */}
			<div className="flex items-center justify-between space-x-2 rounded-lg border p-4">
				<div className="space-y-0.5">
					<label className="text-sm font-medium">Drop Excess Requests</label>
					<p className="text-muted-foreground text-sm">If enabled, Bifrost will drop requests that exceed pool capacity.</p>
				</div>
				<Switch checked={config.drop_excess_requests} onCheckedChange={(checked) => handleConfigChange("drop_excess_requests", checked)} />
			</div>

			{/* Pool Size */}
			{/* <Card>
				<CardHeader>
					<CardTitle className="flex items-center gap-2 text-base">
						<Database className="h-4 w-4" />
						Connection Pool
					</CardTitle>
					<CardDescription>Set the initial size of the connection pool.</CardDescription>
					<Alert className="mt-4">
						<AlertTriangle className="h-4 w-4" />
						<AlertDescription>
							<strong>Restart Required:</strong> This field has no significance after Bifrost is started. You can change it and save the
							config so that Bifrost uses it on next startup.
						</AlertDescription>
					</Alert>
				</CardHeader>
				<CardContent className="space-y-4">
					<div className="space-y-2">
						<div className="flex items-center gap-2">
							<label htmlFor="initialPoolSize" className="text-sm font-medium">
								Initial Pool Size
							</label>
							<TooltipProvider>
								<Tooltip>
									<TooltipTrigger>
										<Info className="text-muted-foreground h-4 w-4" />
									</TooltipTrigger>
									<TooltipContent>
										<p>Changes take effect on the next application startup. This field has no effect during the rest of the lifecycle.</p>
									</TooltipContent>
								</Tooltip>
							</TooltipProvider>
						</div>
						<div className="flex items-center space-x-2">
							<Input
								id="initialPoolSize"
								type="number"
								min="1"
								max="10000"
								value={config.initial_pool_size}
								onChange={(e) => handleConfigChange("initial_pool_size", parseInt(e.target.value))}
								className="max-w-32"
							/>
							<Badge variant="outline">On Startup</Badge>
						</div>
					</div>
				</CardContent>
			</Card> */}
		</div>
	);
}
