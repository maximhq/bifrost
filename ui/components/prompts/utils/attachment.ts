import { MessageContent } from "@/lib/message";

export function fileToBase64(file: File): Promise<string> {
	return new Promise((resolve, reject) => {
		const reader = new FileReader();
		reader.onload = () => resolve(reader.result as string);
		reader.onerror = reject;
		reader.readAsDataURL(file);
	});
}

export async function fileToAttachment(file: File): Promise<MessageContent | null> {
	if (file.type.startsWith("image/")) {
		const dataUrl = await fileToBase64(file);
		return {
			type: "image_url",
			image_url: { url: dataUrl, detail: "auto" },
		};
	}

	if (file.type.startsWith("audio/")) {
		const dataUrl = await fileToBase64(file);
		// Extract base64 data and format from data URL
		const base64Data = dataUrl.split(",")[1] || "";
		const format = file.name.split(".").pop() || file.type.split("/")[1] || "wav";
		return {
			type: "input_audio",
			input_audio: { data: base64Data, format },
		};
	}

	// Generic file — API expects full data URL with MIME prefix
	const dataUrl = await fileToBase64(file);
	return {
		type: "file",
		file: {
			file_data: dataUrl,
			filename: file.name,
			file_type: file.type || "application/octet-stream",
		},
	};
}