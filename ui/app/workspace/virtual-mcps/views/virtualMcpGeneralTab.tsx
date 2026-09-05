import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Switch } from "@/components/ui/switch";
import { Textarea } from "@/components/ui/textarea";

// Mirrors the backend Slugify: lowercase, keep [a-z0-9], collapse other runs to a single
// hyphen, trim leading/trailing hyphens. Keep in sync with framework/configstore Slugify.
export function slugify(value: string): string {
	let out = "";
	let prevHyphen = false;
	for (const ch of value.toLowerCase().trim()) {
		if ((ch >= "a" && ch <= "z") || (ch >= "0" && ch <= "9")) {
			out += ch;
			prevHyphen = false;
		} else if (!prevHyphen) {
			out += "-";
			prevHyphen = true;
		}
	}
	return out.replace(/^-+|-+$/g, "");
}

interface VirtualMCPGeneralTabProps {
	name: string;
	setName: (value: string) => void;
	endpointSlug: string;
	setEndpointSlug: (value: string) => void;
	description: string;
	setDescription: (value: string) => void;
	enabled: boolean;
	setEnabled: (value: boolean) => void;
	isCreate: boolean;
}

export default function VirtualMCPGeneralTab({
	name,
	setName,
	endpointSlug,
	setEndpointSlug,
	description,
	setDescription,
	enabled,
	setEnabled,
	isCreate,
}: VirtualMCPGeneralTabProps) {
	// The endpoint the server will actually serve: the typed slug if present, else derived from the name.
	const previewSlug = slugify(endpointSlug.trim() || name);
	return (
		<div className="flex flex-col gap-5">
			<div className="flex flex-col gap-2">
				<Label htmlFor="vmcp-name">Name</Label>
				<Input
					id="vmcp-name"
					value={name}
					onChange={(e) => setName(e.target.value)}
					placeholder="Machine Learning Team"
					data-testid="virtual-mcp-name-input"
				/>
			</div>

			<div className="flex flex-col gap-2">
				<Label htmlFor="vmcp-slug">Endpoint slug</Label>
				<Input
					id="vmcp-slug"
					value={endpointSlug}
					onChange={(e) => setEndpointSlug(e.target.value)}
					placeholder="Leave blank to derive from the name"
					disabled={!isCreate}
					className="font-mono"
					data-testid="virtual-mcp-slug-input"
				/>
				<p className="text-muted-foreground text-xs">
					{isCreate
						? "The URL-safe path this Virtual MCP is served at. Immutable after creation."
						: "The endpoint slug cannot be changed after creation."}
				</p>
				{isCreate && previewSlug && (
					<p className="text-muted-foreground text-xs" data-testid="virtual-mcp-slug-preview">
						Served at <span className="text-foreground font-mono">/mcp/{previewSlug}</span>
					</p>
				)}
			</div>

			<div className="flex flex-col gap-2">
				<Label htmlFor="vmcp-description">Description</Label>
				<Textarea
					id="vmcp-description"
					value={description}
					onChange={(e) => setDescription(e.target.value)}
					placeholder="What this Virtual MCP is for (optional)"
					rows={3}
					data-testid="virtual-mcp-description-input"
				/>
			</div>

			<div className="flex items-center justify-between rounded-md border p-3">
				<div className="flex flex-col gap-0.5">
					<Label htmlFor="vmcp-enabled">Enabled</Label>
					<p className="text-muted-foreground text-xs">When disabled, the endpoint stops serving and is not resolved for any key.</p>
				</div>
				<Switch id="vmcp-enabled" checked={enabled} onCheckedChange={setEnabled} data-testid="virtual-mcp-enabled-switch" />
			</div>
		</div>
	);
}