import { withBasePath } from "./basePath";

/**
 * The path callers reach a relay at, carrying server.base_path: "/relay/voice" at the domain
 * root, "/bifrost/relay/voice" under a "/bifrost" prefix. Relay names are URL-safe by schema
 * (letters, digits, dots, dashes, underscores), so the name is not encoded; a placeholder such
 * as "<name>" passes through for help text.
 */
export function relayPath(name: string): string {
	return withBasePath(`/relay/${name}`);
}