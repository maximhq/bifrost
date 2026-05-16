import { UserAccessProfile } from "@enterprise/lib/types/accessProfile";

export interface User {
	id: string;
	name: string;
	email: string;
	role_id?: number;
	role?: {
		id: number;
		name: string;
		description: string;
		is_system_role: boolean;
	};
	profile?: Record<string, unknown>;
	config?: Record<string, unknown>;
	claims?: Record<string, unknown>;
	access_profile?: UserAccessProfile;
	teams?: Array<{ id: string; name: string; business_unit_id?: string; business_unit_name?: string }>;
	created_at: string;
	updated_at: string;
}

export interface GetUsersResponse {
	users: User[];
	total: number;
	page: number;
	limit: number;
	total_pages: number;
	has_more: boolean;
}
