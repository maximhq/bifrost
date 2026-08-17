import type { OdinConfig, OdinConfigInput } from "@/lib/types/odin";
import { baseApi } from "./baseApi";

export const odinApi = baseApi.injectEndpoints({
	endpoints: (builder) => ({
		getOdinConfig: builder.query<OdinConfig, void>({
			query: () => ({ url: "/odin/config" }),
			providesTags: ["OdinConfig"],
		}),
		updateOdinConfig: builder.mutation<OdinConfig, OdinConfigInput>({
			query: (body) => ({ url: "/odin/config", method: "PUT", body }),
			invalidatesTags: ["OdinConfig"],
		}),
	}),
});

export const { useGetOdinConfigQuery, useUpdateOdinConfigMutation } = odinApi;