/**
 * A response handler for endpoints that return plain text on success but a
 * JSON error body on failure: success is read as text, and an error body is
 * parsed when it is JSON so getErrorMessage can show the server's message.
 */
export async function textOrJSONError(response: Response): Promise<unknown> {
	const text = await response.text();
	if (response.ok) return text;
	try {
		return JSON.parse(text);
	} catch {
		return text;
	}
}

/**
 * Whether an error body is a typed unavailable answer, `{reason, message}`, as Warp's 503s are
 * (for example `no_vector_store`, naming the vector store to fix). It has no `error.message`, so
 * the base query must keep it as sent for the page to show the server's message.
 */
export function isReasonedErrorBody(data: unknown): data is { reason: string; message: string } {
	if (!data || typeof data !== "object") return false;
	const { reason, message } = data as { reason?: unknown; message?: unknown };
	return typeof reason === "string" && reason !== "" && typeof message === "string" && message !== "";
}