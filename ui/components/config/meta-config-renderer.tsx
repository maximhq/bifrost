import React from "react";
import { CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Textarea } from "@/components/ui/textarea";
import { Info, PickaxeIcon } from "lucide-react";
import { MetaConfig } from "@/lib/types/config";
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip";

type FieldType = "text" | "textarea";

interface MetaField {
	name: keyof MetaConfig;
	label: string;
	type: FieldType;
	placeholder?: string;
	isJson?: boolean;
}

const providerMetaFields: Record<string, { title: string; fields: MetaField[] }> = {
	azure: {
		title: "Azure OpenAI Meta Config",
		fields: [
			{
				name: "endpoint",
				label: "Endpoint",
				type: "text",
				placeholder: "https://your-resource.openai.azure.com or env.AZURE_ENDPOINT",
			},
			{
				name: "api_version",
				label: "API Version (Optional)",
				type: "text",
				placeholder: "YYYY-MM-DD or env.AZURE_VERSION",
			},
			{
				name: "deployments",
				label: "Deployments (JSON format)",
				type: "textarea",
				placeholder: '{ "gpt-4": "my-deployment" }',
				isJson: true,
			},
		],
	},
	bedrock: {
		title: "AWS Bedrock Meta Config",
		fields: [
			{
				name: "region",
				label: "Region",
				type: "text",
				placeholder: "us-east-1 or env.AWS_REGION",
			},
		],
	},
	vertex: {
		title: "Google Vertex AI Meta Config",
		fields: [
			{
				name: "project_id",
				label: "Project ID",
				type: "text",
				placeholder: "gcp-project-id or env.GCP_PROJECT",
			},
			{
				name: "region",
				label: "Region",
				type: "text",
				placeholder: "us-central1 or env.GCP_REGION",
			},
			{
				name: "auth_credentials",
				label: "Auth Credentials (JSON key)",
				type: "textarea",
				placeholder: "JSON key or env.GCP_CREDS",
			},
		],
	},
};

interface MetaConfigRendererProps {
	provider: string;
	metaConfig: MetaConfig;
	// eslint-disable-next-line @typescript-eslint/no-explicit-any
	onMetaConfigChange: (key: keyof MetaConfig, value: any) => void;
}

const MetaConfigRenderer: React.FC<MetaConfigRendererProps> = ({ provider, metaConfig, onMetaConfigChange }) => {
	const config = providerMetaFields[provider];

	if (!config) {
		return null;
	}

	const renderField = (field: MetaField) => {
		const value = metaConfig[field.name];

		if (field.type === "textarea") {
			return (
				<Textarea
					placeholder={field.placeholder}
					value={field.isJson ? (typeof value === "string" ? value : JSON.stringify(value, null, 2)) : (value as string) || ""}
					onChange={(e) => {
						onMetaConfigChange(field.name, e.target.value);
					}}
					onBlur={(e) => {
						if (field.isJson) {
							try {
								const parsed = JSON.parse(e.target.value);
								onMetaConfigChange(field.name, parsed);
							} catch {
								// Ignore parsing errors, keep it as string
							}
						}
					}}
					rows={4}
					className="max-w-full font-mono text-sm wrap-anywhere"
				/>
			);
		}

		return (
			<Input
				placeholder={field.placeholder}
				value={(value as string) || ""}
				onChange={(e) => onMetaConfigChange(field.name, e.target.value)}
			/>
		);
	};

	return (
		<div className="">
			<CardHeader className="mb-2 px-0">
				<CardTitle className="flex items-center gap-2 text-base">
					<PickaxeIcon className="h-4 w-4" />
					{config.title}
					<TooltipProvider>
						<Tooltip>
							<TooltipTrigger asChild>
								<span>
									<Info className="text-muted-foreground ml-1 h-3 w-3" />
								</span>
							</TooltipTrigger>
							<TooltipContent className="max-w-fit">
								<p>
									Use <code className="rounded bg-neutral-100 px-1 py-0.5 text-neutral-800">env.&lt;VAR&gt;</code> to read the value from an
									environment variable.
								</p>
							</TooltipContent>
						</Tooltip>
					</TooltipProvider>
				</CardTitle>
			</CardHeader>
			<CardContent className="space-y-4 px-0">
				{config.fields.map((field) => (
					<div key={field.name}>
						<label className="block text-sm font-medium">{field.label}</label>
						{renderField(field)}
					</div>
				))}
			</CardContent>
		</div>
	);
};

export default MetaConfigRenderer;
