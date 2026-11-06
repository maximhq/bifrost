// Read-only view of the credential an MCP server holds on its own behalf,
// rendered on the server view sheet's Overview tab. Everything comes from
// MCPClient.credential and the client config: nothing here is editable, and
// no secret material is ever present (the refresh token as presence only,
// header values by name).

import { RefreshTokenStatus } from "@/components/refreshTokenStatus";
import { ScopeChips } from "@/components/scopeChips";
import { Badge } from "@/components/ui/badge";
import { MCP_CREDENTIAL_STATUS_COLORS } from "@/lib/constants/config";
import { MCPClient, MCPClientCredential } from "@/lib/types/mcp";
import {
	formatAbsoluteDate,
	formatAbsoluteDateTime,
	formatRelativePast,
	formatTokenExpiry,
	missingHeaderKeys,
	shouldSuggestReplacementClient,
} from "@/lib/utils/mcpCredential";
import { titleCaseFromSnakeCase } from "@/lib/utils/strings";
import { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import i18n from "@/lib/i18n";
import { SectionHeader } from "./sectionHeader";

interface Props {
	mcpClient: MCPClient;
}

/**
 * MCPClientCredentialSection renders the credential block for the server's
 * auth type: the shared token (oauth), the retained admin token
 * (per_user_oauth, token_exchange), or the retained admin header values
 * (per_user_headers). Renders nothing for auth types without a self-held
 * credential.
 */
export function MCPClientCredentialSection({ mcpClient }: Props) {
	switch (mcpClient.config.auth_type) {
		case "oauth":
		case "per_user_oauth":
		case "token_exchange":
			return <OAuthCredentialBlock mcpClient={mcpClient} />;
		case "per_user_headers":
			return <HeaderCredentialBlock mcpClient={mcpClient} />;
		default:
			return null;
	}
}

function OAuthCredentialBlock({ mcpClient }: Props) {
	const { t } = useTranslation("mcp");
	const copy = oauthCredentialCopy(mcpClient.config.auth_type);
	const credential = mcpClient.credential?.kind === "oauth" ? mcpClient.credential : undefined;

	return (
		<div className="space-y-4" data-testid="mcpclient-credential-section">
			<SectionHeader title={copy.title} description={copy.description} testId="mcpclient-credential-heading" />
			{!credential ? (
				<EmptyCredential>{copy.empty}</EmptyCredential>
			) : (
				<DefinitionList>
					<DefinitionRow label={t("common.status")}>
						<CredentialStatusBadge status={credential.status} />
						{credential.status === "needs_reauth" && <Hint>{copy.needsReauth}</Hint>}
						{credential.status === "needs_reauth" &&
							shouldSuggestReplacementClient(mcpClient.config.auth_type, credential.status_reason) && (
								<Hint>{t("registry.credential.clientRejected")}</Hint>
							)}
						{credential.status_reason && (
							<div
								className="text-muted-foreground mt-1 font-mono text-[11px] break-words"
								data-testid="mcpclient-credential-status-reason"
							>
								{credential.status_reason}
							</div>
						)}
					</DefinitionRow>
					<DefinitionRow label={t("registry.credential.accessTokenExpires")}>
						{credential.expires_at ? (
							<>
								{formatTokenExpiry(credential.expires_at, credential.status, credential.has_refresh_token)}
								<Sub>
									{formatAbsoluteDateTime(credential.expires_at)}
									{credential.last_refreshed_at &&
										` · ${t("registry.credential.refreshed", { time: formatRelativePast(credential.last_refreshed_at) })}`}
								</Sub>
							</>
						) : (
							<>
								<span className="text-muted-foreground">{t("registry.credential.noExpiryReported")}</span>
								{credential.last_refreshed_at && (
									<Sub>{t("registry.credential.refreshed", { time: formatRelativePast(credential.last_refreshed_at) })}</Sub>
								)}
							</>
						)}
					</DefinitionRow>
					<DefinitionRow label={t("sessions.refreshToken")}>
						<RefreshTokenValue credential={credential} notIssuedHint={copy.notIssued} />
					</DefinitionRow>
					<DefinitionRow label={t("registry.credential.grantedScopes")}>
						{credential.scopes?.length ? (
							<ScopeChips scopes={credential.scopes} max={credential.scopes.length} />
						) : (
							<span className="text-muted-foreground">{t("registry.credential.notReported")}</span>
						)}
					</DefinitionRow>
					<DefinitionRow label={t("registry.credential.authorized")}>{formatAbsoluteDate(credential.created_at)}</DefinitionRow>
				</DefinitionList>
			)}
		</div>
	);
}

// Copy for the three auth types that hold an OAuth credential of their own.
// The repair action named in each string matches the label of the
// actions-menu item that replaces that credential.
function oauthCredentialCopy(authType: MCPClient["config"]["auth_type"]) {
	switch (authType) {
		case "per_user_oauth": {
			const repairAction = i18n.t("registry.actions.refreshAdminCredential", { ns: "mcp" });
			return {
				title: i18n.t("registry.credential.adminTitle", { ns: "mcp" }),
				description: i18n.t("registry.credential.adminDescPerUserOauth", { ns: "mcp" }),
				empty: i18n.t("registry.credential.adminEmpty", { ns: "mcp", action: repairAction }),
				needsReauth: i18n.t("registry.credential.needsReauthPerUserOauth", { ns: "mcp", action: repairAction }),
				notIssued: i18n.t("registry.credential.notIssuedPerUserOauth", { ns: "mcp", action: repairAction }),
			};
		}
		case "token_exchange": {
			const repairAction = i18n.t("registry.actions.reverifyAsMe", { ns: "mcp" });
			return {
				title: i18n.t("registry.credential.adminTitle", { ns: "mcp" }),
				description: i18n.t("registry.credential.adminDescTokenExchange", { ns: "mcp" }),
				empty: i18n.t("registry.credential.adminEmpty", { ns: "mcp", action: repairAction }),
				needsReauth: i18n.t("registry.credential.needsReauthTokenExchange", { ns: "mcp", action: repairAction }),
				notIssued: i18n.t("registry.credential.notIssuedTokenExchange", { ns: "mcp", action: repairAction }),
			};
		}
		default: {
			const repairAction = i18n.t("registry.actions.reauthorize", { ns: "mcp" });
			return {
				title: i18n.t("registry.credential.oauthTitle", { ns: "mcp" }),
				description: i18n.t("registry.credential.oauthDesc", { ns: "mcp", action: repairAction }),
				empty: i18n.t("registry.credential.oauthEmpty", { ns: "mcp" }),
				needsReauth: i18n.t("registry.credential.needsReauthShared", { ns: "mcp", action: repairAction }),
				notIssued: i18n.t("registry.credential.notIssuedShared", { ns: "mcp", action: repairAction }),
			};
		}
	}
}

// Same reading as the sessions table's Refresh token column, plus the hint
// that tells the admin what it means for this server. The needs_reauth case
// is already explained under Status, so it carries no second hint.
function RefreshTokenValue({ credential, notIssuedHint }: { credential: MCPClientCredential; notIssuedHint: string }) {
	const { t } = useTranslation("mcp");
	return (
		<>
			<RefreshTokenStatus hasRefreshToken={credential.has_refresh_token} status={credential.status} />
			{credential.status !== "needs_reauth" &&
				(credential.has_refresh_token ? <Hint>{t("registry.credential.renewHint")}</Hint> : <Hint>{notIssuedHint}</Hint>)}
		</>
	);
}

function HeaderCredentialBlock({ mcpClient }: Props) {
	const { t } = useTranslation("mcp");
	const credential = mcpClient.credential?.kind === "headers" ? mcpClient.credential : undefined;
	const covered = credential?.header_keys ?? [];
	const missing = credential ? missingHeaderKeys(mcpClient.config.per_user_header_keys, covered) : [];

	return (
		<div className="space-y-4" data-testid="mcpclient-credential-section">
			<SectionHeader
				title={t("registry.credential.adminVerificationValues")}
				description={t("registry.credential.adminVerificationDesc")}
				testId="mcpclient-credential-heading"
			/>
			{!credential ? (
				<EmptyCredential>{t("registry.credential.adminValuesEmpty")}</EmptyCredential>
			) : (
				<DefinitionList>
					<DefinitionRow label={t("common.status")}>
						<CredentialStatusBadge status={credential.status} />
						{credential.status === "needs_update" && <Hint>{t("registry.credential.needsUpdateHint")}</Hint>}
					</DefinitionRow>
					<DefinitionRow label={t("registry.credential.coversHeaders")}>
						{covered.length === 0 && missing.length === 0 ? (
							<span className="text-muted-foreground">-</span>
						) : (
							<div className="flex flex-wrap items-center gap-1">
								{covered.map((key) => (
									<Badge key={key} variant="outline" className="font-mono text-xs font-normal">
										{key}
									</Badge>
								))}
								{missing.map((key) => (
									<Badge
										key={`missing-${key}`}
										variant="outline"
										className="text-muted-foreground border-dashed bg-transparent font-mono text-xs font-normal"
									>
										{key}
									</Badge>
								))}
								{missing.length > 0 && (
									<span className="text-muted-foreground rounded-sm border px-1 font-mono text-[10px] tracking-wider uppercase">
										{t("registry.credential.missing")}
									</span>
								)}
							</div>
						)}
					</DefinitionRow>
					<DefinitionRow label={t("registry.credential.submitted")}>{formatAbsoluteDate(credential.created_at)}</DefinitionRow>
					<DefinitionRow label={t("registry.credential.lastUpdated")}>{formatRelativePast(credential.updated_at)}</DefinitionRow>
				</DefinitionList>
			)}
		</div>
	);
}

// Status vocabulary mirrors the sessions table's StatusBadge so a credential
// reads the same in both places; colors come from the shared palette so the
// badge matches the server state badge above it.
function CredentialStatusBadge({ status }: { status: MCPClientCredential["status"] }) {
	const { t } = useTranslation("mcp");
	const labels: Record<string, string> = {
		active: t("sessions.status.active"),
		needs_reauth: t("sessions.status.needsReauth"),
		needs_update: t("sessions.status.needsUpdate"),
		orphaned: t("sessions.status.orphaned"),
	};
	return (
		<Badge className={MCP_CREDENTIAL_STATUS_COLORS[status] ?? MCP_CREDENTIAL_STATUS_COLORS.unknown}>
			{labels[status] ?? titleCaseFromSnakeCase(status)}
		</Badge>
	);
}

// DefinitionList and DefinitionRow lay out label/value rows; the view sheet's other sections use them too.
export function DefinitionList({ children }: { children: ReactNode }) {
	return <dl className="grid grid-cols-1 gap-x-4 gap-y-3.5 rounded-md border p-4 text-sm sm:grid-cols-[168px_1fr]">{children}</dl>;
}

export function DefinitionRow({ label, children }: { label: string; children: ReactNode }) {
	return (
		<>
			<dt className="text-muted-foreground pt-0.5">{label}</dt>
			<dd className="m-0 min-w-0">{children}</dd>
		</>
	);
}

function Sub({ children }: { children: ReactNode }) {
	return <span className="text-muted-foreground mt-0.5 block text-xs">{children}</span>;
}

function Hint({ children }: { children: ReactNode }) {
	return <span className="text-muted-foreground mt-1 block max-w-prose text-xs">{children}</span>;
}

function EmptyCredential({ children }: { children: ReactNode }) {
	return (
		<div className="text-muted-foreground rounded-md border border-dashed p-4 text-sm" data-testid="mcpclient-credential-empty">
			{children}
		</div>
	);
}