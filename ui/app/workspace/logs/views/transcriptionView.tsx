import { Badge } from "@/components/ui/badge";
import { CodeEditor } from "@/components/ui/codeEditor";
import { BifrostTranscribe, TranscriptionInput } from "@/lib/types/logs";
import { Clock, FileAudio, Mic } from "lucide-react";
import { useTranslation } from "react-i18next";
import AudioPlayer from "./audioPlayer";

interface TranscriptionViewProps {
	transcriptionInput?: TranscriptionInput;
	transcriptionOutput?: BifrostTranscribe;
	isStreaming?: boolean;
}

export default function TranscriptionView({ transcriptionInput, transcriptionOutput, isStreaming }: TranscriptionViewProps) {
	const { t } = useTranslation("observability");
	const formatTime = (seconds: number) => {
		const mins = Math.floor(seconds / 60);
		const secs = (seconds % 60).toFixed(1);
		return `${mins}:${secs.padStart(4, "0")}`;
	};

	return (
		<div className="space-y-4">
			{/* Transcription Input */}
			{transcriptionInput && (
				<div className="w-full rounded-sm border">
					<div className="flex items-center gap-2 border-b px-6 py-2 text-sm font-medium">
						<FileAudio className="h-4 w-4" />
						{t("logs.transcription.input")}
					</div>
					<div className="space-y-4 p-6">
						<div className="text-muted-foreground mb-2 text-xs font-medium">{t("logs.transcription.audioFile")}</div>
						{/* Audio Controls */}
						<AudioPlayer src={transcriptionInput.file} />
					</div>
				</div>
			)}

			{/* Transcription Output */}
			{(transcriptionOutput || isStreaming) && (
				<div className="w-full rounded-sm border">
					<div className="flex items-center gap-2 border-b px-6 py-2 text-sm font-medium">
						<Mic className="h-4 w-4" />
						{t("logs.transcription.output")}
					</div>

					<div className="space-y-4 p-6">
						{!transcriptionOutput && isStreaming ? (
							<div className="font-mono text-xs">{t("logs.transcription.streamedUnavailable")}</div>
						) : (
							<>
								{/* Main Transcription Text */}
								<div>
									<div className="font-mono text-xs">{transcriptionOutput?.text}</div>
								</div>

								{/* Basic Information */}
								{(transcriptionOutput?.task || transcriptionOutput?.language || transcriptionOutput?.duration) && (
									<div className="grid grid-cols-1 gap-4 md:grid-cols-3">
										{transcriptionOutput?.task && (
											<div>
												<div className="text-muted-foreground mb-2 text-xs font-medium">{t("logs.transcription.task")}</div>
												<div className="font-mono text-xs">{transcriptionOutput.task}</div>
											</div>
										)}

										{transcriptionOutput?.language && (
											<div>
												<div className="text-muted-foreground mb-2 text-xs font-medium">{t("logs.transcription.detectedLanguage")}</div>
												<div className="font-mono text-xs">{transcriptionOutput.language}</div>
											</div>
										)}

										{transcriptionOutput?.duration && (
											<div>
												<div className="text-muted-foreground mb-2 text-xs font-medium">{t("logs.transcription.duration")}</div>
												<div className="font-mono text-xs">{transcriptionOutput.duration.toFixed(1)}s</div>
											</div>
										)}
									</div>
								)}

								{/* Words with Timing */}
								{transcriptionOutput?.words && transcriptionOutput.words.length > 0 && (
									<div>
										<div className="text-muted-foreground mb-2 text-xs font-medium">{t("logs.transcription.wordLevelTiming")}</div>
										<div className="max-h-40 overflow-y-auto">
											<div className="flex flex-wrap gap-2">
												{transcriptionOutput.words.map((word, index) => (
													<div
														key={index}
														className="inline-flex items-center gap-1 rounded border px-2 py-1 text-xs"
														title={`${formatTime(word.start)} - ${formatTime(word.end)}`}
													>
														<span>{word.word}</span>
														<span className="text-muted-foreground text-xs">{formatTime(word.start)}</span>
													</div>
												))}
											</div>
										</div>
									</div>
								)}

								{/* Segments */}
								{transcriptionOutput?.segments && transcriptionOutput.segments.length > 0 && (
									<div>
										<div className="text-muted-foreground mb-2 text-xs font-medium">{t("logs.transcription.segments")}</div>
										<div className="max-h-60 space-y-2 overflow-y-auto">
											{transcriptionOutput.segments.map((segment) => (
												<div key={segment.id} className="rounded border p-3">
													<div className="mb-2 flex items-center justify-between">
														<Badge variant="outline" className="text-xs">
															{t("logs.transcription.segment", { id: segment.id })}
														</Badge>
														<div className="text-muted-foreground flex items-center gap-1 text-xs">
															<Clock className="h-3 w-3" />
															{formatTime(segment.start)} - {formatTime(segment.end)}
														</div>
													</div>
													<div className="text-sm">{segment.text}</div>
													<div className="text-muted-foreground mt-2 flex gap-4 text-xs">
														<span>{t("logs.transcription.avgLogProb", { value: segment.avg_logprob.toFixed(3) })}</span>
														<span>{t("logs.transcription.noSpeech", { value: (segment.no_speech_prob * 100).toFixed(1) })}</span>
														<span>{t("logs.transcription.temp", { value: segment.temperature.toFixed(1) })}</span>
													</div>
												</div>
											))}
										</div>
									</div>
								)}

								{/* Log Probabilities */}
								{transcriptionOutput?.logprobs && transcriptionOutput.logprobs.length > 0 && (
									<div>
										<div className="text-muted-foreground mb-2 text-xs font-medium">{t("logs.transcription.logProbabilities")}</div>
										<CodeEditor
											className="z-0 w-full"
											shouldAdjustInitialHeight={true}
											maxHeight={200}
											wrap={true}
											code={JSON.stringify(transcriptionOutput.logprobs, null, 2)}
											lang="json"
											readonly={true}
											options={{
												scrollBeyondLastLine: false,
												collapsibleBlocks: true,
												lineNumbers: "off",
												alwaysConsumeMouseWheel: false,
											}}
										/>
									</div>
								)}
							</>
						)}
					</div>
				</div>
			)}
		</div>
	);
}