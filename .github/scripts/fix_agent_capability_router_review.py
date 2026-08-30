from pathlib import Path


def replace_once(path: str, old: str, new: str) -> None:
    p = Path(path)
    text = p.read_text()
    count = text.count(old)
    if count != 1:
        raise SystemExit(f"{path}: expected exactly one anchor, found {count}")
    p.write_text(text.replace(old, new, 1))


# 1. Make the new docs page discoverable in Mintlify navigation.
replace_once(
    "docs/docs.json",
    '              "plugins/building-dynamic-binary",\n              "plugins/sequencing",\n',
    '              "plugins/building-dynamic-binary",\n              "plugins/sequencing",\n              "plugins/agent-capability-router",\n',
)

# 2. Classify Responses messages from typed discriminators, never from user text.
replace_once(
    "plugins/agentcapabilityrouter/extractor.go",
    '''func inferResponsesKind(message schemas.ResponsesMessage, text string) string {
\tencoded, _ := json.Marshal(message)
\tlower := strings.ToLower(string(encoded))
\tswitch {
\tcase strings.Contains(lower, "function_call_output"), strings.Contains(lower, "tool_result"):
\t\treturn "tool-result"
\tcase strings.Contains(lower, "function_call"), strings.Contains(lower, "tool_call"):
\t\treturn inferToolKind(text)
\tcase strings.Contains(lower, `"role":"user"`):
\t\treturn "user"
\tcase strings.Contains(lower, `"role":"assistant"`):
\t\treturn "assistant"
\tdefault:
\t\treturn "context"
\t}
}
''',
    '''func inferResponsesKind(message schemas.ResponsesMessage, text string) string {
\tif message.Type != nil {
\t\tswitch string(*message.Type) {
\t\tcase "function_call_output":
\t\t\treturn "tool-result"
\t\tcase "function_call":
\t\t\treturn inferToolKind(text)
\t\t}
\t}
\tif message.Role != nil {
\t\tswitch string(*message.Role) {
\t\tcase "user":
\t\t\treturn "user"
\t\tcase "assistant":
\t\t\treturn "assistant"
\t\t}
\t}
\treturn "context"
}
''',
)

# 3. Add regression coverage for Responses discriminator spoofing and real tool calls.
extractor_test = Path("plugins/agentcapabilityrouter/extractor_test.go")
text = extractor_test.read_text()
marker = "func TestHistoryLimitKeepsNewestMessages"
if "TestExtractResponsesUserTextCannotSpoofToolKind" in text:
    raise SystemExit("Responses regression test already exists")
insert = r'''func TestExtractResponsesUserTextCannotSpoofToolKind(t *testing.T) {
\trole := schemas.ResponsesInputMessageRoleUser
\tcontent := schemas.ResponsesMessageContent{ContentStr: schemas.Ptr("Explain function_call_output and tool_result without calling a tool")}
\treq := &schemas.BifrostRequest{ResponsesRequest: &schemas.BifrostResponsesRequest{
\t\tModel: "agent-main-auto",
\t\tInput: []schemas.ResponsesMessage{{
\t\t\tRole:    &role,
\t\t\tContent: &content,
\t\t}},
\t}}
\tsnapshot := extractAgentSignals(req, 8)
\tif len(snapshot.Events) != 1 || snapshot.Events[0].Kind != "user" {
\t\tt.Fatalf("user text spoofed a Responses tool discriminator: %#v", snapshot)
\t}
}

func TestInferResponsesKindUsesFunctionCallType(t *testing.T) {
\tmessageType := schemas.ResponsesMessageTypeFunctionCall
\tkind := inferResponsesKind(schemas.ResponsesMessage{Type: &messageType}, "apply_patch")
\tif kind != "edit" {
\t\tt.Fatalf("function_call kind = %q, want edit", kind)
\t}
}

'''
if marker not in text:
    raise SystemExit("extractor test insertion marker not found")
extractor_test.write_text(text.replace(marker, insert + marker, 1))

# 4. Add a strict configuration schema for the native built-in plugin.
schema_path = Path("transports/config.schema.json")
schema = schema_path.read_text()
if '"const": "agent-capability-router"' in schema:
    raise SystemExit("agent capability router schema already exists")
