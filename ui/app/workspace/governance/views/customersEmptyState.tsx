import { Button } from "@/components/ui/button";
import { WalletCards } from "lucide-react";
import { ArrowUpRight } from "lucide-react";

const CUSTOMERS_DOCS_URL = "https://docs.getbifrost.ai/features/governance/virtual-keys#customers";

interface CustomersEmptyStateProps {
	onAddClick: () => void;
	canCreate?: boolean;
}

export function CustomersEmptyState({ onAddClick, canCreate = true }: CustomersEmptyStateProps) {
	return (
		<div className="flex min-h-[80vh] w-full flex-col items-center justify-center gap-4 py-16 text-center">
			<div className="text-muted-foreground">
				<WalletCards className="h-[5.5rem] w-[5.5rem]" strokeWidth={1} />
			</div>
			<div className="flex flex-col gap-1">
				<h1 className="text-muted-foreground text-xl font-medium">客户拥有自己的团队、预算和访问控制</h1>
				<div className="text-muted-foreground mx-auto mt-2 max-w-[600px] text-sm font-normal">创建客户账户以管理多租户用量、分配团队，并为每个客户设置支出和速率限制。</div>
				<div className="mx-auto mt-6 flex flex-row flex-wrap items-center justify-center gap-2">
					<Button
						variant="outline"
						aria-label="了解更多客户（在新标签页打开）"
						data-testid="customer-button-read-more"
						onClick={() => {
							window.open(`${CUSTOMERS_DOCS_URL}?utm_source=bfd`, "_blank", "noopener,noreferrer");
						}}
					>阅读更多<ArrowUpRight className="text-muted-foreground h-3 w-3" />
					</Button>
					<Button aria-label="添加您的第一个客户" onClick={onAddClick} disabled={!canCreate} data-testid="customer-button-create">添加客户</Button>
				</div>
			</div>
		</div>
	);
}