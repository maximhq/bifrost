import { baseApi } from "./baseApi";

export interface OAuth2ConsentFlowDetail {
	client_name: string;
	available_modes: Array<"vk" | "session" | "user">;
	logged_in_user?: { id: string; name?: string };
	expires_at: string;
}

export interface OAuth2ConsentSubmitRequest {
	mode: "vk" | "session" | "user";
	value?: string; // VK plaintext for mode=vk; absent for session/user
}

export interface OAuth2ConsentSubmitResponse {
	redirect_url: string;
}

export const oauth2ConsentApi = baseApi.injectEndpoints({
	endpoints: (builder) => ({
		getOAuth2ConsentFlow: builder.query<OAuth2ConsentFlowDetail, string>({
			query: (flowId) => ({
				url: `/oauth2/consent/flows/${encodeURIComponent(flowId)}`,
			}),
		}),
		submitOAuth2ConsentFlow: builder.mutation<
			OAuth2ConsentSubmitResponse,
			{ flowId: string; body: OAuth2ConsentSubmitRequest }
		>({
			query: ({ flowId, body }) => ({
				url: `/oauth2/consent/flows/${encodeURIComponent(flowId)}`,
				method: "PUT",
				body,
			}),
		}),
	}),
});

export const {
	useGetOAuth2ConsentFlowQuery,
	useSubmitOAuth2ConsentFlowMutation,
} = oauth2ConsentApi;
