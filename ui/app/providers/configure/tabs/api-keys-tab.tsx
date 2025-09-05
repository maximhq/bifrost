"use client";

import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { TagInput } from "@/components/ui/tag-input";
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip";
import { AzureKeyConfig, BedrockKeyConfig, Key as KeyType, VertexKeyConfig } from "@/lib/types/config";
import { cn } from "@/lib/utils";
import { Info, Plus, X } from "lucide-react";
import { AzureConfig } from "../provider-configs/azure-config";
import { BedrockConfig } from "../provider-configs/bedrock-config";
import { VertexConfig } from "../provider-configs/vertex-config";

interface ApiKeysTabProps {
	keys: KeyType[];
	effectiveProviderType: string;
	keysRequired: boolean;
	onAddKey: () => void;
	onRemoveKey: (index: number) => void;
	onUpdateKey: (index: number, field: keyof KeyType, value: string | number | string[]) => void;
	onUpdateKeyAzureConfig: (index: number, field: keyof AzureKeyConfig, value: string | Record<string, string>) => void;
	onUpdateKeyVertexConfig: (index: number, field: keyof VertexKeyConfig, value: string) => void;
	onUpdateKeyBedrockConfig: (index: number, field: keyof BedrockKeyConfig, value: string | Record<string, string>) => void;
}

export function ApiKeysTab({
	keys,
	effectiveProviderType,
	keysRequired,
	onAddKey,
	onRemoveKey,
	onUpdateKey,
	onUpdateKeyAzureConfig,
	onUpdateKeyVertexConfig,
	onUpdateKeyBedrockConfig,
}: ApiKeysTabProps) {
	if (!keysRequired) {
		return null;
	}

	return (
		<div data-tab="api-keys" className="max-h-[60vh] space-y-4 overflow-x-hidden overflow-y-auto">
			<div className="flex items-center justify-between">
				<Button className="ml-auto" type="button" variant="outline" size="sm" onClick={onAddKey}>
					<Plus className="h-4 w-4" />
					Add Key
				</Button>
			</div>

			<div className="space-y-4">
				{keys.map((key, index) => (
					<div key={index} className="space-y-4 rounded-sm border p-4">
						<div className="flex gap-4">
							{effectiveProviderType !== "vertex" && effectiveProviderType !== "bedrock" && (
								<div className="flex-1">
									<div className="mb-2 text-sm font-medium">API Key</div>
									<Input
										placeholder="API Key or env.MY_KEY"
										value={key.value}
										onChange={(e) => onUpdateKey(index, "value", e.target.value)}
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
									onChange={(e) => onUpdateKey(index, "weight", e.target.value)}
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
								onValueChange={(newModels) => onUpdateKey(index, "models", newModels)}
							/>
						</div>

						{/* Provider-specific configurations */}
						{effectiveProviderType === "azure" && key.azure_key_config && (
							<AzureConfig keyIndex={index} keyConfig={key.azure_key_config} onUpdate={onUpdateKeyAzureConfig} />
						)}

						{effectiveProviderType === "vertex" && key.vertex_key_config && (
							<VertexConfig keyIndex={index} keyConfig={key.vertex_key_config} onUpdate={onUpdateKeyVertexConfig} />
						)}

						{effectiveProviderType === "bedrock" && key.bedrock_key_config && (
							<BedrockConfig
								keyIndex={index}
								keyConfig={key.bedrock_key_config}
								onUpdate={onUpdateKeyBedrockConfig}
								showIAMAlert={index === 0}
							/>
						)}

						{keys.length > 1 && (
							<Button type="button" variant="destructive" size="sm" onClick={() => onRemoveKey(index)} className="mt-2">
								<X className="h-4 w-4" />
								Remove Key
							</Button>
						)}
					</div>
				))}
			</div>
		</div>
	);
}
