"use client";

import { FormControl, FormField, FormItem, FormLabel } from "@/components/ui/form";
import { Input } from "@/components/ui/input";
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover";
import { Switch } from "@/components/ui/switch";
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip";
import { BaseProvider, RequestType } from "@/lib/types/config";
import { isRequestTypeDisabled } from "@/lib/utils/validation";
import { Settings2 } from "lucide-react";
import { useEffect, useMemo } from "react";
import { Control, useFormContext } from "react-hook-form";

interface AllowedRequestsFieldsProps {
	control: Control<any>;
	namePrefix?: string;
	providerType?: BaseProvider;
	disabled?: boolean;
}

// Provider-specific endpoint paths
const PROVIDER_ENDPOINTS: Partial<Record<BaseProvider, Partial<Record<RequestType, string>>>> = {
	openai: {
		list_models: "/v1/models",
		text_completion: "/v1/completions",
		text_completion_stream: "/v1/completions",
		chat_completion: "/v1/chat/completions",
		chat_completion_stream: "/v1/chat/completions",
		responses: "/v1/responses",
		responses_stream: "/v1/responses",
		embedding: "/v1/embeddings",
		speech: "/v1/audio/speech",
		speech_stream: "/v1/audio/speech",
		transcription: "/v1/audio/transcriptions",
		transcription_stream: "/v1/audio/transcriptions",
		image_generation: "/v1/images/generations",
		image_generation_stream: "/v1/images/generations",
		image_edit: "/v1/images/edits",
		image_edit_stream: "/v1/images/edits",
		image_variation: "/v1/images/variations",
		count_tokens: "/v1/responses/tokens",
	},
	anthropic: {
		chat_completion: "/v1/messages",
		chat_completion_stream: "/v1/messages",
		responses: "/v1/messages",
		responses_stream: "/v1/messages",
	},
	cohere: {
		chat_completion: "/v2/chat",
		chat_completion_stream: "/v2/chat",
		responses: "/v2/chat",
		responses_stream: "/v2/chat",
		embedding: "/v2/embed",
	},
};

// Helper function to get the appropriate placeholder
const getPlaceholder = (providerType: BaseProvider | undefined, requestKey: RequestType): string => {
	if (providerType && PROVIDER_ENDPOINTS[providerType]?.[requestKey]) {
		return PROVIDER_ENDPOINTS[providerType][requestKey]!;
	}
	return PROVIDER_ENDPOINTS["openai"]?.[requestKey] ?? "";
};

const REQUEST_TYPES: Array<{ key: RequestType; label: string }> = [
	{ key: "list_models", label: "List Models" },
	{ key: "text_completion", label: "Text Completion" },
	{ key: "text_completion_stream", label: "Text Completion Stream" },
	{ key: "chat_completion", label: "Chat Completion" },
	{ key: "chat_completion_stream", label: "Chat Completion Stream" },
	{ key: "responses", label: "Responses" },
	{ key: "responses_stream", label: "Responses Stream" },
	{ key: "embedding", label: "Embedding" },
	{ key: "speech", label: "Speech" },
	{ key: "speech_stream", label: "Speech Stream" },
	{ key: "transcription", label: "Transcription" },
	{ key: "transcription_stream", label: "Transcription Stream" },
	{ key: "image_generation", label: "Image Generation" },
	{ key: "image_generation_stream", label: "Image Generation Stream" },
	{ key: "image_edit", label: "Image Edit" },
	{ key: "image_edit_stream", label: "Image Edit Stream" },
	{ key: "image_variation", label: "Image Variation" },
	{ key: "count_tokens", label: "Count Tokens" },
];

export function AllowedRequestsFields({ control, namePrefix = "allowed_requests", providerType, disabled = false }: AllowedRequestsFieldsProps) {
	const leftColumn = REQUEST_TYPES.slice(0, 6);
	const rightColumn = REQUEST_TYPES.slice(6);
	const { getValues, setValue } = useFormContext();

	// Reset disabled fields when providerType changes
	useEffect(() => {
		REQUEST_TYPES.forEach(({ key }) => {
			const fieldName = `${namePrefix}.${key}`;
			setValue(fieldName, !isRequestTypeDisabled(providerType, key), { shouldDirty: true });
		});
	}, [providerType, namePrefix, setValue, getValues]);

	const isPathOverrideDisabled = useMemo(() => providerType === "gemini" || providerType === "bedrock", [providerType]);

	const renderRequestField = (requestType: { key: RequestType; label: string }) => {
		const isDisabled = isRequestTypeDisabled(providerType, requestType.key);
		const placeholder = getPlaceholder(providerType, requestType.key);

		return (
			<FormField
				key={requestType.key}
				control={control}
				name={`${namePrefix}.${requestType.key}`}
				render={({ field: allowedField }) => (
					<FormItem
						className={`flex flex-row items-center justify-between rounded-lg border p-3 ${isDisabled ? "bg-muted/30 opacity-60" : ""}`}
					>
						<div className="space-y-0.5">
							<FormLabel className={isDisabled ? "cursor-not-allowed" : ""}>{requestType.label}</FormLabel>
						</div>
						<div className="flex items-center gap-2">
							{/* Settings icon for path override - only show when enabled */}
							{allowedField.value && !isDisabled && !isPathOverrideDisabled && !disabled && (
								<FormField
									control={control}
									name={`request_path_overrides.${requestType.key}`}
									render={({ field: pathField }) => (
										<Popover>
											<PopoverTrigger asChild>
												<button
													type="button"
													className="text-muted-foreground hover:text-foreground transition-colors"
													aria-label="Customize endpoint path"
												>
													<Settings2 className="h-4 w-4" />
												</button>
											</PopoverTrigger>
											<PopoverContent className="w-80" align="end" onOpenAutoFocus={(e) => e.preventDefault()}>
												<div className="space-y-2">
													<h4 className="text-sm font-medium">Custom Path or URL</h4>
													<p className="text-muted-foreground text-xs">
														Override with a path (e.g., /v1/chat) or a full URL (e.g., https://api.example.com/chat) to bypass
														base_url
													</p>
													<Input placeholder={placeholder} {...pathField} value={pathField.value || ""} className="h-9" />
												</div>
											</PopoverContent>
										</Popover>
									)}
								/>
							)}

							<FormControl>
								{isDisabled ? (
									<TooltipProvider>
										<Tooltip>
											<TooltipTrigger asChild>
												<div>
													<Switch checked={isDisabled ? false : allowedField.value} disabled={true} size="md" />
												</div>
											</TooltipTrigger>
											<TooltipContent>
												<p>Not supported by {providerType}</p>
											</TooltipContent>
										</Tooltip>
									</TooltipProvider>
								) : (
									<Switch checked={allowedField.value} onCheckedChange={allowedField.onChange} size="md" disabled={disabled} />
								)}
							</FormControl>
						</div>
					</FormItem>
				)}
			/>
		);
	};

	return (
		<div className="space-y-4">
			<div>
				<div className="text-sm font-medium">Allowed Request Types</div>
				<p className="text-muted-foreground text-xs">
					Select which request types this custom provider can handle.{" "}
					{!isPathOverrideDisabled ? "Click the settings icon to customize endpoint paths or use full URLs." : ""}
				</p>
			</div>

			<div className="grid grid-cols-2 gap-4">
				<div className="space-y-3">{leftColumn.map(renderRequestField)}</div>
				<div className="space-y-3">{rightColumn.map(renderRequestField)}</div>
			</div>
		</div>
	);
}
