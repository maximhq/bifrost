import { Button } from "@/components/ui/button";
import { CalendarDaysIcon, EllipsisVerticalIcon, PlusIcon, RocketIcon, ShieldIcon, Trash2Icon, UserIcon } from "lucide-react";
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger } from "@/components/ui/dropdownMenu";
import { Input } from "@/components/ui/input";
import { useCallback, useMemo, useState } from "react";
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover";
import { RuleGroupType } from "react-querybuilder";
import { Label } from "@/components/ui/label";
import { celOperatorsRouting } from "@/lib/config/celOperatorsRouting";
import { CELFieldDefinition, CELRuleBuilder } from "@/components/ui/custom/celBuilder";
import { convertRuleGroupToCEL } from "@/lib/utils/celConverterRouting";
import { Textarea } from "@/components/ui/textarea";
import { format } from "date-fns";
import { useGetProvidersQuery } from "@/lib/store/apis/providersApi";
import { useGetPromptVersionQuery, useGetVersionsQuery, useGetVirtualKeysQuery } from "@/lib/store";
import { getProviderLabel } from "@/lib/constants/logs";
import { usePromptContext } from "../context";
import { PromptVersion } from "@/lib/types/prompts";

export const baseRoutingFields: CELFieldDefinition[] = [
	{
		name: "model",
		label: "Model",
		placeholder: "e.g., gpt-4, claude-3-sonnet",
		inputType: "text",
		valueEditorType: (operator: string) =>
			operator === "=" || operator === "!=" ? "select" : operator === "in" || operator === "notIn" ? "select" : "text",
		operators: ["=", "!=", "in", "notIn", "contains", "beginsWith", "endsWith", "matches"],
		defaultOperator: "=",
	},
	{
		name: "provider",
		label: "Provider",
		placeholder: "Select provider",
		inputType: "select",
		valueEditorType: (operator: string) =>
			operator === "matches" ? "text" : operator === "in" || operator === "notIn" ? "select" : "select",
		operators: ["=", "!=", "in", "notIn", "matches"],
		defaultOperator: "=",
	},
	{
		name: "headers",
		label: "Header",
		placeholder: "e.g., authorization, x-custom-header (use lowercase)",
		inputType: "keyValue",
		valueEditorType: "keyValue",
		operators: ["=", "!=", "contains", "beginsWith", "endsWith", "matches", "null", "notNull"],
		defaultOperator: "=",
	},
	{
		name: "params",
		label: "Query Parameter",
		placeholder: "e.g., api_key, user_id",
		inputType: "keyValue",
		valueEditorType: "keyValue",
		operators: ["=", "!=", "contains", "beginsWith", "endsWith", "matches", "null", "notNull"],
		defaultOperator: "=",
		description: "Check if the query parameter matches the given pattern.",
	},
	{
		name: "payload",
		label: "Request Payload",
		placeholder: 'e.g., { "key": "value" }',
		inputType: "textarea",
		valueEditorType: "textarea",
		operators: ["=", "!=", "contains", "beginsWith", "endsWith", "matches", "null", "notNull"],
		defaultOperator: "=",
		description: "Check if the request payload matches the given JSON pattern.",
	},
];

const defaultQuery: RuleGroupType = {
	combinator: "and",
	rules: [
		{
			id: "3c02825f-02fe-47ee-8c78-b29d949a99f5",
			field: "model",
			operator: "=",
			valueSource: "value",
			value: "",
		},
	],
};

export function PromptDeploymentView() {
	const { selectedVersionId, selectedPromptId } = usePromptContext();
	const { data: versionsData } = useGetVersionsQuery(selectedPromptId ?? "", { skip: !selectedPromptId });
	const [openDeploymentPopover, setOpenDeploymentPopover] = useState(false);
	const [view, setView] = useState<"list" | "deploy">("list");
	const [deployments, setDeployments] = useState<DeploymentItem[]>(initialDeploymentItems);
	const versions = versionsData?.versions ?? [];
	const selectedVersion = versions.find((v) => v.id === selectedVersionId);
	const latestVersion = versions.find((v) => v.is_latest);
	const displayVersion = selectedVersion ?? latestVersion;

	const handleSave = useCallback((item: { weight: number; filter: RuleGroupType }) => {
		const nextVersion = deployments.length > 0 ? Math.max(...deployments.map((d) => d.version)) + 1 : 1;
		setDeployments((prev) => [
			...prev,
			{
				version: nextVersion,
				weight: item.weight,
				filter: item.filter,
				created_at: new Date().toISOString(),
				created_by: { id: 0, name: "You", email: "" },
			},
		]);
		setView("list");
	}, [deployments]);

	const handleDelete = useCallback((version: number) => {
		setDeployments((prev) => prev.filter((d) => d.version !== version));
	}, []);

	return (
		<div>
			<Popover open={openDeploymentPopover} onOpenChange={(open)=>{
				setOpenDeploymentPopover(open);
				if (!open) {
					setView("list");
				}
			}}>
				<PopoverTrigger asChild>
					<Button variant="outline" className="h-8 bg-transparent" disabled={!displayVersion}>
						<RocketIcon className="h-4 w-4" />
						Deploy
					</Button>
				</PopoverTrigger>
				<PopoverContent
					className="custom-scrollbar bg-popover/95 w-full max-w-[var(--radix-popover-content-available-width)] min-w-[380px] overflow-hidden overscroll-none border p-0 shadow-xl backdrop-blur"
					align="end"
					style={{ maxHeight: "calc(var(--radix-popover-content-available-height) - 40px)" }}
					onFocusOutside={(e) => e.preventDefault()}
				>
					<div className="p-4">
						{view === "list" ? (
							<ExistingDeploymentView onDeploy={() => setView("deploy")} displayVersion={displayVersion} deployments={deployments} onDelete={handleDelete} />
						) : (
							<NewDeploymentView onCancel={() => setView("list")} onSave={handleSave} />
						)}
					</div>
				</PopoverContent>
			</Popover>
		</div>
	);
}

