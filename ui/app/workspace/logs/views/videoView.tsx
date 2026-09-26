import { ExternalLink, Video } from "lucide-react";

import { CopyableId } from "@/components/copyableId";
import { Badge } from "@/components/ui/badge";
import { RequestTypeLabels } from "@/lib/constants/logs";
import { useTranslation } from "react-i18next";
import {
	BifrostVideoDeleteOutput,
	BifrostVideoDownloadOutput,
	BifrostVideoGenerationOutput,
	BifrostVideoListOutput,
	VideoOutput as VideoAsset,
} from "@/lib/types/logs";

import CollapsibleBox from "./collapsibleBox";
import { CodeEditor } from "@/components/ui/codeEditor";

interface VideoGenerationInput {
	prompt: string;
}

type VideoOutput = BifrostVideoGenerationOutput | BifrostVideoDownloadOutput | BifrostVideoDeleteOutput;

interface VideoViewProps {
	videoInput?: VideoGenerationInput;
	videoOutput?: VideoOutput;
	videoListOutput?: BifrostVideoListOutput;
	requestType?: string;
}

function getMethodTypeLabel(requestType: string | undefined, fallback: string): string {
	if (!requestType) return fallback;
	return RequestTypeLabels[requestType.toLowerCase() as keyof typeof RequestTypeLabels] || fallback;
}

function getVideoSrc(video: VideoAsset): string | null {
	if (video.url) return video.url;
	if (video.base64) return `data:${video.content_type || "video/mp4"};base64,${video.base64}`;
	return null;
}

