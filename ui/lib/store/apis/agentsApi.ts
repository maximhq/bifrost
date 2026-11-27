import type {
	AgentPushConfigFilters,
	AgentPushConfigView,
	AgentRegistrationView,
	CreateAgentRequest,
	GetAgentPushConfigsResponse,
	GetAgentsResponse,
	InspectAgentCardRequest,
	InspectAgentCardResponse,
	UpdateAgentRequest,
} from "@/lib/types/agents";
import { baseApi } from "./baseApi";

export const agentsApi = baseApi.injectEndpoints({
	endpoints: (builder) => ({
		getAgentPushConfigs: builder.query<GetAgentPushConfigsResponse, AgentPushConfigFilters>({
			query: ({ agent_names, ...params }) => ({
				url: "/agents/push-configs",
				params: { ...params, agent_names: agent_names?.join(",") },
			}),
			providesTags: ["Agents"],
		}),

		deleteAgentPushConfig: builder.mutation<void, Pick<AgentPushConfigView, "agent_name" | "task_id" | "config_id">>({
			query: ({ agent_name, task_id, config_id }) => ({
				url: `/agents/push-configs/${encodeURIComponent(agent_name)}/${encodeURIComponent(task_id)}/${encodeURIComponent(config_id)}`,
				method: "DELETE",
			}),
			invalidatesTags: ["Agents"],
		}),

		getAgents: builder.query<GetAgentsResponse, void>({
			query: () => ({ url: "/agents" }),
			providesTags: ["Agents"],
		}),

		inspectAgentCard: builder.mutation<InspectAgentCardResponse, InspectAgentCardRequest>({
			query: (body) => ({ url: "/agents/inspect", method: "POST", body }),
		}),
		createAgent: builder.mutation<AgentRegistrationView, CreateAgentRequest>({
			query: (body) => ({ url: "/agents", method: "POST", body }),
			invalidatesTags: ["Agents"],
		}),

		updateAgent: builder.mutation<AgentRegistrationView, { name: string; data: UpdateAgentRequest }>({
			query: ({ name, data }) => ({ url: `/agents/${encodeURIComponent(name)}`, method: "PUT", body: data }),
			invalidatesTags: (_result, _error, { name }) => ["Agents", { type: "Agents", id: name }],
		}),

		deleteAgent: builder.mutation<void, string>({
			query: (name) => ({ url: `/agents/${encodeURIComponent(name)}`, method: "DELETE" }),
			invalidatesTags: ["Agents"],
		}),
	}),
});

export const {
	useGetAgentPushConfigsQuery,
	useDeleteAgentPushConfigMutation,
	useGetAgentsQuery,
	useInspectAgentCardMutation,
	useCreateAgentMutation,
	useUpdateAgentMutation,
	useDeleteAgentMutation,
} = agentsApi;