// Additive provider-harness regression for MCP additionalProperties.
// Only the independently executed provider-bound raw_request can establish wire parity.
// Import and collection augmentation are local and make no network/provider calls.
const label = 'mcp-schema-probe-additional-properties';
const checks = [
  {tool:'schema_probe_mcp-closed_schema_probe', mode:'false'},
  {tool:'schema_probe_mcp-object_schema_probe', mode:'object'},
  {tool:'schema_probe_mcp-absent_schema_probe', mode:'absent'},
];
const script = [
  "var txt = pm.response.text() || '';",
  "pm.test('MCP schema parity: provider request succeeded', function () { pm.expect(pm.response.code, 'failed: ' + txt.slice(0, 600)).to.equal(200); });",
  "var b = {}; try { b = pm.response.json() || {}; } catch (e) { b = {}; }",
  "var raw = (b.extra_fields || {}).raw_request;",
  "if (typeof raw === 'string') { try { raw = JSON.parse(raw); } catch (e) { raw = null; } }",
  "pm.test('MCP schema parity: provider-bound raw request captured', function () { pm.expect(raw, 'missing raw_request evidence').to.be.an('object'); });",
  "var decl = Array.isArray(raw && raw.tools) ? raw.tools : [];",
  ...checks.flatMap(({tool,mode}) => [
    "pm.test('MCP schema parity: " + tool + " appears exactly once', function () {",
    "  var matching = decl.filter(function (t) { return t && (t.function || t).name === " + JSON.stringify(tool) + "; });",
    "  pm.expect(matching.length).to.equal(1);",
    "  var params = matching.length ? ((matching[0].function || matching[0]).parameters || {}) : {};",
    "  pm.expect(params.type).to.equal('object');",
    "  pm.expect(params.required).to.include('probe');",
    ...(mode === 'false' ? [
      "  pm.expect(params, 'schema closure missing on provider wire').to.have.property('additionalProperties');",
      "  pm.expect(params.additionalProperties).to.equal(false);",
    ] : mode === 'object' ? [
      "  pm.expect(params, 'schema object missing on provider wire').to.have.property('additionalProperties');",
      "  pm.expect(params.additionalProperties).to.deep.equal({type:'string'});",
    ] : [
      "  pm.expect(Object.prototype.hasOwnProperty.call(params, 'additionalProperties'), 'undeclared keyword must stay absent').to.equal(false);",
    ]),
    "});",
  ]),
];
function request(route, body, description) {
  return {
    name:`openai/gpt-4o-mini ${description} MCP additionalProperties provider wire - ${label} - #10070`,
    event:[{listen:'test',script:{type:'text/javascript',exec:script}}],
    request:{
      method:'POST',
      header:[
        {key:'Content-Type',value:'application/json'},
        {key:'x-bf-mcp-include-clients',value:'schema_probe_mcp'},
        {key:'x-bf-send-back-raw-request',value:'true'},
      ],
      body:{mode:'raw',raw:JSON.stringify(body,null,2)},
      url:{raw:'{{baseUrl}}/'+route,host:['{{baseUrl}}'],path:route.split('/')},
    },
  };
}
export const MCP_SCHEMA_PROBE_FOLDER_NAME =
  '352. MCP additionalProperties on provider wire ('+label+') (PR #10070)';
export function buildMCPAdditionalPropertiesHarnessFolder() {
  return {
    name:MCP_SCHEMA_PROBE_FOLDER_NAME,
    description:'Live provider-wire regression on isolated local SSE schema_probe_mcp client. Requires real provider-bound raw_request. Checks additionalProperties false, schema-object and absence across chat completions and Responses. Collection generation never executes these paid tests.',
    item:[
      request('v1/chat/completions',{
        model:'openai/gpt-4o-mini',max_tokens:16,tool_choice:'none',
        messages:[{role:'user',content:'Reply only OK.'}],
      },'chat completions'),
      request('v1/responses',{
        model:'openai/gpt-4o-mini',max_output_tokens:16,tool_choice:'none',
        input:'Reply only OK.',
      },'responses'),
    ],
  };
}
