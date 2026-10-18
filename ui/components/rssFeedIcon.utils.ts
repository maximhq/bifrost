import { isSafeWebURL, RSS_FEED_LOGO_DATA_URI_PATTERN } from "@/lib/utils/validation";

/** Builtin icon keys rendered from bundled assets rather than fetched from the feed's site. */
const PROVIDER_ICON_KEYS = ["openai", "anthropic", "gemini"] as const;

export type RSSFeedIconProviderKey = (typeof PROVIDER_ICON_KEYS)[number];

export type RSSFeedIconSource =
	| { kind: "bifrost" }
	| { kind: "provider"; provider: RSSFeedIconProviderKey }
	| { kind: "image"; url: string }
	| { kind: "fallback" };

/** Sites whose favicon stands in for a builtin icon key with no bundled asset. */
const FAVICON_HOSTS: Record<string, string> = {
	cursor: "cursor.com",
};

/**
 * Picks what to draw for a feed. Builtin keys map to bundled icons; a custom
 * logo (iconUrl) comes next; anything else uses the favicon of the feed URL's
 * host, and a feed whose URL cannot be parsed gets the generic RSS glyph.
 */
export function rssFeedIconSource(icon: string | undefined, feedUrl: string, iconUrl?: string): RSSFeedIconSource {
	if (icon === "bifrost") return { kind: "bifrost" };
	if (icon && (PROVIDER_ICON_KEYS as readonly string[]).includes(icon)) {
		return { kind: "provider", provider: icon as RSSFeedIconProviderKey };
	}
	if (icon && Object.hasOwn(FAVICON_HOSTS, icon)) return { kind: "image", url: `https://${FAVICON_HOSTS[icon]}/favicon.ico` };
	if (iconUrl && isRSSFeedLogoValue(iconUrl)) return { kind: "image", url: iconUrl };
	try {
		const parsed = new URL(feedUrl);
		if (parsed.protocol !== "http:" && parsed.protocol !== "https:") return { kind: "fallback" };
		return { kind: "image", url: `${parsed.protocol}//${parsed.host}/favicon.ico` };
	} catch {
		return { kind: "fallback" };
	}
}

/** Image types accepted for an uploaded logo; mirrors the server's data: URI check. */
export const RSS_FEED_LOGO_TYPES = ["image/png", "image/jpeg"];

/** Leading bytes of each accepted logo type; the server checks the same signatures. */
const RSS_FEED_LOGO_SIGNATURES: Record<string, number[]> = {
	"image/png": [0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a],
	"image/jpeg": [0xff, 0xd8, 0xff],
};

/** Uploaded logos are stored inline in the feed row, so they are capped well below anything a tray icon needs. */
export const RSS_FEED_LOGO_MAX_BYTES = 96 * 1024;

/** True for a value the server accepts as a logo: an http(s) URL without credentials, or a base64 PNG or JPEG data URI. */
export function isRSSFeedLogoValue(value: string): boolean {
	if (value.startsWith("data:")) return RSS_FEED_LOGO_DATA_URI_PATTERN.test(value);
	return isSafeWebURL(value);
}

/** Why an uploaded logo file is refused, or undefined when it is acceptable. */
export function rssFeedLogoFileError(file: Pick<File, "type" | "size">): string | undefined {
	if (!RSS_FEED_LOGO_TYPES.includes(file.type)) return "Logo must be a PNG or JPEG image";
	if (file.size > RSS_FEED_LOGO_MAX_BYTES) return "Logo must be 96 KB or smaller";
	return undefined;
}

/** Why an uploaded logo's leading bytes are refused, or undefined when they match an accepted image type. */
export function rssFeedLogoBytesError(bytes: Uint8Array, type: string): string | undefined {
	const signature = RSS_FEED_LOGO_SIGNATURES[type];
	if (!signature || signature.some((byte, i) => bytes[i] !== byte)) return "This file is not a valid PNG or JPEG image";
	return undefined;
}

/** Why an uploaded logo file is refused (declared type, size, then its leading bytes), or undefined when it is acceptable. */
export async function checkRSSFeedLogoFile(file: File): Promise<string | undefined> {
	const error = rssFeedLogoFileError(file);
	if (error) return error;
	return rssFeedLogoBytesError(new Uint8Array(await file.slice(0, 8).arrayBuffer()), file.type);
}

/** Reads an uploaded logo into the data: URI the API stores. */
export function readRSSFeedLogoFile(file: File): Promise<string> {
	return new Promise((resolve, reject) => {
		const reader = new FileReader();
		reader.onload = () => resolve(String(reader.result));
		reader.onerror = () => reject(reader.error ?? new Error("Could not read the logo file"));
		reader.readAsDataURL(file);
	});
}