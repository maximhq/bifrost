export interface RSSNewsSwitchState {
	checked: boolean;
	/** False until the gateway setting is known; the switch stays disabled meanwhile. */
	ready: boolean;
	/** The setting could not be loaded, so the sheet shows an error next to the switch. */
	failed: boolean;
}

/**
 * State of the gateway-wide news switch. Showing a default before the setting
 * loads would let an admin "turn off" news that is already off, so the switch
 * stays unknown until the gateway answers.
 */
export function rssNewsSwitchState(settings: { enabled: boolean } | undefined, isError: boolean): RSSNewsSwitchState {
	if (!settings) return { checked: false, ready: false, failed: isError };
	return { checked: settings.enabled, ready: true, failed: false };
}
/** The add-feed form field a server error belongs under. */
export function rssFeedFormErrorField(message: string): "name" | "url" | "icon_url" {
	if (/icon_url/i.test(message)) return "icon_url";
	if (/\bname\b/i.test(message)) return "name";
	// The server fetches the URL before saving, so anything else is its verdict on the URL.
	return "url";
}