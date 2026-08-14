import { Button } from "@/components/ui/button";
import { Wallet } from "lucide-react";
import { ArrowUpRight } from "lucide-react";

const MODEL_LIMITS_DOCS_URL = "https://docs.getbifrost.ai/features/governance";

interface ModelLimitsEmptyStateProps {
	onAddClick: () => void;
	canCreate?: boolean;
}

export function ModelLimitsEmptyState({ onAddClick, canCreate = true }: ModelLimitsEmptyStateProps) {
	return (
		<div className="flex min-h-[80vh] w-full flex-col items-center justify-center gap-4 py-16 text-center">
			<div className="text-muted-foreground">
				<Wallet className="h-[5.5rem] w-[5.5rem]" strokeWidth={1} />
			</div>
			<div className="flex flex-col gap-1">
				<h1 className="text-muted-foreground text-xl font-medium">预算和速率限制</h1>
				<div className="text-muted-foreground mx-auto mt-2 max-w-[600px] text-sm font-normal">在任何作用域设置支出上限和速率限制：虚拟密钥、用户、提供商或特定模型。</div>
				<div className="mx-auto mt-6 flex flex-row flex-wrap items-center justify-center gap-2">
					<Button
						variant="outline"
						aria-label="了解更多预算和限制（在新标签页打开）"
						data-testid="model-limits-button-read-more"
						onClick={() => {
							window.open(`${MODEL_LIMITS_DOCS_URL}?utm_source=bfd`, "_blank", "noopener,noreferrer");
						}}
					>阅读更多<ArrowUpRight className="text-muted-foreground h-3 w-3" />
					</Button>
					<Button
						aria-label="添加您的第一条限制"
						onClick={onAddClick}
						disabled={!canCreate}
						data-testid="model-limits-button-create"
					>添加限制</Button>
				</div>
			</div>
		</div>
	);
}