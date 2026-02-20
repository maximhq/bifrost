"use client";

import { ExternalLink, Video } from "lucide-react";

import { Badge } from "@/components/ui/badge";
import { BifrostVideoGenerationOutput } from "@/lib/types/logs";

interface VideoGenerationInput {
	prompt: string;
}

interface VideoViewProps {
	videoInput?: VideoGenerationInput;
	videoOutput?: BifrostVideoGenerationOutput;
	requestType?: string;
}

function getMethodTypeLabel(requestType?: string): string {
	if (!requestType) return "Video";
	const normalized = requestType.toLowerCase();
	if (normalized.includes("video_retrieve")) return "Video Retrieve";
	if (normalized.includes("video_generation")) return "Video Generation";
	return "Video";
}

export default function VideoView({ videoInput, videoOutput, requestType }: VideoViewProps) {
	const methodTypeLabel = getMethodTypeLabel(requestType);
	const outputURL = videoOutput?.videos?.[0]?.url;

	return (
		<div className="space-y-4">
			{videoInput && (
				<div className="w-full rounded-sm border">
					<div className="flex items-center gap-2 border-b px-6 py-2 text-sm font-medium">
						<Video className="h-4 w-4" />
						{methodTypeLabel} Input
					</div>
					<div className="space-y-2 p-6">
						<div className="text-muted-foreground text-xs font-medium">PROMPT</div>
						<div className="font-mono text-xs">{videoInput.prompt}</div>
					</div>
				</div>
			)}

			{videoOutput && (
				<div className="w-full rounded-sm border">
					<div className="flex items-center gap-2 border-b px-6 py-2 text-sm font-medium">
						<Video className="h-4 w-4" />
						{methodTypeLabel} Output
					</div>
					<div className="space-y-3 p-6">
						<div className="grid grid-cols-3 gap-3">
							{videoOutput.status && (
								<div className="space-y-1">
									<div className="text-muted-foreground text-xs font-medium">STATUS</div>
									<Badge variant="secondary" className="uppercase">
										{videoOutput.status}
									</Badge>
								</div>
							)}
							{videoOutput.progress !== undefined && (
								<div className="space-y-1">
									<div className="text-muted-foreground text-xs font-medium">PROGRESS</div>
									<div className="font-mono text-xs">{videoOutput.progress}%</div>
								</div>
							)}
							{videoOutput.id && (
								<div className="space-y-1">
									<div className="text-muted-foreground text-xs font-medium">VIDEO ID</div>
									<div className="font-mono text-xs break-all">{videoOutput.id}</div>
								</div>
							)}
						</div>

						{outputURL && (
							<div className="space-y-2">
								<video className="w-full rounded-sm border bg-black" controls preload="metadata" src={outputURL}>
									<track kind="captions" />
								</video>
								<a
									href={outputURL}
									target="_blank"
									rel="noopener noreferrer"
									className="text-primary inline-flex items-center gap-1 text-xs underline"
								>
									Open video URL
									<ExternalLink className="h-3 w-3" />
								</a>
							</div>
						)}
					</div>
				</div>
			)}
		</div>
	);
}
