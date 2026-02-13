"use client";

import { ExternalLink, Video } from "lucide-react";

import { Badge } from "@/components/ui/badge";
import {
	BifrostVideoDownloadOutput,
	BifrostVideoGenerationOutput,
} from "@/lib/types/logs";

interface VideoGenerationInput {
	prompt: string;
}

type VideoOutput = BifrostVideoGenerationOutput | BifrostVideoDownloadOutput;

interface VideoViewProps {
	videoInput?: VideoGenerationInput;
	videoOutput?: VideoOutput;
	requestType?: string;
}

function getMethodTypeLabel(requestType?: string): string {
	if (!requestType) return "Video";
	const normalized = requestType.toLowerCase();
	if (normalized.includes("video_download")) return "Video Download";
	if (normalized.includes("video_retrieve")) return "Video Retrieve";
	if (normalized.includes("video_generation")) return "Video Generation";
	return "Video";
}

export default function VideoView({ videoInput, videoOutput, requestType }: VideoViewProps) {
	const methodTypeLabel = getMethodTypeLabel(requestType);
	const isDownload = requestType?.toLowerCase().includes("video_download");
	const downloadOutput =
		isDownload && videoOutput ? (videoOutput as BifrostVideoDownloadOutput) : null;
	const generationOutput =
		!isDownload && videoOutput ? (videoOutput as BifrostVideoGenerationOutput) : null;
	const outputURL = generationOutput?.videos?.[0]?.url;

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
						{downloadOutput ? (
							<>
								<div className="grid grid-cols-3 gap-3">
									{downloadOutput.video_id && (
										<div className="space-y-1">
											<div className="text-muted-foreground text-xs font-medium">VIDEO ID</div>
											<div className="font-mono text-xs break-all">{downloadOutput.video_id}</div>
										</div>
									)}
									{downloadOutput.content_type && (
										<div className="space-y-1">
											<div className="text-muted-foreground text-xs font-medium">CONTENT TYPE</div>
											<div className="font-mono text-xs">{downloadOutput.content_type}</div>
										</div>
									)}
								</div>
								<p className="text-muted-foreground text-xs">
									Video content was successfully downloaded (content is not stored in logs)
								</p>
							</>
						) : generationOutput ? (
							<>
								<div className="grid grid-cols-3 gap-3">
									{generationOutput.status && (
										<div className="space-y-1">
											<div className="text-muted-foreground text-xs font-medium">STATUS</div>
											<Badge variant="secondary" className="uppercase">
												{generationOutput.status}
											</Badge>
										</div>
									)}
									{generationOutput.progress !== undefined && (
										<div className="space-y-1">
											<div className="text-muted-foreground text-xs font-medium">PROGRESS</div>
											<div className="font-mono text-xs">{generationOutput.progress}%</div>
										</div>
									)}
									{generationOutput.id && (
										<div className="space-y-1">
											<div className="text-muted-foreground text-xs font-medium">VIDEO ID</div>
											<div className="font-mono text-xs break-all">{generationOutput.id}</div>
										</div>
									)}
								</div>

								{generationOutput.error &&
									(generationOutput.error.message || generationOutput.error.code) && (
										<div className="flex items-start gap-2 rounded-md border px-3 py-2 text-sm">
											<div className="space-y-1">
												<div className="font-medium text-muted-foreground">
													Error from provider
												</div>
												{generationOutput.error.code && (
													<div className="font-medium">
														{generationOutput.error.code}
													</div>
												)}
												{generationOutput.error.message && (
													<div className="text-muted-foreground">
														{generationOutput.error.message}
													</div>
												)}
											</div>
										</div>
									)}

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
							</>
						) : null}
					</div>
				</div>
			)}
		</div>
	);
}
