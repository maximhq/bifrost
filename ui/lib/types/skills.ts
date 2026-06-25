// Skills Repository Types

export interface Skill {
	id: string;
	name: string;
	description: string;
	license?: string;
	compatibility?: string;
	metadata: Record<string, string>;
	extra_frontmatter: Record<string, unknown>;
	allowed_tools?: string;
	skill_md_body: string;
	latest_version: string;
	highest_version: string;
	file_count?: number;
	created_by?: string;
	created_at: string;
	updated_at: string;
	files: SkillFile[];
}

export interface SkillFile {
	id: string;
	skill_version_id: string;
	path: string;
	source_type: "url" | "text" | "dataurl" | "upload";
	content?: string; // text (hydrated from blob on read)
	source_url?: string;
	dataurl?: string;
	storage_key?: string;
	blob_id?: string;
	mime_type: string;
	file_size_bytes: number;
	created_at: string;
	updated_at: string;
}

export interface SkillVersion {
	id: string;
	skill_id: string;
	version: string;
	skill_md_body: string;
	frontmatter_snapshot: Record<string, unknown>;
	files: SkillFile[];
	created_by?: string;
	created_at: string;
}

// Lean version summary returned in version list responses.
export type SkillVersionSummary = Pick<SkillVersion, "id" | "skill_id" | "version" | "created_by" | "created_at">;

// Request/Response types

export interface SkillFileEntry {
	path: string;
	source_type: "url" | "text" | "dataurl" | "upload";
	content?: string; // text
	source_url?: string; // url
	dataurl?: string; // dataurl
	upload_id?: string; // upload
	storage_key?: string; // upload
	blob_id?: string; // upload
	file_size_bytes?: number;
	mime_type: string;
	__local?: boolean; // client-only: unsaved file row can reopen full source form
}

export interface CreateSkillRequest {
	name: string;
	description: string;
	license?: string;
	compatibility?: string;
	metadata?: Record<string, string>;
	extra_frontmatter?: Record<string, unknown>;
	allowed_tools?: string;
	skill_md_body: string;
	version: string;
	files?: SkillFileEntry[];
}

export type UpdateSkillRequest = Omit<CreateSkillRequest, "name"> & {
	serve?: boolean; // when false, creates a new version without switching serving
};

export type SkillListItem = Omit<Skill, "skill_md_body" | "files"> & {
	file_count: number;
};

export interface ListSkillsResponse {
	skills: SkillListItem[];
	total: number;
	limit: number;
	offset: number;
}

export interface GetSkillResponse {
	skill: Skill;
}

export interface CreateSkillResponse {
	skill: Skill;
}

export interface UpdateSkillResponse {
	skill: Skill;
}

export interface ListSkillVersionsResponse {
	versions: SkillVersionSummary[];
	total: number;
	limit: number;
	offset: number;
}

export interface ShiftSkillVersionRequest {
	id: string;
	version: string;
}

export type AllSkillsVersionBump = "patch" | "minor" | "major";

export interface AllSkillsVersionResponse {
	version: string;
}

export interface BumpAllSkillsVersionRequest {
	bump: AllSkillsVersionBump;
}

export interface UploadFileResponse {
	upload_id: string;
	storage_key?: string;
	blob_id?: string;
	filename: string;
	mime_type: string;
	file_size_bytes: number;
}