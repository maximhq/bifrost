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

/**
 * How the endpoint signs in to the upstream: the auth type and its one credential. Laid out like the
 * provider key form; the AWS fields follow the Bedrock key's authentication tabs.
 */
export function CredentialFields({ form, disabled }: { form: UseFormReturn<RelayFormValues>; disabled?: boolean }) {
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
							label="Authentication"
							info="How the gateway authenticates to the upstream. Callers never send this credential; they authenticate to Bifrost with a virtual key."
						/>
						<Select onValueChange={field.onChange} value={field.value} disabled={disabled}>
							<FormControl>
								<SelectTrigger className="w-60" data-testid="relay-auth-type">
									<SelectValue />
								</SelectTrigger>
							</FormControl>
							<SelectContent>
								{RelayAuthTypes.map((t) => (
									<SelectItem key={t} value={t}>
										{RelayAuthTypeLabels[t]}
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
								<InfoLabel label="Header Name (Required)" info="The request header that carries the credential, such as X-API-Key." />
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
									label="Value Prefix (Optional)"
									info="Text put before the credential in the header, such as Token followed by a space."
								/>
								<FormControl>
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
							<InfoLabel label="Query Parameter (Required)" info="The query parameter that carries the credential, such as api_key." />
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
								label={authType === "basic" ? "Credentials (Required)" : "API Key (Required)"}
								info="Stored encrypted and never sent by callers; the gateway adds it to every relayed request."
							/>
							<FormControl>
								<SecretVarInput
									placeholder={authType === "basic" ? "user:password or env.API_CREDENTIALS" : "API Key or env.API_KEY"}
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
								<InfoLabel label="AWS Service (Required)" info="The SigV4 signing name, such as textract, translate or s3." />
								<FormControl>
									<Input placeholder="textract" data-testid="relay-aws-service" disabled={disabled} {...field} value={field.value ?? ""} />
								</FormControl>
								<FormMessage />
							</FormItem>
						)}
					/>
					<div className="space-y-2">
						<InfoLabel
							label="Authentication Method"
							info="IAM Role uses the credentials of the environment Bifrost runs in (instance role, IRSA, env vars). Explicit uses the keys entered here."
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
									IAM Role (Inherited)
								</TabsTrigger>
								<TabsTrigger value="explicit" data-testid="relay-aws-explicit-tab">
									Explicit Credentials
								</TabsTrigger>
							</TabsList>
						</Tabs>
						{awsMode === "iam_role" && (
							<p className="text-muted-foreground text-sm">Uses IAM roles attached to your environment (EC2, Lambda, ECS, EKS).</p>
						)}
					</div>
					{awsMode === "explicit" &&
						(
							[
								["access_key", "Access Key (Required)", "your-aws-access-key or env.AWS_ACCESS_KEY_ID"],
								["secret_key", "Secret Key (Required)", "your-aws-secret-key or env.AWS_SECRET_ACCESS_KEY"],
								["session_token", "Session Token (Optional)", "your-aws-session-token or env.AWS_SESSION_TOKEN"],
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
								<InfoLabel label="Region (Required)" info="The AWS region requests are signed for, such as us-east-1." />
								<FormControl>
									<SecretVarInput
										placeholder="us-east-1 or env.AWS_REGION"
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
									label="Assume Role ARN (Optional)"
									info="Assume an IAM role before requests. Works with both explicit credentials and inherited IAM (EC2, ECS, EKS)."
								/>
								<FormControl>
									<SecretVarInput
										placeholder="arn:aws:iam::123456789:role/MyRole or env.AWS_ROLE_ARN"
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
									label="External ID (Optional)"
									info={<>Required by the role&apos;s trust policy when using cross-account access</>}
								/>
								<FormControl>
									<SecretVarInput placeholder="external-id or env.AWS_EXTERNAL_ID" disabled={disabled} {...field} value={field.value} />
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