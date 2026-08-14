import { Button } from "@/components/ui/button";
import { KeyRound } from "lucide-react";
import { ArrowUpRight } from "lucide-react";

const VIRTUAL_KEYS_DOCS_URL = "https://docs.getbifrost.ai/features/governance/virtual-keys";

interface VirtualKeysEmptyStateProps {
	onAddClick: () => void;
	canCreate?: boolean;
}

export function VirtualKeysEmptyState({ onAddClick, canCreate = true }: VirtualKeysEmptyStateProps) {
	return (
		<div
			className="flex min-h-[80vh] w-full flex-col items-center justify-center gap-4 py-16 text-center"
			data-testid="virtual-keys-empty-state"
		>
			<div className="text-muted-foreground">
				<KeyRound className="h-[5.5rem] w-[5.5rem]" strokeWidth={1} />
			</div>
			<div className="flex flex-col gap-1">
				<h1 className="text-muted-foreground text-xl font-medium">虚拟密钥控制访问、预算和速率限制</h1>
				<div className="text-muted-foreground mx-auto mt-2 max-w-[600px] text-sm font-normal">创建虚拟密钥，为团队、客户或 API 客户端分配权限、支出限额和使用配额。</div>
				<div className="mx-auto mt-6 flex flex-row flex-wrap items-center justify-center gap-2">
					<Button
						variant="outline"
						aria-label="了解更多虚拟密钥（在新标签页打开）"
						data-testid="virtual-keys-button-read-more"
						onClick={() => {
							window.open(`${VIRTUAL_KEYS_DOCS_URL}?utm_source=bfd`, "_blank", "noopener,noreferrer");
						}}
					>阅读更多<ArrowUpRight className="text-muted-foreground h-3 w-3" />
					</Button>
					<Button aria-label="添加您的第一个虚拟密钥" onClick={onAddClick} disabled={!canCreate} data-testid="create-vk-btn">添加虚拟密钥</Button>
				</div>
			</div>
		</div>
	);
}