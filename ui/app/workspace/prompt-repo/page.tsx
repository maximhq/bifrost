import { cn } from "@/lib/utils";
import { FolderGit } from "lucide-react";

export default function PromptRepoPage() {
	return (
		<div className={cn("flex flex-col items-center justify-center gap-4 text-center mx-auto w-full max-w-7xl min-h-[calc(100vh-130px)]")}>
			<div className="text-muted-foreground">
				<FolderGit className="h-10 w-10" />
			</div>
			<div className="flex flex-col gap-1">
				<h1 className="text-muted-foreground text-xl font-medium">Prompt repository is coming soon</h1>
				<div className="text-muted-foreground mt-2 max-w-[600px] text-sm font-normal">
				This feature will allow you to manage and version your prompts. Please check back soon for updates.
				</div>
			</div>
		</div>
	);
}
