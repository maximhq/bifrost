export type NotificationSeverity = "info" | "success" | "warning" | "error";
export type NotificationAudience = "all" | "roles";

export interface Notification {
	id: string;
	audience: NotificationAudience;
	role_ids?: number[];
	severity: NotificationSeverity;
	title: string;
	message: string;
	action_label?: string;
	action_path?: string;
	subject_type?: string;
	subject_id?: string;
	/** Set once the subject is handled; the row stays visible but stops asking for action. */
	resolved_at?: string;
	resolution?: string;
	created_at: string;
	expires_at: string;
}

export interface NotificationListResponse {
	notifications: Notification[];
	next_cursor?: string;
}