import { usePromptContext } from "../context";
import { FolderSheet } from "../sheets/folderSheet";
import { PromptSheet } from "../sheets/promptSheet";
import { CommitVersionSheet } from "../sheets/commitVersionSheet";

export function PromptSheets() {
	const { folderSheet, setFolderSheet, promptSheet, setPromptSheet, commitSheet, setCommitSheet, setUrlState } = usePromptContext();

	return (
		<>
			<FolderSheet
				open={folderSheet.open}
				onOpenChange={(open) => setFolderSheet({ ...folderSheet, open })}
				folder={folderSheet.folder}
				onSaved={() => {}}
			/>

			<PromptSheet
				open={promptSheet.open}
				onOpenChange={(open) => setPromptSheet({ ...promptSheet, open })}
				prompt={promptSheet.prompt}
				folderId={promptSheet.folderId}
				onSaved={(newPromptId) => {
					if (newPromptId) {
						setUrlState({ promptId: newPromptId, sessionId: null, versionId: null });
					}
				}}
			/>

			{commitSheet.session && (
				<CommitVersionSheet
					open={commitSheet.open}
					onOpenChange={(open) => setCommitSheet({ ...commitSheet, open })}
					session={commitSheet.session}
					onCommitted={(versionId) => {
						setUrlState({ versionId, sessionId: null });
					}}
				/>
			)}
		</>
	);
}