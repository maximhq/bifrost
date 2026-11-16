// Additive provider-harness regression for MCP additionalProperties.
// This fixture checks the actual provider-bound tool schema through the gateway
// on configured local SSE MCP test client. No provider request is sent here.
// The provider harness is paid: this module is only a pure collection builder.
const label = 'mcp-schema-probe-additional-properties';
const probeTool = 'schema_probe_mcp-closed_schema_probe';
const script = [
  "var txt = pm.response.text() || '';",
  "pm.test('MCP closed schema: provider request succeeded', function () { pm.expect(pm.response.code, 'failed: ' + txt.slice(0, 600)).to.equal(200); });",
  "var b = {}; try { b = pm.response.json() || {}; } catch (e) { b = {}; }",
  "var raw = (b.extra_fields || {}).raw_request;",
  "if (typeof raw === 'string') { try { raw = JSON.parse(raw); } catch (e) { raw = null; } }",
  "pm.test('MCP closed schema: provider-bound raw request captured', function () { pm.expect(raw, 'missing raw_request evidence').to.be.an('object'); });",
  "var decl = Array.isArray(raw && raw.tools) ? raw.tools : [];",
  "var matching = decl.filter(function (t) { return t && (t.function || t).name === 'schema_probe_mcp-closed_schema_probe'; });",
  "pm.test('MCP closed schema: isolated tool forwarded once', function () { pm.expect(matching.length).to.equal(1); });",
  "pm.test('MCP closed schema: additionalProperties false preserved', function () {",
  "  var params = matching.length ? ((matching[0].function || matching[0]).parameters || {}) : {};",
  "  pm.expect(params.type).to.equal('object');",
  "  pm.expect(params, 'schema closure missing on provider wire').to.have.property('additionalProperties');",
  "  pm.expect(params.additionalProperties).to.equal(false);",
  "  pm.expect(params.required).to.include('probe');",
  "});",
];
function request(route,body,description) {
 return {
  name:`openai/gpt-4o-mini ${description} MCP closed additionalProperties provider wire - ${label} - #10070`,
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
 '352. MCP closed additionalProperties on provider wire ('+label+') (PR #10070)';
export function buildMCPAdditionalPropertiesHarnessFolder() {
 return {
  name:MCP_SCHEMA_PROBE_FOLDER_NAME,
  description:'Live provider-wire regression against isolated local SSE read-only MCP schema_probe_mcp client. The fixture closed_schema_probe explicitly sets additionalProperties:false. Requires configured provider; never run by augmentation. Both request status and provider-bound raw_request parameters must pass.',
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
