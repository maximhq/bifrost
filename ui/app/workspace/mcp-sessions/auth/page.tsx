// Auth landing route for inline-401 URLs returned by the inference path.
//
// The inference response embeds a frontend URL of the form
//   {base}/workspace/mcp-sessions/auth?flow={flowId}
// pointing here. The page fetches the pending flow's metadata, shows the
// user what they're about to authenticate, and on "Authenticate" click
// asks the backend for the upstream provider authorize URL and redirects
// the browser to it. The upstream provider redirects back to
// /api/oauth/callback which completes the flow server-side.

import FullPageLoader from "@/components/fullPageLoader";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { useToast } from "@/hooks/use-toast";
import {
  getErrorMessage,
  useGetMCPFlowDetailQuery,
  useIsAuthEnabledQuery,
  useStartMCPFlowMutation,
} from "@/lib/store";
import { MCPFlowDetail } from "@/lib/types/mcpSessions";
import { Link } from "@tanstack/react-router";
import {
  ExternalLink,
  Fingerprint,
  KeyRound,
  Loader2,
  ShieldCheck,
  UserRound,
} from "lucide-react";
import { useQueryState } from "nuqs";

export default function MCPSessionsAuthPage() {
  const { toast } = useToast();
  const [flowId] = useQueryState("flow");
  const skip = !flowId;
  const {
    data: flow,
    isLoading,
    isError,
    error,
  } = useGetMCPFlowDetailQuery(flowId ?? "", { skip });
  const [startFlow, { isLoading: starting }] = useStartMCPFlowMutation();

  if (!flowId) {
    return (
      <CenteredCard>
        <h1 className="text-xl font-semibold">Missing flow identifier</h1>
        <p className="text-muted-foreground mt-2 text-sm">
          This URL is missing the{" "}
          <code className="bg-muted rounded px-1 py-0.5">flow</code> query
          parameter. Open the link from your inference response or the sessions
          tab.
        </p>
        <div className="mt-6">
          <SessionsTabLink />
        </div>
      </CenteredCard>
    );
  }

  if (isLoading) {
    return <FullPageLoader />;
  }

  if (isError || !flow) {
    const status = (error as { status?: number } | undefined)?.status;
    if (status === 401) {
      return <InvalidLinkView />;
    }
    if (status === 403) {
      return (
        <CenteredCard>
          <h1 className="text-xl font-semibold">
            This authentication flow isn't yours
          </h1>
          <p className="text-muted-foreground mt-2 text-sm">
            The pending flow belongs to a different identity. Ask the teammate
            whose VK or user identity triggered the original request to complete
            it, or trigger a new request yourself.
          </p>
          <div className="mt-6">
            <SessionsTabLink />
          </div>
        </CenteredCard>
      );
    }
    if (status === 404) {
      return (
        <CenteredCard>
          <h1 className="text-xl font-semibold">
            This authentication flow has expired or been completed
          </h1>
          <p className="text-muted-foreground mt-2 text-sm">
            Pending flows expire after a short window. If you still need to
            authenticate, trigger the original action again so a fresh flow is
            created.
          </p>
          <div className="mt-6">
            <SessionsTabLink />
          </div>
        </CenteredCard>
      );
    }
    return (
      <CenteredCard>
        <h1 className="text-xl font-semibold">
          Could not load this authentication flow
        </h1>
        <p className="text-muted-foreground mt-2 text-sm">
          {getErrorMessage(error)}
        </p>
      </CenteredCard>
    );
  }

  // Flow row exists but isn't pending: it's already been completed, failed,
  // or expired. Don't show the "Authenticate" button since startFlow would
  // reject (BuildUpstreamAuthorizeURL rejects non-pending flows).
  if (flow.status !== "pending") {
    return <CompletedFlowView flow={flow} />;
  }

  const handleAuthenticate = async () => {
    try {
      const res = await startFlow(flowId).unwrap();
      window.location.href = res.authorize_url;
    } catch (err) {
      toast({
        title: "Failed to start authentication",
        description: getErrorMessage(err),
        variant: "destructive",
      });
    }
  };

  const mcpClientName =
    flow.mcp_client?.name || flow.mcp_client?.client_id || "MCP server";
  const isReauth = flow.has_active_token === true;

  return (
    <CenteredCard>
      <div className="mb-5 flex size-12 items-center justify-center rounded-full bg-primary/10">
        <ShieldCheck className="text-primary size-6" />
      </div>
      <h1 className="text-xl font-semibold tracking-tight">
        {isReauth ? "Re-authenticate with" : "Authenticate with"}{" "}
        {mcpClientName}
      </h1>
      <p className="text-muted-foreground mt-2 text-sm">
        {isReauth ? (
          <>
            An active credential already exists for the binding below.
            Completing this flow will <strong>replace</strong> it with a fresh
            credential. You can also close this tab to keep using the existing
            one.
          </>
        ) : (
          <>
            You'll be redirected to the provider to sign in and grant access.
            Bifrost stores the resulting credential against the binding below so
            this request and future ones can proceed automatically.
          </>
        )}
      </p>

      <dl className="bg-muted/40 mt-6 space-y-3 rounded-sm border p-4 text-sm">
        <DetailRow
          label="MCP client"
          value={mcpClientName}
          mono={!flow.mcp_client?.name}
        />
        <DetailRow label="Bound to" value={<BindingValue flow={flow} />} />
        <DetailRow label="Flow expires" value={formatExpiry(flow.expires_at)} />
      </dl>

      <div className="mt-6 flex gap-3">
        <Button
          onClick={handleAuthenticate}
          disabled={starting}
          data-testid="mcp-auth-authenticate-button"
        >
          {starting ? (
            <Loader2 className="size-4 animate-spin" />
          ) : (
            <ExternalLink className="size-4" />
          )}
          <span>{isReauth ? "Re-authenticate" : "Authenticate"}</span>
        </Button>
        <SessionsTabLink variant="ghost" />
      </div>
    </CenteredCard>
  );
}

