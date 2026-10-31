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