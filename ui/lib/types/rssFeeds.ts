export type RSSFeedSource = "builtin" | "custom";

export interface RSSFeed {
	id: number;
	slug: string;
	name: string;
	url: string;
	icon?: string;
	icon_url?: string;
	source: RSSFeedSource;
	enabled: boolean;
	created_at: string;
	updated_at: string;
}

export interface RSSFeedListResponse {
	feeds: RSSFeed[];
}

export interface RSSItem {
	id: string;
	feed_id: number;
	feed_slug: string;
	feed_name: string;
	feed_icon?: string;
	feed_url: string;
	title: string;
	link: string;
	summary?: string;
	published_at: string;
}

export interface RSSFeedError {
	feed_id: number;
	feed_name: string;
	message: string;
}

export interface RSSItemsResponse {
	/** False when news is switched off for the gateway; items and errors are then empty. */
	enabled: boolean;
	items: RSSItem[];
	errors: RSSFeedError[];
	/** Custom logo per feed id, sent once instead of on every item; absent when no feed has one. */
	feed_icons?: Record<string, string>;
}

export interface CreateRSSFeedRequest {
	name: string;
	url: string;
	icon_url?: string;
	enabled?: boolean;
}

export interface UpdateRSSFeedRequest {
	id: number;
	name?: string;
	url?: string;
	icon_url?: string;
	enabled?: boolean;
}
export interface RSSSettings {
	enabled: boolean;
}