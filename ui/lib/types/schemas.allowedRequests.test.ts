import { describe, expect, it } from "vitest";

import { allowedRequestsSchema } from "./schemas";

const stored = {
	text_completion: true,
	text_completion_stream: true,
	chat_completion: true,
	chat_completion_stream: true,
	responses: true,
	responses_stream: true,
	embedding: true,
	speech: true,
	speech_stream: true,
	transcription: true,
	transcription_stream: true,
	image_generation: true,
	image_generation_stream: true,
	image_edit: true,
	image_edit_stream: true,
	image_variation: true,
	rerank: true,
	video_generation: true,
	video_edit: true,
	video_retrieve: true,
	video_download: true,
	video_delete: true,
	video_list: true,
	video_remix: true,
	count_tokens: true,
	list_models: true,
	websocket_responses: true,
	realtime: false,
};

describe("allowedRequestsSchema", () => {
	// Providers saved before a flag existed carry no key for it; newer flags are optional so
	// those stored shapes still validate.
	it("accepts a stored allowed_requests without the live flag", () => {
		const result = allowedRequestsSchema.safeParse(stored);
		expect(result.success).toBe(true);
	});

	it("still accepts an explicit live flag", () => {
		const result = allowedRequestsSchema.safeParse({ ...stored, live: true });
		expect(result.success).toBe(true);
		expect(result.data?.live).toBe(true);
	});
});