function CompletedFlowView({ flow }: { flow: MCPFlowDetail }) {
  const mcpClientName =
    flow.mcp_client?.name || flow.mcp_client?.client_id || "this MCP server";
  // has_active_token wins over the flow's row status: a pending flow with an
  // existing active token means OAuth was re-initiated unnecessarily.
  const effectivelyAuthorized =
    flow.status === "authorized" || flow.has_active_token;
  const title = effectivelyAuthorized
    ? "Already authenticated"
    : flow.status === "expired"
      ? "This authentication flow has expired"
      : "This authentication flow can no longer be completed";
  const body = effectivelyAuthorized
    ? `The OAuth credential for ${mcpClientName} is already stored. You can close this tab.`
    : "Trigger the original action again so a fresh flow is created.";
  return (
    <CenteredCard>
      <h1 className="text-xl font-semibold tracking-tight">{title}</h1>
      <p className="text-muted-foreground mt-2 text-sm">{body}</p>
      <dl className="bg-muted/40 mt-6 space-y-3 rounded-sm border p-4 text-sm">
        <DetailRow
          label="MCP client"
          value={mcpClientName}
          mono={!flow.mcp_client?.name}
        />
        <DetailRow label="Bound to" value={<BindingValue flow={flow} />} />
      </dl>
      <div className="mt-6">
        <SessionsTabLink />
      </div>
    </CenteredCard>
  );
}

function DetailRow({
  label,
  value,
  mono = false,
}: {
  label: string;
  value: React.ReactNode;
  mono?: boolean;
}) {
  return (
    <div className="flex items-start justify-between gap-4">
      <dt className="text-muted-foreground text-xs font-medium tracking-wide uppercase">
        {label}
      </dt>
      <dd className={`text-right text-sm ${mono ? "font-mono" : ""}`}>
        {value}
      </dd>
    </div>
  );
}

function BindingValue({ flow }: { flow: MCPFlowDetail }) {
  if (flow.flow_mode === "user") {
    const userID = flow.user_id;
    if (!userID) {
      return (
        <span className="inline-flex items-center gap-2">
          <UserRound className="text-muted-foreground size-3.5" />
          <Badge variant="secondary">First signed-in user</Badge>
        </span>
      );
    }
    const displayName = flow.user?.name || flow.user?.email;
    return (
      <span className="inline-flex items-center gap-2">
        <UserRound className="text-muted-foreground size-3.5" />
        {displayName ? (
          <span>{displayName}</span>
        ) : (
          <span className="font-mono">{userID}</span>
        )}
      </span>
    );
  }
  if (flow.flow_mode === "vk" && flow.virtual_key) {
    return (
      <span className="inline-flex items-center gap-2">
        <KeyRound className="text-muted-foreground size-3.5" />
        <span>{flow.virtual_key.name || flow.virtual_key.id}</span>
      </span>
    );
  }
  if (flow.flow_mode === "session" && flow.session_id) {
    return (
      <span className="inline-flex items-center gap-2">
        <Fingerprint className="text-muted-foreground size-3.5" />
        <span className="font-mono">{flow.session_id}</span>
      </span>
    );
  }
  return <span className="text-muted-foreground italic">Unknown</span>;
}

function formatExpiry(iso: string): string {
  try {
    const t = new Date(iso).getTime();
    if (Number.isNaN(t)) return iso;
    const diffMs = t - Date.now();
    if (diffMs < 0) return "Expired";
    const mins = Math.floor(diffMs / 60_000);
    if (mins < 1) return "in less than a minute";
    if (mins === 1) return "in 1 minute";
    return `in ${mins} minutes`;
  } catch {
    return iso;
  }
}

function CenteredCard({ children }: { children: React.ReactNode }) {
  return (
    <div className="mx-auto flex min-h-[60vh] w-full max-w-xl items-center justify-center p-6">
      <div className="bg-card w-full rounded-sm border p-8 shadow-sm">
        {children}
      </div>
    </div>
  );
}

function SessionsTabLink({
  variant = "outline",
}: {
  variant?: "outline" | "ghost";
}) {
  // Hide the link only when the visitor has no dashboard session — for them,
  // /workspace/mcp-sessions would 401 and bounce to /login. Admins (cookie
  // present) still see it. ClientLayout already cached this query for the
  // route, so this is a free hook call.
  const { data: authState } = useIsAuthEnabledQuery();
  if (authState?.is_auth_enabled && !authState.has_valid_token) {
    return null;
  }
  return (
    <Button asChild variant={variant} data-testid="mcp-auth-sessions-tab-link">
      <Link to="/workspace/mcp-sessions">Open sessions tab</Link>
    </Button>
  );
}

// InvalidLinkView renders when the per-user-flow API returns 401, which now
// means the caller arrived without either a valid dashboard session or a
// valid mcp_auth temp token. Most often this is an expired or hand-edited
// link — the temp token embedded in the URL fragment has aged out or the
// fragment was dropped along the way. Trigger the original action again to
// get a fresh URL.
function InvalidLinkView() {
  return (
    <CenteredCard>
      <h1 className="text-xl font-semibold tracking-tight">
        This authentication link is no longer valid
      </h1>
      <p className="text-muted-foreground mt-2 text-sm">
        The link may have expired, been used already, invalid, or had its
        short-lived token stripped. Trigger the original action again so a fresh
        authentication link is created.
      </p>
    </CenteredCard>
  );
}
