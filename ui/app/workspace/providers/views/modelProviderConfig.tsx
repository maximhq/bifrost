import { Button } from "@/components/ui/button";
import { ModelProvider } from "@/lib/types/config";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { SettingsIcon, Trash } from "lucide-react";
import { useMemo, useState } from "react";
import ProviderConfigSheet from "../dialogs/providerConfigSheet";
import ModelProviderKeysTableView from "./modelProviderKeysTableView";
import ProviderGovernanceTable from "./providerGovernanceTable";
import { keysRequired } from "./utils";

interface Props {
	provider: ModelProvider;
	onRequestDelete?: () => void;
}

export default function ModelProviderConfig({ provider, onRequestDelete }: Props) {
	const [showConfigSheet, setShowConfigSheet] = useState(false);
	const hasGovernanceAccess = useRbac(RbacResource.Governance, RbacOperation.View);
	const hasDeleteProviderAccess = useRbac(RbacResource.ModelProvider, RbacOperation.Delete);
	const isCustomProvider = !!provider.custom_provider_config;

	const showApiKeys = useMemo(() => {
		if (provider.custom_provider_config) {
			return !(provider.custom_provider_config?.is_key_less ?? false);
		}
		return keysRequired(provider.name);
	}, [provider.name, provider.custom_provider_config?.is_key_less]);

	const editConfigButton = (
		<div className="flex items-center gap-2">
			{isCustomProvider && onRequestDelete && hasDeleteProviderAccess && (
				<Button variant="outline" onClick={onRequestDelete} className="text-destructive hover:bg-destructive/10 hover:text-destructive" aria-label="Delete provider" data-testid="provider-delete-btn">
					<Trash className="h-4 w-4" />
				</Button>
			)}
			<Button variant="outline" onClick={() => setShowConfigSheet(true)}>
				<SettingsIcon className="h-4 w-4" />
				Edit Provider Config
			</Button>
		</div>
	);

	return (
		<div className="flex w-full flex-col gap-2">
			<ProviderConfigSheet show={showConfigSheet} onCancel={() => setShowConfigSheet(false)} provider={provider} />
			<ModelProviderKeysTableView provider={provider} headerActions={editConfigButton} isKeyless={!showApiKeys} providerName={provider.name} />
			{hasGovernanceAccess  ? <ProviderGovernanceTable className="mt-4" provider={provider} /> : null}
		</div>
	);
}
