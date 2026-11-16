import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';
import {fileURLToPath} from 'node:url';
import {buildMCPAdditionalPropertiesHarnessFolder,MCP_SCHEMA_PROBE_FOLDER_NAME} from './mcp-additional-properties-harness.mjs';

const dirname=path.dirname(fileURLToPath(import.meta.url));
const root=path.resolve(dirname,'../../../../..');
test('MCP provider-wire harness includes two real routes, no paid calls on import',()=>{
 const folder=buildMCPAdditionalPropertiesHarnessFolder();
 assert.equal(folder.name,MCP_SCHEMA_PROBE_FOLDER_NAME);
 assert.equal(folder.item.length,2);
 assert.deepEqual(folder.item.map(row=>row.request.url.raw),[
  '{{baseUrl}}/v1/chat/completions','{{baseUrl}}/v1/responses',
 ]);
 for(const item of folder.item) {
  const {request,event}=item;
  assert.match(item.name,/openai\/gpt-4o-mini/u);
  assert.match(item.name,/mcp-schema-probe-additional-properties/u);
  assert.equal(request.method,'POST');
  assert.ok(request.header.some(x=>x.key==='x-bf-mcp-include-clients'&&x.value==='schema_probe_mcp'));
  assert.ok(request.header.some(x=>x.key==='x-bf-send-back-raw-request'&&x.value==='true'));
  const body=JSON.parse(request.body.raw);
  assert.equal(body.model,'openai/gpt-4o-mini');
  assert.equal(body.tool_choice,'none');
  const checks=event[0].script.exec.join('\n');
  assert.match(checks,/raw_request/u);
  assert.match(checks,/matching\.length/u);
  assert.match(checks,/additionalProperties/u);
  assert.match(checks,/\.to\.equal\(false\)/u);
  assert.match(checks,/pm\.response\.code/u);
 }
});
test('MCP test client is isolated and fixture advertises the original closed schema',()=>{
 const config=JSON.parse(fs.readFileSync(path.join(root,'tests/integrations/python/config.json'),'utf8'));
 const clients=config.mcp.client_configs.filter(x=>x.name==='schema_probe_mcp');
 assert.equal(clients.length,1);
 assert.deepEqual(clients[0].tools_to_execute,['closed_schema_probe']);
 assert.deepEqual(clients[0].tools_to_auto_execute,[]);
 const src=fs.readFileSync(path.join(root,'examples/mcps/remote-test-server/main.go'),'utf8');
 assert.match(src,/mcp\.NewToolWithRawSchema\(/u);
 assert.match(src,/"additionalProperties":false/u);
 assert.doesNotMatch(src,/closedSchemaTool\.InputSchema\.AdditionalProperties/u);
 assert.match(src,/s\.AddTool\(closedSchemaTool/u);
});
test('augmentation integrates the regression without modifying the checked-in giant Postman collection',()=>{
 const augment=fs.readFileSync(path.join(root,'tests/e2e/api/runners/augment-provider-harness.mjs'),'utf8');
 assert.match(augment,/collection\.item\.push\(buildMCPAdditionalPropertiesHarnessFolder\(\)\)/u);
 assert.match(augment,/duplicate MCP schema parity provider harness folder/u);
});
