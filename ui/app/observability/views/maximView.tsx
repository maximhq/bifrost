import { MaximFormSchema } from "@/lib/types/schemas";
import { MaximFormFragment } from "../fragments/maximFormFragment";

export default function MaximView() {
	const handleMaximConfigSave = (config: MaximFormSchema) => {
		console.log("Saving Maxim config:", config);
	};

	return (
		<div className="flex w-full flex-col gap-4">
			<div className="border-secondary flex w-full flex-col gap-2 rounded-sm border p-4">
				<div className="text-muted-foreground text-xs font-medium">Configuration</div>
				<div className="text-muted-foreground mb-2 text-xs font-normal">
					You can send in header <code>x-bf-log-repo-id</code> with a repository ID to log to a specific repository.
				</div>
				<MaximFormFragment
					onSave={handleMaximConfigSave}
					showRestartAlert={true}
					initialConfig={{
						api_key: "",
						log_repo_id: "",
					}}
				/>
			</div>
		</div>
	);
}
