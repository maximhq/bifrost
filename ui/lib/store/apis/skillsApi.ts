import { baseApi } from "@/lib/store/apis/baseApi";
import {
	AllSkillsVersionResponse,
	BumpAllSkillsVersionRequest,
	CreateSkillRequest,
	CreateSkillResponse,
	GetSkillResponse,
	ListSkillsResponse,
	ListSkillVersionsResponse,
	ShiftSkillVersionRequest,
	UpdateSkillRequest,
	UpdateSkillResponse,
	UploadFileResponse,
} from "@/lib/types/skills";

// Inject Skills Repository endpoints into baseApi
export const skillsApi = baseApi.injectEndpoints({
	overrideExisting: true,
	endpoints: (builder) => ({
		// List all skills (paginated)
		listSkills: builder.query<
			ListSkillsResponse,
			{ limit?: number; offset?: number; search?: string; sort_by?: "name" | "updated_at" | "created_at"; order?: "asc" | "desc" } | void
		>({
			query: (params) => {
				const searchParams = new URLSearchParams();
				if (params?.limit != null) searchParams.set("limit", String(params.limit));
				if (params?.offset != null) searchParams.set("offset", String(params.offset));
				if (params?.search) searchParams.set("search", params.search);
				if (params?.sort_by) searchParams.set("sort_by", params.sort_by);
				if (params?.order) searchParams.set("order", params.order);
				const qs = searchParams.toString();
				return `/skills${qs ? `?${qs}` : ""}`;
			},
			providesTags: ["技能"],
		}),

		// Get single skill by ID (optionally at a specific version)
		getSkill: builder.query<GetSkillResponse, string | { id: string; version?: string }>({
			query: (arg) => {
				const id = typeof arg === "string" ? arg : arg.id;
				const version = typeof arg === "string" ? undefined : arg.version;
				return `/skills/${id}${version ? `?version=${encodeURIComponent(version)}` : ""}`;
			},
			providesTags: (_result, _error, arg) => {
				const id = typeof arg === "string" ? arg : arg.id;
				return [{ type: "技能", id }];
			},
		}),

		// Create a new skill
		createSkill: builder.mutation<CreateSkillResponse, CreateSkillRequest>({
			query: (data) => ({
				url: "/skills",
				method: "POST",
				body: data,
			}),
			invalidatesTags: ["技能"],
		}),

		// Update an existing skill (creates a new version)
		updateSkill: builder.mutation<UpdateSkillResponse, { id: string; data: UpdateSkillRequest }>({
			query: ({ id, data }) => ({
				url: `/skills/${id}`,
				method: "PUT",
				body: data,
			}),
			invalidatesTags: (_result, _error, { id }) => ["技能", { type: "技能", id }],
		}),

		// Delete a skill
		deleteSkill: builder.mutation<void, string>({
			query: (id) => ({
				url: `/skills/${id}`,
				method: "DELETE",
			}),
			invalidatesTags: (_result, _error, id) => ["技能", { type: "技能", id }],
		}),

		// List versions for a skill (paginated)
		listSkillVersions: builder.query<
			ListSkillVersionsResponse,
			{ id: string; limit?: number; offset?: number; search?: string; sort_by?: "version" | "created_at"; order?: "asc" | "desc" }
		>({
			query: ({ id, ...params }) => {
				const searchParams = new URLSearchParams();
				if (params?.limit != null) searchParams.set("limit", String(params.limit));
				if (params?.offset != null) searchParams.set("offset", String(params.offset));
				if (params?.search) searchParams.set("search", params.search);
				if (params?.sort_by) searchParams.set("sort_by", params.sort_by);
				if (params?.order) searchParams.set("order", params.order);
				const qs = searchParams.toString();
				return `/skills/${id}/versions${qs ? `?${qs}` : ""}`;
			},
			providesTags: (_result, _error, { id }) => [{ type: "技能", id: `${id}-versions` }],
		}),

		// Shift a skill to serve a specific version
		shiftSkillVersion: builder.mutation<GetSkillResponse, ShiftSkillVersionRequest>({
			query: ({ id, version }) => ({
				url: `/skills/${id}/shift-version`,
				method: "POST",
				body: { version },
			}),
			invalidatesTags: (_result, _error, { id }) => ["技能", { type: "技能", id }, { type: "技能", id: `${id}-versions` }],
		}),

		// Current all-skills repository version
		getAllSkillsVersion: builder.query<AllSkillsVersionResponse, void>({
			query: () => "/skills/all/version",
			providesTags: [{ type: "技能", id: "all-version" }],
		}),

		// Manually bump the all-skills repository version
		bumpAllSkillsVersion: builder.mutation<AllSkillsVersionResponse, BumpAllSkillsVersionRequest>({
			query: (data) => ({
				url: "/skills/all/version",
				method: "PUT",
				body: data,
			}),
			invalidatesTags: [{ type: "技能", id: "all-version" }],
		}),

		// Upload a skill file
		uploadSkillFile: builder.mutation<UploadFileResponse, { file: File }>({
			query: ({ file }) => {
				const formData = new FormData();
				formData.append("file", file);
				return {
					url: "/skills/files/upload",
					method: "POST",
					body: formData,
					// Let the browser set the Content-Type with boundary for multipart
					headers: {
						// Remove the default Content-Type so fetch sets multipart boundary
					},
					formData: true,
				};
			},
		}),
	}),
});

export const {
	useListSkillsQuery,
	useGetSkillQuery,
	useCreateSkillMutation,
	useUpdateSkillMutation,
	useDeleteSkillMutation,
	useListSkillVersionsQuery,
	useShiftSkillVersionMutation,
	useGetAllSkillsVersionQuery,
	useBumpAllSkillsVersionMutation,
	useUploadSkillFileMutation,
} = skillsApi;