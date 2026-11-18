import { parseJSONExact } from "@/lib/types/schemas";

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
 * Reads a JSON response keeping whole numbers past 2^53 exact (relay body overrides), so a save
 * sends back what was stored. A success that is not JSON stays a parsing error; an error body that
 * is not JSON is kept as text for getErrorMessage.
 */
export async function exactJSON(response: Response): Promise<unknown> {
	const text = await response.text();
	if (!text) return null;
	if (response.ok) return parseJSONExact(text);
	try {
		return parseJSONExact(text);
	} catch {
		return text;
	}
}