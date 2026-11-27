import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { parseAsString, useQueryStates } from "nuqs";
import { RunCompare } from "./views/runCompare";
import { RunReport } from "./views/runReport";
import { RunsList } from "./views/runsList";

// TestRunsPage is the test runs screen. The URL says what it shows: one run's report
// (`id`), a comparison of runs (`compare`, IDs separated by commas), or the list.
export default function TestRunsPage() {
	const canCreate = useRbac(RbacResource.Evaluators, RbacOperation.Create);
	const canUpdate = useRbac(RbacResource.Evaluators, RbacOperation.Update);
	const canDelete = useRbac(RbacResource.Evaluators, RbacOperation.Delete);
	const [urlState, setUrlState] = useQueryStates(
		{ id: parseAsString.withDefault(""), compare: parseAsString.withDefault("") },
		{ history: "push" },
	);

	if (urlState.id) {
		return (
			<RunReport
				key={urlState.id}
				id={urlState.id}
				canUpdate={canUpdate}
				canDelete={canDelete}
				onDeleted={() => void setUrlState({ id: null, compare: null })}
			/>
		);
	}
	if (urlState.compare) {
		return <RunCompare ids={urlState.compare.split(",").filter(Boolean)} />;
	}
	return (
		<RunsList
			canCreate={canCreate}
			canUpdate={canUpdate}
			canDelete={canDelete}
			onOpen={(id) => void setUrlState({ id, compare: null })}
			onCompare={(ids) => void setUrlState({ id: null, compare: ids.join(",") })}
		/>
	);
}