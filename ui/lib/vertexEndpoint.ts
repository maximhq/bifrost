import type { VertexEndpoint } from "./types/config";

// Parse only the serving routes the supported endpoint formats use. The route
// gives the method, not the body format, which stays the caller's choice.
// A pasted URL supplies explicit per-alias project/region overrides; credentials
// remain on the key. Query parameters and fragments are never persisted.
export function parseVertexEndpointURL(value: string): {
	endpoint: Pick<VertexEndpoint, "endpoint_id" | "method">;
	projectId: string;
	region: string;
} | null {
	try {
		const url = new URL(value);
		if (url.protocol !== "https:" || url.username || url.password || url.port || url.search || url.hash) return null;
		const match = url.pathname.match(
			/^\/v1(?:beta1)?\/projects\/([A-Za-z0-9_-]+)\/locations\/([a-z0-9-]+)\/endpoints\/([A-Za-z0-9_-]+)(:rawPredict|:streamRawPredict|\/chat\/completions)\/?$/,
		);
		if (!match) return null;
		const [, projectId, region, endpointId, route] = match;
		if (url.hostname !== `${region}-aiplatform.googleapis.com` || !region.includes("-")) return null;
		return {
			endpoint: { endpoint_id: endpointId, method: route === "/chat/completions" ? "chat_completions" : "raw_predict" },
			projectId,
			region,
		};
	} catch {
		return null;
	}
}