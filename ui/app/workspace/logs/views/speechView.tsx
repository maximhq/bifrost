import { BifrostSpeech, SpeechInput } from "@/lib/types/logs";
import { AlertCircle, Play, Volume2 } from "lucide-react";
import React, { Component, useMemo } from "react";
import AudioPlayer from "./audioPlayer";

interface SpeechViewProps {
	speechInput?: SpeechInput;
	speechOutput?: BifrostSpeech;
	isStreaming?: boolean;
}

// Error boundary specifically for audio player errors
class AudioErrorBoundary extends Component<{ children: React.ReactNode }, { hasError: boolean; error: Error | null }> {
	constructor(props: { children: React.ReactNode }) {
		super(props);
		this.state = { hasError: false, error: null };
	}

	static getDerivedStateFromError(error: Error) {
		return { hasError: true, error };
	}

	componentDidCatch(error: Error, errorInfo: React.ErrorInfo) {
		console.error("Audio player error:", error, errorInfo);
	}

	render() {
		if (this.state.hasError) {
			return (
				<div className="flex items-center gap-2 rounded-sm border border-red-200 bg-red-50 p-4 text-sm text-red-800">
					<AlertCircle className="h-4 w-4" />
					<span>Failed to load audio player: {this.state.error?.message || "Unknown error"}</span>
				</div>
			);
		}

		return this.props.children;
	}
}

export default function SpeechView({ speechInput, speechOutput, isStreaming }: SpeechViewProps) {
	const memoizedVoiceString = useMemo(() => {
		if (!speechInput?.voice) return "";
		if (typeof speechInput.voice === "string") return speechInput.voice;
		return JSON.stringify(speechInput.voice);
	}, [speechInput?.voice]);

	return (
		<div className="space-y-4">
			{/* Speech Input */}
			{speechInput && (
				<div className="w-full rounded-sm border">
					<div className="flex items-center gap-2 border-b px-6 py-2 text-sm font-medium">
						<Volume2 className="h-4 w-4" />
						Speech Input
					</div>
					<div className="space-y-4 p-6">
						<div>
							<div className="text-muted-foreground mb-2 text-xs font-medium">TEXT TO SYNTHESIZE</div>
							<div className="font-mono text-xs">{speechInput.input}</div>
						</div>

						{speechInput.instructions && (
							<div>
								<div className="text-muted-foreground mb-2 text-xs font-medium">INSTRUCTIONS</div>
								<div className="font-mono text-xs">{speechInput.instructions}</div>
							</div>
						)}

						<div className="grid grid-cols-2 gap-4">
							<div>
								<div className="text-muted-foreground mb-2 text-xs font-medium">VOICE</div>
								<div className="font-mono text-xs">{memoizedVoiceString}</div>
							</div>

							{speechInput.response_format && (
								<div>
									<div className="text-muted-foreground mb-2 text-xs font-medium">FORMAT</div>
									<div className="font-mono text-xs">{speechInput.response_format}</div>
								</div>
							)}
						</div>
					</div>
				</div>
			)}

			{/* Speech Output */}
			{(speechOutput || isStreaming) && (
				<div className="w-full rounded-sm border">
					<div className="flex items-center gap-2 border-b px-6 py-2 text-sm font-medium">
						<Play className="h-4 w-4" />
						Speech Output
					</div>
					<div className="space-y-4 p-6">
						<AudioErrorBoundary>
							<AudioPlayer src={speechOutput?.audio || ""} />
						</AudioErrorBoundary>
					</div>
				</div>
			)}
		</div>
	);
}
