import { AlertCircle } from "lucide-react";
import { useQueryState } from "nuqs";

export default function MCPSessionsAuthFailedPage() {
	const [error] = useQueryState("error");
	return (
		<div className="mx-auto flex min-h-[60vh] w-full max-w-xl items-center justify-center p-6">
			<div className="bg-card w-full rounded-sm border p-8 text-center shadow-sm">
				<div className="bg-destructive/10 mx-auto mb-5 flex size-12 items-center justify-center rounded-full">
					<AlertCircle className="text-destructive size-6" />
				</div>
				<h1 className="text-xl font-semibold tracking-tight">认证失败</h1>
				<p className="text-muted-foreground mt-2 text-sm">{error ?? "We couldn't complete the authentication flow."}</p>
				<p className="text-muted-foreground mt-4 text-sm">您可以关闭此标签页，并从 MCP 客户端重试原始请求以生成新的认证链接。</p>
			</div>
		</div>
	);
}