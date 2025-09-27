"use client";

import { FormControl, FormField, FormItem, FormLabel } from "@/components/ui/form";
import { Switch } from "@/components/ui/switch";
import { Control } from "react-hook-form";

interface AllowedRequestsFieldsProps {
	control: Control<any>;
	namePrefix?: string;
}

const REQUEST_TYPES = [
	{ key: "text_completion", label: "Text Completion" },
	{ key: "chat_completion", label: "Chat Completion" },
	{ key: "chat_completion_stream", label: "Chat Completion Stream" },
	{ key: "embedding", label: "Embedding" },
	{ key: "speech", label: "Speech" },
	{ key: "speech_stream", label: "Speech Stream" },
	{ key: "transcription", label: "Transcription" },
	{ key: "transcription_stream", label: "Transcription Stream" },
];

export function AllowedRequestsFields({ control, namePrefix = "allowed_requests" }: AllowedRequestsFieldsProps) {
	const leftColumn = REQUEST_TYPES.slice(0, 4);
	const rightColumn = REQUEST_TYPES.slice(4);

	return (
		<div className="space-y-4">
			<div>
				<div className="text-sm font-medium">Allowed Request Types</div>
				<p className="text-muted-foreground text-xs">Select which request types this custom provider can handle</p>
			</div>

			<div className="grid grid-cols-2 gap-4">
				<div className="space-y-3">
					{leftColumn.map((requestType) => (
						<FormField
							key={requestType.key}
							control={control}
							name={`${namePrefix}.${requestType.key}`}
							render={({ field }) => (
								<FormItem className="flex flex-row items-center justify-between rounded-lg border p-3">
									<div className="space-y-0.5">
										<FormLabel>{requestType.label}</FormLabel>
									</div>
									<FormControl>
										<Switch checked={field.value} onCheckedChange={field.onChange} size="md" />
									</FormControl>
								</FormItem>
							)}
						/>
					))}
				</div>
				<div className="space-y-3">
					{rightColumn.map((requestType) => (
						<FormField
							key={requestType.key}
							control={control}
							name={`${namePrefix}.${requestType.key}`}
							render={({ field }) => (
								<FormItem className="flex flex-row items-center justify-between rounded-lg border p-3">
									<div className="space-y-0.5">
										<FormLabel>{requestType.label}</FormLabel>
									</div>
									<FormControl>
										<Switch checked={field.value} onCheckedChange={field.onChange} size="md" />
									</FormControl>
								</FormItem>
							)}
						/>
					))}
				</div>
			</div>
		</div>
	);
}
