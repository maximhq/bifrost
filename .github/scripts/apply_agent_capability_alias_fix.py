from pathlib import Path


def replace_once(path: str, old: str, new: str) -> None:
    p = Path(path)
    text = p.read_text()
    count = text.count(old)
    if count != 1:
        raise SystemExit(f"{path}: expected one match, found {count}")
    p.write_text(text.replace(old, new, 1))


replace_once(
    "transports/config.schema.json",
    '''                    "aliases": {
                      "type": "object",
                      "description": "Logical automatic model aliases managed by the plugin",
                      "properties": {
                        "main": { "type": "string", "minLength": 1 },
                        "worker": { "type": "string", "minLength": 1 }
                      },
                      "additionalProperties": false
                    },''',
    '''                    "aliases": {
                      "type": "object",
                      "description": "Logical automatic model aliases managed by the plugin",
                      "properties": {
                        "main": { "type": "string", "minLength": 1 },
                        "worker": { "type": "string", "minLength": 1 }
                      },
                      "required": ["main", "worker"],
                      "additionalProperties": false
                    },''',
)

replace_once(
    "plugins/agentcapabilityrouter/config.go",
    '''\tif input.Aliases != nil {
\t\tif input.Aliases.Main != "" {
\t\t\tresolved.Aliases.Main = input.Aliases.Main
\t\t}
\t\tif input.Aliases.Worker != "" {
\t\t\tresolved.Aliases.Worker = input.Aliases.Worker
\t\t}
\t}
''',
    '''\tif input.Aliases != nil {
\t\tif input.Aliases.Main == "" || input.Aliases.Worker == "" {
\t\t\treturn resolvedConfig{}, fmt.Errorf("aliases.main and aliases.worker are required when aliases is configured")
\t\t}
\t\tresolved.Aliases = *input.Aliases
\t}
''',
)

replace_once(
    "plugins/agentcapabilityrouter/config_test.go",
    '\t\tAliases:             &AliasConfig{Main: "main-dynamic"},\n',
    '\t\tAliases:             &AliasConfig{Main: "main-dynamic", Worker: "worker-dynamic"},\n',
)
replace_once(
    "plugins/agentcapabilityrouter/config_test.go",
    '\tif cfg.Aliases.Main != "main-dynamic" || cfg.Aliases.Worker != "agent-worker-auto" {\n',
    '\tif cfg.Aliases.Main != "main-dynamic" || cfg.Aliases.Worker != "worker-dynamic" {\n',
)
replace_once(
    "plugins/agentcapabilityrouter/config_test.go",
    '\t\t{"duplicate aliases", &Config{Aliases: &AliasConfig{Main: "same", Worker: "same"}}},\n',
    '\t\t{"empty aliases", &Config{Aliases: &AliasConfig{}}},\n'
    '\t\t{"partial aliases", &Config{Aliases: &AliasConfig{Main: "only-main"}}},\n'
    '\t\t{"duplicate aliases", &Config{Aliases: &AliasConfig{Main: "same", Worker: "same"}}},\n',
)

validator = Path("transports/bifrost-http/lib/validator_test.go")
text = validator.read_text()
anchor = '''\t\t{
\t\t\tname:    "missing config",
\t\t\tconfig:  `{"plugins":[{"enabled":true,"name":"agent-capability-router"}]}`,
\t\t\twantErr: true,
\t\t},
'''
addition = anchor + '''\t\t{
\t\t\tname:    "empty aliases",
\t\t\tconfig:  `{"plugins":[{"enabled":true,"name":"agent-capability-router","config":{"aliases":{}}}]}`,
\t\t\twantErr: true,
\t\t},
\t\t{
\t\t\tname:    "partial aliases",
\t\t\tconfig:  `{"plugins":[{"enabled":true,"name":"agent-capability-router","config":{"aliases":{"main":"agent-main-auto"}}}]}`,
\t\t\twantErr: true,
\t\t},
'''
if text.count(anchor) != 1:
    raise SystemExit(f"validator_test.go: expected one insertion anchor, found {text.count(anchor)}")
validator.write_text(text.replace(anchor, addition, 1))

replace_once(
    "docs/plugins/agent-capability-router.mdx",
    'Main and worker aliases must be different. Deterministic aliases such as `agent-main-max`, `agent-main-cheap`, provider-prefixed models, and every unrelated model name are not intercepted.\n',
    'If `aliases` is configured, both `aliases.main` and `aliases.worker` must be provided, and they must be different. Omitting `aliases` keeps both defaults. Deterministic aliases such as `agent-main-max`, `agent-main-cheap`, provider-prefixed models, and every unrelated model name are not intercepted.\n',
)