export function NewDeploymentView({ onCancel, onSave }: { onCancel: () => void; onSave: (item: { weight: number; filter: RuleGroupType }) => void }) {
	const [query, setQuery] = useState<RuleGroupType>(defaultQuery);
	const [weight, setWeight] = useState(1);

	const handleQueryChange = useCallback((expression: string, newQuery: RuleGroupType) => {
		setQuery(newQuery);
	}, []);

	const noopConvertToCEL = useCallback(() => "", []);
	const noopValidateRegex = useCallback(() => null, []);
	const builderContext = useMemo(() => ({
		allowCustomModels: true,
		menuPortalTarget: typeof window !== "undefined" ? document.body : null,
	}), []);

	// Fetch providers and virtual keys to populate the provider field
	const { data: providers } = useGetProvidersQuery();
	const { data: virtualKeysData } = useGetVirtualKeysQuery();

	const configuredProviders = useMemo(() => {
		const activeVirtualKeys = virtualKeysData?.virtual_keys?.filter((vk) => vk.is_active) ?? [];
		return (providers ?? []).filter((p) => {
			if (p.keys && p.keys.length > 0) return true;
			return activeVirtualKeys.some(
				(vk) => !vk.provider_configs || vk.provider_configs.length === 0 || vk.provider_configs.some((pc) => pc.provider === p.name),
			);
		});
	}, [providers, virtualKeysData]);

	const fields = useMemo<CELFieldDefinition[]>(() => {
		const providerValues =
			configuredProviders.length > 0
				? configuredProviders.map((p) => ({ name: p.name, label: getProviderLabel(p.name) }))
				: [{ name: "_no_providers", label: "No providers configured", disabled: true }];

		return baseRoutingFields.map((field) => {
			if (field.name === "provider") {
				return { ...field, values: providerValues };
			}
			return field;
		});
	}, [configuredProviders]);

	return (
		<div className="flex flex-col gap-4">
			<div className="space-y-1">
				<h3 className="text-base font-semibold tracking-tight">Create deployment</h3>
				<p className="text-muted-foreground text-sm">Define rules to control when this prompt version should be selected.</p>
			</div>
			<div className="flex flex-col gap-1.5">
				<Label className="text-muted-foreground text-xs font-medium tracking-wide uppercase">Weight</Label>
				<Input
					type="number"
					min={0}
					max={100}
					step={1}
					value={weight}
					onChange={(e) => setWeight(Math.max(0, Math.min(100, parseFloat(e.target.value) || 0)))}
					className="h-8 w-24"
				/>
				<p className="text-muted-foreground text-xs">Relative weight for traffic distribution across deployments.</p>
			</div>
			<div className="bg-muted/20 rounded-sm border p-3">
				<div className="mb-3 flex items-center justify-between">
					<Label className="text-muted-foreground text-xs font-medium tracking-wide uppercase">Deployment Rules</Label>
				</div>
				<CELRuleBuilder
					onChange={handleQueryChange}
					initialQuery={query}
					isLoading={false}
					fields={fields}
					operators={celOperatorsRouting}
					convertToCEL={noopConvertToCEL}
					validateRegex={noopValidateRegex}
					builderContext={builderContext}
					options={{ hideCELExpression: true }}
				/>
			</div>
			<div className="flex justify-end gap-2 border-t pt-3">
				<Button variant="outline" className="h-8 bg-transparent" onClick={onCancel}>
					Back
				</Button>
				<Button className="h-8 shadow-sm" onClick={() => onSave({ weight, filter: query })}>
					Save
				</Button>
			</div>
		</div>
	);
}

