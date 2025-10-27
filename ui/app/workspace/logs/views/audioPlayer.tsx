import { Button } from "@/components/ui/button";
import { Pause, Play, Download } from "lucide-react";
import { useState } from "react";

const AudioPlayer = ({ src }: { src: string }) => {
	const [isPlaying, setIsPlaying] = useState(false);
	const [audio] = useState<HTMLAudioElement | null>(typeof window !== "undefined" ? new Audio() : null);
	const [error, setError] = useState<string | null>(null);

	const createAudioBlob = (base64Data: string): Blob | null => {
		try {
			return new Blob([Uint8Array.from(atob(base64Data), (c) => c.charCodeAt(0))], {
				type: "audio/mpeg",
			});
		} catch (err) {
			console.error("Failed to decode audio data:", err);
			setError("Failed to decode audio data. The audio file may be corrupted.");
			return null;
		}
	};

	const handlePlayPause = () => {
		if (!audio || !src) return;

		if (isPlaying) {
			audio.pause();
			setIsPlaying(false);
		} else {
			const audioBlob = createAudioBlob(src);
			if (!audioBlob) return;

			const audioUrl = URL.createObjectURL(audioBlob);
			audio.src = audioUrl;
			audio.play().catch((err) => {
				console.error("Failed to play audio:", err);
				setError("Failed to play audio. Please try again.");
				setIsPlaying(false);
			});
			setIsPlaying(true);

			audio.onended = () => {
				setIsPlaying(false);
				URL.revokeObjectURL(audioUrl);
			};
		}
	};

	const handleDownload = () => {
		if (!src) return;

		const audioBlob = createAudioBlob(src);
		if (!audioBlob) return;

		const audioUrl = URL.createObjectURL(audioBlob);

		const a = document.createElement("a");
		a.href = audioUrl;
		a.download = "speech-output.mp3";
		document.body.appendChild(a);
		a.click();
		document.body.removeChild(a);
		URL.revokeObjectURL(audioUrl);
	};

	return (
		<div className="flex flex-col gap-2">
			<div className="flex items-center gap-2">
				<Button onClick={handlePlayPause} variant="outline" size="sm" className="flex items-center gap-2" disabled={!!error}>
					{isPlaying ? <Pause className="h-4 w-4" /> : <Play className="h-4 w-4" />}
					{isPlaying ? "Pause" : "Play"}
				</Button>

				<Button onClick={handleDownload} variant="outline" size="sm" className="flex items-center gap-2" disabled={!!error}>
					<Download className="h-4 w-4" />
					Download
				</Button>
			</div>
			{error && <div className="text-sm text-red-500">{error}</div>}
		</div>
	);
};

export default AudioPlayer;
