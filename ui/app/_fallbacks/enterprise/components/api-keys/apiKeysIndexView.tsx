import { Alert, AlertDescription } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { useGetCoreConfigQuery } from "@/lib/store";
import { useCopyToClipboard } from "@/hooks/useCopyToClipboard";
import { Link } from "@tanstack/react-router";
import { Copy, InfoIcon, KeyRound } from "lucide-react";
import { useMemo } from "react";
import { useTranslation } from "react-i18next";
import ContactUsView from "../views/contactUsView";

export default function APIKeysView() {
	const { t } = useTranslation();
	const { data: bifrostConfig, isLoading } = useGetCoreConfigQuery({ fromDB: true });
	const isAuthConfigure = useMemo(() => {
		return bifrostConfig?.auth_config?.is_enabled;
	}, [bifrostConfig]);

	const curlExample = `# Base64 encode your username:password
# Example: echo -n "username:password" | base64
curl --location 'http://localhost:8080/v1/chat/completions'
--header 'Content-Type: application/json' 
--header 'Accept: application/json' 
--header 'Authorization: Basic <base64_encoded_username:password>' 
--data '{ 
  "model": "openai/gpt-4", 
  "messages": [ 
    { 
      "role": "user", 
      "content": "explain big bang?" 
    } 
  ] 
}'`;

	const { copy: copyToClipboard } = useCopyToClipboard();

	if (isLoading) {
		return <div>{t("common.loading")}</div>;
	}
	if (!isAuthConfigure) {
		return (
			<Alert variant="default">
				<InfoIcon className="text-muted h-4 w-4" />
				<AlertDescription>
					<p className="text-md text-muted-foreground">
						{t("workspace.config.apiKeys.authRequiredPrefix")}{" "}
						<Link to="/workspace/config/security" className="text-md text-primary underline">
							{t("workspace.config.apiKeys.configureSecuritySettings")}
						</Link>
						.<br />
						<br />
						{t("workspace.config.apiKeys.authRequiredSuffix")}
					</p>
				</AlertDescription>
			</Alert>
		);
	}

	const isInferenceAuthDisabled = bifrostConfig?.auth_config?.disable_auth_on_inference ?? false;

	return (
		<div className="mx-auto w-full max-w-4xl space-y-4">
			<Alert variant="default">
				<InfoIcon className="text-muted h-4 w-4" />
				<AlertDescription>
					<p className="text-md text-muted-foreground">
						{isInferenceAuthDisabled ? (
							<>
								{t("workspace.config.apiKeys.inferenceAuthDisabledPrefix")}{" "}
								<strong>{t("workspace.config.apiKeys.disabledForInferenceCalls")}</strong>.{" "}
								{t("workspace.config.apiKeys.inferenceAuthDisabledSuffix")}{" "}
								<code className="bg-muted rounded px-1 py-0.5 text-sm">username:password</code>{" "}
								{t("workspace.config.apiKeys.basicAuthEncodingSuffix")}
							</>
						) : (
							<>
								{t("workspace.config.apiKeys.basicAuthNoticePrefix")}{" "}
								<code className="bg-muted rounded px-1 py-0.5 text-sm">username:password</code>{" "}
								{t("workspace.config.apiKeys.basicAuthEncodingSuffix")}
							</>
						)}
					</p>
					{!isInferenceAuthDisabled && (
						<>
							<br />
							<p className="text-md text-muted-foreground">
								<strong>{t("workspace.config.apiKeys.example")}</strong>
							</p>

							<div className="relative mt-2 w-full min-w-0 overflow-x-auto">
								<Button variant="ghost" size="sm" onClick={() => copyToClipboard(curlExample)} className="absolute top-2 right-2 z-10 h-8">
									<Copy className="h-4 w-4" />
								</Button>
								<pre className="bg-muted min-w-max rounded p-3 pr-12 font-mono text-sm whitespace-pre">{curlExample}</pre>
							</div>
						</>
					)}
				</AlertDescription>
			</Alert>

			<ContactUsView
				className="mt-4 rounded-md border px-3 py-8"
				icon={<KeyRound size={48} />}
				title={t("workspace.config.apiKeys.scopeBasedApiKeys")}
				description={t("workspace.config.apiKeys.scopeBasedApiKeysDesc")}
				readmeLink="https://docs.getbifrost.io/enterprise/api-keys"
			/>
		</div>
	);
}