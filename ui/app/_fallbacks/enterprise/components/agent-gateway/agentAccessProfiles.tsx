// This no-op fallback keeps the extension point available when no custom access section is provided.
export default function AgentAccessProfiles(_props: { agentName: string; active: boolean }) {
	return null;
}