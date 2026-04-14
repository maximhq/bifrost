import { Card } from "@/components/ui/card";
import { ImageMessageData } from "@/lib/types/logs";
import React from "react";

interface ImageMessageProps {
	image: ImageMessageData | null;
}

export const ImageMessage: React.FC<ImageMessageProps> = ({ image }) => {
	// No usable image data
	if (!image || (!image.url && !image.b64_json)) {
		return null;
	}

	// Convert output_format to MIME type for data URLs
	const getMimeType = (format?: string): string => {
		switch (format?.toLowerCase()) {
			case "png":
				return "image/png";
			case "jpeg":
			case "jpg":
				return "image/jpeg";
			case "webp":
				return "image/webp";
			default:
				// Default to PNG for backward compatibility
				return "image/png";
		}
	};

	const dataUrl = image.url ? image.url : `data:${getMimeType(image.output_format)};base64,${image.b64_json}`;

	return (
		<div className="my-4">
			<Card className="p-0">
				<div className="border-border overflow-auto border">
					<img src={dataUrl} alt={image.prompt || `image-${image.index ?? 0}`} className="h-auto w-auto" loading="lazy" />
				</div>
			</Card>
		</div>
	);
};