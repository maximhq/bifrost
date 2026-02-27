"use client";

import { useState, useEffect } from "react";
import { BifrostImageGenerationOutput } from "@/lib/types/logs";
import { Image, ChevronLeft, ChevronRight } from "lucide-react";
import { ImageMessage } from "@/components/chat/ImageMessage";
import { Button } from "@/components/ui/button";
import { RequestTypeLabels } from "@/lib/constants/logs";

interface ImageGenerationInput {
	prompt: string;
}

interface ImageViewProps {
	imageInput?: ImageGenerationInput;
	imageOutput?: BifrostImageGenerationOutput;
	requestType?: string;
}

// Helper function to get method type label from request type
function getMethodTypeLabel(requestType?: string): string {
	if (!requestType) return "Image Generation";
	
	const normalizedType = requestType.toLowerCase();
	if (normalizedType.includes("image_edit")) {
		return RequestTypeLabels[normalizedType as keyof typeof RequestTypeLabels] || "Image Edit";
	}
	if (normalizedType.includes("image_variation")) {
		return RequestTypeLabels[normalizedType as keyof typeof RequestTypeLabels] || "Image Variation";
	}
	return RequestTypeLabels[normalizedType as keyof typeof RequestTypeLabels] || "Image Generation";
}

export default function ImageView({ imageInput, imageOutput, requestType }: ImageViewProps) {
	const [currentIndex, setCurrentIndex] = useState(0);

	// Get all valid images
	const images = imageOutput?.data?.filter(img => img.url || img.b64_json) ?? [];
	const totalImages = images.length;
	const currentImage = images[currentIndex] ?? null;

	// Get method type label
	const methodTypeLabel = getMethodTypeLabel(requestType);

	// Clamp currentIndex when images array changes to ensure it's always valid
	useEffect(() => {
		if (totalImages === 0) {
			setCurrentIndex(0);
		} else {
			setCurrentIndex(prev => Math.min(prev, totalImages - 1));
		}
	}, [totalImages]);

	// Looping navigation
	const goToPrevious = () => setCurrentIndex(prev => prev === 0 ? totalImages - 1 : prev - 1);
	const goToNext = () => setCurrentIndex(prev => prev === totalImages - 1 ? 0 : prev + 1);

	return (
		<div className="space-y-4">
			{/* Image Input */}
			{imageInput && (
				<div className="w-full rounded-sm border">
					<div className="flex items-center gap-2 border-b px-6 py-2 text-sm font-medium">
						<Image className="h-4 w-4" />
						{methodTypeLabel} Input
					</div>
					<div className="space-y-4 p-6">
						<div className="text-muted-foreground mb-2 text-xs font-medium">PROMPT</div>
						<div className="font-mono text-xs">{imageInput.prompt}</div>
					</div>
				</div>
			)}

			{/* Image Output */}
			{(currentImage) && (
				<div className="w-full rounded-sm border">
					<div className="flex items-center gap-2 border-b px-6 py-2 text-sm font-medium">
						<Image className="h-4 w-4" />
						{methodTypeLabel} Output
					</div>
					<div className="space-y-4 p-6">
						{currentImage && (
							<>
								{currentImage.revised_prompt && (
									<div className="mb-4">
										<div className="text-muted-foreground mb-2 text-xs font-medium">REVISED PROMPT</div>
										<div className="font-mono text-xs">{currentImage.revised_prompt}</div>
									</div>
								)}
								<ImageMessage 
									image={{
										...currentImage,
									output_format: imageOutput?.output_format,
									}} 
								/>

								{totalImages > 1 && (
									<div className="flex items-center justify-center gap-4 mt-3">
										<Button variant="outline" size="sm" onClick={goToPrevious} aria-label="Previous image" title="Previous image">
											<ChevronLeft className="h-4 w-4" />
										</Button>
										<span className="text-sm text-muted-foreground">{currentIndex + 1} / {totalImages}</span>
										<Button variant="outline" size="sm" onClick={goToNext} aria-label="Next image" title="Next image">
											<ChevronRight className="h-4 w-4" />
										</Button>
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