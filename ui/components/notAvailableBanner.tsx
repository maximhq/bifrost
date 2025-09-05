import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { Database } from "lucide-react";
import Link from "next/link";

const NotAvailableBanner = () => {
	return (
		<div className="h-base flex items-center justify-center p-4">
			<div className="w-full max-w-md">
				<Alert className="border-destructive/50 text-destructive/50 dark:text-destructive/70 dark:border-destructive/70 [&>svg]:text-destructive dark:bg-card bg-red-50">
					<AlertTitle className="flex items-center gap-2">
						<Database className="dark:text-destructive/70 text-destructive/50 h-4 w-4" />
						Config store setup is missing.
					</AlertTitle>
					<AlertDescription className="mt-2 space-y-2 text-xs">
						<div>The UI requires a database connection to store configuration data, but no database is currently configured.</div>
						<div className="text-muted-foreground">
							To enable the UI, please add the database settings to your config.json (see{" "}
							<Link
								href="https://www.getmaxim.ai/bifrost/docs/#two-configuration-modes"
								target="_blank"
								rel="noopener noreferrer"
								className="font-medium underline underline-offset-2"
							>
								documentation
							</Link>
							).
						</div>
					</AlertDescription>
				</Alert>
			</div>
		</div>
	);
};

export default NotAvailableBanner;
