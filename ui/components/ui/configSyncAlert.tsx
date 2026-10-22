import { Info } from "lucide-react";
import { Trans, useTranslation } from "react-i18next";

import { Alert, AlertDescription } from "@/components/ui/alert";

interface ConfigSyncAlertProps {
	className?: string;
}

export function ConfigSyncAlert({ className }: ConfigSyncAlertProps) {
	const { t } = useTranslation("common");
	return (
		<Alert variant="info" className={className}>
			<Info className="h-4 w-4" />
			<AlertDescription>
				<p>
					<Trans
						t={t}
						i18nKey="configSyncAlert.message"
						components={{ code0: <code className="bg-muted rounded px-1 py-0.5 font-mono text-xs" /> }}
					/>
				</p>
			</AlertDescription>
		</Alert>
	);
}