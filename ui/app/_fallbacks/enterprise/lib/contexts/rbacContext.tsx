import { createContext, useContext } from "react";

// RBAC Resource Names (must match backend definitions)
export enum RbacResource {
	GuardrailsConfig = "GuardrailsConfig",
	GuardrailsProviders = "GuardrailsProviders",
	GuardrailRules = "GuardrailRules",
	UserProvisioning = "UserProvisioning",
	Cluster = "Cluster",
	Settings = "设置",
	Users = "用户",
	Logs = "日志",
	Observability = "可观测性",
	Dashboard = "仪表盘",
	VirtualKeys = "VirtualKeys",
	ModelProvider = "ModelProvider",
	Plugins = "插件",
	MCPGateway = "MCPGateway",
	MCPToolGroups = "MCPToolGroups",
	MCPLogs = "MCPLogs",
	AdaptiveRouter = "AdaptiveRouter",
	AuditLogs = "AuditLogs",
	Customers = "客户",
	Teams = "团队",
	RBAC = "RBAC",
	Governance = "治理",
	RoutingRules = "RoutingRules",
	PromptRepository = "PromptRepository",
	PromptDeploymentStrategy = "PromptDeploymentStrategy",
	AccessProfiles = "AccessProfiles",
	APIKeys = "APIKeys",
	Inference = "推理",
	Metrics = "Metrics",
	FeatureFlags = "FeatureFlags",
	CircuitBreaker = "CircuitBreaker",
	Devices = "设备",
	Inventory = "Inventory",
	EdgeConfig = "EdgeConfig",
	SkillsRepository = "SkillsRepository",
}

// RBAC Operation Names (must match backend definitions)
export enum RbacOperation {
	Read = "Read",
	View = "View",
	Create = "创建",
	Update = "Update",
	Delete = "删除",
	Reveal = "Reveal",
	Download = "下载",
}

interface RbacContextType {
	isAllowed: (resource: RbacResource, operation: RbacOperation) => boolean;
	permissions: Record<string, Record<string, boolean>>;
	isLoading: boolean;
	refetch: () => void;
}

const RbacContext = createContext<RbacContextType | null>(null);

// Dummy provider that allows all permissions
export function RbacProvider({ children }: { children: React.ReactNode }) {
	return (
		<RbacContext.Provider
			value={{
				isAllowed: () => true, // Always allow in OSS
				permissions: {},
				isLoading: false,
				refetch: () => {},
			}}
		>
			{children}
		</RbacContext.Provider>
	);
}

// Hook that always returns true (no restrictions in OSS)
export function useRbac(_resource: RbacResource, _operation: RbacOperation): boolean {
	return true;
}

// Hook to access full RBAC context
export function useRbacContext() {
	const context = useContext(RbacContext);
	if (!context) {
		// Return dummy values if used outside provider
		return {
			isAllowed: () => true,
			permissions: {},
			isLoading: false,
			refetch: () => {},
		};
	}
	return context;
}