export default function VideoView({ videoInput, videoOutput, videoListOutput, requestType }: VideoViewProps) {
	const { t } = useTranslation("observability");
	const methodTypeLabel = getMethodTypeLabel(requestType, t("logs.media.video"));
	const normalizedType = requestType?.toLowerCase() ?? "";
	const isDownload = normalizedType.includes("video_download");
	const isDelete = normalizedType.includes("video_delete");
	const downloadOutput = isDownload && videoOutput ? (videoOutput as BifrostVideoDownloadOutput) : null;
	const deleteOutput = isDelete && videoOutput ? (videoOutput as BifrostVideoDeleteOutput) : null;
	const generationOutput = !isDownload && !isDelete && videoOutput ? (videoOutput as BifrostVideoGenerationOutput) : null;
	const videos = generationOutput?.videos?.filter((video) => video.url || video.base64) ?? [];

	return (
		<div className="space-y-4">
			{videoInput && (
				<div className="w-full rounded-sm border">
					<div className="flex items-center gap-2 border-b px-6 py-2 text-sm font-medium">
						<Video className="h-4 w-4" />
						{t("logs.video.input", { type: methodTypeLabel })}
					</div>
					<div className="space-y-2 p-6">
						<div className="text-muted-foreground text-xs font-medium">{t("logs.video.prompt")}</div>
						<div className="font-mono text-xs">{videoInput.prompt}</div>
					</div>
				</div>
			)}

			{videoOutput && (
				<div className="w-full rounded-sm border">
					<div className="flex items-center gap-2 border-b px-6 py-2 text-sm font-medium">
						<Video className="h-4 w-4" />
						{t("logs.video.output", { type: methodTypeLabel })}
					</div>
					<div className="space-y-3 p-6">
						{downloadOutput ? (
							<>
								<div className="grid grid-cols-1 gap-3 md:grid-cols-3">
									{downloadOutput.video_id && (
										<div className="space-y-1">
											<div className="text-muted-foreground text-xs font-medium">{t("logs.video.videoId")}</div>
											<div className="flex items-center gap-1">
												<div className="font-mono text-xs break-all">{downloadOutput.video_id}</div>
												<CopyableId id={downloadOutput.video_id} entityLabel={t("logs.video.entityLabel")} testId="video-view-copy-download-video-id-button" />
											</div>
										</div>
									)}
									{downloadOutput.content_type && (
										<div className="space-y-1">
											<div className="text-muted-foreground text-xs font-medium">{t("logs.video.contentType")}</div>
											<div className="font-mono text-xs">{downloadOutput.content_type}</div>
										</div>
									)}
								</div>
								<p className="text-muted-foreground text-xs">{t("logs.video.downloaded")}</p>
							</>
						) : deleteOutput ? (
							<div className="grid grid-cols-1 gap-3 md:grid-cols-3">
								{deleteOutput.id && (
									<div className="space-y-1">
										<div className="text-muted-foreground text-xs font-medium">{t("logs.video.videoId")}</div>
										<div className="flex items-center gap-1">
											<div className="font-mono text-xs break-all">{deleteOutput.id}</div>
											<CopyableId id={deleteOutput.id} entityLabel={t("logs.video.entityLabel")} testId="video-view-copy-delete-video-id-button" />
										</div>
									</div>
								)}
								<div className="space-y-1">
									<div className="text-muted-foreground text-xs font-medium">{t("logs.video.deleted")}</div>
									<Badge variant="secondary" className="uppercase">
										{deleteOutput.deleted ? "true" : "false"}
									</Badge>
								</div>
							</div>
						) : generationOutput ? (
							<>
								<div className="grid grid-cols-1 gap-3 md:grid-cols-3">
									{generationOutput.id && (
										<div className="space-y-1">
											<div className="text-muted-foreground text-xs font-medium">{t("logs.video.videoId")}</div>
											<div className="flex items-center gap-1">
												<div className="font-mono text-xs break-all">{generationOutput.id}</div>
												<CopyableId id={generationOutput.id} entityLabel={t("logs.video.entityLabel")} testId="video-view-copy-generation-video-id-button" />
											</div>
										</div>
									)}
									{generationOutput.status && (
										<div className="space-y-1">
											<div className="text-muted-foreground text-xs font-medium">{t("logs.video.status")}</div>
											<Badge variant="secondary" className="uppercase">
												{generationOutput.status}
											</Badge>
										</div>
									)}
									{generationOutput.progress !== undefined && (
										<div className="space-y-1">
											<div className="text-muted-foreground text-xs font-medium">{t("logs.video.progress")}</div>
											<div className="font-mono text-xs">{generationOutput.progress}%</div>
										</div>
									)}
									{generationOutput.seconds && (
										<div className="space-y-1">
											<div className="text-muted-foreground text-xs font-medium">{t("logs.video.duration")}</div>
											<div className="font-mono text-xs">{generationOutput.seconds}s</div>
										</div>
									)}
									{generationOutput.size && (
										<div className="space-y-1">
											<div className="text-muted-foreground text-xs font-medium">{t("logs.video.size")}</div>
											<div className="font-mono text-xs">{generationOutput.size}</div>
										</div>
									)}
									{generationOutput.remixed_from_video_id && (
										<div className="space-y-1">
											<div className="text-muted-foreground text-xs font-medium">{t("logs.video.remixedFrom")}</div>
											<div className="font-mono text-xs break-all">{generationOutput.remixed_from_video_id}</div>
										</div>
									)}
								</div>

								{generationOutput.error && (generationOutput.error.message || generationOutput.error.code) && (
									<div className="flex items-start gap-2 rounded-md border px-3 py-2 text-sm">
										<div className="space-y-1">
											<div className="text-muted-foreground font-medium">{t("logs.video.errorFromProvider")}</div>
											{generationOutput.error.code && <div className="font-medium">{generationOutput.error.code}</div>}
											{generationOutput.error.message && <div className="text-muted-foreground">{generationOutput.error.message}</div>}
										</div>
									</div>
								)}

								{videos.map((video, index) => {
									const src = getVideoSrc(video);
									if (!src) return null;
									return (
										<div key={index} className="space-y-2">
											<video className="w-full rounded-sm border bg-black" controls preload="metadata" src={src}>
												<track kind="captions" />
											</video>
											{video.url && (
												<a
													href={video.url}
													target="_blank"
													rel="noopener noreferrer"
													data-testid="video-view-open-video-url-link"
													className="text-primary inline-flex items-center gap-1 text-xs underline"
												>
													{t("logs.video.openVideoUrl")}
													<ExternalLink className="h-3 w-3" />
												</a>
											)}
										</div>
									);
								})}
							</>
						) : null}
					</div>
				</div>
			)}

			{videoListOutput && (
				<CollapsibleBox
					title={t("logs.video.listOutput", { count: videoListOutput.data?.length ?? 0 })}
					onCopy={() => JSON.stringify(videoListOutput, null, 2)}
				>
					<CodeEditor
						className="z-0 w-full"
						shouldAdjustInitialHeight={true}
						maxHeight={450}
						wrap={true}
						code={JSON.stringify(videoListOutput.data, null, 2)}
						lang="json"
						readonly={true}
						options={{ scrollBeyondLastLine: false, lineNumbers: "off", alwaysConsumeMouseWheel: false }}
					/>
				</CollapsibleBox>
			)}
		</div>
	);
}