import { Tooltip, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip";
import { useAppSelector } from "@/lib/store";
import { cn } from "@/lib/utils";

const GuardrailProviders = [
	{
		id: "bedrock",
		name: "AWS Bedrock",
		description: "",
	},
	{
		id: "azure",
		name: "Azure",
		description: "Azure guardrail provider",
	},
	{
		id: "mistral",
		name: "Mistral moderation",
		description: "",
	},
	{
		id: "mistral",
		name: "Mistral moderation",
		description: "",
	},
	{
		id: "pangea",
		name: "Pangea",
		description: "",
	},
	{
		id: "patronus-ai",
		name: "Patronus AI",
		description: "",
	},
	{
		id: "Pillar",
		name: "Pillar",
		description: "",
	},
];

export default function Guardrails() {
    const selectedProvider = useAppSelector((state) => state.provider.selectedProvider);


	return (
		<div className="flex h-full flex-row gap-4">
			<div className="flex flex-col">
				<TooltipProvider>
					<div className="flex w-[250px] flex-col gap-2 pb-10">
						<div className="rounded-md bg-zinc-50/50 p-4 dark:bg-zinc-800/20">
							{/* Standard Providers */}
							<div className="mb-4">
								<div className="text-muted-foreground mb-2 text-xs font-medium">Guardrail Providers</div>
								{GuardrailProviders.map((p) => (
									<Tooltip key={p.name}>
										<TooltipTrigger
											className={cn(
												"mb-1 flex w-full items-center gap-2 rounded-sm border px-3 py-1.5 text-sm",
												selectedProvider?.name === p.name
													? "bg-secondary opacity-100 hover:opacity-100"
													: "hover:bg-secondary cursor-pointer border-transparent opacity-100 hover:border",
											)}
											onClick={(e) => {
												e.preventDefault();
												e.stopPropagation();
											}}
											asChild
										>
											<span></span>
										</TooltipTrigger>
									</Tooltip>
								))}
							</div>
						</div>
					</div>
				</TooltipProvider>
			</div>
		</div>
	);
}
