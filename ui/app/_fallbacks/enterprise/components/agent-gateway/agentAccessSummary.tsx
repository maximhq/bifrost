import type { AgentRegistrationView } from "@/lib/types/agents";

export default function AgentAccessSummary({ fallback }: { agent: AgentRegistrationView; fallback: string }) {
	return fallback;
}