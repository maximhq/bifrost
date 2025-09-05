"use client";

import { Input } from "@/components/ui/input";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Switch } from "@/components/ui/switch";
import { KnownProvider } from "@/lib/types/config";

interface AllowedRequests {
	text_completion: boolean;
	chat_completion: boolean;
	chat_completion_stream: boolean;
	embedding: boolean;
	speech: boolean;
	speech_stream: boolean;
	transcription: boolean;
	transcription_stream: boolean;
}

interface ApiStructureTabProps {
	customProviderName: string;
	baseProviderType: KnownProvider | "";
	allowedRequests: AllowedRequests;
	isCustomProvider: boolean;
	selectedProvider: string;
	onUpdateCustomProviderName: (value: string) => void;
	onUpdateBaseProviderType: (value: KnownProvider) => void;
	onUpdateAllowedRequest: (requestType: keyof AllowedRequests, value: boolean) => void;
}

export function ApiStructureTab({
	customProviderName,
	baseProviderType,
	allowedRequests,
	isCustomProvider,
	selectedProvider,
	onUpdateCustomProviderName,
	onUpdateBaseProviderType,
	onUpdateAllowedRequest,
}: ApiStructureTabProps) {
	if (!isCustomProvider) {
		return null;
	}

	return (
		<div className="space-y-6">
			<div className="space-y-4">
				<div>
					<label className="mb-2 block text-sm font-medium">Provider Name</label>
					<Input
						placeholder="Enter custom provider name"
						value={customProviderName}
						disabled={selectedProvider !== "custom"}
						onChange={(e) => onUpdateCustomProviderName(e.target.value)}
					/>
					<p className="text-muted-foreground mt-1 text-xs">A unique name for your custom provider</p>
				</div>

				<div>
					<label className="mb-2 block text-sm font-medium">Base Provider Type</label>
					<Select
						value={baseProviderType}
						disabled={selectedProvider !== "custom"}
						onValueChange={(value) => onUpdateBaseProviderType(value as KnownProvider)}
					>
						<SelectTrigger className="w-full">
							<SelectValue placeholder="Select base provider" />
						</SelectTrigger>
						<SelectContent>
							<SelectItem value="openai">OpenAI</SelectItem>
							<SelectItem value="anthropic">Anthropic</SelectItem>
							<SelectItem value="bedrock">AWS Bedrock</SelectItem>
							<SelectItem value="cohere">Cohere</SelectItem>
							<SelectItem value="gemini">Gemini</SelectItem>
						</SelectContent>
					</Select>
					<p className="text-muted-foreground mt-1 text-xs">The underlying provider this custom provider will use</p>
				</div>
			</div>

			{/* Allowed Requests Configuration */}
			<div className="space-y-2">
				<div className="text-sm font-medium">Allowed Request Types</div>
				<p className="text-muted-foreground text-xs">Select which request types this custom provider can handle</p>

				<div className="grid grid-cols-2 gap-4">
					<div className="space-y-3">
						<div className="flex items-center justify-between">
							<label className="text-sm">Text Completion</label>
							<Switch
								size="md"
								checked={allowedRequests.text_completion}
								onCheckedChange={(checked) => onUpdateAllowedRequest("text_completion", checked)}
							/>
						</div>
						<div className="flex items-center justify-between">
							<label className="text-sm">Chat Completion</label>
							<Switch
								size="md"
								checked={allowedRequests.chat_completion}
								onCheckedChange={(checked) => onUpdateAllowedRequest("chat_completion", checked)}
							/>
						</div>
						<div className="flex items-center justify-between">
							<label className="text-sm">Chat Completion Stream</label>
							<Switch
								size="md"
								checked={allowedRequests.chat_completion_stream}
								onCheckedChange={(checked) => onUpdateAllowedRequest("chat_completion_stream", checked)}
							/>
						</div>
						<div className="flex items-center justify-between">
							<label className="text-sm">Embedding</label>
							<Switch
								size="md"
								checked={allowedRequests.embedding}
								onCheckedChange={(checked) => onUpdateAllowedRequest("embedding", checked)}
							/>
						</div>
					</div>
					<div className="space-y-3">
						<div className="flex items-center justify-between">
							<label className="text-sm">Speech</label>
							<Switch size="md" checked={allowedRequests.speech} onCheckedChange={(checked) => onUpdateAllowedRequest("speech", checked)} />
						</div>
						<div className="flex items-center justify-between">
							<label className="text-sm">Speech Stream</label>
							<Switch
								size="md"
								checked={allowedRequests.speech_stream}
								onCheckedChange={(checked) => onUpdateAllowedRequest("speech_stream", checked)}
							/>
						</div>
						<div className="flex items-center justify-between">
							<label className="text-sm">Transcription</label>
							<Switch
								size="md"
								checked={allowedRequests.transcription}
								onCheckedChange={(checked) => onUpdateAllowedRequest("transcription", checked)}
							/>
						</div>
						<div className="flex items-center justify-between">
							<label className="text-sm">Transcription Stream</label>
							<Switch
								size="md"
								checked={allowedRequests.transcription_stream}
								onCheckedChange={(checked) => onUpdateAllowedRequest("transcription_stream", checked)}
							/>
						</div>
					</div>
				</div>
			</div>
		</div>
	);
}
