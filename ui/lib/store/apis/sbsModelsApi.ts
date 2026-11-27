// SBS fork: Model Catalog "Test" button (POST /api/sbs/models/test).
import { baseApi } from "./baseApi";

export interface SBSModelTestRequest {
	provider: string;
	model: string;
	prompt?: string;
	mode?: "chat" | "embedding";
}

export interface SBSModelTestResult {
	ok: boolean;
	provider: string;
	model: string;
	mode: "chat" | "embedding";
	latency_ms: number;
	reply?: string;
	dimensions?: number;
	tokens?: number;
	status_code?: number;
	error?: string;
	/** The exact body Bifrost sent to the provider. */
	raw_request?: unknown;
}

export const sbsModelsApi = baseApi.injectEndpoints({
	endpoints: (builder) => ({
		testSBSModel: builder.mutation<SBSModelTestResult, SBSModelTestRequest>({
			query: (body) => ({ url: "/sbs/models/test", method: "POST", body }),
		}),
	}),
});

export const { useTestSBSModelMutation } = sbsModelsApi;
