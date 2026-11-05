[fix]: preserve Claude thinking summaries and signatures in streaming Responses [@cjl99](https://github.com/cjl99)
- fix: anthropic - Claude Haiku 5.x takes reasoning effort as adaptive thinking plus output_config.effort, chat and Responses requests drop its sampling params, and custom providers with an Anthropic base read their base provider's datasheet capabilities (#9490) [@macdod](https://github.com/macdod)
- fix: anthropic - Responses requests drop empty text blocks instead of forwarding them for a 400 (#9318) [@david-choi-querypie](https://github.com/david-choi-querypie)
- fix: anthropic - preserve explicit web search and web fetch versions, and keep a declared code execution tool beside versions that do not auto-inject it (#9258) [@ykakade](https://github.com/ykakade)
- fix: multi-key providers - follow-up calls on a stored response, container or video now find the key that created it instead of returning 404 from another key; video list pages across every key