anchor = '''          {
            "if": {
              "properties": {
                "name": {
                  "const": "maxim"
'''
router_schema = '''          {
            "if": {
              "properties": {
                "name": {
                  "const": "agent-capability-router"
                }
              }
            },
            "then": {
              "required": ["config"],
              "properties": {
                "config": {
                  "type": "object",
                  "description": "Configuration for deterministic agent capability classification and logical alias routing",
                  "properties": {
                    "shadow_mode": {
                      "type": "boolean",
                      "description": "Classify and log without rewriting managed model aliases when true",
                      "default": true
                    },
                    "confidence_threshold": {
                      "type": "number",
                      "exclusiveMinimum": 0,
                      "maximum": 1,
                      "description": "Minimum classifier confidence required to use a non-general capability lane",
                      "default": 0.7
                    },
                    "history_messages": {
                      "type": "integer",
                      "minimum": 1,
                      "maximum": 32,
                      "description": "Maximum number of recent normalized messages considered by the classifier",
                      "default": 8
                    },
                    "aliases": {
                      "type": "object",
                      "description": "Logical automatic model aliases managed by the plugin",
                      "properties": {
                        "main": { "type": "string", "minLength": 1 },
                        "worker": { "type": "string", "minLength": 1 }
                      },
                      "additionalProperties": false
                    },
                    "active_roles": {
                      "type": "object",
                      "description": "Enable capability classification independently for managed roles",
                      "properties": {
                        "main": { "type": "boolean" },
                        "worker": { "type": "boolean" }
                      },
                      "additionalProperties": false
                    },
                    "keywords": {
                      "type": "object",
                      "description": "Optional per-capability keyword overrides",
                      "properties": {
                        "orchestrate": { "type": "array", "items": { "type": "string" } },
                        "implement": { "type": "array", "items": { "type": "string" } },
                        "debug": { "type": "array", "items": { "type": "string" } },
                        "tool-loop": { "type": "array", "items": { "type": "string" } },
                        "explore": { "type": "array", "items": { "type": "string" } },
                        "summarize": { "type": "array", "items": { "type": "string" } },
                        "general": { "type": "array", "items": { "type": "string" } }
                      },
                      "additionalProperties": false
                    }
                  },
                  "additionalProperties": false
                }
              }
            }
          },
'''
count = schema.count(anchor)
if count != 1:
    raise SystemExit(f"config schema anchor count = {count}, want 1")
schema = schema.replace(anchor, router_schema + anchor, 1)
schema_path.write_text(schema)

# 5. Validate the strict schema contract with positive and negative cases.
validator_test = Path("transports/bifrost-http/lib/validator_test.go")
text = validator_test.read_text()
if "TestValidateConfigSchema_AgentCapabilityRouter" in text:
    raise SystemExit("agent capability router schema tests already exist")
append = r'''
func TestValidateConfigSchema_AgentCapabilityRouter(t *testing.T) {
\tschema := loadLocalSchema(t)
\ttests := []struct {
\t\tname    string
\t\tconfig  string
\t\twantErr bool
\t}{
\t\t{
\t\t\tname: "valid strict config",
\t\t\tconfig: `{"plugins":[{"enabled":true,"name":"agent-capability-router","config":{"shadow_mode":false,"confidence_threshold":0.8,"history_messages":12,"aliases":{"main":"agent-main-auto","worker":"agent-worker-auto"},"active_roles":{"main":true,"worker":false},"keywords":{"debug":["failed","panic"],"tool-loop":["go test"]}}}]}`,
\t\t},
\t\t{
\t\t\tname:    "missing config",
\t\t\tconfig:  `{"plugins":[{"enabled":true,"name":"agent-capability-router"}]}`,
\t\t\twantErr: true,
\t\t},
\t\t{
\t\t\tname:    "unknown config field",
\t\t\tconfig:  `{"plugins":[{"enabled":true,"name":"agent-capability-router","config":{"unexpected":true}}]}`,
\t\t\twantErr: true,
\t\t},
\t\t{
\t\t\tname:    "invalid confidence threshold",
\t\t\tconfig:  `{"plugins":[{"enabled":true,"name":"agent-capability-router","config":{"confidence_threshold":1.1}}]}`,
\t\t\twantErr: true,
\t\t},
\t\t{
\t\t\tname:    "invalid history depth",
\t\t\tconfig:  `{"plugins":[{"enabled":true,"name":"agent-capability-router","config":{"history_messages":33}}]}`,
\t\t\twantErr: true,
\t\t},
\t\t{
\t\t\tname:    "unknown alias field",
\t\t\tconfig:  `{"plugins":[{"enabled":true,"name":"agent-capability-router","config":{"aliases":{"main":"agent-main-auto","extra":"bad"}}}]}`,
\t\t\twantErr: true,
\t\t},
\t\t{
\t\t\tname:    "unknown role field",
\t\t\tconfig:  `{"plugins":[{"enabled":true,"name":"agent-capability-router","config":{"active_roles":{"planner":true}}}]}`,
\t\t\twantErr: true,
\t\t},
\t\t{
\t\t\tname:    "unknown keyword capability",
\t\t\tconfig:  `{"plugins":[{"enabled":true,"name":"agent-capability-router","config":{"keywords":{"mystery":["foo"]}}}]}`,
\t\t\twantErr: true,
\t\t},
\t}

\tfor _, tt := range tests {
\t\tt.Run(tt.name, func(t *testing.T) {
\t\t\terr := ValidateConfigSchema([]byte(tt.config), schema)
\t\t\tif tt.wantErr && err == nil {
\t\t\t\tt.Fatal("expected schema validation error")
\t\t\t}
\t\t\tif !tt.wantErr && err != nil {
\t\t\t\tt.Fatalf("expected valid agent capability router config, got: %v", err)
\t\t\t}
\t\t})
\t}
}
'''
validator_test.write_text(text.rstrip() + "\n" + append)
