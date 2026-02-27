import { CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { ToolCase } from "lucide-react";
import ContactUsView from "../views/contactUsView";

export default function MCPToolGroups() {
	return (
		<>
			<CardHeader className="mb-4 px-0">
				<CardTitle>
					<h1 className="text-foreground text-lg font-semibold">MCP tool groups</h1>
				</CardTitle>
				<CardDescription>Configure tool groups for MCP servers to organize and govern tools.</CardDescription>
			</CardHeader>
			<div className="rounded-sm border">
				<div className="flex w-full flex-col items-center justify-center py-16">
					<ContactUsView
						className="mx-auto w-full max-w-lg"
						icon={<ToolCase className="h-[5.5rem] w-[5.5rem]" strokeWidth={1} />}
						title="Unlock MCP Tool Groups"
						description="This feature is a part of the Bifrost enterprise license. Configure tool groups for MCP servers to organize your MCP tools and govern them across your organization."
						readmeLink="https://docs.getbifrost.ai/mcp/overview"
					/>
				</div>
			</div>
		</>
	);
}
