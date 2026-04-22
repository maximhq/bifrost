/**
 * Reusable PDF export utility.
 *
 * Captures an array of DOM sections as images via html2canvas and composes
 * them into a multi-page A4 PDF with jsPDF. Libraries are dynamically
 * imported so they only load when actually needed.
 *
 * Usage:
 *   await generatePdf(
 *     [{ element: el, label: "Overview" }, ...],
 *     "dashboard-export",
 *   );
 */

export interface PdfSection {
	/** DOM element to capture */
	element: HTMLElement;
	/** Optional heading printed above the section in the PDF */
	label?: string;
}

export interface PdfBranding {
	/** Path to logo image (relative to public dir, e.g. "/bifrost-logo.webp") */
	logoSrc: string;
	/** Text shown next to the logo */
	text?: string;
}

export interface PdfOptions {
	/** Canvas scale factor (default 1.5) */
	scale?: number;
	/** JPEG quality 0-1 (default 0.92) */
	quality?: number;
	/** Page margin in mm (default 10) */
	margin?: number;
	/** Page orientation (default "portrait") */
	orientation?: "portrait" | "landscape";
	/** Branding shown at the bottom-right of every page */
	branding?: PdfBranding;
}

/** Load an image and return its data URL + natural dimensions. */
async function loadImage(src: string): Promise<{ dataUrl: string; width: number; height: number }> {
	return new Promise((resolve, reject) => {
		const img = new Image();
		img.crossOrigin = "anonymous";
		img.onload = () => {
			const canvas = document.createElement("canvas");
			canvas.width = img.naturalWidth;
			canvas.height = img.naturalHeight;
			const ctx = canvas.getContext("2d");
			ctx?.drawImage(img, 0, 0);
			resolve({
				dataUrl: canvas.toDataURL("image/png"),
				width: img.naturalWidth,
				height: img.naturalHeight,
			});
		};
		img.onerror = reject;
		img.src = src;
	});
}

export async function generatePdf(sections: PdfSection[], filename: string, options: PdfOptions = {}): Promise<void> {
	const { scale = 1.5, quality = 0.92, margin = 10, orientation = "portrait", branding } = options;

	const [{ default: html2canvas }, { jsPDF }] = await Promise.all([import("html2canvas-pro"), import("jspdf")]);

	// Pre-load branding logo if configured
	let logoData: { dataUrl: string; width: number; height: number } | null = null;
	if (branding?.logoSrc) {
		try {
			logoData = await loadImage(branding.logoSrc);
		} catch {
			// Logo failed to load — continue without it
		}
	}

	const pdf = new jsPDF({ orientation, unit: "mm", format: "a4" });
	const pageWidth = pdf.internal.pageSize.getWidth();
	const pageHeight = pdf.internal.pageSize.getHeight();
	const contentWidth = pageWidth - margin * 2;
	let cursorY = margin;

	for (let i = 0; i < sections.length; i++) {
		const { element, label } = sections[i];

		// Yield between sections so the UI stays responsive
		await new Promise((r) => setTimeout(r, 0));

		const canvas = await html2canvas(element, {
			scale,
			useCORS: true,
			logging: false,
			backgroundColor: "#ffffff",
		});

		const imgHeight = (canvas.height * contentWidth) / canvas.width;
		const headingHeight = label ? 10 : 0;

		// Start a new page if the heading + a meaningful chunk won't fit
		if (cursorY + headingHeight + 20 > pageHeight - margin) {
			pdf.addPage();
			cursorY = margin;
		}

		if (label) {
			pdf.setFontSize(14);
			pdf.setTextColor(30, 30, 30);
			pdf.text(label, margin, cursorY + 5);
			cursorY += headingHeight;
		}

		// Slice the captured image into page-sized chunks
		let yOffset = 0;
		while (yOffset < imgHeight) {
			const remainingOnPage = pageHeight - cursorY - margin;
			const sliceHeight = Math.min(remainingOnPage, imgHeight - yOffset);

			const sourceY = (yOffset / imgHeight) * canvas.height;
			const sourceH = (sliceHeight / imgHeight) * canvas.height;

			const sliceCanvas = document.createElement("canvas");
			sliceCanvas.width = canvas.width;
			sliceCanvas.height = Math.round(sourceH);
			const ctx = sliceCanvas.getContext("2d");
			if (ctx) {
				ctx.drawImage(canvas, 0, sourceY, canvas.width, sourceH, 0, 0, canvas.width, Math.round(sourceH));
				const sliceImg = sliceCanvas.toDataURL("image/jpeg", quality);
				pdf.addImage(sliceImg, "JPEG", margin, cursorY, contentWidth, sliceHeight);
			}

			cursorY += sliceHeight;
			yOffset += sliceHeight;

			if (yOffset < imgHeight) {
				pdf.addPage();
				cursorY = margin;
			}
		}

		// Small gap between sections
		cursorY += 4;
	}

	// Stamp branding on every page
	if (branding && (logoData || branding.text)) {
		const totalPages = pdf.getNumberOfPages();
		const brandingText = branding.text ?? "";
		const logoH = 3.5; // logo height in mm
		const logoW = logoData ? (logoData.width / logoData.height) * logoH : 0;
		const gap = logoData && brandingText ? 1.5 : 0;

		pdf.setFontSize(8);
		pdf.setTextColor(150, 150, 150);
		const textW = brandingText ? pdf.getTextWidth(brandingText) : 0;
		const totalW = textW + gap + logoW;

		for (let p = 1; p <= totalPages; p++) {
			pdf.setPage(p);
			const x = pageWidth - margin - totalW;
			const y = pageHeight - margin + 2;

			if (brandingText) {
				pdf.setFontSize(8);
				pdf.setTextColor(150, 150, 150);
				pdf.text(brandingText, x, y + logoH / 2 + 1);
			}

			if (logoData) {
				pdf.addImage(logoData.dataUrl, "PNG", x + textW + gap, y, logoW, logoH);
			}
		}
	}

	const now = new Date();
	const dateStamp = `${now.getFullYear()}-${String(now.getMonth() + 1).padStart(2, "0")}-${String(now.getDate()).padStart(2, "0")}`;
	pdf.save(`${filename}-${dateStamp}.pdf`);
}