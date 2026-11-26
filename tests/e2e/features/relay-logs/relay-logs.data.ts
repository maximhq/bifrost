import { createServer, type IncomingHttpHeaders, type Server } from "http";
import type { AddressInfo } from "net";

/**
 * Test data for the Relay Logs E2E suite: a local upstream that stands in for a paid API, and the
 * payload factories for the relay and the virtual keys that call it.
 *
 * Payloads are object literals with fields in the order the API documents them. Never build them
 * from a Record/Map and re-serialize: field order matters for backend validation.
 */

export const RELAYS_API = "/api/relays";
export const RELAY_LOGS_API = "/api/relay-logs";
export const VK_API = "/api/governance/virtual-keys";
export const TEAMS_API = "/api/governance/teams";

/** The upstream credential the relay must inject; callers never see it. */
export const UPSTREAM_KEY = "sk-upstream-relay-logs-e2e";

/** Endpoint pricing: a fee per call plus a price per credit the upstream reports. */
export const FEE_PER_REQUEST = 0.001;
export const RATE_PER_UNIT = 0.008;
/** The /v1/extract route waives the fee and doubles the rate. */
export const EXTRACT_RATE_PER_UNIT = 0.016;

export const SEARCH_CREDITS = 4;
export const EXTRACT_CREDITS = 2;

/** Formula costs of the priced calls, as the Relay Logs table shows them with 4 decimals. */
export const SEARCH_COST = FEE_PER_REQUEST + SEARCH_CREDITS * RATE_PER_UNIT; // 0.033
export const EXTRACT_COST = EXTRACT_CREDITS * EXTRACT_RATE_PER_UNIT; // 0.032

/** A marker only the search call carries, used to find its row and body. */
export const SEARCH_QUERY_MARKER = "bifrost-relay-logs-e2e";

export interface SeenRequest {
  method: string;
  path: string;
  headers: IncomingHttpHeaders;
}

export interface RelayUpstream {
  url: string;
  seen: SeenRequest[];
  close(): Promise<void>;
}

/**
 * Starts a local upstream on a free port. Every reply is JSON; the priced routes carry a
 * `usage.credits` number the relay reads for the cost formula.
 */
export function startRelayUpstream(): Promise<RelayUpstream> {
  const seen: SeenRequest[] = [];
  const server: Server = createServer((req, res) => {
    seen.push({ method: req.method ?? "", path: req.url ?? "", headers: req.headers });
    req.resume();
    req.on("end", () => {
      const path = (req.url ?? "").split("?")[0];
      if (path === "/v1/search") {
        res.writeHead(200, { "Content-Type": "application/json" });
        res.end(JSON.stringify({ results: ["a", "b"], usage: { credits: SEARCH_CREDITS } }));
        return;
      }
      if (path === "/v1/extract") {
        res.writeHead(200, { "Content-Type": "application/json" });
        res.end(JSON.stringify({ text: "extracted", usage: { credits: EXTRACT_CREDITS } }));
        return;
      }
      if (path === "/v1/status") {
        res.writeHead(200, { "Content-Type": "application/json" });
        res.end(JSON.stringify({ ok: true }));
        return;
      }
      res.writeHead(404, { "Content-Type": "application/json" });
      res.end(JSON.stringify({ error: "not found" }));
    });
  });
  return new Promise((resolve) => {
    server.listen(0, "127.0.0.1", () => {
      const url = `http://127.0.0.1:${(server.address() as AddressInfo).port}`;
      resolve({
        url,
        seen,
        close: () => new Promise<void>((done) => server.close(() => done())),
      });
    });
  });
}

/** A unique relay name; relays are keyed by name, so it doubles as the cleanup handle. */
export function relayLogsRelayName(): string {
  return `e2e-relay-logs-${Date.now()}`;
}

/**
 * The relay the pagination tests page through. Its name must not contain the main relay's, since
 * rows are told apart by the relay name in their text.
 */
export function relayLogsPagedRelayName(): string {
  return `e2e-relay-paged-${Date.now()}`;
}

/** How many calls the paged relay gets: more than the smallest page size the table offers (10). */
export const PAGED_ROW_COUNT = 12;

/** The relay the suite calls: priced endpoint, a route override, a free GET and a disabled route. */
export function createRelayLogsRelayPayload(name: string, baseUrl: string) {
  return {
    name,
    description: "E2E relay logs",
    base_url: baseUrl,
    auth: { type: "bearer" },
    credential: { value: { value: UPSTREAM_KEY } },
    usage: { cost_per_request: FEE_PER_REQUEST, units_path: "usage.credits", cost_per_unit: RATE_PER_UNIT },
    routes: [
      { method: "POST", path: "/v1/search" },
      { method: "POST", path: "/v1/extract", usage: { cost_per_request: 0, cost_per_unit: EXTRACT_RATE_PER_UNIT } },
      { method: "GET", path: "/v1/status" },
      { method: "POST", path: "/v1/disabled", enabled: false },
    ],
  };
}

/** A team to attribute the granted key's calls to, so the sheet shows a team link. */
export function createRelayLogsTeamPayload(name: string) {
  return {
    name,
  };
}

/** A key granted the relay, owned by a team, with a budget the calls are charged to. */
export function createGrantedKeyPayload(name: string, relayName: string, teamId: string) {
  return {
    name,
    is_active: true,
    team_id: teamId,
    budgets: [{ max_limit: 100, reset_duration: "1M" }],
    relay_grants: [{ relay_name: relayName }],
  };
}

/** A key with no relay grant; its calls are refused with 403 and still logged. */
export function createUngrantedKeyPayload(name: string) {
  return {
    name,
    is_active: true,
  };
}

/** A granted key whose content logging is off, so its rows carry no bodies. */
export function createContentHiddenKeyPayload(name: string, relayName: string) {
  return {
    name,
    is_active: true,
    disable_content_logging: true,
    relay_grants: [{ relay_name: relayName }],
  };
}

/** One Relay Logs row as GET /api/relay-logs returns it (the fields the tests read). */
export interface RelayLogRow {
  id: string;
  request_id: string;
  relay_name: string;
  kind: "call" | "job";
  method: string;
  path: string;
  route_path?: string;
  status: string;
  status_code: number;
  billable: boolean;
  streamed: boolean;
  cost?: number;
  latency?: number;
  virtual_key_id?: string;
  virtual_key_name?: string;
  team_id?: string;
  content_hidden: boolean;
  request_body?: string;
  response_body?: string;
}
