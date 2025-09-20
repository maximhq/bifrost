import { Input } from "@/components/ui/input";
import { OtelFormSchema } from "@/lib/types/schemas";
import { OtelFormFragment } from "../fragments/otelFormFragment";

export default function OtelView() {
	const baseUrl = `${window.location.protocol}//${window.location.host}`;

	const handleOtelConfigSave = (config: OtelFormSchema) => {
		console.log("Saving OTEL config:", config);
	};

	return (
		<div className="flex w-full flex-col gap-4">
			<div className="border-secondary flex w-full flex-col gap-2 rounded-sm border p-4">
				<div className="text-muted-foreground mb-2 text-xs font-medium">Metrics (scraping endpoint)</div>
				<Input className="bg-accent mb-2 font-mono" value={`${baseUrl}/metrics`} readOnly showCopyButton />
			</div>
			<div className="border-secondary flex w-full flex-col gap-3 rounded-sm border px-4 py-2">
				<div className="text-muted-foreground mb-2 text-xs font-medium">Traces Configuration</div>
				<OtelFormFragment
					onSave={handleOtelConfigSave}
					initialConfig={{
						push_url: "",
						trace_type: "traditional",
					}}
				/>
			</div>
		</div>
	);
}
