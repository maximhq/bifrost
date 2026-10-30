import { FormControl, FormField, FormItem, FormLabel, FormMessage } from "@/components/ui/form";
import { Input } from "@/components/ui/input";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { SecretVarInput } from "@/components/ui/secretVarInput";
import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { RelayAuthTypeLabels, RelayAuthTypes } from "@/lib/types/relays";
import type { RelayFormValues } from "@/lib/types/schemas";
import { useEffect, useState } from "react";
import type { UseFormReturn } from "react-hook-form";
import { InfoLabel } from "../fragments/infoLabel";
import { useTranslation } from "react-i18next";

/**
 * How the endpoint signs in to the upstream: the auth type and its one credential. Laid out like the
 * provider key form; the AWS fields follow the Bedrock key's authentication tabs.
 */
export function CredentialFields({ form, disabled }: { form: UseFormReturn<RelayFormValues>; disabled?: boolean }) {
	const { t } = useTranslation("config");

	const authType = form.watch("auth.type");
	const hasExplicitAWS = !!(form.getValues("aws.access_key")?.value || form.getValues("aws.access_key")?.ref);
	const [awsMode, setAwsMode] = useState<"iam_role" | "explicit">(hasExplicitAWS ? "explicit" : "iam_role");
	useEffect(() => {
		if (hasExplicitAWS) setAwsMode("explicit");
	}, [hasExplicitAWS]);

	return (
		<div className="space-y-4">
			<FormField
				control={form.control}
				name="auth.type"
				render={({ field }) => (
					<FormItem>
						<InfoLabel
							label={t("relays.credentials.authLabel")}
							info={t("relays.credentials.authInfo")}
						/>
						<Select onValueChange={field.onChange} value={field.value} disabled={disabled}>
							<FormControl>
								<SelectTrigger className="w-60" data-testid="relay-auth-type">
									<SelectValue />
								</SelectTrigger>
							</FormControl>
							<SelectContent>
								{RelayAuthTypes.map((authKind) => (
										<SelectItem key={authKind} value={authKind}>
											{RelayAuthTypeLabels[authKind]}
									</SelectItem>
								))}
							</SelectContent>
						</Select>
						<FormMessage />
					</FormItem>
				)}
			/>

			{authType === "header" && (
				<div className="grid grid-cols-1 gap-4 md:grid-cols-2">
					<FormField
						control={form.control}
						name="auth.header_name"
						render={({ field }) => (
							<FormItem>
								<InfoLabel label={t("relays.credentials.headerNameLabel")} info={t("relays.credentials.headerNameInfo")} />
								<FormControl>
									<Input placeholder="x-api-key" data-testid="relay-header-name" disabled={disabled} {...field} value={field.value ?? ""} />
								</FormControl>
								<FormMessage />
							</FormItem>
						)}
					/>
					<FormField
						control={form.control}
						name="auth.value_prefix"
						render={({ field }) => (
							<FormItem>
								<InfoLabel
									label={t("relays.credentials.valuePrefixLabel")}
									info={t("relays.credentials.valuePrefixInfo")}
								/>
								<FormControl>
									{/* i18n-ignore: sample prefix */}
									<Input placeholder="Token " disabled={disabled} {...field} value={field.value ?? ""} />
								</FormControl>
								<FormMessage />
							</FormItem>
						)}
					/>
				</div>
			)}
			{authType === "query" && (
				<FormField
					control={form.control}
					name="auth.query_param"
					render={({ field }) => (
						<FormItem>
							<InfoLabel label={t("relays.credentials.queryParamLabel")} info={t("relays.credentials.queryParamInfo")} />
							<FormControl>
								<Input placeholder="api_key" data-testid="relay-query-param" disabled={disabled} {...field} value={field.value ?? ""} />
							</FormControl>
							<FormMessage />
						</FormItem>
					)}
				/>
			)}

			{authType !== "none" && authType !== "aws_sigv4" && (
				<FormField
					control={form.control}
					name="credential_value"
					render={({ field }) => (
						<FormItem>
							<InfoLabel
								label={authType === "basic" ? t("relays.credentials.credentialsLabel") : t("relays.credentials.apiKeyLabel")}
								info={t("relays.credentials.credentialInfo")}
							/>
							<FormControl>
								<SecretVarInput
									placeholder={authType === "basic" ? t("relays.credentials.basicPlaceholder") : t("relays.credentials.apiKeyPlaceholder")}
									data-testid="relay-credential"
									disabled={disabled}
									{...field}
									value={field.value}
								/>
							</FormControl>
							<FormMessage />
						</FormItem>
					)}
				/>
			)}

			{authType === "aws_sigv4" && (
				<>
					<FormField
						control={form.control}
						name="auth.aws.service"
						render={({ field }) => (
							<FormItem>
								<InfoLabel label={t("relays.credentials.awsServiceLabel")} info={t("relays.credentials.awsServiceInfo")} />
								<FormControl>
									<Input placeholder="textract" data-testid="relay-aws-service" disabled={disabled} {...field} value={field.value ?? ""} />
								</FormControl>
								<FormMessage />
							</FormItem>
						)}
					/>
					<div className="space-y-2">
						<InfoLabel
							label={t("relays.credentials.awsMethodLabel")}
							info={t("relays.credentials.awsMethodInfo")}
						/>
						<Tabs
							value={awsMode}
							onValueChange={(v) => {
								setAwsMode(v as "iam_role" | "explicit");
								if (v === "iam_role") {
									form.setValue("aws.access_key", undefined, { shouldDirty: true });
									form.setValue("aws.secret_key", undefined, { shouldDirty: true });
									form.setValue("aws.session_token", undefined, { shouldDirty: true });
								}
							}}
						>
							<TabsList className="flex w-full justify-start">
								<TabsTrigger value="iam_role" data-testid="relay-aws-iam-role-tab">
									
										{t("relays.credentials.iamRoleTab")}

								</TabsTrigger>
								<TabsTrigger value="explicit" data-testid="relay-aws-explicit-tab">
									
										{t("relays.credentials.explicitTab")}

								</TabsTrigger>
							</TabsList>
						</Tabs>
						{awsMode === "iam_role" && (
							<p className="text-muted-foreground text-sm">{t("relays.credentials.iamRoleInfo")}</p>
						)}
					</div>
					{awsMode === "explicit" &&
						(
							[
								["access_key", t("relays.credentials.accessKeyLabel"), t("relays.credentials.accessKeyPlaceholder")],
								["secret_key", t("relays.credentials.secretKeyLabel"), t("relays.credentials.secretKeyPlaceholder")],
								["session_token", t("relays.credentials.sessionTokenLabel"), t("relays.credentials.sessionTokenPlaceholder")],
							] as const
						).map(([key, label, placeholder]) => (
							<FormField
								key={key}
								control={form.control}
								name={`aws.${key}`}
								render={({ field }) => (
									<FormItem>
										<FormLabel>{label}</FormLabel>
										<FormControl>
											<SecretVarInput
												placeholder={placeholder}
												data-testid={`relay-aws-${key}`}
												disabled={disabled}
												{...field}
												value={field.value}
											/>
										</FormControl>
										<FormMessage />
									</FormItem>
								)}
							/>
						))}
					<FormField
						control={form.control}
						name="aws.region"
						render={({ field }) => (
							<FormItem>
								<InfoLabel label={t("relays.credentials.regionLabel")} info={t("relays.credentials.regionInfo")} />
								<FormControl>
									<SecretVarInput
										placeholder={t("relays.credentials.regionPlaceholder")}
										data-testid="relay-aws-region"
										disabled={disabled}
										{...field}
										value={field.value}
									/>
								</FormControl>
								<FormMessage />
							</FormItem>
						)}
					/>
					<FormField
						control={form.control}
						name="aws.role_arn"
						render={({ field }) => (
							<FormItem>
								<InfoLabel
									label={t("relays.credentials.roleArnLabel")}
									info={t("relays.credentials.roleArnInfo")}
								/>
								<FormControl>
									<SecretVarInput
										placeholder={t("relays.credentials.roleArnPlaceholder")}
										disabled={disabled}
										{...field}
										value={field.value}
									/>
								</FormControl>
								<FormMessage />
							</FormItem>
						)}
					/>
					<FormField
						control={form.control}
						name="aws.external_id"
						render={({ field }) => (
							<FormItem>
								<InfoLabel
									label={t("relays.credentials.externalIdLabel")}
									info={t("relays.credentials.externalIdInfo")}
								/>
								<FormControl>
									<SecretVarInput placeholder={t("relays.credentials.externalIdPlaceholder")} disabled={disabled} {...field} value={field.value} />
								</FormControl>
								<FormMessage />
							</FormItem>
						)}
					/>
				</>
			)}
		</div>
	);
}