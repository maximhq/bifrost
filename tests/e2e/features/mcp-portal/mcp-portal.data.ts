import { randomUUID } from "crypto";

export function createPortalServerData() {
  const suffix = `${Date.now()}_${Math.random().toString(36).slice(2, 8)}`;
  return {
    name: `portal_headers_${suffix}`,
    client_id: randomUUID(),
    connection_type: "openapi",
    auth_type: "per_user_headers",
    allow_by_default: false,
    per_user_header_keys: ["x-api-key"],
    tools_to_execute: ["*"],
    openapi_config: {
      spec: JSON.stringify({
        openapi: "3.0.3",
        info: { title: "Portal credentials", version: "1.0.0" },
        servers: [{ url: "https://petstore.swagger.io/v3" }],
        paths: {
          "/pet/findByStatus": {
            get: { operationId: "findPets", responses: { "200": { description: "Pets" } } },
          },
        },
      }),
    },
  };
}

export function createPortalKeyData(serverName: string, tools: string[] = ["findPets"]) {
  return {
    name: `portal_key_${Date.now()}_${Math.random().toString(36).slice(2, 8)}`,
    is_active: true,
    mcp_configs: [{ mcp_client_name: serverName, tools_to_execute: tools }],
  };
}

export function createPortalOAuthServerData() {
  return {
    client_id: randomUUID(),
    name: `portal_oauth_${Date.now()}_${Math.random().toString(36).slice(2, 8)}`,
    connection_type: "http",
    connection_string: "http://localhost:3003/mcp",
    auth_type: "per_user_oauth",
    oauth_config: {},
    is_ping_available: false,
    allow_by_default: false,
    tools_to_execute: ["*"],
  };
}