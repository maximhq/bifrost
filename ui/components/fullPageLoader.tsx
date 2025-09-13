import { Loader2 } from "lucide-react";

function FullPageLoader() {
	return (
		<div className="h-base pb-1/2 flex items-center justify-center">
			<Loader2 className="h-4 w-4 animate-spin" />
		</div>
	);
}

export default FullPageLoader;