function ExistingDeploymentView({ onDeploy, displayVersion, deployments, onDelete }: { onDeploy: () => void; displayVersion?: PromptVersion; deployments: DeploymentItem[]; onDelete: (version: number) => void }) {
	if (deployments.length === 0) {
		return (
			<div className="flex flex-col items-center gap-3 py-8 text-center w-[500px]">
				<div className="bg-muted/50 flex size-12 items-center justify-center rounded-full">
					<RocketIcon className="text-muted-foreground h-6 w-6" />
				</div>
				<div className="space-y-1">
					<h3 className="text-sm font-medium">No deployments yet</h3>
					<p className="text-muted-foreground max-w-[260px] text-xs">Deploy a prompt version with deployment rules to control when it should be selected.</p>
				</div>
				{displayVersion && (
					<Button className="mt-1 h-8 gap-2" onClick={onDeploy}>
						<RocketIcon className="h-4 w-4" />
						Deploy v{displayVersion.version_number}
					</Button>
				)}
			</div>
		);
	}

	return (
		<div className="flex flex-col gap-4">
			<div className="flex items-start justify-between gap-3">
				<div className="space-y-1">
					<h3 className="text-base font-medium tracking-tight">Active deployments</h3>
					<p className="text-muted-foreground text-sm">Manage deployed prompt versions and inspect their deployment conditions.</p>
				</div>

				{displayVersion && (
					<Button className="h-8 gap-2" onClick={onDeploy}>
						<RocketIcon className="h-4 w-4" />
						Deploy v{displayVersion.version_number}
					</Button>
				)}
			</div>

			<div className="flex flex-col gap-4">
				{deployments.map((deployment) => (
					<div
						key={deployment.version}
						className="bg-muted/10 hover:bg-muted/20 flex flex-col gap-3 rounded-sm border p-3 transition-colors"
					>
						<div className="flex items-center justify-between">
							<div className="flex items-center gap-2">
								<div className="bg-background inline-flex items-center rounded-sm border px-2 py-1 text-xs font-medium">
									Version v{deployment.version}
								</div>
								<div className="bg-background inline-flex items-center rounded-sm border px-2 py-1 text-xs font-medium">
									Weight: {deployment.weight}
								</div>
							</div>

							<DropdownMenu>
								<DropdownMenuTrigger asChild>
									<Button variant="outline" className="size-7 bg-transparent" size={"icon"}>
										<EllipsisVerticalIcon className="h-4 w-4" />
									</Button>
								</DropdownMenuTrigger>
								<DropdownMenuContent align="end">
									<DropdownMenuItem>
										<ShieldIcon className="h-4 w-4" />
										Mark as fallback
									</DropdownMenuItem>
									<DropdownMenuItem variant="destructive" onClick={() => onDelete(deployment.version)}>
										<Trash2Icon className="h-4 w-4" />
										Delete
									</DropdownMenuItem>
								</DropdownMenuContent>
							</DropdownMenu>
						</div>

						<div className="flex flex-col gap-2">
							<Label className="text-muted-foreground text-xs tracking-wide uppercase">Deployment rules (CEL expression)</Label>
							<Textarea
								value={convertRuleGroupToCEL(deployment.filter) || "No rules defined yet"}
								readOnly
								className="bg-background/90 min-h-[84px] resize-none border font-mono text-sm"
								rows={4}
							/>
						</div>
						<div className="text-muted-foreground flex flex-wrap items-center gap-3 text-xs">
							<div className="inline-flex items-center gap-1.5">
								<CalendarDaysIcon className="h-3.5 w-3.5" />
								<span>{format(new Date(deployment.created_at), "MMM d, yyyy hh:mm a")}</span>
							</div>
						</div>
					</div>
				))}
			</div>
		</div>
	);
}

interface DeploymentItem {
	version: number;
	weight: number;
	filter: RuleGroupType;
	created_at: string;
	created_by: {
		id: number;
		name: string;
		email: string;
	};
}

const initialDeploymentItems: DeploymentItem[] = [
	{
		version: 1,
		weight: 0.4,
		filter: {
			combinator: "or",
			id: "a719de17-b278-48d6-ba49-2c4f1df654b6",
			rules: [
				{
					field: "model",
					id: "3af1c1fa-3d97-4c58-a411-b3ca070e76a0",
					operator: "=",
					value: "gemini-2.0-flash",
					valueSource: "value",
				},
				{
					combinator: "or",
					id: "b9ca5822-775d-4b40-9ff2-4343f75ce534",
					not: false,
					rules: [
						{
							field: "model",
							id: "2cf51c66-fcc6-4c2a-bc84-5e4bf6b72335",
							operator: "!=",
							value: "MiniMaxAI/MiniMax-M2.5",
							valueSource: "value",
						},
					],
				},
			],
		},
		created_at: "2026-03-23T12:00:00Z",
		created_by: {
			id: 1,
			name: "John Doe",
			email: "john.doe@example.com",
		},
	},
	{
		version: 2,
		weight: 0.3,
		filter: {
			combinator: "and",
			id: "c8d2e45f-1234-5678-9abc-def012345678",
			rules: [
				{
					field: "user_id",
					id: "f1e2d3c4-5678-9abc-def0-123456789abc",
					operator: "in",
					value: "premium_users",
					valueSource: "value",
				},
				{
					field: "region",
					id: "a1b2c3d4-e5f6-7890-abcd-ef0123456789",
					operator: "=",
					value: "us-west",
					valueSource: "value",
				},
			],
		},
		created_at: "2026-03-24T14:30:00Z",
		created_by: {
			id: 2,
			name: "Jane Smith",
			email: "jane.smith@example.com",
		},
	}
];
