import { useState, useEffect } from "react";
import { BifrostOCRResponse, OCRDocument } from "@/lib/types/logs";
import { Button } from "@/components/ui/button";
import { CodeEditor } from "@/components/ui/codeEditor";
import { ChevronLeft, ChevronRight, FileText } from "lucide-react";

function getImageSrc(b64: string): string {
	if (b64.startsWith("/9j/")) return `data:image/jpeg;base64,${b64}`;
	if (b64.startsWith("iVBOR")) return `data:image/png;base64,${b64}`;
	if (b64.startsWith("UklGR")) return `data:image/webp;base64,${b64}`;
	if (b64.startsWith("R0lGO")) return `data:image/gif;base64,${b64}`;
	return `data:image/png;base64,${b64}`;
}

interface OCRViewProps {
	ocrInput?: OCRDocument;
	ocrOutput?: BifrostOCRResponse;
}

export default function OCRView({ ocrInput, ocrOutput }: OCRViewProps) {
	const pages = ocrOutput?.pages ?? [];
	const totalPages = pages.length;
	const [currentIndex, setCurrentIndex] = useState(0);

	useEffect(() => {
		if (totalPages === 0) {
			setCurrentIndex(0);
		} else {
			setCurrentIndex((prev) => Math.min(prev, totalPages - 1));
		}
	}, [totalPages]);

	const goToPrevious = () => setCurrentIndex((prev) => (prev === 0 ? totalPages - 1 : prev - 1));
	const goToNext = () => setCurrentIndex((prev) => (prev === totalPages - 1 ? 0 : prev + 1));

	const currentPage = pages[currentIndex] ?? null;
	const pageImages = currentPage?.images?.filter((img) => img.image_base64) ?? [];

	return (
		<div className="space-y-4">
			{/* OCR Input */}
			{ocrInput && (
				<div className="w-full rounded-sm border">
					<div className="flex items-center gap-2 border-b px-6 py-2 text-sm font-medium">
						<FileText className="h-4 w-4" />
						OCR Input
					</div>
					<div className="space-y-4 p-6">
						<div>
							<div className="text-muted-foreground mb-2 text-xs font-medium">TYPE</div>
							<div className="font-mono text-xs">{ocrInput.type === "document_url" ? "Document" : "Image"}</div>
						</div>
						{(ocrInput.document_url || ocrInput.image_url) && (
							<div>
								<div className="text-muted-foreground mb-2 text-xs font-medium">
									{ocrInput.type === "document_url" ? "DOCUMENT URL" : "IMAGE URL"}
								</div>
								<div className="font-mono text-xs break-all">{ocrInput.document_url ?? ocrInput.image_url}</div>
							</div>
						)}
					</div>
				</div>
			)}

			{/* OCR Output */}
			{ocrOutput && (
				<div className="w-full rounded-sm border">
					<div className="flex items-center gap-2 border-b px-6 py-2 text-sm font-medium">
						<FileText className="h-4 w-4" />
						OCR Output
					</div>

					<div className="space-y-4 p-6">
						{ocrOutput.usage_info && (
							<div className="grid grid-cols-3 gap-3">
								<div className="space-y-1">
									<div className="text-muted-foreground text-xs font-medium">PAGES PROCESSED</div>
									<div className="font-mono text-xs">{ocrOutput.usage_info.pages_processed}</div>
								</div>
								<div className="space-y-1">
									<div className="text-muted-foreground text-xs font-medium">DOCUMENT SIZE</div>
									<div className="font-mono text-xs">{(ocrOutput.usage_info.doc_size_bytes / 1024).toFixed(1)} KB</div>
								</div>
							</div>
						)}

						{ocrOutput.document_annotation && (
							<div>
								<div className="text-muted-foreground mb-2 text-xs font-medium">DOCUMENT ANNOTATION</div>
								<div className="font-mono text-xs">{ocrOutput.document_annotation}</div>
							</div>
						)}

						{currentPage && (
							<>
								{currentPage.dimensions && (
									<div className="grid grid-cols-3 gap-3">
										<div className="space-y-1">
											<div className="text-muted-foreground text-xs font-medium">DIMENSIONS</div>
											<div className="font-mono text-xs">
												{currentPage.dimensions.width} × {currentPage.dimensions.height}px
											</div>
										</div>
										<div className="space-y-1">
											<div className="text-muted-foreground text-xs font-medium">DPI</div>
											<div className="font-mono text-xs">{currentPage.dimensions.dpi}</div>
										</div>
									</div>
								)}

								{currentPage.markdown ? (
									<div>
										<div className="text-muted-foreground mb-2 text-xs font-medium">MARKDOWN</div>
										<CodeEditor
											className="z-0 w-full"
											shouldAdjustInitialHeight
											maxHeight={400}
											wrap
											code={currentPage.markdown}
											lang="markdown"
											readonly
											options={{
												scrollBeyondLastLine: false,
												lineNumbers: "off",
												alwaysConsumeMouseWheel: false,
											}}
										/>
									</div>
								) : (
									<div className="text-muted-foreground font-mono text-xs">No text extracted from this page.</div>
								)}

								{pageImages.length > 0 && (
									<div>
										<div className="text-muted-foreground mb-2 text-xs font-medium">EXTRACTED IMAGES ({pageImages.length})</div>
										<div className="flex flex-wrap gap-2">
											{pageImages.map((img) => (
												<img
													key={img.id}
													src={getImageSrc(img.image_base64!)}
													alt={`Image ${img.id}`}
													className="max-h-48 max-w-48 rounded border object-contain"
												/>
											))}
										</div>
									</div>
								)}

								{totalPages > 1 && (
									<div className="mt-3 flex items-center justify-center gap-4">
										<Button
											variant="outline"
											size="sm"
											onClick={goToPrevious}
											aria-label="Previous page"
											title="Previous page"
											data-testid="ocr-view-pagination-prev-button"
										>
											<ChevronLeft className="h-4 w-4" />
										</Button>
										<span className="text-muted-foreground text-sm">
											Page {currentIndex + 1} / {totalPages}
										</span>
										<Button
											variant="outline"
											size="sm"
											onClick={goToNext}
											aria-label="Next page"
											title="Next page"
											data-testid="ocr-view-pagination-next-button"
										>
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